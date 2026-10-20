package rsl_test

import (
	"errors"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/rsl"
)

// The minimal document from the RSL 1.0 specification.
const minimalRSL = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="/">
    <license>
      <permits type="usage">search</permits>
      <payment type="free"/>
    </license>
  </content>
</rsl>`

func TestParseMinimalDocument(t *testing.T) {
	t.Parallel()
	doc, err := rsl.Parse(strings.NewReader(minimalRSL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Contents) != 1 {
		t.Fatalf("contents = %d, want 1", len(doc.Contents))
	}
	c := doc.Contents[0]
	if c.URL != "/" {
		t.Fatalf("url = %q, want /", c.URL)
	}
	if len(c.Licenses) != 1 {
		t.Fatalf("licenses = %d, want 1", len(c.Licenses))
	}
	lic := c.Licenses[0]
	if len(lic.Permits) != 1 || lic.Permits[0].Type != "usage" {
		t.Fatalf("permits = %+v, want one usage statement", lic.Permits)
	}
	if len(lic.Permits[0].Tokens) != 1 || lic.Permits[0].Tokens[0] != "search" {
		t.Fatalf("permitted tokens = %v, want [search]", lic.Permits[0].Tokens)
	}
	if lic.Payment == nil || lic.Payment.Type != "free" {
		t.Fatalf("payment = %+v, want free", lic.Payment)
	}
}

// Tokens are space separated inside one element, which is how the published
// examples write several uses at once.
func TestParseSplitsTokenLists(t *testing.T) {
	t.Parallel()
	const body = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="/premium/*">
    <license>
      <permits type="usage">ai-input ai-index search</permits>
      <prohibits type="usage">ai-train</prohibits>
      <payment type="purchase"><amount currency="USD">0.05</amount></payment>
    </license>
  </content>
</rsl>`
	doc, err := rsl.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	lic := doc.Contents[0].Licenses[0]
	want := []string{"ai-input", "ai-index", "search"}
	if len(lic.Permits[0].Tokens) != len(want) {
		t.Fatalf("permitted = %v, want %v", lic.Permits[0].Tokens, want)
	}
	for i, w := range want {
		if lic.Permits[0].Tokens[i] != w {
			t.Fatalf("permitted[%d] = %q, want %q", i, lic.Permits[0].Tokens[i], w)
		}
	}
	if len(lic.Prohibits) != 1 || lic.Prohibits[0].Tokens[0] != "ai-train" {
		t.Fatalf("prohibited = %+v, want [ai-train]", lic.Prohibits)
	}
	if lic.Payment.Amounts[0].Currency != "USD" || lic.Payment.Amounts[0].Value != "0.05" {
		t.Fatalf("amount = %+v, want 0.05 USD", lic.Payment.Amounts[0])
	}
}

func TestParseReadsSeveralContentsAndLicenses(t *testing.T) {
	t.Parallel()
	const body = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="/premium/*">
    <license><permits type="usage">search</permits><payment type="free"/></license>
    <license><permits type="usage">ai-input</permits><payment type="purchase"><amount>1.00</amount></payment></license>
  </content>
  <content url="/free/*">
    <license><permits type="usage">all</permits><payment type="free"/></license>
  </content>
</rsl>`
	doc, err := rsl.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(doc.Contents) != 2 {
		t.Fatalf("contents = %d, want 2", len(doc.Contents))
	}
	if len(doc.Contents[0].Licenses) != 2 {
		t.Fatalf("first rule licenses = %d, want 2", len(doc.Contents[0].Licenses))
	}
}

// The user and geography axes are read the same way as usage.
func TestParseReadsEveryAxis(t *testing.T) {
	t.Parallel()
	const body = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="/">
    <license>
      <permits type="usage">search</permits>
      <permits type="user">education</permits>
      <permits type="geo">DE FR</permits>
      <payment type="free"/>
    </license>
  </content>
</rsl>`
	doc, err := rsl.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	axes := map[string][]string{}
	for _, u := range doc.Contents[0].Licenses[0].Permits {
		axes[u.Type] = u.Tokens
	}
	if len(axes) != 3 {
		t.Fatalf("axes = %v, want usage, user and geo", axes)
	}
	if len(axes["geo"]) != 2 {
		t.Fatalf("geo = %v, want two countries", axes["geo"])
	}
}

// Elements this package does not read must not fail the document: a real RSL
// file carries warranties, attestations and payment-protocol metadata.
func TestParseSkipsUnreadElements(t *testing.T) {
	t.Parallel()
	const body = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="/" server="https://licenses.example">
    <license>
      <permits type="usage">search</permits>
      <payment type="purchase">
        <standard>https://rslcollective.org/standard.pdf</standard>
        <amount currency="EUR">0.05</amount>
        <accepts type="application/x402+json">https://pay.example</accepts>
      </payment>
      <legal type="warranty">ownership</legal>
      <legal type="disclaimer">as-is</legal>
    </license>
  </content>
</rsl>`
	doc, err := rsl.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p := doc.Contents[0].Licenses[0].Payment
	if p.Standard != "https://rslcollective.org/standard.pdf" {
		t.Fatalf("standard = %q, want the framework address", p.Standard)
	}
	if len(p.Amounts) != 1 || p.Amounts[0].Value != "0.05" {
		t.Fatalf("amounts = %+v, want the single declared price", p.Amounts)
	}
}

// Namespace spelling is not part of the document's meaning.
func TestParseIgnoresNamespaceSpelling(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"prefixed": `<r:rsl xmlns:r="https://rslstandard.org/rsl"><r:content url="/">` +
			`<r:license><r:payment type="free"/></r:license></r:content></r:rsl>`,
		"no namespace": `<rsl><content url="/"><license><payment type="free"/></license></content></rsl>`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			doc, err := rsl.Parse(strings.NewReader(body))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if len(doc.Contents) != 1 || doc.Contents[0].Licenses[0].Payment.Type != "free" {
				t.Fatalf("doc = %+v, want one free rule", doc)
			}
		})
	}
}

func TestParseRejections(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"home page instead": `<!doctype html><html><body>not here</body></html>`,
		"empty body":        ``,
		"unclosed element":  `<rsl><content url="/">`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := rsl.Parse(strings.NewReader(body))
			if err == nil {
				t.Fatal("parse accepted a document that is not RSL")
			}
			if !errors.Is(err, rsl.ErrNotRSL) {
				t.Fatalf("err = %v, want ErrNotRSL", err)
			}
		})
	}
}

// A payment type is matched without regard to case, because the type attribute
// is a fixed vocabulary rather than free text.
func TestParseFoldsPaymentType(t *testing.T) {
	t.Parallel()
	doc, err := rsl.Parse(strings.NewReader(
		`<rsl><content url="/"><license><payment type="FREE"/></license></content></rsl>`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := doc.Contents[0].Licenses[0].Payment.Type; got != "free" {
		t.Fatalf("payment type = %q, want free", got)
	}
}
