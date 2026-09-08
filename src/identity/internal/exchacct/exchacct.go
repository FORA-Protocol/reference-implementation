// Package exchacct owns what it means for an agent to hold an account at an
// Exchange: opening one, asking after one, and the local note of where the agent
// has been registered.
//
// # Why this is not in the tool handlers
//
// The rule these operations encode is a workflow, not a projection. A
// registration is pre-checked against a freshly fetched schema, echoes that same
// fetch's terms digest, and leaves a local note the Exchange's own answer can
// later revoke. That is three collaborators and an ordering constraint between
// them, and it belongs one layer below the surface that happens to expose it —
// the MCP adapter today, and whatever asks next.
//
// # The note is a hint, and the Exchange is the authority
//
// Nothing here treats the local note as a fact. It records THAT a registration
// was accepted and nothing about what was submitted; a status answer from the
// Exchange refreshes it when there is an account and deletes it when there is
// not. Registrations made outside this service leave no note at all, which is
// why the per-Exchange question is the authoritative one and the note-backed
// list says so about itself.
//
// # Log event names
//
// The events emitted here keep their identity.mcp.* names. They are the same
// events an operator has always keyed on, and the layer a line is emitted from
// is not what the name is for.
//
// The note store's own failures are named for the STORE — identity.mcp.notes.* —
// rather than for a tool. Recording a note happens on both the register and the
// status path, so naming that failure after either one would put status-refresh
// failures into the set an operator collects when diagnosing registrations.
package exchacct

import (
	"context"
	"errors"
	"fmt"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/account"
)

// RequirementsReader reads what one Exchange asks of a registration.
//
// Named for the actor, like Caller and account.RegistrationLog, and not for what
// it returns: account.Requirements is the value type, it is imported into this
// same file, and one name for both leaves a reader working out which is meant at
// every mention.
//
// Every read is a fresh fetch of that Exchange's manifest: the protocol forbids
// echoing a cached terms digest, and reading the schema from the same fresh
// bytes means a stale local schema cannot refuse a payload the Exchange would
// have taken.
type RequirementsReader interface {
	Requirements(ctx context.Context, exchange string) (account.Requirements, error)
}

// Caller sends the two account RPCs, signed as the agent on whose behalf this
// service is acting. The identity travels on the context, so nothing here can
// ask for a different agent's signature.
//
// AccountStatus reports "this agent holds no account here" by returning an error
// that wraps account.ErrNoAccount. That is part of the contract rather than one
// client's private vocabulary: it is the answer this service turns into
// "registered: false" and into dropping a note the Exchange has contradicted, so
// an implementation that does not send it makes a normal answer read as a
// failure.
type Caller interface {
	Register(ctx context.Context, req *forav1.RegisterRequest) (*forav1.RegisterResponse, error)
	AccountStatus(
		ctx context.Context, req *forav1.GetAccountStatusRequest,
	) (*forav1.GetAccountStatusResponse, error)
}

// Account is one account as of the moment it was established.
type Account struct {
	// Exchange is the bare domain this account is at.
	Exchange string
	// Registered is whether an account exists there.
	Registered bool
	// Active is whether it may transact right now, nil wherever nobody
	// authoritative was asked or there is no account to be active.
	//
	// Register always sets it: the Exchange has just answered about an account
	// that now exists, so false there means suspended rather than unknown. On a
	// Status answer it is nil whenever there is no account.
	Active *bool
	// BillingRef is the Exchange-minted handle, empty where no authority
	// reported one.
	BillingRef string
	// AsOf is when this answer was established.
	AsOf time.Time
}

// IsActive answers Active as a plain bool, reading nil as false.
//
// For a caller that has no third state to render — a register answer, or a log
// line's one field. It is a method rather than the same two-clause expression
// written at each such site, so "nil means nobody was asked" is decided once.
func (a Account) IsActive() bool {
	return a.Active != nil && *a.Active
}

// confirmation is the part of an Exchange's answer both account RPCs carry. The
// register response and the status response are different protobuf types with
// these two getters in common, which is what lets one function serve both.
type confirmation interface {
	GetActive() bool
	GetBillingRef() string
}

