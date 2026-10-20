// Package repo provides narrow, domain-specific interfaces over the generated
// sqlc Querier. Handlers and services depend on these interfaces, not on the
// fat Querier directly.
package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/db/sqlc"
)

// Exchange is the domain representation of a known exchange row.
// Decouples service logic from sqlc-generated types (repository pattern).
type Exchange struct {
	ID                string
	Domain            string
	Endpoint          string
	TrustLevel        string
	ProbeState        ProbeState
	SupportedProfiles []string
	Priority          int32
}

// ExchangeRepo is the read port handlers and services depend on. The probe
// state and the endpoint are not writable here: from inside request handling a
// handler could mark an exchange down, or move the address the registry records
// for it. Those writes belong to ExchangeHealthRepo.
type ExchangeRepo interface {
	ListUnblocked(ctx context.Context) ([]Exchange, error)
	GetByDomain(ctx context.Context, domain string) (Exchange, error)
	UpsertFromBootstrap(ctx context.Context, m Exchange) (Exchange, error)
}

// ExchangeHealthRepo is the health refresher's port: read every probeable row,
// write back what each probe learned. Separate from ExchangeRepo so the write
// stays off every request-path consumer's surface (Architecture Rule 3).
// *PgxExchangeRepo satisfies both.
type ExchangeHealthRepo interface {
	ListUnblocked(ctx context.Context) ([]Exchange, error)
	SetProbeResult(ctx context.Context, exchangeID, endpoint string, state ProbeState) error
}

// The trust levels an operator may give an exchange, spelled as the
// broker.trust_level enum spells them. Declared here rather than taken from the
// generated sqlc enum so services stay off the generated package; a guard test
// pins that the two agree.
const (
	TrustLevelDiscovered = "DISCOVERED"
	TrustLevelVerified   = "VERIFIED"
	TrustLevelPreferred  = "PREFERRED"
	TrustLevelBlocked    = "BLOCKED"
)

// ProbeState is what one refresher pass concluded about an exchange, spelled as
// the broker.exchange_probe_state enum spells it. Declared here rather than
// taken from the generated sqlc enum so services stay off the generated
// package; a guard test pins that the two agree.
//
// There are three because a pass learns three different things, and the two
// unroutable ones are not the same fact. An outage clears on its own; a refusal
// is a decision the resolver reached about the Exchange's manifest and repeats
// identically until somebody changes that manifest or this build. A single
// boolean could only say "the last pass did not work", so both request paths
// read it and reported a refusal as retryable — and an agent told to try again
// about a manifest this Broker will never read retries forever.
type ProbeState string

const (
	// ProbeLive means the pass resolved the Exchange's endpoint and the
	// Exchange answered: the health endpoint its manifest names returned 200,
	// or its manifest names none and was fetched and valid.
	ProbeLive ProbeState = "LIVE"
	// ProbeDown means the pass could not reach the Exchange, or it did not
	// answer. Transient.
	ProbeDown ProbeState = "DOWN"
	// ProbeRefused means the pass READ the Exchange's manifest and refused it —
	// no endpoint, an endpoint on a host the manifest does not speak for, a
	// document version this build does not accept, a document that breaks the
	// manifest schema, or a health endpoint no pass can dial. Settled.
	ProbeRefused ProbeState = "REFUSED"
)

// Admission is whether the broker may route to one registry row, and it is the
// SINGLE derivation of that rule — purchase admission, the resolve router's
// guard and the routing-skip reason all read it here. Four values
// because the callers owe four answers, split by whether the state clears on
// its own and by which party has to act.
//
// AdmissionLive is deliberately not the zero value: a row nobody filled in must
// not read as routable.
type Admission int

const (
	// AdmissionBlocked means the operator withdrew trust. Settled, so a refusal
	// owes a final answer.
	AdmissionBlocked Admission = iota
	// AdmissionDown means trusted, but the last probe could not reach the
	// exchange. Clears within a refresher interval, so a refusal owes a
	// retryable answer.
	AdmissionDown
	// AdmissionRefused means trusted and reachable, and the last probe read the
	// exchange's manifest and refused it. Settled like AdmissionBlocked and for
	// the same reason — retrying reads the same document — but a different
	// party fixes it, so the two are not merged: an operator resolves BLOCKED,
	// and only the exchange resolves this.
	AdmissionRefused
	// AdmissionLive means registered, trusted, and answering health probes.
	AdmissionLive
)

// Admission classifies this row. Trust is read first: an exchange can be
// BLOCKED and probing LIVE at once, and the operator's decision outranks the
// probe.
//
// LIVE is matched, never defaulted to. The zero-value Exchange carries an empty
// ProbeState, and so does a row read through a path that forgot to select the
// column; both must come out unroutable. They come out AdmissionDown rather
// than AdmissionRefused because an unreadable state is this broker's own fault
// and says nothing about the exchange, and the retryable answer is the one that
// does not blame the wrong party.
func (e Exchange) Admission() Admission {
	switch {
	case e.TrustLevel == TrustLevelBlocked:
		return AdmissionBlocked
	case e.ProbeState == ProbeRefused:
		return AdmissionRefused
	case e.ProbeState == ProbeLive:
		return AdmissionLive
	default:
		return AdmissionDown
	}
}

// ErrNotFound is returned when a row is not present.
var ErrNotFound = errors.New("repo: not found")

// PgxExchangeRepo wraps sqlc.Querier + pool for transactional ops.
type PgxExchangeRepo struct {
	q    *sqlc.Queries
	pool *pgxpool.Pool
}

// NewExchangeRepo constructs an ExchangeRepo backed by the given pool.
func NewExchangeRepo(pool *pgxpool.Pool) *PgxExchangeRepo {
	return &PgxExchangeRepo{q: sqlc.New(pool), pool: pool}
}

