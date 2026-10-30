//go:build integration

package app_test

import (
	"regexp"
	"strings"
	"testing"
)

// The licensing step's checkbox: whether the automatic import may replace the
// terms of the site's pages that are already in the catalog. It is saved with
// the terms, held by the Exchange -- read back here through the Exchange's
// registrations route, the way every console page reads it -- and shown on the
// domain page.

// TestConsole_TheTakeOverAnswerIsWhatTheDomainIsRegisteredWith: a publisher
// who ticks the box saves yes, and the domain page says the import applies the
// terms to pages already in the catalog. One who leaves it unticked saves no,
// and the page says those pages keep their own terms.
func TestConsole_TheTakeOverAnswerIsWhatTheDomainIsRegisteredWith(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ticked bool
		says   string
	}{
		{"ticked", true, "also applies these terms to the pages it finds already in the catalog"},
		{"left unticked", false, "Pages that were already in the catalog keep their own terms"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startConsole(t, verifiedClaims())
			f.signIn(t)
			termsStep := map[string]any{"terms": addDomainTerms()}
			if tc.ticked {
				termsStep["import_replaces_manual_terms"] = true
			}
			addDomainAnswering(t, f, "stoa.example", termsStep)

			sites := f.mine(t).Sites
			if len(sites) != 1 || sites[0].ImportReplacesManualTerms != tc.ticked {
				t.Fatalf("the Exchange holds %+v; want one site saved with the answer %v", sites, tc.ticked)
			}
			if body := f.get2(t, "/domains/stoa.example"); !strings.Contains(body, tc.says) {
				t.Fatalf("the domain page does not say %q: %s", tc.says, head(body))
			}
		})
	}
}

// TestConsole_TheTermsStepAsksTheTakeOverQuestion pins what the browser is
// handed: the checkbox on the licensing step, bound to an answer of its own,
// and the POST the step's Next sends carrying that answer under the name the
// terms route decodes. The console's script tests run the wizard in a DOM and
// read the body it posts; this test is the other half, holding the names in
// the scripts this console serves to the ones its handler reads.
func TestConsole_TheTermsStepAsksTheTakeOverQuestion(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)

	page := f.get2(t, "/domains/new")
	for _, want := range []string{
		`x-model="importReplacesManualTerms"`,
		"Replace the terms of pages that are already in the catalog",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the wizard page does not carry %q: %s", want, head(page))
		}
	}
	body := postedObject(t, wizardScripts(t, f, page), termsCall)
	if !strings.Contains(body, "import_replaces_manual_terms: this.importReplacesManualTerms") {
		t.Fatalf("the terms POST does not send the checkbox's answer under the name the route decodes: %s", body)
	}
}

// TestConsole_TheWizardOffersNoSkipNoCancelAndKeepsNothingInTheBrowser holds
// the page to what it has given up. Every step saves on the Exchange, so there
// is no Skip for now and no Cancel: Finish later leaves with everything kept.
// And nothing about a domain being added is kept in the browser's storage,
// where it would outlive the answer the Exchange holds.
func TestConsole_TheWizardOffersNoSkipNoCancelAndKeepsNothingInTheBrowser(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)

	page := f.get2(t, "/domains/new")
	mustContain(t, "the wizard page", page, "Finish later")
	mustNotContain(t, "the wizard page", page, "Skip for now", ">Cancel<", "wiz-skipped")
	mustNotContain(t, "the wizard's scripts", wizardScripts(t, f, page), "sessionStorage", "localStorage")
}

// TestConsole_TheWizardsWaysOutAreTheScriptsToDecide holds the page's exits to
// the script that decides them. Finish later and the sentence under it are
// shown where the script offers Finish later, which the business step never
// does, and Next and the dead end's way back follow the script's dead end,
// which belongs to the domain step alone. The page and the scripts it serves
// are read together, so a getter renamed on one side leaves a binding to
// nothing here rather than a page that quietly shows every exit or none.
func TestConsole_TheWizardsWaysOutAreTheScriptsToDecide(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	page := f.get2(t, "/domains/new")
	mustContain(t, "the wizard page", page,
		`x-show="canFinishLater">Finish later`,
		`x-show="saved && canFinishLater">Finish later keeps everything you`,
		`x-show="!deadEnd && step !== 'connect'" @click="next()"`,
		`x-show="deadEnd">Back to your domains`)
	mustContain(t, "the wizard's scripts", wizardScripts(t, f, page),
		"get canFinishLater()", "get deadEnd()")
}

// wizardScript matches the wizard's own scripts among the ones a page loads.
var wizardScript = regexp.MustCompile(`<script src="(/static/wizard[^"]*\.js)"`)

// wizardScripts returns every wizard script the page loads, in the order it
// loads them, as the console serves them. The list comes from the page rather
// than from this test, so a script split off later cannot take a call out of
// the check's sight.
func wizardScripts(t *testing.T, f *consoleFixture, page string) string {
	t.Helper()
	found := wizardScript.FindAllStringSubmatch(page, -1)
	if len(found) == 0 {
		t.Fatalf("the wizard page loads no wizard script: %s", head(page))
	}
	var all strings.Builder
	for _, m := range found {
		all.WriteString(f.get2(t, m[1]))
	}
	return all.String()
}

// termsCall is how the wizard script calls the terms route, up to the opening
// brace of the object it posts.
const termsCall = "this.post(this.paths.terms, {"

// postedObject returns the object the wizard script posts through call: the
// text from the call's opening brace to the brace that closes it. The script
// makes that call exactly once; any other count fails the test rather than
// letting the check read the wrong call.
func postedObject(t *testing.T, script, call string) string {
	t.Helper()
	if n := strings.Count(script, call); n != 1 {
		t.Fatalf("the wizard script makes the call %d times, want exactly once: %q", n, call)
	}
	start := strings.Index(script, call) + len(call) - 1
	depth := 0
	for i := start; i < len(script); i++ {
		switch script[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return script[start : i+1]
			}
		}
	}
	t.Fatalf("the object the wizard script posts through %q is never closed", call)
	return ""
}
