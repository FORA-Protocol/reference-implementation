//go:build integration

package app_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/setup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The wizard's last step, Connect your site: the saved domain's files, and the
// check that asks the site where its file and its package stand and verifies
// the domain once both are in place, in either order.
//
// Legs: every call goes through the console's route to the real Exchange, and
// what the Exchange holds is read back through its signed registrations route
// with the console's own client. Whether the Exchange recorded a verification
// is read one tier lower, through its audit repository, because the Exchange
// publishes no read surface for its audit log; exchangetest.AuditActions says
// so on itself.

// TestConsole_TheLastStepVerifiesWhenFileAndPackageAreInPlace: a publisher whose
// file is published and whose package answers ends the wizard with the domain
// verified -- on the Exchange, in their list of domains and on the domain's own
// page -- after one verification on the Exchange.
func TestConsole_TheLastStepVerifiesWhenFileAndPackageAreInPlace(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	out := addDomain(t, f)
	requireVerified(t, out)
	if task(t, out, "file")["passed"] != true || task(t, out, "package")["passed"] != true {
		t.Fatalf("the verified domain's tasks are not both done: %v", out)
	}

	if sites := f.mine(t).Sites; len(sites) != 1 || !sites[0].Active() {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified", sites)
	}
	if n := f.exchange.CountAuditActions(t, "CheckDomain"); n != 1 {
		t.Fatalf("the Exchange recorded %d verifications, want 1", n)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), "stoa.example", "Verified")
	mustNotContain(t, "the domains list", f.get2(t, "/domains"), "Not verified")
	mustContain(t, "the domain page", f.get2(t, "/domains/stoa.example"), "Verified")
}

// TestConsole_TheFileFirstThenThePackageEndsVerified is one order the
// publisher does the two tasks in. The file alone passes its task and leaves
// the package's open, and the Exchange is not asked to verify anything; once
// the package answers too, the next check verifies the domain.
func TestConsole_TheFileFirstThenThePackageEndsVerified(t *testing.T) {
	var up atomic.Bool
	f := startConsole(t, verifiedClaims(), withPages(switchablePackage(&up)))
	f.signIn(t)
	f.walkToConnect(t)
	f.publishHandedOver(t, "stoa.example", f.files(t, "stoa.example"))

	first := f.check(t, "stoa.example")
	file, pkg := task(t, first, "file"), task(t, first, "package")
	if first["verified"] != false || file["passed"] != true || pkg["passed"] != false || pkg["state"] != "absent" {
		t.Fatalf("the file alone answered %v; want its task done and the package's not", first)
	}
	if file["status"] != "Your file is in place and names your account." {
		t.Fatalf("the file task says %q", file["status"])
	}
	f.requireNotVerifiedNorChecked(t)

	up.Store(true)
	requireVerified(t, f.check(t, "stoa.example"))
	if sites := f.mine(t).Sites; len(sites) != 1 || !sites[0].Active() {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified once its package answers", sites)
	}
}

// TestConsole_ThePackageFirstThenTheFileEndsVerified is the other order, and
// the negative half of the step: a package that answers while the site has no
// file does not verify the domain, and the file task says what is missing.
// The Exchange is not asked to verify anything until the file is there.
func TestConsole_ThePackageFirstThenTheFileEndsVerified(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	f.walkToConnect(t)

	first := f.check(t, "stoa.example")
	file, pkg := task(t, first, "file"), task(t, first, "package")
	if first["verified"] != false || file["passed"] != false || file["state"] != "missing" ||
		pkg["passed"] != true || pkg["state"] != "deployed" {
		t.Fatalf("the package alone answered %v; want its task done and the file's missing", first)
	}
	wantMissing := "Your package passed the request on to your site, and your site has no file at " +
		"/.well-known/fora.json yet."
	if file["status"] != wantMissing {
		t.Fatalf("the file task says %q, want %q", file["status"], wantMissing)
	}
	f.requireNotVerifiedNorChecked(t)

	f.publishHandedOver(t, "stoa.example", f.files(t, "stoa.example"))
	requireVerified(t, f.check(t, "stoa.example"))
}

// TestConsole_ADocumentRefusedToAgentsLeavesTheDomainNotVerified: the package
// answers, and the site serves its discovery document to the Exchange and to
// browsers but refuses AI agents at that address. The agents the package
// refuses would be sent to a document they cannot read, so the file task says
// so and the Exchange is not asked to verify the domain.
func TestConsole_ADocumentRefusedToAgentsLeavesTheDomainNotVerified(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(packageServingTheDocument(refusedToAgents)))
	f.signIn(t)
	out := addDomain(t, f)
	file := task(t, out, "file")
	if out["verified"] != false || file["state"] != "refused" ||
		!strings.Contains(file["status"].(string), "does not give AI agents the file") {
		t.Fatalf("a document refused to agents answered %v; want the file task refused", out)
	}
	f.requireNotVerifiedNorChecked(t)
}

