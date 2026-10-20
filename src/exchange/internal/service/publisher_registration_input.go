package service

import (
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitedomain"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// normalizeNewSite validates the caller's input and returns it in the form
// the rows are keyed on. Normalization happens once, here, so the value written
// is the value every later lookup produces from the same input.
func normalizeNewSite(in NewSite, defaults termspreset.Defaults) (NewSite, error) {
	domain, err := normalizeSiteDomain(in.Domain)
	if err != nil {
		return NewSite{}, err
	}
	// The set a publisher can register under is cdn.SelfServe, the one list the
	// console offers from too; why Fastly is not on it is written there. A site
	// whose network is not on it is sent to the support path rather than
	// registered, because there would be no edge package to point them at.
	network := strings.ToLower(strings.TrimSpace(in.CDN))
	if !cdn.Supported(network) {
		return NewSite{}, exchange.Newf(exchange.KindInvalidRequest,
			"cdn %q is not one of the supported integrations (%s)", in.CDN, selfServeList())
	}
	scope, err := normalizePathScope(in.ContentPathScope)
	if err != nil {
		return NewSite{}, err
	}
	var terms *Terms
	if in.Terms != nil {
		validated, err := validateTerms(*in.Terms, defaults)
		if err != nil {
			return NewSite{}, err
		}
		terms = &validated
	}
	sitemap, err := normalizeSitemapURL(in.SitemapURL, domain)
	if err != nil {
		return NewSite{}, err
	}
	return NewSite{
		PublisherID:               in.PublisherID,
		Domain:                    domain,
		CDN:                       network,
		ContentPathScope:          scope,
		Terms:                     terms,
		SitemapURL:                sitemap,
		ImportReplacesManualTerms: in.ImportReplacesManualTerms,
	}, nil
}

// selfServeList names the networks registration admits, in the order the
// console offers them, for the sentence that refuses any other.
func selfServeList() string {
	names := make([]string, 0, len(cdn.SelfServe()))
	for _, p := range cdn.SelfServe() {
		names = append(names, string(p))
	}
	return strings.Join(names, ", ")
}

// normalizeSiteDomain refuses anything that is not a bare hostname.
//
// The rule is shared with the console, which asks a publisher to type the
// value: one copy means a domain the console accepted cannot be refused here
// for its shape, and a domain refused there is refused in the same words.
func normalizeSiteDomain(raw string) (string, error) {
	domain, err := sitedomain.Normalize(raw)
	if err != nil {
		return "", exchange.Newf(exchange.KindInvalidRequest, "%s", err)
	}
	return domain, nil
}

// normalizePathScope trims the entries and refuses one that is not a path. An
// entry without a leading slash would match nothing the edge ever sees, so it
// is a typo that silently narrows the offer to nothing rather than a value to
// accept.
func normalizePathScope(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") {
			return nil, exchange.Newf(exchange.KindInvalidRequest,
				"content path %q must start with /", p)
		}
		out = append(out, p)
	}
	return out, nil
}
