//go:build integration

package app_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/dashboard"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The business name, which the add-a-domain wizard asks for on its first step
// while the account has none. An account holds one publisher, so the name is one
// answer per account: the step is shown until it is answered, and never again. A
// sign-in that finds no business opens the wizard on that step, and so does
// every page that draws the console; once the business is named, the console
// opens, with nothing in it until the first domain is saved.
//
// What the Exchange holds is read back through its own registrations route with
// the console's client (registrationsOf). The audit rows are read one tier lower,
// through the Exchange's audit repository, because the Exchange publishes no read
// surface for its audit log; exchangetest.AuditActions says so on itself.

// businessSteps is what the wizard walks for an account that has not named its
// business, and domainSteps what it walks once the business is named.
var (
	businessSteps = []string{"name:Your business", "domain:Your domain", "terms:Your terms", "connect:Connect your site"}
	domainSteps   = businessSteps[1:]
)

// TestConsole_FirstSignInOpensTheWizardOnTheBusinessStep signs a new account in.
// The sign-in lands on the add-a-domain wizard, which opens on the business step
// with its field empty, whatever name the login provider reports for the
// person: the wizard's starting state carries no name, and the page shows none.
// Back and the progress header's links ask canReturnTo, which never reopens the
// business step once its Next has saved the name. Nothing is registered by
// opening it.
func TestConsole_FirstSignInOpensTheWizardOnTheBusinessStep(t *testing.T) {
	for label, name := range map[string]string{"a name": "Pat Provider-Name", "no name": ""} {
		t.Run(label, func(t *testing.T) {
			f := startConsole(t, claimsFor("sub-1", "editor@publisher.example", name))
			resp := f.do(t, http.MethodGet, "/login", nil)
			page := readAll(t, resp)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != web.NewDomainPath {
				t.Fatalf("the first sign-in ended at %s with %d, want the add-a-domain wizard",
					resp.Request.URL.Path, resp.StatusCode)
			}
			boot := bootIn(t, page)
			assertSteps(t, "the first sign-in", boot, businessSteps)
			if boot.Registered {
				t.Fatal("the wizard opened as if the account had named its business")
			}
			if value, sent := bootFields(t, page)["display_name"]; sent {
				t.Fatalf("the wizard's starting state carries a name for the business step: %v", value)
			}
			if name != "" && strings.Contains(page, name) {
				t.Fatalf("the page shows the name the login provider reports, %q", name)
			}
			mustContain(t, "the wizard page", page, "What is your business called?",
				`id="display-name"`, `x-if="canReturnTo(i)"`, `x-show="canReturnTo(index - 1)">Back`,
				"editor@publisher.example")
			if f.mine(t).Registered {
				t.Fatal("opening the wizard registered a publisher")
			}
			if n := f.exchange.CountAuditActions(t, "RegisterPublisher"); n != 0 {
				t.Fatalf("opening the wizard wrote %d RegisterPublisher audit rows, want none", n)
			}
		})
	}
}

// TestConsole_TheWizardAsksTheBusinessOnlyUntilItIsNamed opens the wizard before
// the business is named, after it, once a domain is saved, and on the page that
// finishes that domain's setup. The business step is in the list only the first
// time: no route changes the name, so showing it again would be a field that
// cannot do anything. The progress header numbers whatever list it is sent.
func TestConsole_TheWizardAsksTheBusinessOnlyUntilItIsNamed(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	assertSteps(t, "the first visit", f.openWizard(t), businessSteps)
	f.registerBusiness(t)
	boot := f.openWizard(t)
	assertSteps(t, "the visit after the business step", boot, domainSteps)
	if !boot.Registered {
		t.Fatal("a named business opened the wizard as if it had none")
	}
	if out := addDomain(t, f); out["verified"] != false {
		t.Fatalf("a console that cannot see the package verified a domain: %v", out)
	}
	assertSteps(t, "a later visit", f.openWizard(t), domainSteps)
	assertSteps(t, "the page that finishes a saved domain", f.bootOn(t, "/domains/stoa.example/setup"), domainSteps)
}

// assertSteps fails the test unless boot's steps are want, as key:title pairs
// in order.
func assertSteps(t *testing.T, visit string, boot wizardBoot, want []string) {
	t.Helper()
	got := make([]string, 0, len(boot.Steps))
	for _, s := range boot.Steps {
		got = append(got, s.Key+":"+s.Title)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("on %s the wizard walks %v, want %v", visit, got, want)
	}
}