// TestConsole_AFailedVerificationStillReportsTheDomainSaved: when the Exchange
// fails while verifying -- here its read of the site fails in a way that says
// nothing about the publisher's document -- the check answers that the domain
// is not verified rather than failing, and the domain stays saved, for the
// next check to try again.
func TestConsole_AFailedVerificationStillReportsTheDomainSaved(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	f.walkToConnect(t)
	f.publishHandedOver(t, "stoa.example", f.files(t, "stoa.example"))
	// The check's own read of the document passes; the read the Exchange makes
	// to verify the site, right after it, fails.
	f.exchange.Manifests.FailAfter("stoa.example", 1, errors.New("the resolver went away"))

	fetched := f.exchange.Manifests.Calls()
	out := f.check(t, "stoa.example")
	if out["verified"] != false || task(t, out, "file")["passed"] != true || task(t, out, "package")["passed"] != true {
		t.Fatalf("the check answered %v; want both tasks done and the domain not verified", out)
	}
	if n := f.exchange.Manifests.Calls() - fetched; n != 2 {
		t.Fatalf("the Exchange read the document %d times; want twice, to report and to verify", n)
	}
	if sites := f.mine(t).Sites; len(sites) != 1 || sites[0].Active() {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved, not verified", sites)
	}
}

// TestConsole_ADomainWithoutTermsIsNeverVerified: a domain saved on the first
// step and never given terms is not verified by the last step's check, however
// ready its site is. The check answers that it is not verified, and nothing
// reads from its site: the console probes nothing, and the Exchange fetches
// nothing and records nothing.
func TestConsole_ADomainWithoutTermsIsNeverVerified(t *testing.T) {
	var probes atomic.Int32
	f := startConsole(t, verifiedClaims(), withPages(countedProbes(deployedPackage(), &probes)))
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")
	f.publishHandedOver(t, "stoa.example", f.files(t, "stoa.example"))

	f.requireQuiet(t, &probes, "checking a domain without terms", func() {
		if out := f.check(t, "stoa.example"); out["verified"] != false {
			t.Fatalf("the check verified a domain without terms: %v", out)
		}
	})
	f.requireNotVerifiedNorChecked(t)
}

// TestConsole_ACheckOfAVerifiedDomainAsksNothing: a verified domain has nothing
// left to check. A check of it -- from a second tab still open on the last step
// after the domain was verified -- answers that it is verified, and nothing
// reads from its site: the console probes nothing, and the Exchange fetches
// nothing and records nothing, so no second verification is asked for.
func TestConsole_ACheckOfAVerifiedDomainAsksNothing(t *testing.T) {
	var probes atomic.Int32
	f := startConsole(t, verifiedClaims(), withPages(countedProbes(deployedPackage(), &probes)))
	f.signIn(t)
	requireVerified(t, addDomain(t, f))

	f.requireQuiet(t, &probes, "checking a verified domain", func() {
		requireVerified(t, f.check(t, "stoa.example"))
	})
}

// TestConsole_ANotVerifiedDomainIsNotSoldUntilVerified: a domain saved and not
// verified sells nothing. The operator's catalog contributor cannot list a
// page of it, although the site already serves the document the wizard handed
// over; once the package answers and the check verifies it, the same page is
// listed and bought.
func TestConsole_ANotVerifiedDomainIsNotSoldUntilVerified(t *testing.T) {
	var up atomic.Bool
	f := startConsoleOver(t, verifiedClaims(), sellingOver(func(*exchangetest.Options) {}),
		withPages(switchablePackage(&up)))
	f.signIn(t)
	if out := addDomain(t, f); out["verified"] != false {
		t.Fatalf("the check with no package answered %v; want saved, not verified", out)
	}

	term := exchangetest.PerUnitTerm("0.05", 20)
	err := f.exchange.PushAsContributor(t, "stoa.example", "/articles/held", term)
	if err == nil || !strings.Contains(err.Error(), "publisher_domain_listing_not_active") {
		t.Fatalf("listing a page of a domain that is not verified = %v; want it refused", err)
	}

	up.Store(true)
	requireVerified(t, f.check(t, "stoa.example"))
	if sale := f.exchange.Buyer(t).Buy(t, "stoa.example", "/articles/held", term); sale.TransactionID == "" {
		t.Fatal("the verified domain's page sold with no transaction id")
	}
}

