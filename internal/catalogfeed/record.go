package catalogfeed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// RecordFor writes the feed line for one resource, from the protocol messages
// that describe it.
//
// It is the inverse of the mapping in this package and lives beside it, so a
// change to one is read against the other. The onboarding preview is what needs
// it: a publisher is shown the line they would put in their own feed, built
// from the terms the preview resolved, and the claim that the Exchange would
// accept that line is only worth making if the line is run back through this
// package's own parse and mapping. Check does exactly that.
//
// A term FORA can express but the feed line cannot is an error rather than a
// line with a statement quietly missing. There are three: a restriction axis
// the schema has no field for, a prohibition on an axis the schema only
// permits on, and a term whose semantics or pricing model the feed has no word
// for.
func RecordFor(entry *forav1.ResourceEntry) (Record, error) {
	if entry == nil {
		return Record{}, fmt.Errorf("catalogfeed: no entry to render")
	}
	rec := Record{
		Domain: entry.GetDomain(),
		Path:   entry.GetPath(),
		Title:  entry.GetTitle(),
	}
	for i, t := range entry.GetTerms() {
		term, err := termFor(t)
		if err != nil {
			return Record{}, fmt.Errorf("term %d: %w", i, err)
		}
		rec.Terms = append(rec.Terms, term)
		if rec.License == nil {
			rec.License = licenseFor(t.GetLicense())
		}
	}
	return rec, nil
}

// termFor renders one license term as the feed spells it.
func termFor(t *forav1.LicenseTerm) (Term, error) {
	semantics, err := sourceToken(t.GetSemantics(), "TERM_SEMANTICS_")
	if err != nil {
		return Term{}, err
	}
	pricing, err := pricingFor(t.GetPricing())
	if err != nil {
		return Term{}, err
	}
	term := Term{Semantics: semantics, Pricing: pricing, Scopes: t.GetScopes()}
	if err := applyRestrictions(&term, t.GetRestrictions()); err != nil {
		return Term{}, err
	}
	if term.Quotas, err = quotasFor(t.GetQuotas()); err != nil {
		return Term{}, err
	}
	if term.Obligations, err = obligationsFor(t.GetObligations()); err != nil {
		return Term{}, err
	}
	return term, nil
}

// applyRestrictions spreads the term's axes across the fields the feed line
// carries. The schema permits on three axes and prohibits on one, so anything
// outside that is reported rather than dropped.
func applyRestrictions(term *Term, restrictions []*forav1.Restriction) error {
	for _, r := range restrictions {
		kind := r.GetKind()
		if len(r.GetProhibited()) > 0 && kind != forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION {
			return fmt.Errorf("the feed line cannot prohibit on the %s axis", kind)
		}
		switch kind {
		case forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION:
			term.Functions = r.GetPermitted()
			term.ProhibitedFunctions = r.GetProhibited()
		case forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE:
			term.UserTypes = r.GetPermitted()
		case forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY:
			term.Geos = r.GetPermitted()
		default:
			return fmt.Errorf("a %s restriction has no field in the feed line", kind)
		}
	}
	return nil
}