// TestConsole_HomeOpensTheWizardUntilTheBusinessIsNamed follows the home page
// through the account's first steps. Without a business it sends the person to
// the wizard. Once the business is named it is My domains inside the console,
// with nothing in it and the button that adds the first domain -- the page the
// wizard's Finish later leads to as well. Once a domain is saved it lists it as
// not verified, with the link that finishes its setup.
func TestConsole_HomeOpensTheWizardUntilTheBusinessIsNamed(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	if to := redirectTo(t, f.step(t, f.home(t))); to.Path != web.NewDomainPath {
		t.Fatalf("the home page sent an account without a business to %s, want the wizard", to)
	}

	f.registerBusiness(t)
	home := f.step(t, f.home(t))
	if home.status != http.StatusOK {
		t.Fatalf("the home page of a named business = %d, want the page itself", home.status)
	}
	empty := []string{`aria-label="Sections"`, "No domains yet.", `href="/domains/new"`}
	mustContain(t, "the home page of a named business", home.body, empty...)
	later := f.openWizard(t).Paths.Home
	if later != web.DomainsPath {
		t.Fatalf("the wizard's Finish later leads to %q, want My domains", later)
	}
	mustContain(t, "where Finish later leads", f.get2(t, later), empty...)

	f.saveDomain(t, "stoa.example")
	home = f.step(t, f.home(t))
	if home.status != http.StatusOK {
		t.Fatalf("the home page with a saved domain = %d, want the page itself", home.status)
	}
	mustContain(t, "the home page with a saved domain", home.body,
		"stoa.example", "Not verified", `href="/domains/stoa.example/setup"`)
	mustNotContain(t, "the home page with a saved domain", home.body, "No domains yet.")
}

// pageAnswers is what each publisher page answers, by its route pattern, an
// account that has not named its business (noBusiness) and one whose business
// has no domain yet (noDomain). A redirect always leads to the wizard. The pages
// about one domain, one transaction or one resource are about something the
// account does not hold, and answer not found -- except the page that finishes
// a domain's setup, which reads the domain from its address before anything
// else and refuses the walk's placeholder, "sample", as no hostname at all.
var pageAnswers = map[string]struct{ noBusiness, noDomain int }{
	"GET " + web.HomePath + "{$}":          {http.StatusSeeOther, http.StatusOK},
	"GET " + web.DomainsPath:               {http.StatusSeeOther, http.StatusOK},
	"GET " + web.NewDomainPath:             {http.StatusOK, http.StatusOK},
	"GET " + web.DomainPrefix + "{domain}": {http.StatusNotFound, http.StatusNotFound},
	"GET " + web.DomainFilePattern:         {http.StatusNotFound, http.StatusNotFound},
	"GET " + web.DomainSetupPattern:        {http.StatusBadRequest, http.StatusBadRequest},
	"GET " + dashboard.PathOverview:        {http.StatusSeeOther, http.StatusOK},
	"GET " + dashboard.PathTransactions:    {http.StatusSeeOther, http.StatusOK},
	"GET " + dashboard.PathTransaction:     {http.StatusSeeOther, http.StatusNotFound},
	"GET " + dashboard.PathCatalog:         {http.StatusSeeOther, http.StatusOK},
	"GET " + dashboard.PathResource:        {http.StatusSeeOther, http.StatusNotFound},
	"GET " + dashboard.PathIncome:          {http.StatusSeeOther, http.StatusOK},
	"GET " + dashboard.PathIncomeCSV:       {http.StatusSeeOther, http.StatusOK},
}

// TestConsole_EveryPageAnswersAnAccountBeforeItsFirstDomain walks every
// publisher page in the console's own route table, as an account that has not
// named its business and as one whose business has no domain yet, and holds
// each page to the answer decided for it in pageAnswers. A page added to the
// table later has no answer there and fails here until it is given one. None
// answers with an internal error, which is what a page mounted without the read
// in front of it answers. The page that asked for the business on its own is
// gone: its address is not found.
func TestConsole_EveryPageAnswersAnAccountBeforeItsFirstDomain(t *testing.T) {
	// Over the ledger, so the income report's file has amounts to write.
	f := startConsoleOver(t, verifiedClaims(), sellingOver(func(*exchangetest.Options) {}))
	f.signIn(t)

	walk := func(state string, want func(pattern string) int) {
		t.Helper()
		for _, pattern := range app.PublisherPagePatterns() {
			if _, decided := pageAnswers[pattern]; !decided {
				t.Fatalf("%s has no answer decided for an account before its first domain", pattern)
			}
			status, header := sendPattern(t, f.noRedirect(), f.server.URL, pattern)
			if status != want(pattern) {
				t.Errorf("%s %s = %d, want %d", pattern, state, status, want(pattern))
			}
			if status == http.StatusSeeOther && header.Get("Location") != web.NewDomainPath {
				t.Errorf("%s %s sent the browser to %q, want the wizard", pattern, state, header.Get("Location"))
			}
		}
	}
	walk("without a business", func(p string) int { return pageAnswers[p].noBusiness })
	for _, pattern := range []string{"GET /business", "POST /business"} {
		if status, _ := sendPattern(t, f.noRedirect(), f.server.URL, pattern); status != http.StatusNotFound {
			t.Fatalf("%s = %d, want %d: the page is gone", pattern, status, http.StatusNotFound)
		}
	}
	if f.mine(t).Registered {
		t.Fatal("walking the pages registered a publisher")
	}

	f.registerBusiness(t)
	walk("with a business and no domain", func(p string) int { return pageAnswers[p].noDomain })
}

