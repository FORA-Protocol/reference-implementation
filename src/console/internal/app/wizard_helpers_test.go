//go:build integration

package app_test

import (
	"maps"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
)

// The steps most tests take before the one they are about, and the read-back
// every test asserts with.

// registerBusiness opens the wizard and answers its first step, which registers
// the payee on the Exchange. It records the payee id the console was given.
func (f *consoleFixture) registerBusiness(t *testing.T) {
	t.Helper()
	f.openWizard(t)
	out := f.ok(t, "/api/publisher", map[string]any{"display_name": "Stoa Press"})
	id, _ := out["publisher_id"].(string)
	if id == "" {
		t.Fatalf("registering the business answered no payee id: %v", out)
	}
	f.publisherID = id
}

// walkToDocument registers the business, looks the domain up and leaves the
// wizard where the discovery document is handed over. Most tests start here.
// Nothing is published on the site yet.
func (f *consoleFixture) walkToDocument(t *testing.T, domain string) map[string]any {
	t.Helper()
	f.registerBusiness(t)
	return f.ok(t, "/api/domain/lookup", map[string]any{"domain": domain})
}

// publish makes stoa.example, the domain addDomain adds, serve a correct
// discovery document for this fixture's payee, the way a publisher who followed
// the instructions has.
func (f *consoleFixture) publish(t *testing.T) {
	t.Helper()
	if f.publisherID == "" {
		t.Fatal("publish before the business is registered: there is no payee to attest")
	}
	const domain = "stoa.example"
	f.exchange.Manifests.Serve(domain, f.exchange.PublisherManifest(domain, f.publisherID))
}

// publishHandedOver makes the domain serve the discovery document the wizard
// handed over, byte for byte as the console generated it. Registering on the
// strength of it is what proves the console's document satisfies the
// Exchange's check, rather than a document the test wrote.
func (f *consoleFixture) publishHandedOver(t *testing.T, domain string, files map[string]any) {
	t.Helper()
	list, _ := files["files"].([]any)
	for _, raw := range list {
		file, _ := raw.(map[string]any)
		if file["name"] != "fora.json" {
			continue
		}
		body, _ := file["body"].(string)
		m, err := forawellknown.ParseManifest([]byte(body), forawellknown.RolePublisher)
		if err != nil {
			t.Fatalf("the handed-over fora.json does not parse: %v", err)
		}
		f.exchange.Manifests.Serve(domain, m)
		return
	}
	t.Fatalf("the wizard handed over no fora.json: %v", files)
}

// addDomain walks the whole wizard and leaves stoa.example saved, returning
// what the last button answered. It is a helper because for the tests that call
// it the wizard is a precondition, not the subject; the tests that are about a
// step drive that step themselves.
//
// Whether the domain comes out verified depends on the console the test
// started: verifying it takes a site reader that sees the package answer, which
// startConsole does not install. requireVerified is for the tests that need it
// verified.
func addDomain(t *testing.T, f *consoleFixture) map[string]any {
	t.Helper()
	return addDomainWith(t, f, "stoa.example", addDomainTerms())
}

// stoaSitemap is a sitemap on stoa.example, which the Exchange keeps as given.
const stoaSitemap = "https://stoa.example/sitemap.xml"

// addDomainTerms is the licensing addDomain registers: charged at 0.02 per
// access, for search and AI input.
func addDomainTerms() map[string]any {
	return map[string]any{
		"pricing_model": "per_unit", "rate": "0.02",
		"permitted_functions": []string{"search", "ai-input"},
	}
}

// addDomainWith is addDomain with the licensing answers the test is about.
func addDomainWith(t *testing.T, f *consoleFixture, domain string, terms map[string]any) map[string]any {
	t.Helper()
	return addDomainAnswering(t, f, domain, map[string]any{"terms": terms})
}

// addDomainAnswering walks the whole wizard for domain and presses the last
// button with lastStep's answers -- the licensing, and whatever else the last
// step asks that the test is about -- and returns what the button answered.
func addDomainAnswering(t *testing.T, f *consoleFixture, domain string, lastStep map[string]any) map[string]any {
	t.Helper()
	return f.pressLastStep(t, f.walkToLastStep(t, domain), lastStep)
}

