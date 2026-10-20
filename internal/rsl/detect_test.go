package rsl

import "testing"

// Detect is the one pipeline both the onboarding preview and the console run
// over a site's licensing document. Each failure kind is a sentence one of them
// shows a publisher, so each is driven here from a document that produces it.

const detectPage = "https://publisher.example/articles/on-rivers"

func TestDetectNamesEachWayADocumentYieldsNoTerms(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want Failure
	}{
		{"not RSL at all", `<html><body>home page</body></html>`, FailureNotReadable},
		{"no rule for the page", `<rsl><content url="/news/"><license>
			<payment type="free"></payment></license></content></rsl>`, FailureNoRule},
		{"a rule with nothing FORA can carry", `<rsl><content url="/articles/"><license>
			</license></content></rsl>`, FailureNothingCarried},
	} {
		got := Detect([]byte(tc.body), detectPage, Defaults{Currency: "EUR", Domain: "publisher.example"})
		if got.Failure != tc.want {
			t.Fatalf("%s: failure = %d, want %d", tc.name, got.Failure, tc.want)
		}
		if len(got.Terms) != 0 {
			t.Fatalf("%s: terms were returned with a failure: %v", tc.name, got.Terms)
		}
	}
}

func TestDetectCarriesTheWarningsOfARuleNothingSurvived(t *testing.T) {
	got := Detect([]byte(`<rsl><content url="/articles/"><license></license></content></rsl>`),
		detectPage, Defaults{Currency: "EUR", Domain: "publisher.example"})
	if len(got.Warnings) == 0 {
		t.Fatal("a rule whose every license was dropped says nothing about why")
	}
}

func TestDetectMapsTheRuleCoveringThePage(t *testing.T) {
	got := Detect([]byte(`<rsl><content url="/articles/"><license>
		<prohibits type="usage">train-ai</prohibits>
		<payment type="free"></payment></license></content></rsl>`),
		detectPage, Defaults{Currency: "EUR", Domain: "publisher.example"})
	if got.Failure != FailureNone || len(got.Terms) != 1 {
		t.Fatalf("detection = %+v, want one term", got)
	}
	if got.ContentURL != "/articles/" {
		t.Fatalf("content url = %q, want the rule that matched", got.ContentURL)
	}
	if cur := got.Terms[0].GetPricing().GetCurrency(); cur != "EUR" {
		t.Fatalf("currency = %q, want the one the caller passed", cur)
	}
}
