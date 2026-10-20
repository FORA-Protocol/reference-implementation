package sitedomain_test

import (
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/sitedomain"
)

// The one rule for what a publisher's domain may be. It is a unit test because
// the function is pure and the branches are the point: each one is a value that
// would otherwise become a DNS query and a request to whatever was typed, and
// then a row keyed on a string the later fetch does not resolve to.

func TestNormalizeAcceptsAndSettlesOnOneSpelling(t *testing.T) {
	for raw, want := range map[string]string{
		"stoa.example":         "stoa.example",
		"  stoa.example  ":     "stoa.example",
		"STOA.Example":         "stoa.example",
		"stoa.example.":        "stoa.example",
		"www.stoa.example":     "www.stoa.example",
		"a.b.c.stoa.example":   "a.b.c.stoa.example",
		"xn--80ak6aa92e.local": "xn--80ak6aa92e.local",
		"my-site.example":      "my-site.example",
		"123.stoa.example":     "123.stoa.example",
	} {
		got, err := sitedomain.Normalize(raw)
		if err != nil {
			t.Fatalf("Normalize(%q) refused it: %v", raw, err)
		}
		if got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestNormalizeRefusesWhatIsNotAHostname(t *testing.T) {
	cases := []struct {
		raw  string
		says string
		why  string
	}{
		{raw: "", says: "required", why: "there is nothing to look up"},
		{raw: "   ", says: "required", why: "whitespace is not an address"},
		{
			raw: "https://stoa.example", says: "bare hostname",
			why: "a pasted URL is the commonest thing a publisher types into this field",
		},
		{raw: "stoa.example/articles", says: "bare hostname", why: "a path is not part of the name"},
		{raw: "stoa.example:8443", says: "bare hostname", why: "the fetch decides the port"},
		{raw: "editor@stoa.example", says: "bare hostname", why: "that is an address for mail"},
		{raw: "203.0.113.4", says: "not an IP address", why: "an address has no stable identity to register"},
		{raw: "2001:db8::1", says: "bare hostname", why: "the colons are caught before the address is"},
		{raw: "localhost", says: "fully qualified", why: "a dot-less name is not a site on the internet"},
		{raw: "стоа.example", says: "not allowed", why: "the name has to be in its punycode form"},
		{raw: "stoa example.com", says: "not allowed", why: "a space is not part of any hostname"},
		{raw: "my_site.example", says: "not a valid hostname", why: "the protocol admits no underscore"},
		{raw: "-stoa.example", says: "not a valid hostname", why: "a label cannot start with a hyphen"},
		{raw: "stoa-.example", says: "not a valid hostname", why: "a label cannot end with a hyphen"},
		{raw: "stoa..example", says: "not a valid hostname", why: "an empty label names nothing"},
		{
			raw: strings.Repeat("a", 255) + ".example", says: "not a valid hostname",
			why: "the protocol bounds a domain at 260 characters",
		},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := sitedomain.Normalize(tc.raw)
			if err == nil {
				t.Fatalf("Normalize(%q) = %q, but %s", tc.raw, got, tc.why)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("Normalize(%q) says %q, want it to say %q", tc.raw, err, tc.says)
			}
		})
	}
}
