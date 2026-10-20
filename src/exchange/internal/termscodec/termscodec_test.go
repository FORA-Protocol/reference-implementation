package termscodec_test

import (
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/termscodec"
)

// TestRoundTripKeepsEveryPricingField pins the codec as a pure inverse pair:
// every field a priced term carries — model, rate, currency, unit, estimated
// quantity — and its restrictions come back equal, and the stored document uses
// proto field names, the shape fora.catalog.terms has always been written in.
func TestRoundTripKeepsEveryPricingField(t *testing.T) {
	t.Parallel()
	unit := "accesses"
	terms := []*forav1.LicenseTerm{
		{
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
			Pricing: &forav1.Pricing{
				Model:             forav1.PricingModel_PRICING_MODEL_PER_UNIT,
				Rate:              "0.05",
				Currency:          "EUR",
				Unit:              &unit,
				EstimatedQuantity: proto.Int32(3),
			},
			Restrictions: []*forav1.Restriction{{
				Kind:      forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
				Permitted: []string{"ai-input"},
			}},
		},
		{
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
			Pricing:   &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE, Currency: "EUR"},
		},
	}

	raw, err := termscodec.Marshal(terms)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"estimated_quantity"`) {
		t.Fatalf("stored JSON does not use proto field names: %s", raw)
	}
	back, err := termscodec.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(back) != len(terms) {
		t.Fatalf("decoded %d terms, want %d", len(back), len(terms))
	}
	for i := range terms {
		if !proto.Equal(back[i], terms[i]) {
			t.Errorf("term %d differs after the round trip:\n got %v\nwant %v", i, back[i], terms[i])
		}
	}
}

// TestEmptyAndMalformed pins the two edges: no terms is the empty array, empty
// input decodes to nothing, and a document that is not an array is an error
// rather than an empty list.
func TestEmptyAndMalformed(t *testing.T) {
	t.Parallel()
	raw, err := termscodec.Marshal(nil)
	if err != nil || string(raw) != "[]" {
		t.Fatalf("Marshal(nil) = %s, %v; want [] and nil", raw, err)
	}
	if terms, err := termscodec.Unmarshal(nil); err != nil || terms != nil {
		t.Fatalf("Unmarshal(nil) = %v, %v; want nil, nil", terms, err)
	}
	if _, err := termscodec.Unmarshal([]byte(`{"not":"an array"}`)); err == nil {
		t.Fatal("Unmarshal of a non-array document succeeded; want an error")
	}
}
