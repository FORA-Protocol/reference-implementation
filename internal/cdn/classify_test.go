package cdn_test

import (
	"net"
	"net/http"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
)

func TestClassifyCNAME(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		target string
		want   cdn.Provider
	}{
		{"cloudfront", "d1abc2def3.cloudfront.net.", cdn.ProviderCloudFront},
		{"cloudflare", "publisher.example.cdn.cloudflare.net", cdn.ProviderCloudflare},
		{"cloudflare apex", "site.cloudflare.com", cdn.ProviderCloudflare},
		{"fastly", "publisher.example.global.fastly.net", cdn.ProviderFastly},
		{"fastly load balanced", "dualstack.x.fastlylb.net", cdn.ProviderFastly},
		{"upper case answer", "D1ABC.CLOUDFRONT.NET.", cdn.ProviderCloudFront},
		{"akamai enhanced tls", "www.publisher.example.edgekey.net.", cdn.ProviderAkamai},
		{"akamai standard tls", "www.publisher.example.edgesuite.net", cdn.ProviderAkamai},
		{"akamai shared", "publisher.example.akamaized.net", cdn.ProviderAkamai},
		{"akamai edge hop", "e1234.dscx.akamaiedge.net", cdn.ProviderAkamai},
		{"akamai standard edge hop", "a1319.dscr.akamai.net", cdn.ProviderAkamai},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e, ok := cdn.ClassifyCNAME("www.publisher.example", tc.target)
			if !ok {
				t.Fatalf("%q was not recognised", tc.target)
			}
			if e.Provider != tc.want {
				t.Fatalf("provider = %q, want %q", e.Provider, tc.want)
			}
			if e.Source != cdn.SourceCNAME {
				t.Fatalf("source = %q, want a DNS delegation", e.Source)
			}
			if e.Name != "www.publisher.example" {
				t.Fatalf("name = %q, want the host that was resolved", e.Name)
			}
		})
	}
}

func TestClassifyCNAMENonAnswers(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"no delegation":        "www.publisher.example",
		"delegation to self":   "WWW.PUBLISHER.EXAMPLE.",
		"empty answer":         "",
		"unrecognised network": "publisher.example.cdn.someother.example",
		// Akamai's traffic-management DNS sits in front of any network, so it
		// says nothing about who serves the content.
		"akamai traffic management": "www.publisher.example.akadns.net",
		// A suffix must be a suffix: a network's name inside a domain someone
		// else registered is not that network.
		"lookalike domain": "cloudfront.net.attacker.example",
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, ok := cdn.ClassifyCNAME("www.publisher.example", target); ok {
				t.Fatalf("%q was read as a supported network", target)
			}
		})
	}
}

func TestClassifyNS(t *testing.T) {
	t.Parallel()
	servers := []*net.NS{{Host: "kate.ns.cloudflare.com."}, {Host: "rob.ns.cloudflare.com."}}
	got := cdn.ClassifyNS("publisher.example", servers)
	if len(got) != 2 {
		t.Fatalf("evidence = %+v, want one per name server", got)
	}
	if got[0].Provider != cdn.ProviderCloudflare || got[0].Source != cdn.SourceNS {
		t.Fatalf("evidence[0] = %+v", got[0])
	}
}

// Name servers that belong to no network say nothing. Route 53 in particular
// fronts CloudFront sites constantly and must not be read as evidence.
func TestClassifyNSIgnoresOtherProviders(t *testing.T) {
	t.Parallel()
	servers := []*net.NS{
		{Host: "ns-1234.awsdns-56.org."},
		{Host: "ns1.name.com."},
		nil,
	}
	if got := cdn.ClassifyNS("publisher.example", servers); len(got) != 0 {
		t.Fatalf("evidence = %+v, want none", got)
	}
}

