// Package account holds the developer-account domain type and its store contract:
// the durable link a developer sign-up creates between the OIDC identity a person
// authenticates as and the agent subdomain the registry mints for them. It is a
// thin domain package (types + error sentinels, no I/O); the Postgres
// implementation lives in the repo package, and the sign-up service depends on the
// Store interface here.
package account

import (
	"context"
	"errors"
	"strings"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// Developer is one developer account keyed on its OIDC identity (issuer + subject).
// Subdomain is the agent identity the registry minted for it — stable across the
// developer's future key rotations.
//
// It carries no business data about the operator behind the account. It once held
// a legal entity, an address and a jurisdiction, collected by a sign-up form and
// forwarded as registration_data when an agent opened an Exchange account. That
// call now carries the fields the AGENT supplies, per Exchange, so this service
// neither needs them nor keeps them.
type Developer struct {
	Issuer    string
	Subject   string
	Email     string
	Subdomain string
}

// ExchangeRegistration is one local note that this service registered an agent
// at an Exchange: which Exchange, and when the registration was last confirmed.
//
// It carries no part of what was submitted, and that is the point rather than an
// omission — the registration details are the operator's business data and this
// service does not keep them. The migration that creates the table states the
// same thing, at length, beside the columns.
//
// It is a HINT. The Exchange is the only party that knows whether an account
// exists; a registration made outside this adapter leaves no note, and a note
// can outlive an account the Exchange closed.
type ExchangeRegistration struct {
	Exchange     string
	RegisteredAt time.Time
}

// MaxExchangeRegistrations is how many notes one agent may accumulate.
//
// The exchange half of the key is a value an authenticated agent chooses per
// call, and with no allowlist configured — which the deployment documentation
// describes as the normal case — the key space is the whole DNS namespace. So
// this is a caller-influenced store, and it is bounded for the same reason every
// in-memory one in this service is: somewhere a caller can make the process grow
// is somewhere a caller will.
//
// 64 rather than a number tuned to anything: an agent doing real business
// registers at a handful of Exchanges, so the cap is far above the honest case
// and small enough that the worst case is a bounded row count per agent rather
// than an unbounded one. Recording past it drops that agent's oldest notes, and
// a note is a hint the next status call rebuilds, so losing the oldest costs the
// agent nothing it cannot ask for again.
const MaxExchangeRegistrations = 64

// RegistrationLog records and reads those notes. It is a separate port from
// Store rather than more methods on it: Store is the developer identity a
// sign-up creates, and this is a per-agent list of who it does business with —
// different data with a different lifetime and a different reader.
type RegistrationLog interface {
	// Record notes that subdomain registered at exchange, at. Repeating a note
	// refreshes its timestamp rather than adding a second row, and recording
	// past MaxExchangeRegistrations drops that agent's oldest notes.
	Record(ctx context.Context, subdomain, exchange string, at time.Time) error
	// Forget drops the note for one Exchange. It is called when the Exchange
	// itself reports no account: a note the authority has just contradicted is
	// known-false rather than merely stale, and leaving it would have the
	// no-argument status call keep listing an account that does not exist.
	Forget(ctx context.Context, subdomain, exchange string) error
	// List returns subdomain's notes, ordered by Exchange domain, at most
	// MaxExchangeRegistrations of them. An agent with none gets an empty list,
	// never an error — having registered nowhere is a normal state.
	List(ctx context.Context, subdomain string) ([]ExchangeRegistration, error)
}

// CanonicalExchange renders every spelling of one Exchange identity as one
// string. It is what this service compares and stores.
//
// It answers the PARTY question: do two values name the same Exchange? So it
// folds the two differences the protocol folds, and only those.
//
// Case, because domain names are case-insensitive. An agent writing
// Exchange.Example and one writing exchange.example name the same host, and the
// receiving Exchange lowercases the host before checking the audience its
// signature claims.
//
// A written-out :443, because a schemeless domain is read as https throughout
// the protocol SDK, so 443 spelled out and 443 left implicit are one port. The
// SDK's audience check folds it when deciding whether two spellings name the
// same party, and the endpoint anchor folds it when deciding where that party
// lives. Both spellings therefore reach one account at one Exchange. Any other
// port is kept, 80 included: folding that would read a scheme into a value that
// names none.
//
// Without the port fold the note store is the place the two spellings come
// apart. Its key is (subdomain, exchange) over plain text, so one Exchange
// becomes two rows, a forget on one spelling cannot remove the other, and the
// hint list names one Exchange twice.
//
// Only a value helpers.IsBareDomain accepts is folded. Canonicalising is not
// narrowing: a malformed value comes out exactly as it arrived, so the shape
// rule still sees what the caller wrote rather than something repaired on the
// way. The port split below is safe for the same reason — on a value that rule
// has accepted, a colon is a port separator and nothing else.
//
// The deployment's allowlist deliberately does NOT compare through this
// function. It answers a different question — did an operator list this value —
// and reads their list as written. See exchpolicy.
func CanonicalExchange(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if !helpers.IsBareDomain(domain) {
		return domain
	}
	host, port, found := strings.Cut(domain, ":")
	if !found || port == "443" {
		return host
	}
	return domain
}

// ErrNotBareDomain reports a value that is not the shape a domain-valued field
// admits. It is a sentinel rather than a bare message so a caller can tell a
// refused ARGUMENT from a peer that would not answer, and render the two
// differently.
//
// It lives here for the reason ErrNoAccount does: it is part of what the
// requirements leg promises its consumer, not one implementation's private
// vocabulary. A service depending on that leg through a narrow interface must be
// able to name this without importing the concrete reader that produces it.
var ErrNotBareDomain = errors.New("account: exchange must be a bare domain, not a URL")

// ErrNotPermitted reports a domain this deployment's Exchange policy excludes.
// The refusal happens before any fetch, so an excluded domain is never dialled.
//
// Declared beside ErrNotBareDomain and for the same reason: a caller has to tell
// "this deployment will not speak to that Exchange" from "that Exchange did not
// answer", and the two have different remedies — one is the operator's setting,
// the other is the peer.
var ErrNotPermitted = errors.New("account: exchange not permitted by this deployment")

// ErrRequirementsUnread reports that the target Exchange's manifest could not be
// read, so there was nothing to pre-check a registration against and no terms
// digest to echo. Nothing was signed and nothing was sent.
//
// It exists because the SDK seam this repository's reader plugs into classifies
// on the SDK's own sentinels, and every failure that is not one of those is read
// as a transport failure worth retrying. That verdict is right, and it is also
// the same verdict a send failure gets -- so without a sentinel of our own the
// two arrive at the caller as one condition, and "the manifest could not be
// read" stops being a state a caller can name. It is declared here for the
// reason ErrNoAccount is: it is part of what the requirements leg promises its
// consumer, not one implementation's private vocabulary.
var ErrRequirementsUnread = errors.New("account: exchange registration requirements could not be read")

// ErrManifestVersionRefused reports an Exchange whose manifest was fetched and read
// but carries a document version this service does not accept: another major,
// or no version at all. Nothing was sent, and nothing will be — the document
// says the same thing on every retry.
//
// Declared beside ErrNotBareDomain and ErrNotPermitted for the same reason: a
// caller has to tell "that Exchange publishes a document this build cannot
// read" from "that Exchange did not answer", because the remedies differ — one
// is a newer build of this service or a word with the Exchange, the other is a
// retry.
var ErrManifestVersionRefused = errors.New("account: exchange manifest version not accepted")

// ErrFieldsRefused reports that the registration payload was refused against the
// schema the target Exchange publishes, before anything was signed and before
// anything was sent. The offending members travel as a typed detail on the
// wrapped error, which is what lets a caller name them rather than quote a
// sentence back at an agent.
//
// It is declared here for the reason ErrRequirementsUnread is, and the condition
// it guards against is sharper. A peer that refuses the SAME payload answers
// with the same shape of typed detail, so the detail alone cannot say which
// happened; only the leg that computed the refusal knows nothing left the
// process. Without a sentinel of our own, a consumer of the account port has to
// recognise the outbound leg's own transport error class to tell a local refusal
// from a remote one — which is a wire vocabulary leaking into a port whose other
// promises are all stated here.
var ErrFieldsRefused = errors.New("account: registration data refused against the published schema")

// FieldsRefusedError is that refusal with the offending members attached, so a
// consumer can name them instead of quoting a sentence back at an agent.
//
// The members keep the protocol's own type rather than a local copy of it. That
// is the whole point: an Exchange refusing the same payload attaches this same
// list, computed by the same validator, so a layer that renders one refusal
// renders both — and asking it to parse member names back out of prose is
// exactly what the protocol's typed error detail exists to prevent.
//
// The type is here rather than in the leg that builds it so the account port
// stays satisfiable without importing a concrete outbound client. Err carries
// the sentinel above, so errors.Is reaches it through Unwrap and a caller that
// only wants to classify never touches this type.
type FieldsRefusedError struct {
	// Fields names each member at fault, in the order the validator reported them.
	Fields []*forav1.RegistrationFieldError
	// Err is the wrapped cause, carrying ErrFieldsRefused.
	Err error
}

func (e *FieldsRefusedError) Error() string { return e.Err.Error() }

func (e *FieldsRefusedError) Unwrap() error { return e.Err }

// ErrNoAccount reports an Exchange's answer that an agent holds no account
// there. It is a normal outcome rather than a failure, and a caller works it out
// with errors.Is.
//
// It is declared beside the ports rather than inside the outbound client that
// classifies it. "No account at this Exchange" is part of what the account leg
// promises its consumer — it is the answer that makes a status call report
// "registered: false" and drop the local note — so a service depending on that
// leg through a narrow interface must not have to import a concrete client to
// name it. The client that turns a wire answer into this value stays free to be
// replaced, which the outbound package's own plan requires: it goes when the two
// account RPCs land in the SDK, and a sentinel defined there would go with it.
var ErrNoAccount = errors.New("account: no account at this exchange")

// Store is the persistence port the sign-up service depends on (interface
// segregation, mirroring directory.CardReader). *repo.PgxDeveloperRepo satisfies it.
type Store interface {
	// BySubject resolves a developer by its OIDC identity, or ErrNotFound.
	BySubject(ctx context.Context, issuer, subject string) (Developer, error)
	// Reserve creates the account row that claims subdomain for (issuer, subject).
	// It returns ErrSubdomainTaken when the subdomain is already claimed (the
	// sign-up retries with a fresh slug) and ErrAlreadyExists when this OIDC
	// identity already has an account (a concurrent sign-in won the race; re-read
	// BySubject).
	Reserve(ctx context.Context, d Developer) (Developer, error)
}

// Domain sentinels the Store maps its persistence errors onto, so the sign-up
// service reasons about outcomes without importing pgx. ErrUnavailable marks a
// transient store outage (retryable), distinct from a definite result.
var (
	ErrNotFound       = errors.New("account: developer not found")
	ErrSubdomainTaken = errors.New("account: subdomain already claimed")
	ErrAlreadyExists  = errors.New("account: developer already exists")
	ErrUnavailable    = errors.New("account: store unavailable")
)
