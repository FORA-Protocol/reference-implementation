//go:build integration

package transport_test

import (
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// Refusals on the publisher-scoped routes, each paired with proof that the
// refused request wrote nothing. A status alone would pass on a refusal that
// had already committed half its work.

// TestConsoleRegistration_RegisterDomainRefusesAGrantlessPublisherID is the
// authorization gate on the write that claims a site. Someone holding a grant
// for their own payee may not register a site under another payee, even when
// that site's document names the other payee; the answer is not-found, so the
// id cannot be probed.
//
// Legs: the refused write goes signed HTTP -> service; the absence of a site is
// read back through the signed resume route, and the absence of an audit row
// through the audit repository, for the reason auditActions gives.
func TestConsoleRegistration_RegisterDomainRefusesAGrantlessPublisherID(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("editor@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	other := newActor("editor@other.example")
	f.registerPublisher(t, other, "Harmonia Records")
	// The document is exactly what the owner would publish, so the refusal can
	// only come from the grant check.
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", ownerID, consoleContributorHost))

	status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
		"actor":              other,
		"publisher_id":       ownerID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/articles/"},
		"terms":              chargingTerms(),
	})
	if status != http.StatusNotFound {
		t.Fatalf("registering a site under another publisher returned status %d, body = %v", status, body)
	}
	if _, sites := listRegistrations(t, f, owner); len(sites) != 0 {
		t.Fatalf("the refused registration left the owner with sites: %v", sites)
	}
	if _, sites := listRegistrations(t, f, other); len(sites) != 0 {
		t.Fatalf("the refused registration left the caller with sites: %v", sites)
	}
	if n := countActions(t, f, "RegisterDomain"); n != 0 {
		t.Fatalf("the refused registration wrote %d RegisterDomain audit rows", n)
	}
}