// confirmed records an Exchange's confirmation of an account and renders it.
//
// Both account calls end the same way: the Exchange has just answered about an
// account that exists, so the local note is written and the answer becomes an
// Account. Written once here, beside IsActive, so the two sides of this value's
// contract sit together — "nil means nobody was asked" is decided in one place
// for the reader, and this is the same decision for the writer. Registered is
// true and Active is non-nil on both paths, which is exactly what the Active
// field's own rule says and could not be relied on while two arms set it.
//
// at is passed rather than read from the clock, because a caller takes one
// timestamp for the whole call: Status stamps its no-account answer with the
// same instant, and a second clock read here would let the two disagree.
func (s *Service) confirmed(
	ctx context.Context, subdomain, exchange string, resp confirmation, at time.Time,
) Account {
	s.note(ctx, subdomain, exchange, at)
	active := resp.GetActive()
	return Account{
		Exchange:   exchange,
		Registered: true,
		Active:     &active,
		BillingRef: resp.GetBillingRef(),
		AsOf:       at,
	}
}

// Hint is one local note: an Exchange this service saw a registration accepted
// at, and when. It carries no billing reference and no activity, because no
// authority was asked.
type Hint struct {
	Exchange string
	AsOf     time.Time
}

// Config wires a Service. Every collaborator is required — New refuses without
// one rather than nil-panicking on the first call. Clock is the exception: it
// defaults to the system clock, so only a test that wants time under its control
// states it.
type Config struct {
	Requirements RequirementsReader
	Caller       Caller
	// Notes is the local record of where this service has registered an agent.
	Notes account.RegistrationLog
	// Clock stamps the notes and every AsOf, so what a caller reads is the time
	// this service chose rather than a database's.
	Clock clock.Clock
}

// Service opens and reports Exchange accounts.
type Service struct {
	reqs  RequirementsReader
	fora  Caller
	notes account.RegistrationLog
	clk   clock.Clock
}

// New builds a Service, failing on missing configuration rather than
// nil-panicking on the first call.
func New(cfg Config) (*Service, error) {
	switch {
	case cfg.Requirements == nil:
		return nil, errors.New("exchacct: Config.Requirements is required")
	case cfg.Caller == nil:
		return nil, errors.New("exchacct: Config.Caller is required")
	case cfg.Notes == nil:
		return nil, errors.New("exchacct: Config.Notes is required")
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System{}
	}
	return &Service{reqs: cfg.Requirements, fora: cfg.Caller, notes: cfg.Notes, clk: clk}, nil
}

// Register opens subdomain's account at exchange, sending fields as the
// registration data.
//
// The Exchange derives WHO is registering from the verified request signature,
// never from the payload, so what goes on the wire is the business detail the
// caller supplied and no identity at all.
//
// The domain is canonicalised before anything reads it, and both this and Status
// do it in the same first line. Domain names are case-insensitive and every
// other party already treats them that way, so a caller that writes
// Exchange.Example must not leave a note no later call can find or delete. Doing
// it here rather than at the surface is the same rule the payload bounds follow:
// no caller can reach the wire or the store without it.
func (s *Service) Register(
	ctx context.Context, subdomain, exchange string, fields map[string]any,
) (Account, error) {
	if err := checkExchange(exchange); err != nil {
		return Account{}, err
	}
	exchange = account.CanonicalExchange(exchange)
	// The protocol's payload bounds, before any network call: a limit that
	// exists to stop work comes before the work it would stop. It is here rather
	// than at the surface so no caller can reach the fetch without it.
	if verdict := helpers.CheckRegistrationData(fields); verdict != helpers.RegistrationDataAccepted {
		return Account{}, &Error{
			Kind: KindFieldsOutOfBounds, Exchange: exchange, Err: errors.New(verdict.String()),
		}
	}
	reqs, err := s.requirements(ctx, subdomain, exchange)
	if err != nil {
		return Account{}, err
	}
	// Branch-free: a nil validator reports no failures, so "publishes none" and
	// "publishes one this service cannot use" both fall through to send.
	if failures := reqs.Schema.Validate(fields); len(failures) > 0 {
		return Account{}, &Error{Kind: KindFieldsRefused, Exchange: exchange, Fields: failures}
	}
	data, err := structpb.NewStruct(fields)
	if err != nil {
		return Account{}, &Error{Kind: KindFieldsMalformed, Exchange: exchange, Err: err}
	}
	resp, err := s.fora.Register(ctx, &forav1.RegisterRequest{
		Ver:              helpers.ProtocolVersion,
		Exchange:         exchange,
		RegistrationData: data,
		// Echoed exactly as published, absent when the Exchange publishes none.
		// The request signature covers this statement, so it is the durable
		// record of which terms revision the operator accepted.
		TermsDigest: reqs.TermsDigest,
	})
	if err != nil {
		return Account{}, &Error{Kind: outboundKind(err), Exchange: exchange, Err: err}
	}
	now := s.clk.Now()
	return s.confirmed(ctx, subdomain, exchange, resp, now), nil
}

