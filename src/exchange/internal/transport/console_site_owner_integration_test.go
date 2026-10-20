//go:build integration

package transport_test

import (
	"fmt"
	"net/http"
	"sync"
	"testing"

	"golang.org/x/sync/errgroup"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// A site belongs to one payee for the whole deployment, and only another payee
// is refused it. The tests in this file cover the states in which the payee
// who already holds the site registers it again: a double submit, a site with
// no listing on this seat, and a second seat selling the same site.

// TestConsoleRegistration_ConcurrentDomainSubmitsRegisterOneSite is the double
// submit for a site: identical registrations for the same publisher in flight
// together.
//
// Every request reads "no site yet" before any of them writes, so all of them
// reach the insert. The first commits; each of the others fails on the unique
// key, rolls back, reads the rows the first one wrote, and answers with them.
// None of them may answer "registered to another publisher", which is what a
// publisher was told about their own site before the recovery existed.
//
// Legs: each write goes signed HTTP -> service -> repositories -> database, and
// the site read-back comes through the signed resume route. The audit count is
// one tier lower, through the audit repository, for the reason auditActions
// gives.
func TestConsoleRegistration_ConcurrentDomainSubmitsRegisterOneSite(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	body := map[string]any{
		"actor":              actor,
		"publisher_id":       publisherID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/articles/"},
		"terms":              chargingTerms(),
	}

	// Each goroutine warms its own connection first, for the reason
	// TestConsoleRegistration_ConcurrentSubmitsMintOnePublisher gives: without
	// it the requests arrive one after another and never race.
	const submits = 8
	states := make([]any, submits)
	var ready sync.WaitGroup
	ready.Add(submits)
	start := make(chan struct{})
	var g errgroup.Group
	for i := range submits {
		g.Go(func() error {
			_, warmErr := f.postConcurrently(transport.ConsoleListRegistrationsPath, map[string]any{"actor": actor})
			ready.Done()
			if warmErr != nil {
				return fmt.Errorf("warm-up ListRegistrations: %w", warmErr)
			}
			<-start
			out, err := f.postConcurrently(transport.ConsoleRegisterDomainPath, body)
			if err != nil {
				return err
			}
			if out["domain"] != "stoa.example" {
				return fmt.Errorf("submit %d did not answer with the site: %v", i, out)
			}
			states[i] = out["state"]
			return nil
		})
	}
	ready.Wait()
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent RegisterDomain: %v", err)
	}
	for i, s := range states {
		if s != repo.ListingStatePendingManifest {
			t.Fatalf("submit %d answered with listing state %v, want %s", i, s, repo.ListingStatePendingManifest)
		}
	}

	listedSite(t, f, actor, "stoa.example")
	if n := countActions(t, f, "RegisterDomain"); n != 1 {
		t.Fatalf("%d concurrent submits wrote %d RegisterDomain audit rows, want 1", submits, n)
	}
}

// TestConsoleRegistration_ListsAnOwnSiteThatHasNoListing covers a site the
// publisher already holds with no listing on this seat, which is what a seat
// sees after its listing was removed. Registering it again writes the listing
// and leaves the site row as it was.
//
// Legs: the registration goes signed HTTP -> service -> repositories ->
// database, and the result is read back through the signed resume route.
func TestConsoleRegistration_ListsAnOwnSiteThatHasNoListing(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	// Tier-2 arrange through the production write ports, because no public
	// route removes a listing or registers a site without listing it: the only
	// route that writes a site writes its listing in the same transaction.
	repotest.SeedSite(t, f.ctx, f.pool, repotest.Site{
		Domain:      "stoa.example",
		PublisherID: publisherID,
		CDN:         "cloudfront",
		Unlisted:    true,
	})
	if _, sites := listRegistrations(t, f, actor); len(sites) != 0 {
		t.Fatalf("an unlisted site is already listed: %v", sites)
	}

	// Registered as Cloudflare, although the site row says CloudFront: the site
	// row is not rewritten, because a site sits behind one CDN whichever seat
	// sells it.
	registerSite(t, f, actor, publisherID, "stoa.example", "cloudflare")

	site := listedSite(t, f, actor, "stoa.example")
	if site["state"] != repo.ListingStateActive {
		t.Fatalf("listing state = %v, want %s", site["state"], repo.ListingStateActive)
	}
	if site["cdn"] != "cloudfront" {
		t.Fatalf("site cdn = %v, want the cloudfront the site row already held", site["cdn"])
	}
	if terms, _ := site["terms"].(map[string]any); terms["pricing_model"] != "per_unit" {
		t.Fatalf("listing terms = %v, want the terms this registration submitted", terms)
	}
	if n := countActions(t, f, "RegisterDomain"); n != 1 {
		t.Fatalf("the registration wrote %d RegisterDomain audit rows, want 1", n)
	}
}

