// Package exchangeclient calls the Exchange's publisher-registration routes on
// behalf of the person signed in to the console.
//
// Every call is signed under RFC 9421 with the console's own key, published in
// the console's own Web Bot Auth directory, and carries the acting person as an
// asserted actor. That split is the whole trust model: the signature proves
// which process is speaking, the actor says which person it is speaking for,
// and the Exchange decides what that person may do. The console never asserts
// what someone owns.
package exchangeclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/core"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forahttpsig"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// maxResponseBytes bounds what one Exchange answer can make the console read.
// The Exchange is a trusted party, but a bound is what keeps a misconfigured
// endpoint -- a proxy error page, a wrong URL -- from being read without limit.
const maxResponseBytes = 1 << 20

// signatureTTL is how long one outbound signature stays valid. Short, because
// the console and the Exchange are one hop apart and a signature that outlives
// the call it was made for is only useful to somebody who captured it.
const signatureTTL = 2 * time.Minute

// The contract's shapes, under the names this package's callers already use.
// They are aliases rather than copies: consoleapi is the one definition the
// Exchange decodes with, so a field added there is a field sent here.
type (
	// Actor is the person a call is made for.
	Actor = consoleapi.Actor
	// Publisher is the payee a person acts for.
	Publisher = consoleapi.Publisher
	// Terms is the licensing a publisher agreed for one site.
	Terms = consoleapi.Terms
	// Registration is one site as the Exchange reports it.
	Registration = consoleapi.Registration
	// Registrations is what the console opens on: the payee, and every site.
	Registrations = consoleapi.Registrations
	// CheckResult is the outcome of asking the Exchange to read a site's
	// published document.
	CheckResult = consoleapi.CheckResult
	// PreflightResult is the outcome of asking what a check would say about a
	// domain, saved or not.
	PreflightResult = consoleapi.PreflightResult
	// NewSite is what saving a site takes.
	NewSite = consoleapi.NewSite
	// DomainTerms is what setting a saved site's terms takes.
	DomainTerms = consoleapi.DomainTerms
)

// Client is the console's handle on one Exchange.
type Client struct {
	base string
	http *http.Client
	// clk dates the cached denomination.
	clk clock.Clock
	// denomination is the Exchange's last answer about what it prices in.
	denomination denominationCache
}

// Config is what the client needs from the composition root.
type Config struct {
	// BaseURL is the Exchange's origin, scheme included.
	BaseURL string
	// Directory is the console's own Web Bot Auth directory origin, which the
	// Exchange fetches to learn the key below. It travels in every signature as
	// the covered Signature-Agent value, so it cannot be repointed by anyone
	// who captures a request.
	Directory string
	// SigningKey is the console's Ed25519 private key.
	SigningKey ed25519.PrivateKey
	// Base is the transport to sign over. Nil takes the default.
	Base http.RoundTripper
	// Clk sources the signature window. Nil takes the system clock.
	Clk clock.Clock
	// Timeout bounds one call end to end.
	Timeout time.Duration
}

// New builds the client. It refuses a configuration that could not produce a
// verifiable call rather than failing on the first request a publisher makes.
func New(cfg Config) (*Client, error) {
	base := strings.TrimSuffix(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		return nil, errors.New("exchangeclient: the Exchange base URL is required")
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("exchangeclient: the Exchange base URL is not a URL: %w", err)
	}
	if strings.TrimSpace(cfg.Directory) == "" {
		return nil, errors.New("exchangeclient: the console's own directory origin is required")
	}
	if len(cfg.SigningKey) != ed25519.PrivateKeySize {
		return nil, errors.New("exchangeclient: an Ed25519 signing key is required")
	}
	clk := cfg.Clk
	if clk == nil {
		clk = clock.System{}
	}
	// Registration is deliberately retry-safe, so the console does re-send an
	// identical body -- a double submit, a re-entered form. The transport puts a
	// fresh nonce on every FORA signature, so two such requests in one
	// wall-clock second carry different signatures and the second is answered
	// idempotently rather than refused as a replay. The window plays no part in
	// that: it is the SDK's plain clock window, now() to now()+signatureTTL.
	rt, err := forahttpsig.New(cfg.Base, cfg.Directory, cfg.SigningKey,
		core.ClockWindow(clk.Now, signatureTTL),
		forahttpsig.WithFORATargets(func(r *http.Request) bool {
			return strings.HasPrefix(r.URL.Path, consoleapi.PathPrefix)
		}))
	if err != nil {
		return nil, fmt.Errorf("exchangeclient: build signing transport: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		base: base,
		clk:  clk,
		http: &http.Client{
			Transport: rt,
			Timeout:   timeout,
			// A registration endpoint that answered a redirect is a
			// misconfiguration, and following one would re-send a signed body to
			// a host the signature does not name.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return errors.New("exchangeclient: the Exchange answered a redirect")
			},
		},
	}, nil
}

