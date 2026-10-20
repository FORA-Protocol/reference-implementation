//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/transport"
)

// TestConsoleRegistration_RefusesAnUnsignedCaller is the property the whole
// actor assertion rests on. The Exchange believes who the console says is
// acting only because the signature proves which process is speaking, so an
// unsigned request must not reach a handler at all.
func TestConsoleRegistration_RefusesAnUnsignedCaller(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("intruder@elsewhere.example")

	raw, err := json.Marshal(map[string]any{"actor": actor, "display_name": "Not Ours"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost,
		f.server.URL+consolePath(transport.ConsoleRegisterPublisherPath), strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The server's own client, with no signing transport in front of it.
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatalf("unsigned POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned registration status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	assertNoPublisherFor(t, f, actor)
}

// TestConsoleRegistration_RefusesASignerFromAnotherDirectory closes the hole
// the fixed directory exists to close. A caller that signs correctly with a key
// its OWN directory publishes must still be refused: the Exchange reads the one
// directory it was configured with, so only the configured console can assert
// an actor.
func TestConsoleRegistration_RefusesASignerFromAnotherDirectory(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("intruder@elsewhere.example")
	client := f.imposterClient(t)

	raw, err := json.Marshal(map[string]any{"actor": actor, "display_name": "Not Ours"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(f.ctx, http.MethodPost,
		f.server.URL+consolePath(transport.ConsoleRegisterPublisherPath), strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("imposter POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("imposter registration status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	assertNoPublisherFor(t, f, actor)
}

// TestConsoleRegistration_RefusesTheConsoleKeyNamingAnotherDirectory pins the
// other half of the fixed-directory rule under the Web Bot Auth profile: each
// signature is resolved in the directory its own Signature-Agent member names,
// and the console surface accepts only the configured console origin there. A
// request signed with the console's own key but naming any other directory is
// refused, even though the key would verify in the console's directory.
func TestConsoleRegistration_RefusesTheConsoleKeyNamingAnotherDirectory(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("misnamed@publisher.example")
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.client = consoleSigningClient(t, f.server, startConsoleDirectory(t, otherPriv), f.consoleKey)

	status, body := f.post(t, consolePath(transport.ConsoleRegisterPublisherPath), map[string]any{
		"actor": actor, "display_name": "Misnamed",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("the console key under another directory = %d %v, want %d", status, body, http.StatusUnauthorized)
	}
	f.client = consoleSigningClient(t, f.server, f.directory, f.consoleKey)
	assertNoPublisherFor(t, f, actor)
}

// TestConsoleRegistration_RefusesAnUnverifiedEmail keeps the verified-email
// gate on the side that writes the grant. The console gates it too, and that is
// not enough: the Exchange is what records who may act for a payee, so it must
// refuse rather than trust the caller to have refused.
func TestConsoleRegistration_RefusesAnUnverifiedEmail(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("unverified@publisher.example")
	actor.EmailVerified = false

	status, body := f.post(t, consolePath(transport.ConsoleRegisterPublisherPath), map[string]any{
		"actor": actor, "display_name": "Stoa Press",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("unverified-email registration status = %d, body = %v", status, body)
	}
	actor.EmailVerified = true
	assertNoPublisherFor(t, f, actor)
}

// TestConsoleRegistration_RefusesAnIncompleteActor covers the other half of the
// same gate: a console that failed to assert anyone. An empty issuer or subject
// would otherwise write a grant no later login could match.
//
// No read route can list what such an actor holds, because every read refuses
// the same actor. The audit log is the witness instead: the grant and its audit
// row commit together, so no RegisterPublisher row means no grant.
func TestConsoleRegistration_RefusesAnIncompleteActor(t *testing.T) {
	f := startConsoleFixture(t)
	for name, blank := range map[string]func(*consoleActorBody){
		"empty subject": func(a *consoleActorBody) { a.OIDCSubject = "" },
		"empty issuer":  func(a *consoleActorBody) { a.OIDCIssuer = "" },
	} {
		t.Run(name, func(t *testing.T) {
			actor := newActor("editor@publisher.example")
			blank(&actor)
			status, body := f.post(t, consolePath(transport.ConsoleRegisterPublisherPath), map[string]any{
				"actor": actor, "display_name": "Stoa Press",
			})
			if status != http.StatusBadRequest {
				t.Fatalf("registration status = %d, body = %v", status, body)
			}
		})
	}
	if n := countActions(t, f, "RegisterPublisher"); n != 0 {
		t.Fatalf("refused registrations wrote %d RegisterPublisher audit rows", n)
	}
}

// TestConsoleRegistration_RefusesAnotherPublishersSite covers the enumeration
// and the takeover in one test. A second person may not register a site that
// belongs to someone else, and the refusal must not tell them who does.
func TestConsoleRegistration_RefusesAnotherPublishersSite(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("editor@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	registerSite(t, f, owner, ownerID, "stoa.example", "cloudflare")

	other := newActor("editor@other.example")
	otherID := f.registerPublisher(t, other, "Harmonia Records")
	// Free terms, unlike the owner's charging ones, so a takeover that wrote
	// the listing would show in the owner's terms below.
	status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
		"actor":              other,
		"publisher_id":       otherID,
		"domain":             "stoa.example",
		"cdn":                "cloudflare",
		"content_path_scope": []string{"/"},
		"terms":              map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("taking another publisher's site returned status %d, body = %v", status, body)
	}
	msg, _ := body["error"].(string)
	if strings.Contains(msg, ownerID) {
		t.Fatalf("the refusal named the other publisher: %q", msg)
	}
	// The different terms must not change which rule refused the request.
	if !strings.Contains(msg, "registered to another publisher") {
		t.Fatalf("the takeover was refused for another reason: %q", msg)
	}

	// The site is still the first publisher's, and the second publisher holds
	// nothing -- both read back through the console's own resume call.
	status, list := f.post(t, consolePath(transport.ConsoleListRegistrationsPath), map[string]any{"actor": other})
	if status != http.StatusOK {
		t.Fatalf("ListRegistrations status = %d, body = %v", status, list)
	}
	if sites, _ := list["sites"].([]any); len(sites) != 0 {
		t.Fatalf("the refused registration left %d sites on the second publisher", len(sites))
	}
	ownerSite := listedSite(t, f, owner, "stoa.example")
	if ownerSite["state"] != repo.ListingStateActive {
		t.Fatalf("the owner's listing state after the refused takeover = %v, want %s",
			ownerSite["state"], repo.ListingStateActive)
	}
	if terms, _ := ownerSite["terms"].(map[string]any); terms["pricing_model"] != "per_unit" || terms["rate"] != "0.02" {
		t.Fatalf("the owner's listing terms after the refused takeover = %v, want the terms it registered", terms)
	}
}

// TestConsoleRegistration_RefusesAGrantlessPublisherID is the authorization
// gate on every publisher-scoped call. Someone holding a grant for their own
// payee may not act for another, and the answer is not-found rather than
// forbidden so the id cannot be probed.
func TestConsoleRegistration_RefusesAGrantlessPublisherID(t *testing.T) {
	f := startConsoleFixture(t)
	owner := newActor("editor@publisher.example")
	ownerID := f.registerPublisher(t, owner, "Stoa Press")
	registerSite(t, f, owner, ownerID, "stoa.example", "cloudflare")
	before := snapshotSite(t, f, owner, "stoa.example")

	other := newActor("editor@other.example")
	f.registerPublisher(t, other, "Harmonia Records")

	status, body := f.post(t, consolePath(transport.ConsoleCheckDomainPath), map[string]any{
		"actor": other, "publisher_id": ownerID, "domain": "stoa.example",
	})
	if status != http.StatusNotFound {
		t.Fatalf("checking another publisher's site returned status %d, body = %v", status, body)
	}
	assertSiteUntouched(t, f, owner, "stoa.example", before)
}

// TestConsoleRegistration_RefusesAnUnsupportedCDN keeps registration to the
// integrations the Exchange can generate configuration for. A site behind
// something else is sent to the support path rather than registered into a
// state nothing can complete.
func TestConsoleRegistration_RefusesAnUnsupportedCDN(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	// Fastly is in this list on purpose. The detector recognises it and nothing
	// here builds a package a publisher could install on Fastly Compute, so a
	// site behind it is refused the same as a network we never heard of.
	for _, cdn := range []string{"", "none", "akamai", "fastly"} {
		status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
			"actor":              actor,
			"publisher_id":       publisherID,
			"domain":             "stoa.example",
			"cdn":                cdn,
			"content_path_scope": []string{"/articles/"},
			"terms":              chargingTerms(),
		})
		if status != http.StatusBadRequest {
			t.Fatalf("cdn %q returned status %d, body = %v", cdn, status, body)
		}
	}
}

// TestConsoleRegistration_RefusesTermsItCannotOffer keeps a registration from
// naming an arrangement nothing can resolve. Such a site would register cleanly
// and then produce no offer, with the failure surfacing days later. A refused
// registration writes nothing: the person holds no site and no audit row
// records one. Terms that are absent altogether are not on the list: that is a
// site saved before its terms were chosen, which saves.
func TestConsoleRegistration_RefusesTermsItCannotOffer(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	cases := map[string]any{
		"unknown model": map[string]any{"pricing_model": "bespoke", "permitted_functions": []string{"search"}},
		"charging with no rate": map[string]any{
			"pricing_model": "per_unit", "permitted_functions": []string{"search"},
		},
		"no permitted use": map[string]any{"pricing_model": "free", "permitted_functions": []string{}},
		"unknown use": map[string]any{
			"pricing_model": "free", "permitted_functions": []string{"telepathy"},
		},
	}
	for name, terms := range cases {
		t.Run(name, func(t *testing.T) {
			status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
				"actor":              actor,
				"publisher_id":       publisherID,
				"domain":             "stoa.example",
				"cdn":                "cloudflare",
				"content_path_scope": []string{"/articles/"},
				"terms":              terms,
			})
			if status != http.StatusBadRequest {
				t.Fatalf("status %d, body = %v", status, body)
			}
			if _, sites := listRegistrations(t, f, actor); len(sites) != 0 {
				t.Fatalf("the refused registration left a site: %v", sites)
			}
			if n := countActions(t, f, "RegisterDomain"); n != 0 {
				t.Fatalf("the refused registration wrote %d RegisterDomain audit rows", n)
			}
		})
	}
}

// TestConsoleRegistration_RefusesAMalformedDomain covers the values that would
// key a row no later lookup finds: a URL, a port, an address.
func TestConsoleRegistration_RefusesAMalformedDomain(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	for _, domain := range []string{
		"https://stoa.example", "stoa.example/articles", "stoa.example:8443",
		"192.0.2.10", "localhost", "",
	} {
		status, body := f.post(t, consolePath(transport.ConsoleRegisterDomainPath), map[string]any{
			"actor":              actor,
			"publisher_id":       publisherID,
			"domain":             domain,
			"cdn":                "cloudflare",
			"content_path_scope": []string{"/articles/"},
			"terms":              chargingTerms(),
		})
		if status != http.StatusBadRequest {
			t.Fatalf("domain %q returned status %d, body = %v", domain, status, body)
		}
	}
}

// TestConsoleRegistration_CheckReportsEveryUnmetRule walks the manifest states
// a publisher actually reaches while setting up, and pins that each one is
// reported as the edit it needs. A check that answered "failed" would send them
// to re-read a document that is only one line wrong.
func TestConsoleRegistration_CheckReportsEveryUnmetRule(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")
	registerSite(t, f, actor, publisherID, "stoa.example", "cloudflare")
	check := func() []string {
		return checkReasons(t, checkDomain(t, f, actor, publisherID, "stoa.example"))
	}

	// Registration proved the site, so these are the states a site reaches
	// AFTER it is registered: the document is taken down, or edited into
	// something that no longer says what it said.
	f.manifest.withdraw("stoa.example")
	assertReasons(t, check(),
		consoleapi.ReasonManifestMissing)

	// Published, but naming another Exchange.
	elsewhere := publisherManifest("stoa.example", publisherID, consoleContributorHost)
	elsewhere.Exchanges[0].Domain = "other-exchange.example"
	f.manifest.serve("stoa.example", elsewhere)
	assertReasons(t, check(),
		consoleapi.ReasonExchangeNotListed)

	// Names this Exchange, attests somebody else's payee, and omits the
	// operator's contributor: two separate edits, reported together.
	wrong := publisherManifest("stoa.example", "pub_someone_else", "")
	f.manifest.serve("stoa.example", wrong)
	assertReasons(t, check(),
		consoleapi.ReasonResourceOwnerMismatch, consoleapi.ReasonContributorNotListed)

	// Right payee, contributor still missing.
	noContributor := publisherManifest("stoa.example", publisherID, "")
	f.manifest.serve("stoa.example", noContributor)
	assertReasons(t, check(),
		consoleapi.ReasonContributorNotListed)

	// A document that came back and could not be read.
	f.manifest.fail("stoa.example", forawellknown.ErrSchemaInvalid)
	assertReasons(t, check(),
		consoleapi.ReasonManifestUnreadable)

	// Corrected: the listing activates, which is the positive control for every
	// refusal above.
	f.manifest.serve("stoa.example", publisherManifest("stoa.example", publisherID, consoleContributorHost))
	if got := check(); len(got) != 0 {
		t.Fatalf("a corrected manifest still reported %v", got)
	}
}

// TestConsoleRegistration_CheckRefusesAnUnregisteredSite closes the last
// enumeration path: asking about a hostname nobody registered, and asking about
// one somebody else registered, must be indistinguishable.
func TestConsoleRegistration_CheckRefusesAnUnregisteredSite(t *testing.T) {
	f := startConsoleFixture(t)
	actor := newActor("editor@publisher.example")
	publisherID := f.registerPublisher(t, actor, "Stoa Press")

	status, body := f.post(t, consolePath(transport.ConsoleCheckDomainPath), map[string]any{
		"actor": actor, "publisher_id": publisherID, "domain": "never-registered.example",
	})
	if status != http.StatusNotFound {
		t.Fatalf("checking an unregistered site returned status %d, body = %v", status, body)
	}
}

// assertReasons compares a check's reasons as a set: the service reports every
// unmet rule at once, and their order is not a promise the console relies on.
func assertReasons(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("check reported reasons %v, want %v", got, want)
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("check reported reasons %v, want it to include %q", got, w)
		}
	}
}

// assertNoPublisherFor proves a refused registration wrote nothing, read back
// through the console's own resume call. A refusal that still minted a payee
// would pass a status assertion on its own.
func assertNoPublisherFor(t *testing.T, f *consoleFixture, actor consoleActorBody) {
	t.Helper()
	status, list := f.post(t, consolePath(transport.ConsoleListRegistrationsPath), map[string]any{"actor": actor})
	if status != http.StatusOK {
		t.Fatalf("ListRegistrations status = %d, body = %v", status, list)
	}
	if list["registered"] != false {
		t.Fatalf("a refused registration left the actor registered: %v", list)
	}
}
