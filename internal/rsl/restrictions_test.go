package rsl_test

import (
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// restrictionFor returns the axis a case is about, so each assertion below
// reads as one statement.
func restrictionFor(t *testing.T, term *forav1.LicenseTerm, kind forav1.RestrictionKind) *forav1.Restriction {
	t.Helper()
	for _, r := range term.GetRestrictions() {
		if r.GetKind() == kind {
			return r
		}
	}
	t.Fatalf("the term carries no %v restriction: %+v", kind, term.GetRestrictions())
	return nil
}

func assertTokens(t *testing.T, got, want []string, what string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q", what, i, got[i], want[i])
		}
	}
}

func TestUsageStatementsBecomeFunctionRestrictions(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<permits type="usage">ai-input search</permits>
		<prohibits type="usage">ai-train</prohibits>
		<payment type="free"/>
	</license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"ai-input", "search"}, "permitted")
	assertTokens(t, r.GetProhibited(), []string{"ai-train"}, "prohibited")
}

// A statement with no type is a statement about usage, which is how RSL reads
// an omitted type attribute.
func TestUntypedStatementIsAboutUsage(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><permits>search</permits><payment type="free"/></license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"search"}, "permitted")
}

// RSL describes who the user is in five words; four have a FORA counterpart.
func TestUserVocabularyMapping(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"commercial":     "commercial_entity",
		"non-commercial": "non_profit",
		"education":      "academic",
		"personal":       "individual",
		// No FORA type says "a government"; the token is carried as written so
		// the statement is not lost, and the Exchange flags it as unregistered.
		"government": "government",
	}
	for written, want := range cases {
		t.Run(written, func(t *testing.T) {
			t.Parallel()
			m := mapFirst(t, `<license><permits type="user">`+written+`</permits><payment type="free"/></license>`)
			assertAccepted(t, m.Terms)
			r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE)
			assertTokens(t, r.GetPermitted(), []string{want}, "permitted")
		})
	}
}

func TestGeoStatementsBecomeGeographyRestrictions(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><permits type="geo">de EU</permits><payment type="free"/></license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY)
	assertTokens(t, r.GetPermitted(), []string{"DE", "EU"}, "permitted")
}

// A token spelled as one of its accepted aliases lands on the registered form,
// so two documents saying the same thing produce the same term.
func TestTokensAreCanonicalised(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><permits type="usage">SCRAPE Generative-AI</permits><payment type="free"/></license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"crawl", "ai-input"}, "permitted")
}