// RegisterPublisher mints or returns the payee this person acts for.
func (c *Client) RegisterPublisher(ctx context.Context, actor Actor, displayName string) (Publisher, error) {
	var out Publisher
	err := c.call(ctx, consoleapi.PathRegisterPublisher, consoleapi.RegisterPublisherRequest{
		Actor: actor, DisplayName: displayName,
	}, &out)
	return out, err
}

// RegisterDomain saves the site under the publisher's payee and lists it on
// the Exchange's seat, not verified. The Exchange reads nothing from the site to
// do it; CheckDomain is what verifies it.
func (c *Client) RegisterDomain(ctx context.Context, in NewSite) (Registration, error) {
	var out Registration
	err := c.call(ctx, consoleapi.PathRegisterDomain, in, &out)
	return out, err
}

// SetDomainTerms sets the terms of one of the publisher's sites that is saved
// and not verified yet, and answers with the site as it now stands. A verified
// site's terms are refused.
func (c *Client) SetDomainTerms(ctx context.Context, in DomainTerms) (Registration, error) {
	var out Registration
	err := c.call(ctx, consoleapi.PathSetDomainTerms, in, &out)
	return out, err
}

// CheckDomain asks the Exchange to read a saved site's published manifest and
// to verify the site when it proves the publisher controls it.
func (c *Client) CheckDomain(ctx context.Context, actor Actor, publisherID, domain string) (CheckResult, error) {
	var out CheckResult
	err := c.call(ctx, consoleapi.PathCheckDomain, consoleapi.DomainRequest{
		Actor: actor, PublisherID: publisherID, Domain: domain,
	}, &out)
	return out, err
}

// PreflightDomain asks what a check would say about a domain right now,
// without saving or verifying anything. It is what the add-a-domain wizard
// checks each time the publisher says they have published their document: the
// Exchange reads the document and reports, and nothing is written.
func (c *Client) PreflightDomain(
	ctx context.Context, actor Actor, publisherID, domain string,
) (PreflightResult, error) {
	var out PreflightResult
	err := c.call(ctx, consoleapi.PathPreflightDomain, consoleapi.DomainRequest{
		Actor: actor, PublisherID: publisherID, Domain: domain,
	}, &out)
	return out, err
}

// HasDomains reports whether the person's publisher has saved a domain,
// verified or not: whether they are past their first one. The console decides
// from it whether a page draws the dashboard's sections and whether the wizard
// offers to cancel.
func HasDomains(r Registrations) bool {
	return r.Registered && len(r.Sites) > 0
}

// HasVerifiedDomain reports whether any of the person's domains is verified,
// which is what anything can be sold from. The console decides from it whether
// a person opens on the dashboard, where sales are, or on their domains, where
// a domain that is not verified says what it still needs.
func HasVerifiedDomain(r Registrations) bool {
	for _, site := range r.Sites {
		if site.Active() {
			return true
		}
	}
	return false
}

// ListRegistrations reads back what this person has registered.
func (c *Client) ListRegistrations(ctx context.Context, actor Actor) (Registrations, error) {
	var out Registrations
	err := c.call(ctx, consoleapi.PathListRegistrations, consoleapi.ListRegistrationsRequest{Actor: actor}, &out)
	return out, err
}

// call performs one signed request and decodes the answer.
func (c *Client) call(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("exchangeclient: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("exchangeclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The Exchange logs under the same correlation id, so one publisher action
	// is one thread across both services' logs.
	// A call made outside a request still carries an id, so the Exchange's log
	// line for it can be found.
	req.Header.Set(reqctx.HeaderRequestID, reqctx.IDOrNew(ctx))
	resp, err := c.http.Do(req)
	if err != nil {
		return &Error{Status: 0, Message: "the Exchange could not be reached", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &Error{Status: resp.StatusCode, Message: "the Exchange's answer could not be read", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return errorFrom(resp.StatusCode, raw)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Status: resp.StatusCode, Message: "the Exchange's answer could not be decoded", Err: err}
	}
	return nil
}
