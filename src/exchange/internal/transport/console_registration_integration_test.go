//go:build integration

package transport_test

import (
	"net/http"
	"slices"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// TestConsoleRegistration_PublisherAndSiteRoundTrip drives one publisher
// through the whole registration surface and reads every result back through
// the same surface: register the payee, save a site, verify it, and list what
// the person now has.
//
// Every leg is a protocol round-trip. The writes go through the signed HTTP
// routes, the service, the repositories and the database; the read comes back
// through the signed HTTP route that the console itself uses to resume a
// half-finished setup. Nothing in the test reads a row.
func TestConsoleRegistration_PublisherAndSiteRoundTrip(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")

	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	status, reg := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
		"actor":              actor,
		"publisher_id":       publisherID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/articles/"},
		"terms":              chargingTerms(),
	})
	if status != http.StatusOK {
		t.Fatalf("RegisterDomain status = %d, body = %v", status, reg)
	}
	assertSavedCloudflareSite(t, reg)

	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))
	if checked := checkDomain(t, f, actor, publisherID, "stoa.example"); checked["passed"] != true {
		t.Fatalf("CheckDomain on a matching document did not pass: %v", checked)
	}

	status, list := f.post(t, consolePath(transport.ConsoleListRegistrationsPath), map[string]any{"actor": actor})
	if status != http.StatusOK {
		t.Fatalf("ListRegistrations status = %d, body = %v", status, list)
	}
	if list["registered"] != true {
		t.Fatalf("ListRegistrations reports registered = %v after a registration", list["registered"])
	}
	if list["publisher_id"] != publisherID {
		t.Fatalf("ListRegistrations publisher_id = %v, want %s", list["publisher_id"], publisherID)
	}
	sites, _ := list["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("ListRegistrations returned %d sites, want 1", len(sites))
	}
	site, _ := sites[0].(map[string]any)
	if site["domain"] != "stoa.example" {
		t.Fatalf("listed site domain = %v, want stoa.example", site["domain"])
	}
	if site["state"] != repo.ListingStateActive || site["verified_at"] == nil {
		t.Fatalf("the checked site lists state %v, verified_at %v; want it verified",
			site["state"], site["verified_at"])
	}
	terms, _ := site["terms"].(map[string]any)
	if terms["pricing_model"] != "per_unit" || terms["rate"] != "0.02" {
		t.Fatalf("listed site terms = %v, want the arrangement it registered with", terms)
	}
	if terms["attribution_required"] != true {
		t.Fatalf("listed site did not keep the attribution answer: %v", terms)
	}
}

// assertSavedCloudflareSite holds what the domain route answered for a
// Cloudflare site it has just saved.
func assertSavedCloudflareSite(t *testing.T, reg map[string]any) {
	t.Helper()
	// Saved, not verified: nothing has proved the site is the caller's yet.
	if got := reg["state"]; got != repo.ListingStatePendingManifest {
		t.Fatalf("new listing state = %v, want %s", got, repo.ListingStatePendingManifest)
	}
	if reg["verified_at"] != nil {
		t.Fatalf("a site nothing has verified reports verified_at %v", reg["verified_at"])
	}
	// Every supported integration verifies inside an edge function, so a
	// CloudFront site registers on Ed25519 like the other one.
	if got := reg["signing_scheme"]; got != "ED25519" {
		t.Fatalf("signing_scheme = %v, want ED25519", got)
	}
	if got := reg["cdn"]; got != "cloudflare" {
		t.Fatalf("cdn = %v, want cloudflare", got)
	}
}

// TestConsoleRegistration_RepeatedSubmitsMintOnePublisher is the retry-safety
// property stated as a test: a double submit, a lost response or a re-entered
// form must not leave a person holding two payees.
//
// It also covers the second form the same property takes, a repeated domain
// registration, which has to come back as the existing site rather than as a
// duplicate or a refusal.
func TestConsoleRegistration_RepeatedSubmitsMintOnePublisher(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")

	first := f.registerPublisher(t, actor, "Stoa Press")
	second := f.registerPublisher(t, actor, "Stoa Press Again")
	if first != second {
		t.Fatalf("a repeated registration minted a second publisher: %s then %s", first, second)
	}

	f.manifest.serve("stoa.example", publisherManifest("stoa.example", first, consoleContributorHost))
	domainBody := map[string]any{
		"actor":              actor,
		"publisher_id":       first,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/articles/"},
		"terms":              chargingTerms(),
	}
	status, one := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), domainBody)
	if status != http.StatusOK {
		t.Fatalf("first RegisterDomain status = %d, body = %v", status, one)
	}
	status, two := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), domainBody)
	if status != http.StatusOK {
		t.Fatalf("repeated RegisterDomain status = %d, body = %v", status, two)
	}

	status, list := f.post(t, consolePath(transport.ConsoleListRegistrationsPath), map[string]any{"actor": actor})
	if status != http.StatusOK {
		t.Fatalf("ListRegistrations status = %d, body = %v", status, list)
	}
	sites, _ := list["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("after two identical registrations the publisher has %d sites, want 1", len(sites))
	}
}

