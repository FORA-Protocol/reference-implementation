// Package probe adapts a per-Broker-instance forawellknown.Cache to the
// Broker's pre-existing Probe API.
//
// v1 requires every publisher to host /.well-known/fora.json. The probe
// surfaces four typed error shapes — ErrManifestMissing (the domain returned
// 404 / no manifest), ErrManifestVersionRefused (a manifest was read and
// carries a document version this build does not accept), ErrManifestUnusable
// (a manifest arrived and broke a document rule), and ErrProbeFailed
// (everything else that stopped a manifest being read). The first three are
// VERDICTS and the fourth is treated as an outage, which is the distinction
// that decides the absence reason: callers MUST refuse a verdict with a
// settled vocabulary (NOT_IN_CATALOG) and only an outage with
// TEMPORARILY_UNAVAILABLE. The broker no longer silently accepts an
// unmanifested publisher.
package probe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
)

// DefaultTTL mirrors forawellknown.DefaultCacheTTL for callers depending on
// this symbol.
const DefaultTTL = forawellknown.DefaultCacheTTL

// AuthorizedExchange is one Exchange the publisher authorizes. The field name
// is kept for backwards compatibility with existing call sites; wire format
// matches the manifest's exchanges[] entries (domain/endpoint/relationship).
type AuthorizedExchange struct {
	Domain            string
	Endpoint          string
	SupportedProfiles []string
}

// Manifest is the Broker's narrow projection of a publisher manifest. It
// carries only the fields the routing code consults.
type Manifest struct {
	Ver       string
	Provider  string
	Exchanges []AuthorizedExchange
}

// Result carries a successfully fetched manifest. Probe returns a typed
// error (ErrManifestMissing, ErrManifestVersionRefused, ErrManifestUnusable,
// ErrProbeFailed, ErrInvalidDomain) when the domain has no usable manifest;
// on the error paths Result is zero-valued.
type Result struct {
	Manifest  Manifest
	FetchedAt time.Time
}

// ErrInvalidDomain is returned for empty or obviously invalid input.
var ErrInvalidDomain = errors.New("probe: invalid domain")

// ErrManifestMissing is returned when the publisher's /.well-known/fora.json
// is absent (404 / explicit no-manifest). v1 requires every publisher to
// host fora.json; callers MUST refuse with the NOT_IN_CATALOG absence
// reason rather than retry the bare URL.
var ErrManifestMissing = errors.New("probe: publisher does not host fora.json")

// ErrProbeFailed wraps a transient fetch / decode / non-2xx failure.
// Callers MUST refuse with the TEMPORARILY_UNAVAILABLE absence reason —
// the probe is not authoritative about catalog membership when the
// upstream is unreachable.
var ErrProbeFailed = errors.New("probe: fora.json fetch failed")

// ErrManifestVersionRefused wraps a manifest that was fetched and read but carries a
// document version this reader does not accept (forawellknown ErrVersionRefused).
// It is a VERDICT, not an outage: the document will carry the same version on
// every retry, so callers MUST refuse with a settled absence reason
// (NOT_IN_CATALOG), never TEMPORARILY_UNAVAILABLE — an agent told "try again"
// about a publisher whose manifest this Broker cannot read would retry forever.
var ErrManifestVersionRefused = errors.New("probe: publisher manifest version not accepted")

// ErrManifestUnusable wraps a manifest that was fetched and parsed but broke a
// document rule — the wire shape the embedded schema decides
// (forawellknown ErrSchemaInvalid), or a document describing a role other than
// the publisher one this probe pins (forawellknown ErrRoleMismatch).
//
// It is a VERDICT for the same reason ErrManifestVersionRefused is: the origin
// serves those bytes, and the next fetch returns them again. Callers MUST refuse
// with a settled absence reason (NOT_IN_CATALOG), never TEMPORARILY_UNAVAILABLE.
// The separation from ErrProbeFailed is what stops the Broker re-probing a
// publisher whose manifest names the wrong role for as long as it stays wrong.
//
// A body that never parsed is NOT in here. It reaches ErrProbeFailed, because a
// proxy answering 200 with an error page establishes nothing about what the
// publisher serves and the next fetch may return the real document.
var ErrManifestUnusable = errors.New("probe: publisher manifest broke a document rule")

// HTTPDoer is the minimal interface the Prober needs from an *http.Client.
type HTTPDoer = forawellknown.HTTPDoer

// Options tunes the Prober.
type Options struct {
	TTL     time.Duration
	Timeout time.Duration
	Scheme  string
	// Clk is the time source consulted to stamp Result.FetchedAt on the
	// success path and to drive the shared cache's TTL comparisons.
	// Defaults to clock.System{}.
	Clk clock.Clock
}

// manifestGetter is the narrow read the Prober needs from the shared
// forawellknown.Cache: fetch a host's publisher manifest, surfacing 404 as
// forawellknown.ErrNoDocument or forawellknown.ErrVersionRefused.
// *forawellknown.Cache satisfies it.
type manifestGetter interface {
	Get(ctx context.Context, host string) (*forawellknown.Manifest, error)
}

// Prober is a thin adapter over forawellknown.Cache that translates
// ErrNoDocument, ErrVersionRefused, the document-rule failures and fetch
// failures into the typed ErrManifestMissing / ErrManifestVersionRefused /
// ErrManifestUnusable / ErrProbeFailed surface the Broker resolve handler
// refuses on.
type Prober struct {
	cache  manifestGetter
	logger *slog.Logger
	clk    clock.Clock
}