// The protocol allows one restriction per axis, so two statements about the
// same axis are one restriction here rather than a term the Exchange refuses.
func TestRepeatedAxisBecomesOneRestriction(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<permits type="usage">search</permits>
		<permits type="usage">ai-index</permits>
		<payment type="free"/>
	</license>`)
	assertAccepted(t, m.Terms)
	count := 0
	for _, r := range m.Terms[0].GetRestrictions() {
		if r.GetKind() == forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("function restrictions = %d, want 1", count)
	}
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"search", "ai-index"}, "permitted")
}

// A token written twice is meant once.
func TestRepeatedTokenIsCarriedOnce(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><permits type="usage">search search SEARCH</permits><payment type="free"/></license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"search"}, "permitted")
}

// The payment type names a use, and that use reaches the term as a permitted
// function: RSL states it in the payment, FORA states it in a restriction.
func TestPaymentTypeAddsItsImpliedUse(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<permits type="usage">search</permits>
		<payment type="training"><amount currency="EUR">0.001</amount></payment>
	</license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"search", "ai-train"}, "permitted")
}

// A publisher who prices training and also forbids it has contradicted
// themselves. Keeping both would produce a term the Exchange refuses outright,
// so the prohibition stands and the price does not add the use back.
func TestImpliedUseYieldsToAnExplicitProhibition(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<prohibits type="usage">ai-train</prohibits>
		<payment type="training"><amount currency="EUR">0.001</amount></payment>
	</license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetProhibited(), []string{"ai-train"}, "prohibited")
	for _, p := range r.GetPermitted() {
		if p == "ai-train" {
			t.Fatal("the payment type added back a use the document prohibits")
		}
	}
}

// A document that permits and prohibits the same use is a contradiction the
// protocol refuses outright. The prohibition stands, the permission is dropped,
// and the publisher is told which of their two statements was not carried —
// because the alternative is a term no exchange accepts, which costs them the
// whole document rather than one word of it.
func TestATokenOnBothListsIsCarriedAsProhibited(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<permits type="usage">search ai-train</permits>
		<prohibits type="usage">ai-train</prohibits>
		<payment type="free"/>
	</license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION)
	assertTokens(t, r.GetPermitted(), []string{"search"}, "permitted")
	assertTokens(t, r.GetProhibited(), []string{"ai-train"}, "prohibited")
	if !anyWarningContains(m.Warnings, "ai-train") {
		t.Fatalf("warnings = %v, want one naming the statement that was dropped", m.Warnings)
	}
}

// The same rule applies on every axis, not only to uses.
func TestATokenOnBothListsIsCarriedAsProhibitedOnEveryAxis(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<permits type="geo">DE</permits>
		<prohibits type="geo">DE</prohibits>
		<payment type="free"/>
	</license>`)
	assertAccepted(t, m.Terms)
	r := restrictionFor(t, m.Terms[0], forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY)
	if len(r.GetPermitted()) != 0 {
		t.Fatalf("permitted = %v, want none: the same region is prohibited", r.GetPermitted())
	}
	assertTokens(t, r.GetProhibited(), []string{"DE"}, "prohibited")
}

func anyWarningContains(warnings []string, substring string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substring) {
			return true
		}
	}
	return false
}

// An axis named with nothing in it says nothing, and an empty restriction
// would only add noise to the term.
func TestEmptyAxisIsDropped(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><permits type="geo"></permits><payment type="free"/></license>`)
	assertAccepted(t, m.Terms)
	if len(m.Terms[0].GetRestrictions()) != 0 {
		t.Fatalf("restrictions = %+v, want none", m.Terms[0].GetRestrictions())
	}
}

// Axes come out in a fixed order, so one document always maps to one term.
func TestAxisOrderIsStable(t *testing.T) {
	t.Parallel()
	for range 5 {
		m := mapFirst(t, `<license>
			<permits type="geo">EU</permits>
			<permits type="user">education</permits>
			<permits type="usage">search</permits>
			<payment type="free"/>
		</license>`)
		got := make([]forav1.RestrictionKind, 0, 3)
		for _, r := range m.Terms[0].GetRestrictions() {
			got = append(got, r.GetKind())
		}
		want := []forav1.RestrictionKind{
			forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
			forav1.RestrictionKind_RESTRICTION_KIND_USER_TYPE,
			forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY,
		}
		if len(got) != len(want) {
			t.Fatalf("restriction kinds = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("restriction kinds = %v, want %v", got, want)
			}
		}
	}
}

// A referenced licensing document is reported rather than carried: the
// protocol requires a digest of any document a term references, and this
// package does not fetch the document to compute one.
func TestReferencedLicenseDocumentIsReportedNotCarried(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license>
		<payment type="purchase">
			<custom>https://publisher.example/licensing/ai</custom>
			<amount currency="EUR">1.00</amount>
		</payment>
	</license>`)
	assertAccepted(t, m.Terms)
	if m.Terms[0].GetLicense().GetUri() != "" {
		t.Fatal("the term carries a referenced document with no digest, which the Exchange would refuse")
	}
	found := false
	for _, w := range m.Warnings {
		if strings.Contains(w, "https://publisher.example/licensing/ai") && strings.Contains(w, "digest") {
			found = true
		}
	}
	if !found {
		t.Fatalf("warnings = %v, want one naming the document and why it is not carried", m.Warnings)
	}
}