// TestConsoleRegistration_CheckDomainActivatesOnAMatchingManifest is the proof
// of domain control on a listing that has not proved it yet. A check against a
// document that fails the rules leaves the listing pending. A check against one
// that names this seat, attests the registered payee, and authorizes the
// operator's catalog contributor moves it to ACTIVE.
//
// Legs: each check goes signed HTTP -> service -> repositories -> database, and
// the listing state is read back through the signed resume route the console
// uses. The audit assertion is one tier lower, through the audit repository,
// for the reason auditActions gives.
func TestConsoleRegistration_CheckDomainActivatesOnAMatchingManifest(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	registerPending(t, f, actor, publisherID, "stoa.example")

	// The document attests another payee, so the check fails and the listing
	// stays pending.
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", "pub_someone_else", consoleContributorHost))
	if failed := checkDomain(t, f, actor, publisherID, "stoa.example"); failed["passed"] != false {
		t.Fatalf("CheckDomain on a document attesting another payee passed: %v", failed)
	}
	if got := listedSite(t, f, actor, "stoa.example")["state"]; got != repo.ListingStatePendingManifest {
		t.Fatalf("listing state after a failed check = %v, want %s", got, repo.ListingStatePendingManifest)
	}

	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))
	if passed := checkDomain(t, f, actor, publisherID, "stoa.example"); passed["passed"] != true {
		t.Fatalf("CheckDomain on a matching document did not pass: %v", passed)
	}
	site := listedSite(t, f, actor, "stoa.example")
	if site["state"] != repo.ListingStateActive {
		t.Fatalf("listing state after a passing check = %v, want %s", site["state"], repo.ListingStateActive)
	}
	if site["verified_at"] == nil {
		t.Fatal("a passing check left verified_at unset")
	}

	want := []string{"CheckDomain", "CheckDomain", "RegisterDomain", "RegisterPublisher"}
	if got := slices.Sorted(slices.Values(auditActions(t, f))); !slices.Equal(got, want) {
		t.Fatalf("audit actions (sorted) = %v, want %v", got, want)
	}
	// Both checks write a CheckDomain row; the detail tells them apart. Exactly
	// one records an activation, and it is the one that passed.
	var activations int
	for _, d := range auditDetailsOf(t, f, "CheckDomain") {
		if d["activated"] != true {
			continue
		}
		activations++
		if d["passed"] != true {
			t.Fatalf("an activating CheckDomain audit row records a failed check: %v", d)
		}
	}
	if activations != 1 {
		t.Fatalf("CheckDomain audit rows record %d activations, want 1", activations)
	}
}

// checkDomain runs one check through the signed route and returns the body.
func checkDomain(t *testing.T, f *consoleFixture, actor consoleActorBody, publisherID, domain string) map[string]any {
	t.Helper()
	status, body := f.post(t, consolePath(transport.ConsoleCheckDomainPath), map[string]any{
		"actor": actor, "publisher_id": publisherID, "domain": domain,
	})
	if status != http.StatusOK {
		t.Fatalf("CheckDomain status = %d, body = %v", status, body)
	}
	return body
}

// registerSite saves the site through the domain route, publishes the document
// the site has to serve, and verifies the site through the check route, failing
// the test on anything but a verified site. Tests that are about a later step
// use it so their own body states only what they are testing.
func registerSite(t *testing.T, f *consoleFixture, actor consoleActorBody, publisherID, domain, cdn string) {
	t.Helper()
	registerSiteWith(t, f, siteRegistration{actor: actor, publisherID: publisherID, domain: domain, cdn: cdn})
}

// siteRegistration is one RegisterDomain call: who registers which site
// behind which network, and any further members of the request a test is
// about.
type siteRegistration struct {
	actor                    consoleActorBody
	publisherID, domain, cdn string
	more                     map[string]any
}