// New constructs a Prober backed by a fresh in-process forawellknown.Cache.
// ExpectRole pins fetched manifests to ROLE_PUBLISHER so a misrouted document
// is rejected before routing. To share one Cache across Probers in the same
// process, construct the Cache directly and use NewFromCache instead.
func New(client HTTPDoer, logger *slog.Logger, opts Options) *Prober {
	// client is REQUIRED and injected: the SSRF guard is SDK-owned, so the caller
	// constructs it once from resolvers.NewGuardedClientFromEnv at the composition
	// root and passes it here (never an unguarded http.DefaultClient, and this
	// package never re-wraps the SDK factory as a fallback).
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Clk == nil {
		opts.Clk = clock.System{}
	}
	cache := forawellknown.NewCache(forawellknown.CacheOptions{
		Client:     client,
		Scheme:     opts.Scheme,
		TTL:        opts.TTL,
		Timeout:    opts.Timeout,
		Clk:        opts.Clk,
		ExpectRole: forawellknown.RolePublisher,
	})
	return &Prober{cache: cache, logger: logger, clk: opts.Clk}
}

// NewFromCache wires a Prober onto an externally-owned Cache so Broker and
// Exchange can share one forawellknown.Cache instance. clk drives the
// FetchedAt stamp on the success path; pass clock.System{} in production.
func NewFromCache(cache *forawellknown.Cache, logger *slog.Logger, clk clock.Clock) *Prober {
	if logger == nil {
		logger = slog.Default()
	}
	if clk == nil {
		clk = clock.System{}
	}
	return &Prober{cache: cache, logger: logger, clk: clk}
}

// Probe returns the publisher manifest for a domain, fetching or hitting
// cache as needed. The success path returns the manifest with a zero
// error. A domain that does not host fora.json yields
// (Result{}, ErrManifestMissing); a manifest at a document version this
// reader does not accept (forawellknown ErrVersionRefused) yields
// (Result{}, ErrManifestVersionRefused); a manifest that arrived and broke a
// document rule (forawellknown ErrSchemaInvalid or ErrRoleMismatch) yields
// (Result{}, ErrManifestUnusable); everything else that stopped a manifest being
// read yields (Result{}, ErrProbeFailed) wrapping the underlying cause.
//
// The split between the last two is the one that decides whether a caller is
// told to try again. A transport failure, a non-2xx status, or a body that never
// parsed (ErrNotJSON, which the fetch path wraps in ErrFetch) may all clear on
// the next attempt, so they stay on the outage side. A document that arrived and
// broke a rule returns the same answer forever, so it gets a verdict of its own
// the way a refused document version already does. Without that arm a publisher
// whose manifest names the wrong role is re-probed for as long as it stays
// wrong, and the agent asking about it is told the origin is momentarily down.
func (p *Prober) Probe(ctx context.Context, domain string) (Result, error) {
	domain = strings.TrimSpace(strings.ToLower(domain))
	if domain == "" {
		return Result{}, ErrInvalidDomain
	}
	m, err := p.cache.Get(ctx, domain)
	switch {
	case err == nil:
		return Result{
			Manifest:  toBrokerManifest(m),
			FetchedAt: p.clk.Now(),
		}, nil
	case errors.Is(err, forawellknown.ErrNoDocument):
		return Result{}, ErrManifestMissing
	case errors.Is(err, forawellknown.ErrSchemaInvalid), errors.Is(err, forawellknown.ErrRoleMismatch):
		// Deliberately not logged here, for the reason the version arm below
		// states: the caller records this refusal under its own event, with the
		// request_id a line from this plain logger could not carry.
		return Result{}, fmt.Errorf("%w: %w", ErrManifestUnusable, err)
	case errors.Is(err, forawellknown.ErrVersionRefused):
		// Deliberately not logged here, unlike the arm below. This Prober holds a
		// plain logger with no request on it, and the one caller of Probe already
		// records this refusal through reqctx under its own event — so a line
		// here would be the same fact a second time, minus the request_id that
		// makes it traceable.
		return Result{}, fmt.Errorf("%w: %w", ErrManifestVersionRefused, err)
	default:
		// DEBUG, for the reason the arms above are silent: the one caller
		// records this failure itself, at WARN and with the request_id this
		// plain logger cannot carry, so this line only repeats it for whoever
		// is tracing the probe.
		p.logger.DebugContext(ctx, "broker.probe",
			"domain", domain, "err", err)
		return Result{}, fmt.Errorf("%w: %w", ErrProbeFailed, err)
	}
}

func toBrokerManifest(m *forawellknown.Manifest) Manifest {
	if m == nil {
		return Manifest{}
	}
	out := Manifest{
		Ver:       m.GetVer(),
		Provider:  m.GetDomain(),
		Exchanges: make([]AuthorizedExchange, 0, len(m.GetExchanges())),
	}
	for _, ex := range m.GetExchanges() {
		out.Exchanges = append(out.Exchanges, AuthorizedExchange{
			Domain:   ex.GetDomain(),
			Endpoint: ex.GetEndpoint(),
			// Supported profiles are a manifest-level field; forward them
			// unchanged per Exchange entry so Broker routing keeps working
			// without a schema rev of the broker's view.
			SupportedProfiles: m.GetSupportedProfiles(),
		})
	}
	return out
}