func TestClassifyHeaders(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header string
		value  string
		want   cdn.Provider
	}{
		{"cloudflare ray", "cf-ray", "8f2a1b3c4d5e6f70-FRA", cdn.ProviderCloudflare},
		{"cloudflare server", "server", "cloudflare", cdn.ProviderCloudflare},
		{"cloudfront request id", "x-amz-cf-id", "abc123==", cdn.ProviderCloudFront},
		{"cloudfront via", "via", "1.1 abc.cloudfront.net (CloudFront)", cdn.ProviderCloudFront},
		{"cloudfront cache", "x-cache", "Hit from cloudfront", cdn.ProviderCloudFront},
		{"fastly cache node", "x-served-by", "cache-fra-eddf8230051-FRA", cdn.ProviderFastly},
		{"fastly varnish", "via", "1.1 varnish", cdn.ProviderFastly},
		{"akamai request number", "akamai-grn", "0.c51c1202.1789991939.23f6fda4", cdn.ProviderAkamai},
		{"akamai server", "server", "AkamaiGHost", cdn.ProviderAkamai},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			h.Set(tc.header, tc.value)
			got := cdn.ClassifyHeaders(h)
			if len(got) == 0 {
				t.Fatalf("%s: %q produced no evidence", tc.header, tc.value)
			}
			if got[0].Provider != tc.want {
				t.Fatalf("provider = %q, want %q", got[0].Provider, tc.want)
			}
			if got[0].Name != tc.header {
				t.Fatalf("name = %q, want the header that was read", got[0].Name)
			}
			if got[0].Value != "present" {
				t.Fatalf("value = %q, want the value withheld", got[0].Value)
			}
		})
	}
}

// A header value can carry a request identifier or a cache-node name. Naming
// the header tells the publisher where to look; echoing the value does not.
func TestClassifyHeadersWithholdValues(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("cf-ray", "8f2a1b3c4d5e6f70-FRA")
	for _, e := range cdn.ClassifyHeaders(h) {
		if e.Value != "present" {
			t.Fatalf("evidence carried a header value: %+v", e)
		}
	}
}

func TestClassifyHeadersNonAnswers(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	h.Set("server", "nginx")
	h.Set("x-cache", "HIT")
	h.Set("x-served-by", "proxy-07")
	h.Set("via", "1.1 squid")
	if got := cdn.ClassifyHeaders(h); len(got) != 0 {
		t.Fatalf("evidence = %+v, want none", got)
	}
	if got := cdn.ClassifyHeaders(nil); len(got) != 0 {
		t.Fatalf("evidence from no headers = %+v, want none", got)
	}
}

// The order says what each source proves, and the conflict cases are what the
// order exists for.
func TestDecidePrecedence(t *testing.T) {
	t.Parallel()
	cname := cdn.Evidence{Provider: cdn.ProviderCloudFront, Source: cdn.SourceCNAME}
	header := cdn.Evidence{Provider: cdn.ProviderFastly, Source: cdn.SourceHeader}
	ns := cdn.Evidence{Provider: cdn.ProviderCloudflare, Source: cdn.SourceNS}
	cases := []struct {
		name     string
		evidence []cdn.Evidence
		want     cdn.Provider
	}{
		{"nothing at all", nil, cdn.ProviderNone},
		{"delegation alone", []cdn.Evidence{cname}, cdn.ProviderCloudFront},
		{"headers alone", []cdn.Evidence{header}, cdn.ProviderFastly},
		{"name servers alone", []cdn.Evidence{ns}, cdn.ProviderCloudflare},
		{"delegation beats headers", []cdn.Evidence{header, cname}, cdn.ProviderCloudFront},
		// The case the order exists for: a site on Cloudflare DNS whose content
		// is served by CloudFront needs the CloudFront integration.
		{"name servers lose to everything", []cdn.Evidence{ns, header}, cdn.ProviderFastly},
		{"delegation beats name servers", []cdn.Evidence{ns, cname}, cdn.ProviderCloudFront},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := cdn.Decide(tc.evidence); got != tc.want {
				t.Fatalf("decided %q, want %q", got, tc.want)
			}
		})
	}
}