// registerSiteWith is registerSite with further members in the request body.
func registerSiteWith(t *testing.T, f *consoleFixture, in siteRegistration) {
	t.Helper()
	req := siteBody(in.actor, in.publisherID, in.domain)
	req["cdn"] = in.cdn
	for k, v := range in.more {
		req[k] = v
	}
	status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), req)
	if status != http.StatusOK {
		t.Fatalf("RegisterDomain(%s) status = %d, body = %v", in.domain, status, body)
	}
	f.manifest.serve(in.domain, publisherManifest(in.domain, in.publisherID, f.contributor))
	if checked := checkDomain(t, f, in.actor, in.publisherID, in.domain); checked["passed"] != true {
		t.Fatalf("CheckDomain(%s) did not verify the site: %v", in.domain, checked)
	}
}

// registerPending saves the site through the domain route and leaves it
// unverified: nothing is published on it, and it is never checked.
func registerPending(t *testing.T, f *consoleFixture, actor consoleActorBody, publisherID, domain string) {
	t.Helper()
	if status, body := saveSite(t, f, actor, publisherID, domain); status != http.StatusOK ||
		body["state"] != repo.ListingStatePendingManifest {
		t.Fatalf("RegisterDomain(%s) = %d %v; want the site saved, not verified", domain, status, body)
	}
}

// saveSite sends the domain route a site to save under the publisher, with the
// terms most of these tests use, and returns the answer whatever it is.
func saveSite(
	t *testing.T, f *consoleFixture, actor consoleActorBody, publisherID, domain string,
) (int, map[string]any) {
	t.Helper()
	return f.post(t, consolePath(transport.ConsoleRegisterDomainPath), siteBody(actor, publisherID, domain))
}

// siteBody is the domain route's request for one site under the publisher.
func siteBody(actor consoleActorBody, publisherID, domain string) map[string]any {
	return map[string]any{
		"actor":              actor,
		"publisher_id":       publisherID,
		"domain":             domain,
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/articles/"},
		"terms":              chargingTerms(),
	}
}

// checkReasons returns the reasons one check body reported, the body being what
// checkDomain returned. checkDomain has already asserted the transport outcome
// itself: a manifest that does not satisfy the rules is a 200 carrying reasons,
// never an error status, because nothing went wrong with the request.
func checkReasons(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, _ := body["reasons"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		s, _ := r.(string)
		out = append(out, s)
	}
	if body["passed"] == true && len(out) != 0 {
		t.Fatalf("CheckDomain reported passed with reasons %v", out)
	}
	// A check that does not pass never walks an active listing back. The site
	// proved itself when it was verified; what it serves later is between it and
	// the push path, which re-reads the document on every catalogue submission.
	// Two mechanisms that could disagree about the same listing is the thing
	// this avoids.
	reg, _ := body["registration"].(map[string]any)
	if body["passed"] != true && reg["state"] != repo.ListingStateActive {
		t.Fatalf("a failed check moved the listing to state %v, want it left %s",
			reg["state"], repo.ListingStateActive)
	}
	return out
}

// auditActions reads back every audit action recorded for the seat.
//
// This asserts through the repository interface rather than the public API,
// because the Exchange publishes no read surface for its audit log -- the admin
// plane exposes transaction evidence and nothing else. That is a corner cut
// deliberately and named here rather than hidden: the assertion is one tier
// below the route it verifies, so this is a persistence check, not a protocol
// round-trip. A publisher-visible activity read would let it move up a tier.
func auditActions(t *testing.T, f *consoleFixture) []string {
	t.Helper()
	records, err := f.audit.ByTenant(f.ctx, f.seatID)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	actions := make([]string, 0, len(records))
	for _, r := range records {
		actions = append(actions, r.Action)
	}
	return actions
}

// TestConsoleRegistration_AuditRecordsTheActingPerson pins what an operator can
// reconstruct afterwards: every registration write names the OIDC subject that
// made it, not just the process that relayed it.
func TestConsoleRegistration_AuditRecordsTheActingPerson(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	registerSite(t, f, actor, publisherID, "stoa.example", "cloudflare")

	records, err := f.audit.ByTenant(f.ctx, f.seatID)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("registering a publisher, saving a site and verifying it wrote %d audit rows, want 3",
			len(records))
	}
	wantActor := actor.OIDCIssuer + "|" + actor.OIDCSubject
	for _, r := range records {
		if r.Actor == nil || *r.Actor != wantActor {
			t.Fatalf("audit row %q names actor %v, want %q", r.Action, r.Actor, wantActor)
		}
	}
	if got := auditActions(t, f); len(got) != 3 {
		t.Fatalf("audit actions = %v", got)
	}
}
