//go:build integration

package app_test

import (
	"regexp"
	"strings"
	"testing"
)

// The last step's checkbox: whether the automatic import may replace the terms
// of the site's pages that are already in the catalog. It is sent with the
// registration, held by the Exchange -- read back here through the Exchange's
// registrations route, the way every console page reads it -- and shown on the
// domain page.

// TestConsole_TheTakeOverAnswerIsWhatTheDomainIsRegisteredWith: a publisher
// who ticks the box registers yes, and the domain page says the import applies
// the terms to pages already in the catalog. One who leaves it unticked
// registers no, and the page says those pages keep their own terms.
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
			lastStep := map[string]any{"terms": addDomainTerms()}
			if tc.ticked {
				lastStep["import_replaces_manual_terms"] = true
			}
			addDomainAnswering(t, f, "stoa.example", lastStep)

			sites := f.mine(t).Sites
			if len(sites) != 1 || sites[0].ImportReplacesManualTerms != tc.ticked {
				t.Fatalf("the Exchange holds %+v; want one site registered with the answer %v", sites, tc.ticked)
			}
			if body := f.get2(t, "/domains/stoa.example"); !strings.Contains(body, tc.says) {
				t.Fatalf("the domain page does not say %q: %s", tc.says, head(body))
			}
		})
	}
}

// TestConsole_TheLastStepAsksTheTakeOverQuestion pins what the browser is
// handed: the checkbox on the last step, bound to an answer of its own, and the
// registration POST that finish() sends carrying that answer under the name the
// registration route decodes. The console's script tests run the wizard in a
// DOM and read the body it posts; this test is the other half, holding the
// names in the scripts this console serves to the ones its handler reads. The
// script also saves the answer between steps, so the check reads the POST's
// body alone: a match anywhere else in the script says nothing about what is
// registered.
func TestConsole_TheLastStepAsksTheTakeOverQuestion(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)

	page := f.get2(t, "/domains/new")
	for _, want := range []string{
		`x-model="importReplacesManualTerms"`,
		"Replace the terms of pages that are already in the catalog",
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("the wizard page does not carry %q: %s", want, head(page))
		}
	}
	body := registrationPost(t, wizardScripts(t, f, page))
	if !strings.Contains(body, "import_replaces_manual_terms: this.importReplacesManualTerms") {
		t.Fatalf("the registration POST does not send the checkbox's answer under the name the registration decodes: %s", body)
	}
}

// wizardScript matches the wizard's own scripts among the ones a page loads.
var wizardScript = regexp.MustCompile(`<script src="(/static/wizard[^"]*\.js)"`)

// wizardScripts returns every wizard script the page loads, in the order it
// loads them, as the console serves them. The list comes from the page rather
// than from this test, so a script split off later cannot take the
// registration call out of the check's sight.
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

// registrationCall is how the wizard script calls the registration route, up to
// the opening brace of the object it posts.
const registrationCall = "this.post(this.paths.register, {"

// registrationPost returns the object the wizard script posts to the
// registration route: the text from the call's opening brace to the brace that
// closes it. The script makes that call exactly once; any other count fails the
// test rather than letting the check read the wrong call.
func registrationPost(t *testing.T, script string) string {
	t.Helper()
	if n := strings.Count(script, registrationCall); n != 1 {
		t.Fatalf("the wizard script makes the registration call %d times, want exactly once: %q", n, registrationCall)
	}
	start := strings.Index(script, registrationCall) + len(registrationCall) - 1
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
	t.Fatal("the object the wizard script posts to the registration route is never closed")
	return ""
}
