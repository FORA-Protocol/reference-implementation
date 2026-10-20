// Package app is the publisher console's composition root: it assembles the
// sign-in, the Exchange client, the file builder and the pages into one HTTP
// handler, and decides what is gated behind what.
package app

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/session"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/wizard"
)

// Config is everything the console needs, resolved by the caller.
type Config struct {
	Logger   *slog.Logger
	Accounts repo.AccountRepo
	Upstream auth.Upstream
	// SessionKey seals the cookies. It is required: an ephemeral key would
	// sign every person out on each restart, and a console is a place people
	// come back to across a DNS change and a CDN deploy.
	SessionKey []byte
	// SecureCookies follows the console's own origin. It is false only on a
	// local http bring-up, where a secure cookie would never come back.
	SecureCookies bool
	// PublicOrigin is the console's own address. Build refuses an https origin
	// with SecureCookies off, which is what makes the attribute's config-driven
	// value safe: a production deployment cannot end up serving a session
	// cookie a network can read.
	PublicOrigin string
	// Clk is the time source. Nil takes the system clock.
	Clk      clock.Clock
	Exchange *exchangeclient.Client
	// SigningKey is the console's own Ed25519 key. The public half is published
	// at the key directory this service serves, which is where the Exchange
	// reads it from.
	SigningKey ed25519.PrivateKey
	Deployment setup.Deployment
	// Pages reads a publisher's own site: the home page's headers for the
	// network lookup, and a page worth showing for the catalogue preview. It
	// satisfies both narrow ports the two callers hold.
	Pages    SiteReader
	Detector wizard.Detector
	// DefaultRate fills the price field in when a publisher chooses to charge.
	DefaultRate string
	// SupportEmail is the address a publisher writes to when their network is
	// not one the console can set up for them.
	SupportEmail string
	// DashboardPageSize is how many rows a dashboard list page shows. Zero is
	// dashboard.DefaultPageSize, the Exchange's own default page.
	DashboardPageSize int
}

// SiteReader is what the console needs from a reader of publishers' sites. It
// is the union of the two narrow ports its callers hold, declared here at the
// composition root because this is where the one implementation is wired into
// both.
type SiteReader interface {
	setup.PageReader
	wizard.SiteReader
}

// Build assembles the console's HTTP handler.
//
// The gating is the shape of this function, and it is worth reading as one
// piece: the sign-in routes and the styles are open, everything else needs a
// session, and the pages that write anything need a session whose address the
// provider verified. Nothing is gated twice here and nothing is left ungated by
// being added to the wrong group -- a route is in exactly one of the three.
func Build(cfg Config) (http.Handler, error) {
	if len(cfg.SessionKey) != session.KeyLen {
		return nil, errors.New("app: a 32-byte session key is required")
	}
	if cfg.Exchange == nil {
		return nil, errors.New("app: an Exchange client is required")
	}
	if len(cfg.SigningKey) != ed25519.PrivateKeySize {
		return nil, errors.New("app: the console's Ed25519 signing key is required")
	}
	if strings.HasPrefix(cfg.PublicOrigin, "https://") && !cfg.SecureCookies {
		return nil, errors.New(
			"app: an https console must set secure cookies: a session cookie served " +
				"without the secure attribute travels over any downgraded connection")
	}
	clk := cfg.Clk
	if clk == nil {
		clk = clock.System{}
	}
	codec, err := session.NewCodec(cfg.SessionKey, clk)
	if err != nil {
		return nil, err
	}
	renderer, err := web.NewRenderer()
	if err != nil {
		return nil, err
	}
	signin, err := auth.New(auth.Config{
		Upstream:     cfg.Upstream,
		PublicOrigin: cfg.PublicOrigin,
		RenderSignedOut: func(w http.ResponseWriter, r *http.Request) {
			renderer.RenderMessage(w, r, http.StatusOK, "Signed out",
				"You are signed out.", auth.LoginPath, "Sign in")
		},
		Accounts:      cfg.Accounts,
		Codec:         codec,
		SecureCookies: cfg.SecureCookies,
		AfterLogin:    web.HomePath,
		Clk:           clk,
		RenderError: func(w http.ResponseWriter, r *http.Request, status int, message string) {
			renderer.RenderMessage(w, r, status, "Sign-in", message, auth.LoginPath, "Try again")
		},
	})
	if err != nil {
		return nil, err
	}
	h, err := consoleHandlers(cfg, renderer, clk)
	if err != nil {
		return nil, err
	}
	directory, err := directoryHandler(cfg.SigningKey, clk)
	if err != nil {
		return nil, err
	}
	protect, err := crossOriginProtection(cfg.PublicOrigin, renderer)
	if err != nil {
		return nil, err
	}
	return reqctx.RequestIDMiddleware(cfg.Logger, protect.Handler(routes(signin, h, directory))), nil
}

