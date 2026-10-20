package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// ProbeTransport is how a pass reaches an exchange.
//
// HTTP carries every request a pass sends: the manifest fetch and the health
// check the manifest names. Both go to addresses the exchange chose, so this is
// one of the broker's outbound trust boundaries, and production wires the
// SDK-guarded client, which refuses a private or metadata address at dial time
// and refuses a plaintext leg unless the deployment allows one. The health
// check therefore passes the same SSRF and scheme guard as the manifest fetch,
// because it goes through the same client.
//
// Scheme is the scheme the exchange's /.well-known/fora.json is fetched over:
// https in production, http on a stack with no TLS. Empty means https.
type ProbeTransport struct {
	HTTP   *http.Client
	Scheme string
}

// errUnusableHealthEndpoint marks a health_endpoint value no pass may dial.
var errUnusableHealthEndpoint = errors.New("registry: manifest names an unusable health endpoint")

// health reads the exchange's manifest and decides whether the exchange is
// answering.
//
// The manifest is fetched on every pass, not served from the endpoint
// resolver's cache, because a cached document says nothing about whether the
// exchange answers now. Then:
//
//   - a manifest that names a health endpoint is judged by that endpoint: LIVE
//     when it answers 200, DOWN otherwise;
//   - a manifest that names none is judged by the fetch itself. A fetched,
//     valid manifest is an answer from the exchange, so the exchange is LIVE.
//
// The protocol makes health_endpoint optional, so an exchange that publishes
// none, and serves no /healthz, is still a conformant exchange the broker must
// be able to route to.
func (r *Refresher) health(ctx context.Context, domain string) repo.ProbeState {
	manifestURL, err := forawellknown.ManifestURL(domain, r.scheme, "")
	if err != nil {
		r.logger.WarnContext(ctx, "broker.registry.manifest.refused", "domain", domain, "err", err)
		return repo.ProbeRefused
	}
	m, err := forawellknown.Fetch(ctx, domain, forawellknown.FetchOptions{
		Client: r.http, Scheme: r.scheme, Timeout: r.timeout, ExpectRole: forawellknown.RoleExchange,
	})
	if err != nil {
		return r.manifestFailure(ctx, domain, err)
	}
	advertised := m.GetHealthEndpoint()
	if advertised == "" {
		return repo.ProbeLive
	}
	target, err := resolveHealthEndpoint(manifestURL, advertised)
	if err != nil {
		r.logger.WarnContext(ctx, "broker.registry.manifest.refused", "domain", domain, "err", err)
		return repo.ProbeRefused
	}
	if !r.ping(ctx, target) {
		return repo.ProbeDown
	}
	return repo.ProbeLive
}

// manifestFailure classifies a manifest fetch that did not produce a usable
// document, the same way the endpoint resolver's failures are classified. A
// document that arrived and broke a rule is a VERDICT: it repeats identically on
// every pass until the exchange republishes, so it is REFUSED. Anything else
// (no answer, a non-2xx status, a body that is not JSON) is an outage the next
// pass may not see, so it is DOWN.
func (r *Refresher) manifestFailure(ctx context.Context, domain string, err error) repo.ProbeState {
	if errors.Is(err, forawellknown.ErrVersionRefused) ||
		errors.Is(err, forawellknown.ErrSchemaInvalid) ||
		errors.Is(err, forawellknown.ErrRoleMismatch) {
		r.logger.WarnContext(ctx, "broker.registry.manifest.refused", "domain", domain, "err", err)
		return repo.ProbeRefused
	}
	r.logger.Log(ctx, logging.LevelFor(ctx, err, slog.LevelWarn), "broker.registry.manifest",
		"domain", domain, "err", err)
	return repo.ProbeDown
}

// resolveHealthEndpoint turns a manifest's health_endpoint into the absolute
// URL a pass probes.
//
// The protocol describes the member only as the health check endpoint URL and
// says nothing more about its form. A relative value is therefore read the way
// RFC 3986 section 5 reads any reference found in a document: resolved against
// the URL the document was served from. So "/healthz" in the manifest of
// exchange.example means https://exchange.example/healthz, and an absolute
// value is taken as it is.
//
// The result must be an http or https URL with a host and no userinfo. Any
// other scheme is not a health check this broker can make. Userinfo is refused
// for the reason the endpoint rule refuses it: net/http would turn it into an
// Authorization header the broker never chose to send.
func resolveHealthEndpoint(manifestURL, advertised string) (string, error) {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return "", fmt.Errorf("%w: manifest URL %q: %w", errUnusableHealthEndpoint, manifestURL, err)
	}
	ref, err := url.Parse(advertised)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", errUnusableHealthEndpoint, advertised, err)
	}
	u := base.ResolveReference(ref)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: %q: scheme %q is not http or https", errUnusableHealthEndpoint, advertised, u.Scheme)
	}
	if u.User != nil {
		return "", fmt.Errorf("%w: %q carries userinfo", errUnusableHealthEndpoint, advertised)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("%w: %q has no host", errUnusableHealthEndpoint, advertised)
	}
	u.Fragment = ""
	return u.String(), nil
}

// ping asks one absolute health URL whether the exchange is answering. Only a
// 200 counts.
func (r *Refresher) ping(ctx context.Context, target string) bool {
	reqCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, http.NoBody)
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
