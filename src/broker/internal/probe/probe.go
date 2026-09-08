// Package probe adapts a per-Broker-instance forawellknown.Cache to the
// Broker's pre-existing Probe API.
//
// v1 requires every publisher to host /.well-known/fora.json. The probe
// surfaces three typed error shapes — ErrManifestMissing (the domain
// returned 404 / no manifest), ErrManifestVersionRefused (a manifest was
// read and carries a document version this build does not accept), and
// ErrProbeFailed (transient fetch, decode, or non-2xx failures). The first
// two are VERDICTS and the third is an outage, which is the distinction
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
// error (ErrManifestMissing, ErrManifestVersionRefused, ErrProbeFailed,
// ErrInvalidDomain) when the domain has no usable manifest; on the error
// paths Result is zero-valued.
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
// ErrNoDocument, ErrVersionRefused and fetch failures into the typed
// ErrManifestMissing / ErrManifestVersionRefused / ErrProbeFailed surface
// the Broker resolve handler refuses on.
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
// (Result{}, ErrManifestVersionRefused); a transient fetch / decode / non-2xx
// failure (forawellknown ErrFetch / ErrSchemaInvalid / ErrRoleMismatch)
// yields (Result{}, ErrProbeFailed) wrapping the underlying cause.
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
	case errors.Is(err, forawellknown.ErrVersionRefused):
		// Deliberately not logged here, unlike the arm below. This Prober holds a
		// plain logger with no request on it, and the one caller of Probe already
		// records this refusal through reqctx under its own event — so a line
		// here would be the same fact a second time, minus the request_id that
		// makes it traceable.
		return Result{}, fmt.Errorf("%w: %w", ErrManifestVersionRefused, err)
	default:
		p.logger.InfoContext(ctx, "broker.probe",
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
