//go:build integration

package transport_test

import (
	"testing"
)

// The publisher's answer to whether the automatic import may take over the
// pages of a site that are already in the catalog. It is written through the
// console's signed domain route and read back through the console's own resume
// route (registrations/list). Its audit row is read through the repository,
// because the Exchange publishes no read surface for its audit log.

// takeOverAnswer is the request member the answer travels in.
const takeOverAnswer = "import_replaces_manual_terms"

// TestConsoleRegistration_ImportTakeOverAnswerRoundTrip: a site registered with
// the answer yes reads it back, and a site registered without it reads no.
// Each answer is recorded on its registration's audit row. Registering either
// listed site again with the other answer changes nothing and writes no audit
// row: once a site is saved, its answer changes only with its terms, and a
// verified site's terms do not change.
func TestConsoleRegistration_ImportTakeOverAnswerRoundTrip(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	site := func(domain string, more map[string]any) siteRegistration {
		return siteRegistration{actor: actor, publisherID: publisherID, domain: domain, cdn: "cloudflare", more: more}
	}
	registerSiteWith(t, f, site("stoa.example", map[string]any{takeOverAnswer: true}))
	registerSiteWith(t, f, site("harmonia.example", nil))
	want := map[string]bool{"stoa.example": true, "harmonia.example": false}

	assertTakeOverAnswers(t, f, actor, want)
	audited := map[string]any{}
	for _, d := range auditDetailsOf(t, f, "RegisterDomain") {
		domain, _ := d["domain"].(string)
		audited[domain] = d[takeOverAnswer]
	}
	for domain, answer := range want {
		if audited[domain] != answer {
			t.Errorf("the audit row of %s records %s = %v, want %v", domain, takeOverAnswer, audited[domain], answer)
		}
	}

	registerSiteWith(t, f, site("stoa.example", map[string]any{takeOverAnswer: false}))
	registerSiteWith(t, f, site("harmonia.example", map[string]any{takeOverAnswer: true}))
	assertTakeOverAnswers(t, f, actor, want)
	if n := countActions(t, f, "RegisterDomain"); n != len(want) {
		t.Fatalf("%d RegisterDomain audit rows after registering two sites twice, want %d", n, len(want))
	}
}

// assertTakeOverAnswers reads the person's sites back through the console's
// resume route and checks each one's answer.
func assertTakeOverAnswers(t *testing.T, f *consoleFixture, actor consoleActorBody, want map[string]bool) {
	t.Helper()
	_, sites := listRegistrations(t, f, actor)
	if len(sites) != len(want) {
		t.Fatalf("ListRegistrations returned %d sites, want %d: %v", len(sites), len(want), sites)
	}
	for _, s := range sites {
		domain, _ := s["domain"].(string)
		if got, ok := s[takeOverAnswer].(bool); !ok || got != want[domain] {
			t.Errorf("%s reads %s = %v, want %v", domain, takeOverAnswer, s[takeOverAnswer], want[domain])
		}
	}
}