// TestConsole_TheLastStepHandsOverTheSavedDomainsFiles: the files the last step
// offers are built from what the Exchange holds for the saved domain -- the
// document attests this publisher's payee and authorises the deployment's
// contributor -- and each link downloads from the domain's own page, the bytes
// shown. The step also says where the package runs once it answers, and the
// commands it shows log in to wrangler first.
func TestConsole_TheLastStepHandsOverTheSavedDomainsFiles(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToConnect(t)

	out := f.files(t, "stoa.example")
	bodies := fileBodies(t, out)
	for _, want := range []string{"fora.json", "edge-config.json"} {
		if bodies[want] == "" {
			t.Fatalf("the last step did not hand over %s: %v", want, out)
		}
	}
	owner, contributors, err := setup.ManifestReadsBack([]byte(bodies["fora.json"]), testExchangeDomain)
	if err != nil {
		t.Fatalf("the document handed over does not read back: %v", err)
	}
	if owner != f.publisherID || len(contributors) != 1 || contributors[0] != testContributor {
		t.Fatalf("the document attests %q and authorises %v", owner, contributors)
	}
	files, _ := out["files"].([]any)
	for _, raw := range files {
		file, _ := raw.(map[string]any)
		name, _ := file["name"].(string)
		link, _ := file["url"].(string)
		if link != "/domains/stoa.example/files/"+name {
			t.Fatalf("%s downloads from %s, not from the domain's page", name, link)
		}
		if status, body := f.get(t, link); status != http.StatusOK || body != file["body"] {
			t.Fatalf("downloading %s = %d, or not the bytes shown", name, status)
		}
	}
	if out["deployed"] != "Deployed on Cloudflare in front of the whole hostname." {
		t.Fatalf("the step says the package runs %q", out["deployed"])
	}
	if steps, _ := out["edge_steps"].([]any); !strings.Contains(stringOf(steps), "npx wrangler login") {
		t.Fatalf("the deploy commands do not log in to wrangler: %v", steps)
	}
}

// TestConsole_AnotherPublishersDomainIsNotFoundOnEveryStep: the steps after the
// first one act on a saved domain, and a domain somebody else saved is answered
// not-found on each of them -- the answer a domain nobody holds gets -- with
// nothing asked of its site or the Exchange about it, and nothing written.
func TestConsole_AnotherPublishersDomainIsNotFoundOnEveryStep(t *testing.T) {
	var probes atomic.Int32
	f := startConsole(t, verifiedClaims(), withPages(countedProbes(deployedPackage(), &probes)))
	f.signIn(t)
	requireVerified(t, addDomain(t, f.otherPerson(t, otherClaims())))
	f.registerBusiness(t)

	f.requireQuiet(t, &probes, "asking about another publisher's domain", func() {
		for path, body := range map[string]map[string]any{
			web.APITermsPath: {"domain": "stoa.example", "terms": map[string]any{
				"pricing_model": "free", "permitted_functions": []string{"search"},
			}},
			web.APIFilesPath: {"domain": "stoa.example"},
			web.APICheckPath: {"domain": "stoa.example"},
			web.APIPagePath:  {"domain": "stoa.example"},
		} {
			if status, out := f.api(t, path, body); status != http.StatusNotFound {
				t.Fatalf("POST %s for another publisher's domain = %d %v, want 404", path, status, out)
			}
		}
	})
}

// requireQuiet fails the test, naming what, unless call read nothing from any
// site and the Exchange recorded nothing while it ran: the Exchange fetched no
// site's document, the console probed no site, and no audit row was written.
func (f *consoleFixture) requireQuiet(t *testing.T, probes *atomic.Int32, what string, call func()) {
	t.Helper()
	fetched, asked, audits := f.exchange.Manifests.Calls(), probes.Load(), len(f.exchange.AuditActions(t))
	call()
	read, probed := f.exchange.Manifests.Calls()-fetched, probes.Load()-asked
	if written := len(f.exchange.AuditActions(t)) - audits; read != 0 || probed != 0 || written != 0 {
		t.Fatalf("%s read a site's document %d times, probed a site %d times and wrote %d audit rows; want none",
			what, read, probed, written)
	}
}

// requireNotVerifiedNorChecked fails the test unless the fixture's one domain
// is saved, not verified, and was never checked: the Exchange was not asked to
// verify it.
func (f *consoleFixture) requireNotVerifiedNorChecked(t *testing.T) {
	t.Helper()
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].LastCheckedAt != nil {
		t.Fatalf("the Exchange holds %+v; want one domain saved, not verified, never checked", sites)
	}
	if n := f.exchange.CountAuditActions(t, "CheckDomain"); n != 0 {
		t.Fatalf("the Exchange recorded %d verifications; it was asked for none", n)
	}
}

// task is one of the last step's tasks out of its check's answer.
func task(t *testing.T, out map[string]any, name string) map[string]any {
	t.Helper()
	got, ok := out[name].(map[string]any)
	if !ok {
		t.Fatalf("the check answered no %s task: %v", name, out)
	}
	return got
}

// stringOf flattens a decoded answer for a search through its text.
func stringOf(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(raw)
}

// refusedToAgents is a site that serves its discovery document to every
// client but an AI agent.
func refusedToAgents(userAgent string) pagefetch.Probe {
	if asAgent(userAgent) {
		return pagefetch.Probe{Status: http.StatusForbidden, Header: http.Header{}}
	}
	return servedNormally()
}
