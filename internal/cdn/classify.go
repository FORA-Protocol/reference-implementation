package cdn

import (
	"errors"
	"net"
	"net/http"
	"strings"
)

// errNoResolver refuses a detector with nothing to resolve through.
var errNoResolver = errors.New("cdn: a resolver is required")

// cnameSuffixes are the delegation targets each network hands out. A site
// pointing its name at one of these has that network in front of it.
//
// Akamai's customer-facing edge hostnames end in edgesuite.net (Standard TLS),
// edgekey.net (Enhanced TLS) and akamaized.net; those names then chain on to
// akamaiedge.net or akamai.net. Both ends of that chain are listed because
// which one the resolver hands back is not something to rely on. LookupCNAME
// documents the final canonical name; measured on 2026-09-21 against
// Akamai-fronted sites, net.DefaultResolver returned the first hop (the
// edgekey.net name) for some sites and the far end (akamaiedge.net,
// akamai.net) for others, and both ends for the same site on two calls
// seconds apart, because the answer depends on how much of the chain the
// recursive resolver has cached and returns in one answer. Every entry below
// is a live path, not a fallback. akadns.net is deliberately absent: it is
// Akamai's traffic-management DNS, and a site can use it in front of any
// network.
var cnameSuffixes = map[string]Provider{
	".cloudfront.net":     ProviderCloudFront,
	".cloudflare.com":     ProviderCloudflare,
	".cdn.cloudflare.net": ProviderCloudflare,
	".fastly.net":         ProviderFastly,
	".fastlylb.net":       ProviderFastly,
	".edgekey.net":        ProviderAkamai,
	".edgesuite.net":      ProviderAkamai,
	".akamaized.net":      ProviderAkamai,
	".akamaiedge.net":     ProviderAkamai,
	".akamai.net":         ProviderAkamai,
}

// nsSuffix is the only name-server suffix that identifies a network. Route 53
// and Fastly's own DNS carry no such signal, so there is one entry rather than
// three.
const nsSuffix = ".ns.cloudflare.com"

// ClassifyCNAME reads a delegation. The second result is false when the answer
// names no supported network, which includes the common case of a name that is
// its own answer because it has no CNAME at all.
func ClassifyCNAME(name, target string) (Evidence, bool) {
	target = normalizeName(target)
	name = normalizeName(name)
	if target == "" || target == name {
		return Evidence{}, false
	}
	for suffix, provider := range cnameSuffixes {
		if strings.HasSuffix(target, suffix) {
			return Evidence{Provider: provider, Source: SourceCNAME, Name: name, Value: target}, true
		}
	}
	return Evidence{}, false
}

// ClassifyNS reads a zone's name servers.
func ClassifyNS(zone string, servers []*net.NS) []Evidence {
	var out []Evidence
	for _, ns := range servers {
		if ns == nil {
			continue
		}
		host := normalizeName(ns.Host)
		if strings.HasSuffix(host, nsSuffix) {
			out = append(out, Evidence{
				Provider: ProviderCloudflare,
				Source:   SourceNS,
				Name:     normalizeName(zone),
				Value:    host,
			})
		}
	}
	return out
}

// headerRule is one header pattern that identifies a network.
type headerRule struct {
	header   string
	provider Provider
	// matches decides whether this header's value is the network's. A nil
	// matcher means the header's presence is the evidence.
	matches func(value string) bool
}

// headerRules are the response headers each network sets. The order is fixed so
// the evidence list does not depend on map iteration.
var headerRules = []headerRule{
	{header: "cf-ray", provider: ProviderCloudflare},
	{header: "server", provider: ProviderCloudflare, matches: containsFold("cloudflare")},
	{header: "x-amz-cf-id", provider: ProviderCloudFront},
	{header: "via", provider: ProviderCloudFront, matches: containsFold("cloudfront")},
	{header: "x-cache", provider: ProviderCloudFront, matches: containsFold("cloudfront")},
	// Fastly's cache nodes are named "cache-<pop><id>", and that prefix is what
	// distinguishes its x-served-by from another proxy's.
	{header: "x-served-by", provider: ProviderFastly, matches: hasPrefixFold("cache-")},
	{header: "via", provider: ProviderFastly, matches: containsFold("varnish")},
	// Akamai's edge passes the origin's server header through on many
	// responses, so neither of these is present on every Akamai site: on seven
	// measured, akamai-grn (its request number) and server "AkamaiGHost" each
	// appeared on four. The delegation above is the signal that is always
	// there; these confirm it when the page answered.
	{header: "akamai-grn", provider: ProviderAkamai},
	{header: "server", provider: ProviderAkamai, matches: containsFold("akamaighost")},
}

// ClassifyHeaders reads the response headers of the fetched page.
//
// A value is never reported, only the header's name and the fact that it was
// there. These headers carry request identifiers and cache-node names, which
// say more about one request than the publisher needs to see, and naming the
// header is what lets them check it themselves.
func ClassifyHeaders(h http.Header) []Evidence {
	if h == nil {
		return nil
	}
	var out []Evidence
	for _, rule := range headerRules {
		value := h.Get(rule.header)
		if value == "" {
			continue
		}
		if rule.matches != nil && !rule.matches(value) {
			continue
		}
		out = append(out, Evidence{
			Provider: rule.provider,
			Source:   SourceHeader,
			Name:     rule.header,
			Value:    "present",
		})
	}
	return out
}

// Decide picks the provider from the evidence.
//
// The order is what each source actually proves. A delegation on the site's own
// name is the site's own statement about who serves it. The www form is the
// same statement, one step less direct, and is what an apex whose record was
// flattened hides behind. Response headers come next: every network sets them,
// but so can a proxy between us and the origin. Name servers come last, because
// they prove who answers DNS and not who serves the traffic — a site can run
// Cloudflare DNS with its content on CloudFront, and reading the zone first
// would give that site the wrong integration.
func Decide(evidence []Evidence) Provider {
	order := []Source{SourceCNAME, SourceHeader, SourceNS}
	for _, source := range order {
		if p := firstFrom(evidence, source); p != ProviderNone {
			return p
		}
	}
	return ProviderNone
}

// firstFrom returns the provider named by the first piece of evidence from one
// source, keeping the order the evidence was collected in.
func firstFrom(evidence []Evidence, source Source) Provider {
	for _, e := range evidence {
		if e.Source == source {
			return e.Provider
		}
	}
	return ProviderNone
}

// normalizeName folds a DNS name to the form these comparisons expect: lower
// case, without the trailing dot a resolver returns.
func normalizeName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// hostWithoutPort drops a port, which a local deployment's host carries and a
// resolver will not accept.
func hostWithoutPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func containsFold(needle string) func(string) bool {
	return func(value string) bool {
		return strings.Contains(strings.ToLower(value), needle)
	}
}

func hasPrefixFold(prefix string) func(string) bool {
	return func(value string) bool {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(value)), prefix)
	}
}
