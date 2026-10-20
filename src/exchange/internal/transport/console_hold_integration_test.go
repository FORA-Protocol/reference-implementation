//go:build integration

package transport_test

import (
	"net/http"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// A site saved before it is proved: the domain route saves it without reading
// anything from it, the hostname is held for the payee that saved it, and the
// site is not verified -- nothing of it can be sold -- until the check route
// reads a document from it that proves the payee controls it, and only once its
// terms are chosen.
//
// Legs: every write goes signed HTTP -> service -> repositories -> database,
// and every listing is read back through the signed resume route the console
// uses. Audit rows are read one tier lower, through the audit repository, for
// the reason auditActions gives.

// TestConsoleRegistration_SavesASiteBeforeItIsProven: the domain route saves a
// site whose document says nothing at all, and asks nothing of the site to do
// it. The listing is pending, with no verification and no check recorded, and
// the audit row says it was written pending.
func TestConsoleRegistration_SavesASiteBeforeItIsProven(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	fetched := f.manifest.Calls()

	registerPending(t, f, actor, publisherID, "stoa.example")

	if n := f.manifest.Calls() - fetched; n != 0 {
		t.Fatalf("saving a site read its document %d times; saving must read nothing from it", n)
	}
	site := listedSite(t, f, actor, "stoa.example")
	if site["state"] != repo.ListingStatePendingManifest {
		t.Fatalf("saved site state = %v, want %s", site["state"], repo.ListingStatePendingManifest)
	}
	if site["verified_at"] != nil || site["last_checked_at"] != nil {
		t.Fatalf("a site nothing has checked reads verified_at %v, last_checked_at %v",
			site["verified_at"], site["last_checked_at"])
	}
	details := auditDetailsOf(t, f, "RegisterDomain")
	if len(details) != 1 || details[0]["listing_state"] != repo.ListingStatePendingManifest {
		t.Fatalf("RegisterDomain audit rows = %v; want one recording %s", details, repo.ListingStatePendingManifest)
	}
}

// TestConsoleRegistration_AHeldSiteIsRefusedToAnotherPublisher: a site one
// payee saved but never proved is still theirs to everybody else. Another
// payee's registration of it is refused and writes nothing, even when the site
// serves a document naming that other payee. What the other payee's preflight
// answers is TestConsolePreflight_ReportsADomainAnotherPublisherHolds's.
func TestConsoleRegistration_AHeldSiteIsRefusedToAnotherPublisher(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("editor@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	registerPending(t, f, owner, ownerID, "stoa.example")

	stranger := newActor("desk@harmonia.example")
	strangerID := f.registerPublisher(t, stranger, "Harmonia Records")
	// The stranger even serves a document naming themselves: holding the name
	// is not something a later proof undoes.
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", strangerID, consoleContributorHost))
	status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
		"actor":              stranger,
		"publisher_id":       strangerID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/"},
		"terms":              map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	})
	if status != http.StatusBadRequest || body["kind"] != consoleapi.ErrorKindInvalidRequest {
		t.Fatalf("registering a site another payee holds = %d %v; want 400 %s",
			status, body, consoleapi.ErrorKindInvalidRequest)
	}
	if _, sites := listRegistrations(t, f, stranger); len(sites) != 0 {
		t.Fatalf("the refused registration left the stranger with sites: %v", sites)
	}
	if terms, _ := listedSite(t, f, owner, "stoa.example")["terms"].(map[string]any); terms["pricing_model"] != "per_unit" {
		t.Fatalf("the holder's listing terms = %v, want the terms they saved", terms)
	}
	if n := countActions(t, f, "RegisterDomain"); n != 1 {
		t.Fatalf("%d RegisterDomain audit rows, want the holder's one", n)
	}
}

// TestConsoleRegistration_ASiteWithoutTermsIsNeverActivated: a site saved
// before its terms were chosen cannot be verified, however good its document.
// The check is refused as a failed precondition before the document is read,
// and writes nothing: the listing stays pending, no check is recorded on it,
// and no CheckDomain audit row is written.
func TestConsoleRegistration_ASiteWithoutTermsIsNeverActivated(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	status, reg := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
		"actor":              actor,
		"publisher_id":       publisherID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/articles/"},
		"terms":              nil,
	})
	if status != http.StatusOK || reg["terms"] != nil {
		t.Fatalf("saving a site with no terms = %d %v; want it saved with none", status, reg)
	}
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))
	fetched := f.manifest.Calls()

	status, body := f.post(t, consolePath(transport.ConsoleCheckDomainPath), map[string]any{
		"actor": actor, "publisher_id": publisherID, "domain": "stoa.example",
	})
	if status != http.StatusBadRequest || body["kind"] != consoleapi.ErrorKindFailedPrecondition {
		t.Fatalf("checking a site with no terms = %d %v; want 400 %s",
			status, body, consoleapi.ErrorKindFailedPrecondition)
	}
	if n := f.manifest.Calls() - fetched; n != 0 {
		t.Fatalf("the refused check read the document %d times; it must refuse before reading", n)
	}
	site := listedSite(t, f, actor, "stoa.example")
	if site["state"] != repo.ListingStatePendingManifest || site["last_checked_at"] != nil {
		t.Fatalf("after the refused check the site reads state %v, last_checked_at %v; want it untouched",
			site["state"], site["last_checked_at"])
	}
	if n := countActions(t, f, "CheckDomain"); n != 0 {
		t.Fatalf("the refused check wrote %d CheckDomain audit rows", n)
	}
}