// consoleHandlers wires the service layer and the transports over it: the
// console's own pages, the dashboard's pages, and the wizard's JSON routes.
func consoleHandlers(cfg Config, renderer *web.Renderer, clk clock.Clock) (handlers, error) {
	bundles := setup.NewBuilder(cfg.Deployment, cfg.Pages, cfg.Exchange)
	svc, err := wizard.New(wizard.Config{
		Registry:       cfg.Exchange,
		Bundles:        bundles,
		Detector:       cfg.Detector,
		Pages:          cfg.Pages,
		SupportEmail:   cfg.SupportEmail,
		DefaultRate:    cfg.DefaultRate,
		ExchangeDomain: cfg.Deployment.ExchangeDomain,
	})
	if err != nil {
		return handlers{}, err
	}
	resumer := wizard.NewResumer(svc)
	dash := dashboard.NewService(cfg.Exchange.Reads(), cfg.Exchange, clk, cfg.DashboardPageSize)
	pages, err := web.New(web.Config{
		Renderer:  renderer,
		Sites:     wizard.NewSites(cfg.Exchange, bundles, cfg.Deployment.ExchangeDomain),
		Wizard:    svc,
		Resumer:   resumer,
		Dashboard: dash,
	})
	if err != nil {
		return handlers{}, err
	}
	dashPages, err := web.NewDashboard(renderer, dash)
	if err != nil {
		return handlers{}, err
	}
	api, err := web.NewWizardAPI(svc, resumer)
	if err != nil {
		return handlers{}, err
	}
	return handlers{pages: pages, dash: dashPages, api: api}, nil
}

// crossOriginProtection refuses every state-changing request a browser sends
// from another site: every POST the wizard makes, and the sign-out.
//
// It is the console's one CSRF control. A browser labels each request with
// where it came from (Sec-Fetch-Site, or Origin on an older one), and a request
// another site caused is refused before any route sees it. GET, HEAD and
// OPTIONS pass, which is why nothing reached by GET may change anything. A
// request carrying neither header passes too: a browser sends at least one of
// them on every cross-origin POST, so such a request is either not from a
// browser or from this console's own origin.
//
// The console's own public origin is trusted explicitly. Behind a proxy that
// rewrites the Host header, an older browser's Origin would otherwise not match
// the Host the console sees, and the publisher's own pages would be refused.
func crossOriginProtection(publicOrigin string, renderer *web.Renderer) (*http.CrossOriginProtection, error) {
	protect := http.NewCrossOriginProtection()
	if publicOrigin != "" {
		if err := protect.AddTrustedOrigin(publicOrigin); err != nil {
			return nil, fmt.Errorf("app: the console's public origin: %w", err)
		}
	}
	protect.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, web.APIPrefix) {
			web.APICrossOrigin(w, r)
			return
		}
		renderer.RenderMessage(w, r, http.StatusForbidden, "Refused",
			"That request came from another site, so nothing was changed.", web.DomainsPath, "Back to your domains")
	}))
	return protect, nil
}

// healthz answers the container probe. It reports that the process is serving,
// and deliberately checks nothing else: a console that cannot reach the
// Exchange is still a console a person can sign in to and read, and a probe
// that failed on the Exchange's outage would restart a healthy process.
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
