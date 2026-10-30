//go:build integration

package app_test

import (
	"maps"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// The steps most tests take before the one they are about, and the read-back
// every test asserts with.

// registerBusiness takes the wizard's business step the way its Next does,
// which registers the payee on the Exchange, and records the payee id the step
// answered with, which must be the one the Exchange now lists for this person.
func (f *consoleFixture) registerBusiness(t *testing.T) {
	t.Helper()
	out := f.ok(t, web.APIPublisherPath, map[string]any{"display_name": "Stoa Press"})
	f.publisherID, _ = out["publisher_id"].(string)
	if listed := f.mine(t).PublisherID; f.publisherID == "" || f.publisherID != listed {
		t.Fatalf("the business step answered payee %q, and the Exchange lists %q for this person",
			f.publisherID, listed)
	}
}

// lookUp registers the business and asks the domain step's lookup about
// domain, the way Enter on the step does. It saves nothing.
func (f *consoleFixture) lookUp(t *testing.T, domain string) map[string]any {
	t.Helper()
	f.registerBusiness(t)
	return f.ok(t, web.APILookupPath, map[string]any{"domain": domain})
}

// saveDomain is the domain step as the page takes it: the lookup, then Next,
// which saves the domain behind the network the lookup found, with /articles/
// licensed. It returns what the save answered.
func (f *consoleFixture) saveDomain(t *testing.T, domain string) map[string]any {
	t.Helper()
	found := f.ok(t, web.APILookupPath, map[string]any{"domain": domain})
	return f.ok(t, web.APIRegisterPath, map[string]any{
		"domain": domain, "cdn": found["provider"], "content_paths": "/articles/",
	})
}

// walkToTerms registers the business and takes the domain step, which leaves
// the domain saved with no terms. Most tests start here.
func (f *consoleFixture) walkToTerms(t *testing.T, domain string) map[string]any {
	t.Helper()
	f.registerBusiness(t)
	return f.saveDomain(t, domain)
}

// setTerms is the licensing step's Next: it saves answers on domain, after the
// read of its site the step opens with, and returns what the save answered.
// answers carries the terms and whatever else the step asks that the test is
// about; the sitemap the read found is sent unless answers names one.
func (f *consoleFixture) setTerms(t *testing.T, domain string, answers map[string]any) map[string]any {
	t.Helper()
	page := f.ok(t, web.APIPagePath, map[string]any{"domain": domain})
	body := map[string]any{"domain": domain, "sitemap_url": page["sitemap_url"]}
	maps.Copy(body, answers)
	return f.ok(t, web.APITermsPath, body)
}

// walkToConnect takes the first two steps for stoa.example, the domain publish
// serves, with the terms addDomain sets, and leaves the wizard on the last step.
func (f *consoleFixture) walkToConnect(t *testing.T) {
	t.Helper()
	const domain = "stoa.example"
	f.walkToTerms(t, domain)
	f.setTerms(t, domain, map[string]any{"terms": addDomainTerms()})
}

// files asks for a saved domain's files, the way the last step does.
func (f *consoleFixture) files(t *testing.T, domain string) map[string]any {
	t.Helper()
	return f.ok(t, web.APIFilesPath, map[string]any{"domain": domain})
}

// check is the last step's check of domain, the one the page makes when the
// step opens, every few seconds after, and on Check now.
func (f *consoleFixture) check(t *testing.T, domain string) map[string]any {
	t.Helper()
	return f.ok(t, web.APICheckPath, map[string]any{"domain": domain})
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
// handed over, byte for byte as the console generated it. Verifying on the
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

// addDomain walks the whole wizard for stoa.example and returns what the last
// step's check answered. It is a helper because for the tests that call it the
// wizard is a precondition, not the subject; the tests that are about a step
// drive that step themselves.
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

// addDomainTerms is the licensing addDomain sets: charged at 0.02 per fetch,
// for search and AI input.
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

// addDomainAnswering walks the whole wizard for domain with termsStep as the
// licensing step's answers -- the terms, and whatever else that step asks that
// the test is about -- publishes the discovery document the last step hands
// over, and returns what the last step's check then answered.
func addDomainAnswering(t *testing.T, f *consoleFixture, domain string, termsStep map[string]any) map[string]any {
	t.Helper()
	f.walkToTerms(t, domain)
	f.setTerms(t, domain, termsStep)
	f.publishHandedOver(t, domain, f.files(t, domain))
	out := f.check(t, domain)
	if out["domain"] != domain {
		t.Fatalf("the last step checked %v, want %s", out["domain"], domain)
	}
	return out
}

// requireVerified fails the test unless the last step's check says the domain
// is verified: saved, and sold from.
func requireVerified(t *testing.T, checked map[string]any) {
	t.Helper()
	if checked["verified"] != true {
		t.Fatalf("the domain was saved but not verified: %v", checked)
	}
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
// fixture's person. A refused save ends on it: nothing about the domain may be
// written.
func (f *consoleFixture) assertNoDomains(t *testing.T, after string) {
	t.Helper()
	if got := f.mine(t).Sites; len(got) != 0 {
		t.Fatalf("%s registered %d domains on the Exchange; it must register none", after, len(got))
	}
}