// walkToLastStep walks the wizard for domain up to the licensing step, the way
// the page does: the file is published as handed over, and both checks are
// asked. It returns the last step's save, filled in with the answers given on
// the way.
func (f *consoleFixture) walkToLastStep(t *testing.T, domain string) map[string]any {
	t.Helper()
	f.walkToDocument(t, domain)
	files := f.ok(t, "/api/domain/files", map[string]any{
		"domain": domain, "cdn": "cloudflare", "content_paths": "/articles/",
	})
	f.publishHandedOver(t, domain, files)
	if out := f.ok(t, "/api/domain/check-manifest", map[string]any{"domain": domain}); out["passed"] != true {
		t.Fatalf("the document check did not pass: %v", out)
	}
	f.ok(t, "/api/domain/check-edge", map[string]any{
		"domain": domain, "content_paths": "/articles/",
	})
	page := f.ok(t, "/api/domain/page", map[string]any{"domain": domain})
	return map[string]any{
		"domain": domain, "cdn": "cloudflare", "content_paths": "/articles/",
		"sitemap_url": page["sitemap_url"],
	}
}

// pressLastStep sends the last step's save with lastStep's answers over the
// walk's, and returns what it answered.
func (f *consoleFixture) pressLastStep(t *testing.T, walked, lastStep map[string]any) map[string]any {
	t.Helper()
	register := maps.Clone(walked)
	maps.Copy(register, lastStep)
	out := f.ok(t, "/api/domain", register)
	if out["domain"] != walked["domain"] {
		t.Fatalf("the last step did not save %s: %v", walked["domain"], out)
	}
	return out
}

// requireVerified fails the test unless the last button's answer says the
// domain is verified: saved, and sold from.
func requireVerified(t *testing.T, finished map[string]any) {
	t.Helper()
	if finished["verified"] != true {
		t.Fatalf("the domain was saved but not verified: %v", finished)
	}
}

// reasonsOf reads a check's reasons out of an answer.
func reasonsOf(t *testing.T, out map[string]any) []string {
	t.Helper()
	raw, ok := out["reasons"].([]any)
	if !ok {
		t.Fatalf("no reasons in %v", out)
	}
	list := make([]string, 0, len(raw))
	for _, r := range raw {
		s, _ := r.(string)
		list = append(list, s)
	}
	return list
}

// registrationsOf reads what the Exchange holds for one person.
//
// The read goes the way every console page reads it: the console's own signing
// client, the Exchange's registrations/list route, its signature middleware,
// the registration service and the production repositories. Nothing here reads
// a table. The person is asserted as verified whatever the claims say, because
// the Exchange refuses an unverified actor on every route and the question here
// is what that person holds, not whether they may ask.
func (f *consoleFixture) registrationsOf(t *testing.T, claims oidcup.Claims) exchangeclient.Registrations {
	t.Helper()
	regs, err := f.xclient.ListRegistrations(t.Context(), actorFor(claims))
	if err != nil {
		t.Fatalf("read back the Exchange's registrations: %v", err)
	}
	return regs
}

// actorFor is the actor the console asserts for a signed-in person.
func actorFor(claims oidcup.Claims) exchangeclient.Actor {
	return exchangeclient.Actor{
		OIDCIssuer: claims.Issuer, OIDCSubject: claims.Subject,
		Email: claims.Email, EmailVerified: true,
	}
}

// holdDomain saves a domain for the fixture's publisher through the Exchange's
// domain route, signed by the console's own client, and leaves it not
// verified. A test about what the wizard says once a publisher holds many such
// domains arranges them this way rather than walking the wizard for each one.
func (f *consoleFixture) holdDomain(t *testing.T, domain string) {
	t.Helper()
	terms := exchangeclient.Terms{PricingModel: "free", PermittedFunctions: []string{"search"}}
	if _, err := f.xclient.RegisterDomain(t.Context(), exchangeclient.NewSite{
		Actor: actorFor(f.claims), PublisherID: f.publisherID, Domain: domain, CDN: "cloudflare", Terms: &terms,
	}); err != nil {
		t.Fatalf("save %s through the Exchange's domain route: %v", domain, err)
	}
}

// mine is registrationsOf for the fixture's own person.
func (f *consoleFixture) mine(t *testing.T) exchangeclient.Registrations {
	t.Helper()
	return f.registrationsOf(t, f.claims)
}

// assertNoDomains fails the test if the Exchange holds any domain for the
// fixture's person. It is what most wizard tests end on: nothing about a
// domain may be written before the last step.
func (f *consoleFixture) assertNoDomains(t *testing.T, after string) {
	t.Helper()
	if got := f.mine(t).Sites; len(got) != 0 {
		t.Fatalf("%s registered %d domains on the Exchange; it must register none", after, len(got))
	}
}
