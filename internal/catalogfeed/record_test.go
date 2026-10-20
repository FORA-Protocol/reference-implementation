package catalogfeed_test

import (
	"bytes"
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/functiontokens"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/pricingunits"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/quotametrics"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/usertypes"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
)

// A rendered line read back yields the entry it was rendered from.
//
// This is the property the onboarding preview's whole claim rests on: a
// publisher is shown the line, and told the Exchange would accept it. If the
// rendering dropped a statement, the check would pass over a line that no
// longer says what the publisher was shown — the check would be honest and the
// answer still wrong. The entry here carries something on every field the line
// can express, so a dropped one shows up as an inequality rather than as a
// field nobody exercised.
func TestARenderedLineReadsBackAsTheEntryItCameFrom(t *testing.T) {
	t.Parallel()
	entry := richEntry()
	want := proto.Clone(entry).(*forav1.ResourceEntry)
	// The line has no unit_cost field. Dropping one equal to the rate loses
	// nothing: a term that omits unit_cost is charged at its rate, which is the
	// Exchange's own fallback. A unit_cost that differs is refused outright,
	// which the case below covers.
	want.Terms[0].Pricing.UnitCost = nil

	line, report, err := catalogfeed.SampleFor(entry)
	if err != nil {
		t.Fatalf("render and check: %v", err)
	}
	if !report.OK() {
		t.Fatalf("the rendered line would be refused: %+v\n%s", report.Findings, line)
	}
	records, err := catalogfeed.ParseJSONL(bytes.NewReader(line))
	if err != nil {
		t.Fatalf("the rendered line does not parse: %v\n%s", err, line)
	}
	entries, err := catalogfeed.MapRecords(records)
	if err != nil {
		t.Fatalf("the rendered line does not map: %v\n%s", err, line)
	}
	if len(entries) != 1 {
		t.Fatalf("the line mapped to %d entries, want one", len(entries))
	}
	if !proto.Equal(entries[0], want) {
		t.Fatalf("the line lost or changed a statement\n got: %v\nwant: %v", entries[0], want)
	}
}

// One record per line, newline-terminated, in the order given.
func TestRenderWritesOneObjectPerLine(t *testing.T) {
	t.Parallel()
	first, err := catalogfeed.RecordFor(richEntry())
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	second := first
	second.Path = "/news/b"
	raw, err := catalogfeed.Render(first, second)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("render wrote %d lines, want 2: %q", len(lines), raw)
	}
	if !strings.Contains(lines[1], `"/news/b"`) {
		t.Fatalf("the second line is not the second record: %q", lines[1])
	}
}

// A statement FORA can make that the feed line has no field for is reported
// rather than dropped, because a line missing a prohibition says the opposite
// of what the publisher stated.
func TestAStatementTheLineCannotCarryIsRefused(t *testing.T) {
	t.Parallel()
	cases := map[string]*forav1.Restriction{
		"a prohibition on the geography axis": {
			Kind:       forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY,
			Prohibited: []string{"DE"},
		},
		"a prohibition on the user-type axis": {
			Kind:       forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE,
			Prohibited: []string{usertypes.CommercialEntity},
		},
		"an axis the schema has no field for": {
			Kind:      forav1.RestrictionKind_RESTRICTION_KIND_OTHER,
			Permitted: []string{"vendor:thing"},
		},
	}
	for name, restriction := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := richEntry()
			entry.Terms[0].Restrictions = append(entry.Terms[0].Restrictions, restriction)
			if _, err := catalogfeed.RecordFor(entry); err == nil {
				t.Fatal("the line was rendered without the statement it cannot carry")
			}
		})
	}
}

// A per-unit charge that differs from the rate cannot be written as a line, and
// rendering one anyway would hand a publisher a feed that prices their content
// differently from the offer they were shown.
func TestAUnitCostThatDiffersFromTheRateIsRefused(t *testing.T) {
	t.Parallel()
	entry := richEntry()
	other := "0.05"
	entry.Terms[0].Pricing.UnitCost = &other
	if _, err := catalogfeed.RecordFor(entry); err == nil {
		t.Fatal("a line was rendered that drops a charge differing from the rate")
	}
}

// richEntry is one resource carrying something on every field a feed line can
// express, so the round trip above has something to lose on each of them.
func richEntry() *forav1.ResourceEntry {
	title := "Midnight Harbour"
	unit := pricingunits.Fetches
	rate := "0.02"
	detail := "name the publication"
	entry := &forav1.ResourceEntry{
		Domain: "publisher.example",
		Path:   "/news/a",
		Title:  &title,
		Terms: []*forav1.LicenseTerm{{
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
			Scopes:    []string{"subscription:publisher.example"},
			Pricing: &forav1.Pricing{
				Model:    forav1.PricingModel_PRICING_MODEL_PER_UNIT,
				Rate:     rate,
				UnitCost: &rate,
				Unit:     &unit,
				Currency: "EUR",
				Metering: forav1.PricingMetering_PRICING_METERING_OFFLINE_SELF_REPORTED.Enum(),
			},
			Restrictions: []*forav1.Restriction{
				{
					Kind:       forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
					Permitted:  []string{functiontokens.Search, functiontokens.AiInput},
					Prohibited: []string{functiontokens.AiTrain},
				},
				{
					Kind:      forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE,
					Permitted: []string{usertypes.NewsPublisher},
				},
				{
					Kind:      forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY,
					Permitted: []string{"EU"},
				},
			},
			Quotas: []*forav1.Quota{{
				Metric: quotametrics.Accesses,
				Limit:  1000,
				Window: forav1.QuotaWindow_QUOTA_WINDOW_DAILY,
			}},
			Obligations: []*forav1.Obligation{{
				Kind:    forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION,
				Trigger: forav1.ObligationTrigger_OBLIGATION_TRIGGER_ON_USE,
				Detail:  &detail,
			}},
		}},
	}
	helpers.NormalizeResourceEntry(entry)
	return entry
}
