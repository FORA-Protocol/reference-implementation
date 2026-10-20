package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// Terms is the licensing a publisher agreed for one site on one seat: which
// uses they permit, whether attribution is required, and at what price.
//
// It is the shared control set the public preview page and the console both
// present, stored as the publisher answered it. What is NOT stored is the
// license terms those answers produce: those carry a currency and a metering
// unit that belong to the deployment, and freezing one deployment's settings
// into every publisher's row would leave no way to correct them. The answers
// are durable; the terms are derived from them whenever they are needed.
type Terms struct {
	// PricingModel is "free" or "per_unit".
	PricingModel string `json:"pricing_model"`
	// Rate is what one access costs, as a decimal string, and is empty for a
	// free arrangement.
	Rate string `json:"rate,omitempty"`
	// PermittedFunctions are the uses the publisher allows. Everything the
	// vocabulary offers and this list does not carry is refused.
	PermittedFunctions []string `json:"permitted_functions"`
	// ProhibitedFunctions are the uses the publisher prohibits outright, which
	// the built term states rather than leaving them merely unpermitted.
	ProhibitedFunctions []string `json:"prohibited_functions,omitempty"`
	// AttributionRequired says whether every use must credit the publisher.
	AttributionRequired bool `json:"attribution_required"`
}

// Controls renders the terms as the shared builder's control set.
func (t Terms) Controls() termspreset.Controls {
	return termspreset.Controls{
		PricingModel:        t.PricingModel,
		Rate:                t.Rate,
		PermittedFunctions:  t.PermittedFunctions,
		ProhibitedFunctions: t.ProhibitedFunctions,
		AttributionRequired: t.AttributionRequired,
	}
}

// validateTerms refuses an arrangement that could not be turned into license
// terms, and returns it in the form that is stored.
//
// It validates by BUILDING the terms with the shared builder and discarding
// them, rather than by checking the answers against a list of its own. The
// builder is what the generated feed and the catalogue import will use, so a
// value this accepts and that cannot builds is a site that registers cleanly
// and then produces nothing -- the failure arriving days later, at import,
// naming a publisher who did nothing wrong.
func validateTerms(in Terms, defaults termspreset.Defaults) (Terms, error) {
	out := Terms{
		PricingModel:        strings.TrimSpace(in.PricingModel),
		Rate:                strings.TrimSpace(in.Rate),
		PermittedFunctions:  normalizeFunctions(in.PermittedFunctions),
		ProhibitedFunctions: normalizeFunctions(in.ProhibitedFunctions),
		AttributionRequired: in.AttributionRequired,
	}
	if len(out.ProhibitedFunctions) == 0 {
		// Stored without the key rather than as an empty list, so a row
		// written before prohibitions were carried and one that has none read
		// back the same.
		out.ProhibitedFunctions = nil
	}
	if len(out.PermittedFunctions) == 0 {
		// A site that permits nothing is one no agent can license at all. It
		// registers cleanly, produces a feed, and refuses every request against
		// it -- which reads to the publisher as the platform being broken.
		return Terms{}, exchange.Newf(exchange.KindInvalidRequest,
			"at least one permitted use is required")
	}
	if _, err := termspreset.FromControls(out.Controls(), defaults); err != nil {
		return Terms{}, exchange.Wrap(exchange.KindInvalidRequest, err,
			"the licensing you chose cannot be turned into license terms")
	}
	return out, nil
}

// normalizeFunctions trims and de-duplicates the permitted uses, preserving the
// order they arrived in. Order is the publisher's own: the first term built
// from them is the headline an agent is shown.
func normalizeFunctions(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, f := range in {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// encodeTerms renders the terms for storage.
func encodeTerms(t Terms) ([]byte, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return nil, exchange.Wrap(exchange.KindInternal, err, "encode listing terms")
	}
	return raw, nil
}

// encodeOptionalTerms renders terms for storage, or nil -- stored as NULL -- for
// a site saved before its terms were chosen.
func encodeOptionalTerms(t *Terms) ([]byte, error) {
	if t == nil {
		return nil, nil
	}
	return encodeTerms(*t)
}

// optionalTerms reads a listing's terms for the console's view of the site:
// nil when the publisher has not chosen them yet, and the stored answers
// otherwise.
//
// Terms that cannot be read are reported as the zero value rather than failing
// the whole list: one hand-edited row must not make every other site on the
// account unreadable, and the console shows a site with no licensing as one
// that needs attention.
func optionalTerms(raw []byte, domain string) *Terms {
	if len(raw) == 0 {
		return nil
	}
	t, _ := DecodeTerms(raw, domain)
	return &t
}

// DecodeTerms reads terms back out of a listing row. The console's view of a
// registration and the automatic catalog import both read the stored document
// through it, so there is one reading of the shape encodeTerms writes.
//
// A row that does not decode is an operator-written or hand-edited document,
// not a caller fault, so it surfaces as an internal error naming the site
// rather than as something the publisher could fix.
func DecodeTerms(raw []byte, domain string) (Terms, error) {
	if len(raw) == 0 {
		return Terms{}, exchange.Newf(exchange.KindInternal,
			"site %q has no licensing recorded", domain)
	}
	var t Terms
	if err := json.Unmarshal(raw, &t); err != nil {
		return Terms{}, exchange.Wrap(exchange.KindInternal, err,
			fmt.Sprintf("the licensing recorded for site %q cannot be read", domain))
	}
	return t, nil
}

// normalizeSitemapURL checks that the sitemap is an address on the site being
// registered.
//
// Empty is allowed: a publisher who has no sitemap yet can still register, set
// their site up and publish one later. What is refused is an address somewhere
// else, which is the shape that matters -- automatic import fetches this URL
// under the publisher's own authorization, so an address on another host would
// have this deployment read a site nobody authorized.
//
// Both http and https are accepted. The console fills this field from the
// sitemap its own discovery found, and that discovery reads plain http on a
// local stack whose origins serve no TLS (CONSOLE_FETCH_SCHEME), so an
// https-only rule would refuse the address the console itself supplied.
//
// An address carrying credentials is refused. Go's HTTP client turns userinfo
// into a Basic authorization header, so any fetch of the stored address would
// send them, and the stored value would keep a secret in a row every operator
// reads.
func normalizeSitemapURL(raw, domain string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", exchange.Newf(exchange.KindInvalidRequest,
			"the sitemap address %q is not a URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", exchange.Newf(exchange.KindInvalidRequest,
			"the sitemap address must start with https:// or http://")
	}
	if u.User != nil {
		return "", exchange.Newf(exchange.KindInvalidRequest,
			"the sitemap address must not carry a user name or password")
	}
	host := strings.ToLower(u.Hostname())
	if host != domain && !strings.HasSuffix(host, "."+domain) {
		return "", exchange.Newf(exchange.KindInvalidRequest,
			"the sitemap address must be on %s, not %s", domain, host)
	}
	return u.String(), nil
}