// pricingFor renders the term's price. The unit is carried only for the model
// that meters, matching the rule the forward mapping enforces.
//
// The line has no unit_cost field, and reading one back is what makes that
// safe: a term that omits unit_cost is charged at its rate, so a unit_cost
// equal to the rate is carried by the rate alone. A unit_cost that differs is
// the one case where the line would quietly change the price, and it is
// refused rather than rendered.
func pricingFor(p *forav1.Pricing) (*Pricing, error) {
	if p == nil {
		return nil, fmt.Errorf("a term with no pricing has no feed line")
	}
	if uc := p.GetUnitCost(); uc != "" && uc != p.GetRate() {
		return nil, fmt.Errorf(
			"the feed line has no unit_cost field, and this term charges %s per unit against a rate of %s",
			uc, p.GetRate())
	}
	model, err := sourceToken(p.GetModel(), "PRICING_MODEL_")
	if err != nil {
		return nil, err
	}
	out := &Pricing{Model: model, Rate: p.GetRate(), Currency: p.GetCurrency()}
	if p.GetModel() == forav1.PricingModel_PRICING_MODEL_PER_UNIT {
		out.Unit = p.GetUnit()
	}
	if p.Metering != nil {
		if out.Metering, err = sourceToken(p.GetMetering(), "PRICING_METERING_"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func quotasFor(qs []*forav1.Quota) ([]Quota, error) {
	out := make([]Quota, 0, len(qs))
	for _, q := range qs {
		window, err := sourceToken(q.GetWindow(), "QUOTA_WINDOW_")
		if err != nil {
			return nil, err
		}
		out = append(out, Quota{Metric: q.GetMetric(), Limit: q.GetLimit(), Window: window})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func obligationsFor(os []*forav1.Obligation) ([]Obligation, error) {
	out := make([]Obligation, 0, len(os))
	for _, o := range os {
		kind, err := sourceToken(o.GetKind(), "OBLIGATION_KIND_")
		if err != nil {
			return nil, err
		}
		trigger, err := sourceToken(o.GetTrigger(), "OBLIGATION_TRIGGER_")
		if err != nil {
			return nil, err
		}
		out = append(out, Obligation{
			Kind:         kind,
			Trigger:      trigger,
			ScopeLicense: o.GetScopeLicense().GetId(),
			Detail:       o.GetDetail(),
		})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// licenseFor carries the governing document, when a term names one. A term with
// an empty License is the ordinary case and yields nothing.
func licenseFor(l *forav1.License) *License {
	if l == nil {
		return nil
	}
	out := &License{ID: l.GetId(), URI: l.GetUri(), URIDigest: l.GetUriDigest(), Name: l.GetName()}
	if *out == (License{}) {
		return nil
	}
	return out
}

// sourceToken inverts the naming the forward mapping reads. A proto enum value
// is spelled <TYPE>_<VALUE> and the feed spells <value> in lower case, so the
// conversion is derived rather than written as a second switch that could drift
// from the first. A value with no feed spelling — the unspecified sentinel, or
// one added to the protocol since — is an error here, and one this produces that
// the forward mapping does not know fails Check, which every rendered line is
// run through before a publisher sees it.
func sourceToken[E interface{ String() string }](value E, prefix string) (string, error) {
	name := value.String()
	if !strings.HasPrefix(name, prefix) || name == prefix+"UNSPECIFIED" {
		return "", fmt.Errorf("%s has no feed spelling", name)
	}
	return strings.ToLower(strings.TrimPrefix(name, prefix)), nil
}

// Render writes records as the JSON Lines a feed carries: one object per line,
// newline-terminated, in the order given.
func Render(records ...Record) ([]byte, error) {
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	for i, rec := range records {
		if err := enc.Encode(rec); err != nil {
			return nil, fmt.Errorf("render record %d: %w", i, err)
		}
	}
	return out.Bytes(), nil
}

// SampleFor renders one resource as the feed line its publisher would push, and
// runs over it the same check fora-ingest --check runs: the line is parsed, the
// record is mapped, and the SDK validates the entry that mapping produced.
//
// The line is returned whatever the report says. A publisher whose terms the
// Exchange would refuse is better served by the line plus the findings than by
// nothing: the line is what they edit, and the findings say what to edit.
func SampleFor(entry *forav1.ResourceEntry) ([]byte, CheckReport, error) {
	rec, err := RecordFor(entry)
	if err != nil {
		return nil, CheckReport{}, err
	}
	line, err := Render(rec)
	if err != nil {
		return nil, CheckReport{}, err
	}
	report, err := Check(bytes.NewReader(line))
	if err != nil {
		return line, CheckReport{}, err
	}
	return line, report, nil
}