// TestConsole_RefusesABusinessNameItCannotUse sends the business step with
// nothing in the field, with only spaces, with more than any name could need, and
// with a payee the browser chose. Each is refused with what is wrong, and nothing
// reaches the Exchange: no publisher, no audit row.
func TestConsole_RefusesABusinessNameItCannotUse(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	const unusable, unreadable = "Please enter the name of your business.", "That request could not be read."
	for label, tc := range map[string]struct {
		body map[string]any
		says string
	}{
		"empty":              {map[string]any{"display_name": ""}, unusable},
		"spaces":             {map[string]any{"display_name": "   "}, unusable},
		"too long":           {map[string]any{"display_name": strings.Repeat("a", 17*1024)}, unreadable},
		"a payee of its own": {map[string]any{"display_name": "Stoa Press", "publisher_id": "pub_mine"}, unreadable},
	} {
		status, out := f.api(t, web.APIPublisherPath, tc.body)
		if status != http.StatusBadRequest || out["error"] != tc.says {
			t.Fatalf("%s: the business step = %d %v, want %d %q", label, status, out, http.StatusBadRequest, tc.says)
		}
	}
	if f.mine(t).Registered {
		t.Fatal("a refused business name registered a publisher")
	}
	if n := f.exchange.CountAuditActions(t, "RegisterPublisher"); n != 0 {
		t.Fatalf("refused business names wrote %d RegisterPublisher audit rows, want none", n)
	}
}

// TestConsole_TheBusinessStepTwiceKeepsTheOnePublisher sends the business step a
// second time with another name, as a second tab or a repeated click would. The
// console sends it on to the Exchange, whose registration is idempotent by actor:
// both answers name the one payee, under the first name, and the Exchange
// recorded one registration.
func TestConsole_TheBusinessStepTwiceKeepsTheOnePublisher(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	first := f.ok(t, web.APIPublisherPath, map[string]any{"display_name": "Stoa Press"})
	again := f.ok(t, web.APIPublisherPath, map[string]any{"display_name": "Harmonia Records"})
	if again["publisher_id"] != first["publisher_id"] || again["display_name"] != "Stoa Press" {
		t.Fatalf("a second business step answered %v after %v, want the first payee and name", again, first)
	}
	if regs := f.mine(t); regs.PublisherID != first["publisher_id"] || regs.DisplayName != "Stoa Press" {
		t.Fatalf("the Exchange holds payee %q named %q, want %v named %q",
			regs.PublisherID, regs.DisplayName, first["publisher_id"], "Stoa Press")
	}
	if n := f.exchange.CountAuditActions(t, "RegisterPublisher"); n != 1 {
		t.Fatalf("two business steps wrote %d RegisterPublisher audit rows, want 1", n)
	}
}

// TestConsole_WizardCallsRefuseAnAccountWithoutABusiness makes the wizard's
// calls that act for a business from an account that has none, the way a direct
// call or a page left open across a sign-in would. The page never gets there,
// because it opens on the business step first; the calls answer in JSON, so a
// script reads a refusal instead of a page, and nothing is asked of the Exchange
// about the domain or saved.
func TestConsole_WizardCallsRefuseAnAccountWithoutABusiness(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	domain := map[string]any{"domain": "stoa.example"}
	save := map[string]any{"domain": "stoa.example", "cdn": "cloudflare", "content_paths": ""}
	terms := map[string]any{"domain": "stoa.example", "terms": map[string]any{
		"pricing_model": "free", "permitted_functions": []string{"search"},
	}}
	for path, body := range map[string]map[string]any{
		web.APILookupPath:   domain,
		web.APIRegisterPath: save,
		web.APITermsPath:    terms,
		web.APIFilesPath:    domain,
		web.APICheckPath:    domain,
		web.APIPagePath:     domain,
	} {
		status, out := f.api(t, path, body)
		if status != http.StatusConflict {
			t.Fatalf("POST %s without a business = %d, want %d: %v", path, status, http.StatusConflict, out)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, "not registered yet") {
			t.Fatalf("POST %s without a business does not say why: %v", path, out)
		}
	}
	f.assertNoDomains(t, "the wizard's calls without a business")
	if n := f.exchange.Manifests.Calls(); n != 0 {
		t.Fatalf("the Exchange read a document %d times for an account without a business", n)
	}
}
