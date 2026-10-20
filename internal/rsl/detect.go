package rsl

import (
	"bytes"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// Failure says why a licensing document produced no terms for a page. Callers
// word each one for their own reader; the steps that decide which one applies
// are here, once.
type Failure int

const (
	// FailureNone means terms were found.
	FailureNone Failure = iota
	// FailureNotReadable means the body is not an RSL document at all.
	FailureNotReadable
	// FailureNoRule means the document states no rule covering the page.
	FailureNoRule
	// FailureRuleUnreadable means a rule covers the page but could not be
	// mapped.
	FailureRuleUnreadable
	// FailureNothingCarried means every license in the rule was one FORA
	// cannot express. Detection.Warnings say which and why.
	FailureNothingCarried
)

// Detection is what one licensing document says about one page.
type Detection struct {
	// Failure is FailureNone when Terms carries at least one term.
	Failure Failure
	// ContentURL is the address pattern of the rule that covered the page,
	// empty when none did.
	ContentURL string
	// Terms are the mapped license terms.
	Terms []*forav1.LicenseTerm
	// Warnings say what the document stated that the terms do not carry. They
	// are set on success and on FailureNothingCarried, where they are the only
	// account of what was dropped.
	Warnings []string
}

// Detect reads a licensing document and maps the rule covering pageURL.
//
// Fetching the document is the caller's work: whether a site publishes one, and
// whether it could be fetched, are questions about the network rather than
// about RSL.
func Detect(body []byte, pageURL string, d Defaults) Detection {
	doc, err := Parse(bytes.NewReader(body))
	if err != nil {
		return Detection{Failure: FailureNotReadable}
	}
	content, ok := doc.Match(pageURL)
	if !ok {
		return Detection{Failure: FailureNoRule}
	}
	mapped, err := ToLicenseTerms(content, d)
	if err != nil {
		return Detection{Failure: FailureRuleUnreadable, ContentURL: content.URL}
	}
	out := Detection{ContentURL: content.URL, Terms: mapped.Terms, Warnings: mapped.Warnings}
	if len(mapped.Terms) == 0 {
		out.Failure = FailureNothingCarried
	}
	return out
}
