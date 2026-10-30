package termspreset

import (
	"strconv"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// PriceText says what a price asks, in the words a publisher reads it in
// wherever the console shows one: "free", "0.05 EUR per fetch", or
// "12 EUR once" for a flat fee. It reports the price as stated and never
// computes from it. The unit is said the way SingularUnit says it.
//
// A pricing model this package does not name is said in the protocol's own
// token, so a term priced some newer way is shown as what it is rather than
// as a price it is not.
func PriceText(p *forav1.Pricing) string {
	amount := strings.TrimSpace(p.GetRate() + " " + p.GetCurrency())
	switch p.GetModel() {
	case forav1.PricingModel_PRICING_MODEL_FREE:
		return "free"
	case forav1.PricingModel_PRICING_MODEL_PER_UNIT:
		return amount + " per " + SingularUnit(p.GetUnit())
	case forav1.PricingModel_PRICING_MODEL_FLAT:
		return amount + " once"
	default:
		model := strings.ToLower(strings.TrimPrefix(p.GetModel().String(), "PRICING_MODEL_"))
		return strings.TrimSpace(amount + " " + model)
	}
}

// SingularUnit is a metering unit as a price reads it: "per fetch", not
// "per fetches".
//
// The protocol's registered units are plural -- fetches, accesses, pages --
// because they count what was metered: an estimate is five fetches. A unit
// price is about one of them. Only the words change: the token stays plural
// everywhere it is data, in an offer, a catalog feed and the Exchange's
// denomination. The rule is the one the offer preview on the protocol's own
// website uses (singularUnit, in the github.com/FORA-Protocol/protocol
// module), so a publisher reads the same words there and here. A hyphen reads
// as a space: "sq-km" is "sq km".
func SingularUnit(token string) string {
	text := strings.ReplaceAll(strings.TrimSpace(token), "-", " ")
	n := len(text)
	switch {
	case n == 0:
		return ""
	case pluralAddsES(text):
		return text[:n-2]
	case n >= 2 && text[n-1] == 's' && text[n-2] != 's':
		return text[:n-1]
	default:
		return text
	}
}

// pluralAddsES reports whether text ends in a plural that adds "es" to a
// sibilant, as fetches, accesses and boxes do.
func pluralAddsES(text string) bool {
	for _, ending := range []string{"ches", "shes", "ses", "xes", "zes"} {
		if strings.HasSuffix(text, ending) {
			return true
		}
	}
	return false
}

// FunctionLabel names a use the way the console shows it to a publisher, who is
// not going to read the protocol's tokens. A token this package does not name
// is shown as it is.
func FunctionLabel(token string) string {
	switch token {
	case "search":
		return "Search"
	case "ai-input":
		return "AI input"
	case "ai-train":
		return "AI training"
	default:
		return token
	}
}

// DescribeTerm says what one license term asks, in a line: its price, the uses
// it permits and prohibits, and whether it requires attribution. A use is in
// scope only when it matches a permitted one, so a term that names permitted
// uses allows those only, and says so. Anything else
// it states is counted rather than left out, so a term never reads as more
// permissive than it is. It is the one wording of a term everywhere the
// console shows one: the catalog, a transaction's signed offer, and the terms
// a publisher is setting up.
func DescribeTerm(term *forav1.LicenseTerm) string {
	parts := []string{PriceText(term.GetPricing())}
	var permitted, prohibited []string
	others := 0
	for _, r := range term.GetRestrictions() {
		if r.GetKind() != forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION {
			others++
			continue
		}
		permitted = append(permitted, functionLabels(r.GetPermitted())...)
		prohibited = append(prohibited, functionLabels(r.GetProhibited())...)
	}
	if len(permitted) > 0 {
		parts = append(parts, "allowed only: "+strings.Join(permitted, ", "))
	}
	if len(prohibited) > 0 {
		parts = append(parts, "not allowed: "+strings.Join(prohibited, ", "))
	}
	for _, o := range term.GetObligations() {
		if o.GetKind() == forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION {
			parts = append(parts, "attribution required")
			continue
		}
		others++
	}
	if others > 0 {
		parts = append(parts, "and "+strconv.Itoa(others)+" more condition"+plural(others))
	}
	return strings.Join(parts, " · ")
}

// functionLabels is each token's label, in lower case to sit inside a line.
func functionLabels(tokens []string) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, strings.ToLower(FunctionLabel(t)))
	}
	return out
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
