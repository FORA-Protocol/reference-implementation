//go:build integration

package transport_test

import (
	"net/http"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// The preflight route: the question RegisterDomain asks, without the answer it
// writes.
//
// It exists because of the order proof has to happen in. The publisher must
// serve a document naming this seat and this payee BEFORE their domain can be
// stored, so the console needs to ask "would this be accepted now" about a
// domain that has no row -- which CheckDomain answers not-found for, and
// RegisterDomain answers by registering.

// TestConsolePreflight_ReportsAnUnprovedDomainAndWritesNothing is the ordinary
// case: the publisher is partway through setting up and has not published the
// document yet.
func TestConsolePreflight_ReportsAnUnprovedDomainAndWritesNothing(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	status, body := f.post(t, consolePath(transport.ConsolePreflightDomainPath), map[string]any{
		"actor": actor, "publisher_id": publisherID, "domain": "stoa.example",
	})
	if status != http.StatusOK {
		t.Fatalf("Preflight status = %d, body = %v", status, body)
	}
	if body["passed"] != false {
		t.Fatalf("a domain serving no document passed: %v", body)
	}
	reasons, _ := body["reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != consoleapi.ReasonManifestMissing {
		t.Fatalf("reasons = %v, want %s", reasons, consoleapi.ReasonManifestMissing)
	}

	// Publishing the document and asking again passes, which is what makes the
	// refusal a step rather than a dead end.
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))
	status, retry := f.post(t, consolePath(transport.ConsolePreflightDomainPath), map[string]any{
		"actor": actor, "publisher_id": publisherID, "domain": "stoa.example",
	})
	if status != http.StatusOK || retry["passed"] != true {
		t.Fatalf("a published document did not pass: %d %v", status, retry)
	}

	// Neither answer registered anything. A passing preflight is advice; the
	// registration route is what writes.
	status, list := f.post(t, consolePath(transport.ConsoleListRegistrationsPath),
		map[string]any{"actor": actor})
	if status != http.StatusOK {
		t.Fatalf("ListRegistrations status = %d, body = %v", status, list)
	}
	if sites, _ := list["sites"].([]any); len(sites) != 0 {
		t.Fatalf("asking about a domain registered %d of them: %v", len(sites), sites)
	}
}

// TestConsolePreflight_ReportsADomainAnotherPublisherHolds covers the one
// refusal the publisher cannot repair by editing a file.
//
// It is reported here rather than at registration because everything between
// the two is work: publishing a document, installing a package. Asking somebody
// to do all of it for a hostname they will never be able to register is the
// worst possible moment to tell them.
//
// It names no publisher. The caller holds no grant for that one, and naming it
// would turn this route into a lookup of who owns which hostname.
//
// The owner holds the domain without having proved it: saving holds a
// hostname, and a stranger is told it is taken all the same. That answer is
// what sends them to support, whose operator releases a hold that should not
// be there.
func TestConsolePreflight_ReportsADomainAnotherPublisherHolds(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("owner@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	registerPending(t, f, owner, ownerID, "stoa.example")

	stranger := newActor("stranger@elsewhere.example")
	strangerID := f.registerPublisher(t, stranger, "Harmonia Records")
	status, body := f.post(t, consolePath(transport.ConsolePreflightDomainPath), map[string]any{
		"actor": stranger, "publisher_id": strangerID, "domain": "stoa.example",
	})
	if status != http.StatusOK {
		t.Fatalf("Preflight status = %d, body = %v", status, body)
	}
	if body["passed"] != false {
		t.Fatalf("a domain another publisher holds passed: %v", body)
	}
	reasons, _ := body["reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != consoleapi.ReasonDomainTaken {
		t.Fatalf("reasons = %v, want %s alone", reasons, consoleapi.ReasonDomainTaken)
	}
	for _, r := range reasons {
		if text, _ := r.(string); text == ownerID {
			t.Fatalf("the refusal names the other publisher: %v", reasons)
		}
	}
}

// TestConsolePreflight_RefusesAGrantlessPublisherID closes the route that would
// otherwise make this an open fetcher: it takes a hostname and fetches it, so
// the caller has to be a publisher asking about their own setup.
func TestConsolePreflight_RefusesAGrantlessPublisherID(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	f.registerPublisher(t, actor, "Stoa Press")

	status, body := f.post(t, consolePath(transport.ConsolePreflightDomainPath), map[string]any{
		"actor": actor, "publisher_id": "pub_not_theirs", "domain": "stoa.example",
	})
	if status != http.StatusNotFound {
		t.Fatalf("a publisher id the caller holds no grant for = %d, body = %v", status, body)
	}
}

// TestConsolePreflight_RefusesAMalformedDomain pins that the same validation
// registration applies runs here too. A publisher who typed a URL instead of a
// hostname should learn it at the step that asks, not three steps later.
func TestConsolePreflight_RefusesAMalformedDomain(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	for _, domain := range []string{"https://stoa.example", "stoa.example/articles", "stoa.example:8443"} {
		status, body := f.post(t, consolePath(transport.ConsolePreflightDomainPath), map[string]any{
			"actor": actor, "publisher_id": publisherID, "domain": domain,
		})
		if status != http.StatusBadRequest {
			t.Fatalf("preflight on %q = %d, body = %v", domain, status, body)
		}
	}
}
