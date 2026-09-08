package registry

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/sync/errgroup"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraroute"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// DefaultInterval is the polling cadence for exchange health.
const DefaultInterval = 30 * time.Second

// maxConcurrentProbes bounds how many exchanges one pass probes at once. Every
// down exchange costs the full probe timeout, so a sequential pass over ten of
// them overruns the 30s interval and slows recovery for everyone else — exactly
// when the refresher matters most, since one network fault marks many rows down
// at once. Bounded so a large registry cannot open a connection per row.
const maxConcurrentProbes = 8

// EndpointResolver resolves an exchange domain to the endpoint the broker
// actually talks to, read from that exchange's own /.well-known/fora.json.
// Declared here rather than taken from the resolve package so the refresher
// depends on the one method it uses.
type EndpointResolver interface {
	ResolveEndpoint(ctx context.Context, host string) (string, error)
}

// Refresher periodically probes each exchange's /healthz and persists the
// latest probe state back to broker.exchanges.
//
// It probes every non-BLOCKED exchange, the ones already DOWN or REFUSED
// included. That is what lets an exchange recover on its own: probing only the
// LIVE rows makes the state a one-way ratchet, because the first failed probe
// removes the row from the list the next pass reads. It applies to a refused
// manifest as much as to an outage — an exchange that starts publishing a
// document version this build accepts has to be able to come back.
//
// It probes the endpoint from the exchange's OWN well-known, through the same
// resolver discovery uses, never the registry's endpoint column. That column is
// operator-supplied bootstrap an exchange can outgrow by moving; probing it
// would measure an address routing never talks to, and a stale column would
// strand an exchange whose real /healthz answers 200.
//
// It also writes that resolved address back to the column, so the registry
// holds ONE opinion about where an exchange is. The relay's SSRF allowlist
// compares a caller-supplied endpoint against that column, and a column nothing
// maintains would refuse a live exchange as unregistered for good. The value
// written is still one the exchange advertised, vetted by the resolver against
// the registered domain.
//
// A pass probes up to maxConcurrentProbes exchanges at once.
type Refresher struct {
	repo      repo.ExchangeHealthRepo
	endpoints EndpointResolver
	http      *http.Client
	logger    *slog.Logger
	interval  time.Duration
	timeout   time.Duration
}

// NewRefresher constructs a Refresher.
//
// httpClient is REQUIRED and panics at wiring time when nil, rather than
// fabricating a default. The pass dials an address the exchange advertises about
// itself and writes it back to the column the relay's allowlist reads, so this
// client is one of the broker's outbound trust boundaries; a fabricated default
// would be unguarded, and a nil argument is a wiring bug that must fail loud. The
// exchange client pool refuses a nil client for the same reason.
//
// endpoints is required for the same class of reason: probing anything but the
// address the exchange's own well-known advertises measures the wrong thing. A
// nil resolver would panic on the first probe inside the goroutine the
// composition root launches, where nothing recovers, taking the process down at
// start-up instead of failing the wiring.
func NewRefresher(
	r repo.ExchangeHealthRepo, endpoints EndpointResolver, httpClient *http.Client,
	logger *slog.Logger, interval time.Duration,
) *Refresher {
	if httpClient == nil {
		panic("registry: NewRefresher requires an HTTP client")
	}
	if endpoints == nil {
		panic("registry: NewRefresher requires an endpoint resolver")
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Refresher{
		repo: r, endpoints: endpoints, http: httpClient,
		logger: logger, interval: interval, timeout: 3 * time.Second,
	}
}

// Run blocks until ctx is cancelled, polling every interval.
func (r *Refresher) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	if err := r.RefreshOnce(ctx); err != nil {
		r.logger.WarnContext(ctx, "broker.registry.refresh", "err", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.RefreshOnce(ctx); err != nil {
				r.logger.WarnContext(ctx, "broker.registry.refresh", "err", err)
			}
		}
	}
}