// TestConsoleRegistration_ListsAnOwnSiteOnASecondSeat is the multi-seat form of
// the same state: one database, two Exchanges, and a payee who sells the same
// site through both. The second seat finds the site registered to the payee
// and lists it; the first seat's listing is untouched.
//
// Legs: both registrations go signed HTTP -> service -> repositories ->
// database, each through its own seat's Exchange, and each seat's listing is
// read back through that seat's signed resume route.
func TestConsoleRegistration_ListsAnOwnSiteOnASecondSeat(t *testing.T) {
	first := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := first.registerPublisher(t, actor, "Stoa Press")
	registerSite(t, first, actor, publisherID, "stoa.example", "cloudflare")

	second := first.secondSeatFor(t, actor, publisherID)

	status, body := second.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
		"actor":              actor,
		"publisher_id":       publisherID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/"},
		"terms":              map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	})
	if status != http.StatusOK || body["domain"] != "stoa.example" {
		t.Fatalf("registering the payee's own site on a second seat: status %d, body %v", status, body)
	}

	if terms, _ := listedSite(t, second, actor, "stoa.example")["terms"].(map[string]any); terms["pricing_model"] != "free" {
		t.Fatalf("second seat's listing terms = %v, want the free terms it registered", terms)
	}
	if terms, _ := listedSite(t, first, actor, "stoa.example")["terms"].(map[string]any); terms["pricing_model"] != "per_unit" {
		t.Fatalf("first seat's listing terms = %v, want its own terms untouched", terms)
	}
}

// TestConsoleRegistration_ListsEverySiteWithItsOwnSiteRow covers the resume
// read for a publisher with more than one site. The sites are read in one
// query and paired with their listings by domain, so each listed site must
// carry its own site row's values, here the CDN, and not a neighbour's.
//
// Legs: both registrations go signed HTTP -> service -> repositories ->
// database, and the list comes back through the signed resume route.
func TestConsoleRegistration_ListsEverySiteWithItsOwnSiteRow(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	registerSite(t, f, actor, publisherID, "stoa.example", "cloudfront")
	registerSite(t, f, actor, publisherID, "archive.stoa.example", "cloudflare")

	_, sites := listRegistrations(t, f, actor)
	want := map[string]string{"archive.stoa.example": "cloudflare", "stoa.example": "cloudfront"}
	if len(sites) != len(want) {
		t.Fatalf("ListRegistrations returned %d sites, want %d: %v", len(sites), len(want), sites)
	}
	if sites[0]["domain"] != "archive.stoa.example" {
		t.Fatalf("sites are not ordered by domain: first is %v", sites[0]["domain"])
	}
	for _, s := range sites {
		domain, _ := s["domain"].(string)
		if s["cdn"] != want[domain] {
			t.Fatalf("site %s lists cdn %v, want %s", domain, s["cdn"], want[domain])
		}
	}
}

// countActions counts the audit rows with one action on the fixture's seat.
func countActions(t *testing.T, f *consoleFixture, action string) int {
	t.Helper()
	var n int
	for _, a := range auditActions(t, f) {
		if a == action {
			n++
		}
	}
	return n
}