// ListUnblocked returns every non-BLOCKED exchange by priority, INCLUDING the
// ones probing DOWN or REFUSED. It is the registry's only list read and
// deliberately does not pre-filter on probe state, because a filtered list
// cannot tell a caller that a row exists but is not routable right now.
//
// The refresher must see those rows or the state becomes a one-way ratchet —
// that applies to REFUSED as much as to DOWN, since an exchange that publishes
// a manifest this build accepts must be able to come back. The refresher is
// this method's only caller. Resolve and purchase admission read one row
// through GetByDomain and apply Admission themselves, so they too can tell a
// down or refused exchange from one that was never registered.
func (r *PgxExchangeRepo) ListUnblocked(ctx context.Context) ([]Exchange, error) {
	rows, err := r.q.ListUnblockedExchanges(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unblocked exchanges: %w", err)
	}
	return rowsToExchanges(rows)
}

// GetByDomain looks up an exchange by its canonical domain.
func (r *PgxExchangeRepo) GetByDomain(ctx context.Context, domain string) (Exchange, error) {
	const q = `
SELECT exchange_id, domain, endpoint, trust_level, probe_state,
       supported_profiles, priority
  FROM broker.exchanges
 WHERE domain = $1
 LIMIT 1
`
	row := r.pool.QueryRow(ctx, q, domain)
	var (
		id       string
		dom      string
		endpoint string
		trust    sqlc.BrokerTrustLevel
		state    sqlc.BrokerExchangeProbeState
		profiles []byte
		priority int32
	)
	if err := row.Scan(&id, &dom, &endpoint, &trust, &state, &profiles, &priority); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Exchange{}, ErrNotFound
		}
		return Exchange{}, fmt.Errorf("get exchange by domain: %w", err)
	}
	supported, err := decodeProfiles(profiles)
	if err != nil {
		return Exchange{}, err
	}
	return Exchange{
		ID:                id,
		Domain:            dom,
		Endpoint:          endpoint,
		TrustLevel:        string(trust),
		ProbeState:        ProbeState(state),
		SupportedProfiles: supported,
		Priority:          priority,
	}, nil
}

// CanonicalEndpoint is the single stored spelling of an endpoint: trailing "/"
// trimmed. Every write to the endpoint column goes through it, as does any
// caller comparing a freshly resolved address against the stored one.
//
// Without it a trailing-slash spelling and a bare one would read as two
// addresses: the refresher would log a change of address that is not one, and
// the client pool would hold two clients for one Exchange. (The generated
// Connect client trims the base URL itself, so the slash never reaches the
// wire.)
func CanonicalEndpoint(endpoint string) string {
	return strings.TrimRight(endpoint, "/")
}

// UpsertFromBootstrap reconciles a bootstrap entry into the exchanges table.
// The endpoint is canonicalized on store.
func (r *PgxExchangeRepo) UpsertFromBootstrap(ctx context.Context, m Exchange) (Exchange, error) {
	profiles, err := json.Marshal(m.SupportedProfiles)
	if err != nil {
		return Exchange{}, fmt.Errorf("marshal supported_profiles: %w", err)
	}
	row, err := r.q.UpsertExchange(ctx, sqlc.UpsertExchangeParams{
		ExchangeID:        m.ID,
		Domain:            m.Domain,
		Endpoint:          CanonicalEndpoint(m.Endpoint),
		TrustLevel:        sqlc.BrokerTrustLevel(m.TrustLevel),
		SupportedProfiles: profiles,
		Priority:          m.Priority,
	})
	if err != nil {
		return Exchange{}, fmt.Errorf("upsert exchange: %w", err)
	}
	return rowToExchange(row)
}

// SetProbeResult records what one probe pass learned: the address the
// exchange's own well-known advertises, and what the pass concluded about the
// exchange — it answered, it did not answer, or its manifest was refused.
//
// The endpoint is written too, because the well-known is the authority on where
// an exchange is and the column is a cache of it. Discovery already dials the
// resolved address and the probe measures it; leaving the column alone would
// keep a third opinion in the registry, the one an operator reads. Bootstrap
// seeds the row, the probe loop keeps it true — a restart replays the bootstrap value and the next pass corrects it.
func (r *PgxExchangeRepo) SetProbeResult(
	ctx context.Context, exchangeID, endpoint string, state ProbeState,
) error {
	const q = `
UPDATE broker.exchanges
   SET probe_state = $3,
       endpoint = $2,
       last_health_check = NOW(),
       updated_at = NOW()
 WHERE exchange_id = $1
`
	_, err := r.pool.Exec(ctx, q, exchangeID, CanonicalEndpoint(endpoint), string(state))
	if err != nil {
		return fmt.Errorf("set probe result: %w", err)
	}
	return nil
}

// rowsToExchanges converts a queried row set to the domain type.
func rowsToExchanges(rows []sqlc.BrokerExchange) ([]Exchange, error) {
	out := make([]Exchange, 0, len(rows))
	for i := range rows {
		m, err := rowToExchange(rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func rowToExchange(r sqlc.BrokerExchange) (Exchange, error) {
	supported, err := decodeProfiles(r.SupportedProfiles)
	if err != nil {
		return Exchange{}, err
	}
	return Exchange{
		ID:                r.ExchangeID,
		Domain:            r.Domain,
		Endpoint:          r.Endpoint,
		TrustLevel:        string(r.TrustLevel),
		ProbeState:        ProbeState(r.ProbeState),
		SupportedProfiles: supported,
		Priority:          r.Priority,
	}, nil
}

func decodeProfiles(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode supported_profiles: %w", err)
	}
	return out, nil
}

// Silence pgtype unused-import lint in stricter builds; retained for possible
// future fields (timestamps).
var _ pgtype.Text