// TestConsoleRegistration_CheckRefusesAnotherPublishersDomain covers the owner
// check that follows the grant check. The caller asks about their OWN payee,
// so the grant check passes, but the domain belongs to someone else. The answer
// is the not-found an unregistered domain gets, and the owner's listing is not
// touched: a check writes last_checked_at and an audit row, and neither may be
// written for a site the caller does not own.
//
// Legs: the refused check goes signed HTTP -> service; the owner's listing is
// read back through the signed resume route, and the audit rows through the
// audit repository.
func TestConsoleRegistration_CheckRefusesAnotherPublishersDomain(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("editor@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	registerSite(t, f, owner, ownerID, "stoa.example", "cloudflare")
	before := snapshotSite(t, f, owner, "stoa.example")

	other := newActor("editor@other.example")
	otherID := f.registerPublisher(t, other, "Harmonia Records")
	status, body := f.post(t, consolePath(transport.ConsoleCheckDomainPath), map[string]any{
		"actor": other, "publisher_id": otherID, "domain": "stoa.example",
	})
	if status != http.StatusNotFound {
		t.Fatalf("checking another publisher's domain returned status %d, body = %v", status, body)
	}
	assertSiteUntouched(t, f, owner, "stoa.example", before)
}

// TestConsoleRegistration_RefusesEveryCallWithoutASeat covers a deployment
// whose operator has not provisioned the seat for the Exchange's own domain.
// No registration can succeed there, and none may write anything. The answer
// is a server-side outage whose kind says so, and the operator's instruction
// stays in the Exchange's log instead of travelling to the console.
//
// Legs: the refused calls go signed HTTP -> service -> the seat read. The
// absence of a grant is read back through the signed resume route once the
// seat exists, which is the first moment that route can answer.
func TestConsoleRegistration_RefusesEveryCallWithoutASeat(t *testing.T) {
	const unseeded = "unseeded.exchange.example"
	f := startConsoleFixture(t).exchangeAs(t, exchangetest.Options{ExchangeDomain: unseeded, WithoutSeat: true})
	actor := newActor("editor@publisher.example")

	status, body := f.post(t, consolePath(transport.ConsoleRegisterPublisherPath), map[string]any{
		"actor": actor, "display_name": "Stoa Press",
	})
	assertMissingSeat(t, "RegisterPublisher", status, body)
	status, body = f.post(t, consolePath(transport.ConsoleListRegistrationsPath), map[string]any{"actor": actor})
	assertMissingSeat(t, "ListRegistrations", status, body)

	repotest.SeedSeat(t, f.ctx, f.pool, repotest.Seat{Domain: unseeded})
	assertNoPublisherFor(t, f, actor)
}

// assertMissingSeat holds one answer to the refusal a missing seat gets: a 503
// whose kind names the outage, with nothing of the operator's instruction in
// the text.
func assertMissingSeat(t *testing.T, call string, status int, body map[string]any) {
	t.Helper()
	if status != http.StatusServiceUnavailable || body["kind"] != consoleapi.ErrorKindUnavailable {
		t.Fatalf("%s without a seat = %d %v, want %d %s",
			call, status, body, http.StatusServiceUnavailable, consoleapi.ErrorKindUnavailable)
	}
	if msg, _ := body["error"].(string); strings.Contains(msg, "seed") || strings.Contains(msg, "seat") {
		t.Fatalf("%s: the operator's instruction reached the caller: %q", call, msg)
	}
}

// TestConsoleRegistration_RefusesASitemapItCannotFetch keeps the sitemap to an
// address on the registered site that carries no credentials. Automatic import
// fetches it under the publisher's authorization; credentials in it would be
// sent to the site and kept in a row every operator reads.
func TestConsoleRegistration_RefusesASitemapItCannotFetch(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))

	for _, sitemap := range []string{
		"https://user:secret@stoa.example/sitemap.xml",
		"https://user@stoa.example/sitemap.xml",
		"ftp://stoa.example/sitemap.xml",
		"https://elsewhere.example/sitemap.xml",
	} {
		status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
			"actor":              actor,
			"publisher_id":       publisherID,
			"domain":             "stoa.example",
			"cdn":                "cloudflare",
			"content_path_scope": []string{"/articles/"},
			"terms":              chargingTerms(),
			"sitemap_url":        sitemap,
		})
		if status != http.StatusBadRequest {
			t.Fatalf("sitemap %q returned status %d, body = %v", sitemap, status, body)
		}
	}
	if _, sites := listRegistrations(t, f, actor); len(sites) != 0 {
		t.Fatalf("a refused sitemap still registered the site: %v", sites)
	}
}

// siteSnapshot is a registered site as it stood before a call a test expects
// to be refused: its listing, and how many checks had been audited on the seat.
type siteSnapshot struct {
	site   map[string]any
	checks int
}

// snapshotSite records the owner's site before a call that must not touch it.
func snapshotSite(t *testing.T, f *consoleFixture, owner consoleActorBody, domain string) siteSnapshot {
	t.Helper()
	return siteSnapshot{site: listedSite(t, f, owner, domain), checks: countActions(t, f, "CheckDomain")}
}

// assertSiteUntouched proves a refused call on a registered site wrote nothing:
// the listing reads back as it did before, and no further check was audited.
//
// The timestamps render to the second, so a write in the same second as the
// registration would read back unchanged. The audit rows are what close that:
// a check writes its row in the transaction that moves the timestamp.
func assertSiteUntouched(
	t *testing.T, f *consoleFixture, owner consoleActorBody, domain string, before siteSnapshot,
) {
	t.Helper()
	after := listedSite(t, f, owner, domain)
	for _, field := range []string{"state", "verified_at", "last_checked_at"} {
		if after[field] != before.site[field] {
			t.Fatalf("the refused call moved %s from %v to %v", field, before.site[field], after[field])
		}
	}
	if n := countActions(t, f, "CheckDomain"); n != before.checks {
		t.Fatalf("the refused call wrote %d CheckDomain audit rows", n-before.checks)
	}
}