// checkExchange refuses an argument that is not the shape the wire admits.
//
// It runs on BOTH account legs and BEFORE canonicalisation, and both halves of
// that are the point.
//
// Both legs, because a structural guarantee held by one caller is not a
// guarantee. Register reached this rule through the requirements reader, which
// runs it before it dials; Status ran no shape check at all and handed whatever
// it was given to the wire, with only the tool layer's copy in front of it. This
// package's own doc names its consumers as "the MCP adapter today, and whatever
// asks next" — the next one is exactly the caller that would not have the tool
// layer in front of it.
//
// Before canonicalisation, because canonicalising rewrites: it lowercases and
// trims, and it folds a written-out :443. A value checked after that rewrite is
// not the value the caller sent, and refusing is only honest while the two are
// still the same. These calls open accounts and move money, so a caller must
// learn its input was wrong rather than have it quietly reinterpreted.
//
// helpers.IsBareDomain is the rule, not helpers.IsBareHost. The SDK keeps the
// two apart on purpose: IsBareHost asks whether a value is safe to build a URL
// from, IsBareDomain asks whether it is the shape the contract admits, and a
// value bound for the wire wants the second.
func checkExchange(exchange string) error {
	if helpers.IsBareDomain(exchange) {
		return nil
	}
	return &Error{
		Kind:     KindExchangeShape,
		Exchange: exchange,
		Err:      fmt.Errorf("%w: %q", account.ErrNotBareDomain, exchange),
	}
}

// isManifestVersionRefused reports whether err is the one condition both legs of
// this service can meet: the target Exchange's manifest was read and carries a
// document version this build does not accept.
//
// It answers for BOTH spellings on purpose, because the two legs reach the
// condition by different routes. The register leg reads requirements first, so
// the refusal arrives as the port's own account.ErrManifestVersionRefused. The
// status leg reads no requirements, so the same document is refused inside the
// SDK's resolver and arrives as resolvers.ErrManifestVersionRefused underneath a
// CallError. One condition classified in one place is what keeps the two tools
// from telling an agent two different things about one Exchange.
func isManifestVersionRefused(err error) bool {
	return errors.Is(err, account.ErrManifestVersionRefused) ||
		errors.Is(err, resolvers.ErrManifestVersionRefused)
}

// requirementsKind classifies what the requirements reader refused with.
//
// The reader distinguishes a refused ARGUMENT, a policy refusal and a manifest
// it read but cannot accept from a peer that would not answer, and says so with
// sentinels the port declares. Without this, all four arrived as
// KindRequirements — so an operator reading call_failed could not tell an agent
// that named a URL, a domain their own policy excludes, or an Exchange whose
// document this build does not implement, from an Exchange that is down.
func requirementsKind(err error) Kind {
	switch {
	case errors.Is(err, account.ErrNotBareDomain):
		return KindExchangeShape
	case errors.Is(err, account.ErrNotPermitted):
		return KindNotPermitted
	case isManifestVersionRefused(err):
		return KindManifestVersionRefused
	default:
		return KindRequirements
	}
}

// outboundKind classifies a failed account call.
//
// KindOutbound says the request was sent and the Exchange refused it, or the
// call never completed. A manifest this build cannot read is neither: nothing
// was sent, and nothing will be on a retry. The status leg reaches that verdict
// through the SDK resolver rather than through the requirements reader, so
// without this arm it rendered as the generic peer failure — whose own wording
// says the request was sent, which it was not.
func outboundKind(err error) Kind {
	if isManifestVersionRefused(err) {
		return KindManifestVersionRefused
	}
	return KindOutbound
}