// RefreshOnce runs one probe pass over every non-BLOCKED exchange. Run calls it
// on a ticker; it is exported so a caller can drive a pass at a chosen moment.
//
// It returns an error only when the pass could not run at all — the one outcome
// a caller cannot observe any other way. A probe or write that fails for ONE
// exchange is logged and does not abort the pass.
func (r *Refresher) RefreshOnce(ctx context.Context) error {
	entries, err := r.repo.ListUnblocked(ctx)
	if err != nil {
		r.logger.WarnContext(ctx, "broker.registry.list", "err", err)
		return err
	}
	// Deliberately not errgroup.WithContext: a failing probe must not cancel its
	// siblings, so every func returns nil and per-exchange faults are logged.
	var g errgroup.Group
	g.SetLimit(maxConcurrentProbes)
	for _, m := range entries {
		g.Go(func() error {
			r.refreshOne(ctx, m)
			return nil
		})
	}
	_ = g.Wait()
	return nil
}

// refreshOne probes one exchange and persists whatever the pass learned that
// the row does not already say.
func (r *Refresher) refreshOne(ctx context.Context, m repo.Exchange) {
	endpoint, state := r.probe(ctx, m.Domain)
	// A pass that could not resolve the well-known learned nothing about WHERE
	// the exchange is, so it keeps the stored address. Blanking the column would
	// refuse every relay as unregistered — a permanent answer to a transient
	// fault.
	endpoint = cmp.Or(endpoint, m.Endpoint)
	if m.ProbeState == state && m.Endpoint == endpoint {
		return
	}
	if err := r.repo.SetProbeResult(ctx, m.ID, endpoint, state); err != nil {
		r.logger.WarnContext(ctx, "broker.registry.set_probe_result",
			"exchange_id", m.ID, "err", err)
		return
	}
	if m.Endpoint != endpoint {
		// Recorded because an operator has no other way to see it: routing has
		// always followed the well-known silently, so a bootstrap file that was
		// wrong from day one looks exactly like one that is right.
		r.logger.InfoContext(ctx, "broker.registry.endpoint_changed",
			"exchange_id", m.ID, "domain", m.Domain,
			"was", m.Endpoint, "now", endpoint)
	}
	if m.ProbeState != state {
		// The transition is what an operator needs a timestamp for. A routing
		// skip says an exchange is not routable NOW; only this says when it
		// stopped being routable and when it came back, which is what makes an
		// outage measurable afterwards. Both states the row can leave LIVE for
		// ride the same record, so one grep reads the whole history of a row:
		// the value says which, and a DOWN that becomes REFUSED is itself worth
		// seeing, because the exchange came back and is still unusable.
		r.logger.InfoContext(ctx, "broker.registry.probe_state_changed",
			"exchange_id", m.ID, "domain", m.Domain,
			"was", string(m.ProbeState), "now", string(state))
	}
}

// probe resolves the exchange's advertised endpoint, asks it for /healthz, and
// returns the address it measured with what it concluded. An unresolvable
// well-known yields no address and a non-LIVE state: routing resolves the same
// way, so such an exchange cannot be routed to either.
func (r *Refresher) probe(ctx context.Context, domain string) (string, repo.ProbeState) {
	endpoint, err := r.endpoints.ResolveEndpoint(ctx, domain)
	if err != nil {
		// A VERDICT and an outage both leave the exchange unroutable, and they
		// are persisted apart because they are not the same operational fact and
		// the two request paths owe agents different answers about them. A
		// verdict repeats identically on every pass until somebody changes the
		// exchange's manifest or this build; it is not an outage that will clear,
		// and reporting it as one sends an agent round a loop that cannot
		// terminate. Logged at Warn under its own event for the same reason.
		if foraroute.IsVerdict(err) {
			r.logger.WarnContext(ctx, "broker.registry.resolve.refused",
				"domain", domain, "err", err)
			return "", repo.ProbeRefused
		}
		r.logger.InfoContext(ctx, "broker.registry.resolve",
			"domain", domain, "err", err)
		return "", repo.ProbeDown
	}
	// Canonicalized the way the registry stores it, so comparing against the
	// column answers "has it moved" rather than "is it spelled differently" — a
	// manifest advertising a trailing slash would rewrite the row every pass.
	endpoint = repo.CanonicalEndpoint(endpoint)
	if !r.ping(ctx, endpoint) {
		return endpoint, repo.ProbeDown
	}
	return endpoint, repo.ProbeLive
}

// ping asks one canonical endpoint for /healthz.
func (r *Refresher) ping(ctx context.Context, endpoint string) bool {
	url := endpoint + "/healthz"
	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}