// requirements reads the target's manifest and reports the pre-check that can be
// run against it.
//
// A published schema this service cannot use is LOGGED, never refused. A local
// check that cannot run must not become a local veto: the Exchange's own check
// is the deciding one, and refusing here would block a registration it would
// have accepted with no way for the agent past it. The operator still needs to
// know the pre-check was silently skipped, which is what that line is.
func (s *Service) requirements(
	ctx context.Context, subdomain, exchange string,
) (account.Requirements, error) {
	reqs, err := s.reqs.Requirements(ctx, exchange)
	if err != nil {
		return account.Requirements{}, &Error{Kind: requirementsKind(err), Exchange: exchange, Err: err}
	}
	if reqs.SchemaRefused() {
		reqctx.FromContext(ctx).WarnContext(ctx, "identity.mcp.register.schema_unusable",
			"subdomain", subdomain, "exchange", exchange, "verdict", reqs.Verdict.String())
	}
	return reqs, nil
}

// Status asks one Exchange about subdomain's account there and returns its
// answer, which is authoritative as of this call.
//
// "No account here" is a normal answer rather than a failure — an agent working
// out where it still needs to register must be able to learn that without
// catching an error.
func (s *Service) Status(ctx context.Context, subdomain, exchange string) (Account, error) {
	if err := checkExchange(exchange); err != nil {
		return Account{}, err
	}
	exchange = account.CanonicalExchange(exchange)
	resp, err := s.fora.AccountStatus(ctx, &forav1.GetAccountStatusRequest{
		Ver:      helpers.ProtocolVersion,
		Exchange: exchange,
	})
	now := s.clk.Now()
	switch {
	case err == nil:
		// The note is refreshed to match, which is the only way one appears for a
		// registration made outside this service.
		return s.confirmed(ctx, subdomain, exchange, resp, now), nil
	case errors.Is(err, account.ErrNoAccount):
		s.forget(ctx, subdomain, exchange)
		return Account{Exchange: exchange, Registered: false, AsOf: now}, nil
	default:
		return Account{}, &Error{Kind: outboundKind(err), Exchange: exchange, Err: err}
	}
}

// Hints lists what this service last saw, without touching the network.
//
// A store failure IS returned as an error, unlike the note-writing paths: this
// is our own record and the answer depends entirely on it, so an empty list
// would be a wrong answer rather than a degraded one.
//
// It is returned and not also logged here. The caller reports it on the same
// line as every other failure of the tool it is serving, so one query over that
// event covers the whole surface; logging it here as well would record one
// failure twice, at two levels.
func (s *Service) Hints(ctx context.Context, subdomain string) ([]Hint, error) {
	notes, err := s.notes.List(ctx, subdomain)
	if err != nil {
		return nil, &Error{Kind: KindNotes, Err: err}
	}
	hints := make([]Hint, 0, len(notes))
	for _, note := range notes {
		hints = append(hints, Hint{Exchange: note.Exchange, AsOf: note.RegisteredAt})
	}
	return hints, nil
}

// note records that this agent is registered here, for the note-backed list.
//
// A failure is logged and does NOT fail the call. The Exchange has already
// answered; reporting an error now would tell the caller the registration did
// not happen when it did, and the only thing actually lost is a hint the next
// status call rebuilds.
func (s *Service) note(ctx context.Context, subdomain, exchange string, at time.Time) {
	if err := s.notes.Record(ctx, subdomain, exchange, at); err != nil {
		reqctx.FromContext(ctx).WarnContext(ctx, "identity.mcp.notes.record_failed",
			"subdomain", subdomain, "exchange", exchange, "err", err.Error())
	}
}

// forget drops the note for an Exchange that has just said there is no account.
//
// A note the authority contradicted is known-false rather than merely stale, and
// leaving it would have the note-backed list keep naming an account that does
// not exist. As with recording, a store failure is logged and does not fail the
// call: the answer the caller asked for is already in hand.
func (s *Service) forget(ctx context.Context, subdomain, exchange string) {
	if err := s.notes.Forget(ctx, subdomain, exchange); err != nil {
		reqctx.FromContext(ctx).WarnContext(ctx, "identity.mcp.notes.forget_failed",
			"subdomain", subdomain, "exchange", exchange, "err", err.Error())
	}
}
