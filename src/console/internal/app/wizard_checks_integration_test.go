//go:build integration

package app_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
)

// The two checks that gate the wizard. Each asks about the publisher's own live
// site and answers a passed flag the page reads; nothing in the browser decides
// whether a step is done.

// TestConsole_TheDocumentCheckNamesTheRepairAndWritesNothing covers the failure
// the publisher will actually meet: they have not published the document yet,
// or published the wrong one. A check has to name the edit rather than report a
// failure -- and it must leave the domain unregistered, because passing the
// check is not what registers it.
func TestConsole_TheDocumentCheckNamesTheRepairAndWritesNothing(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	// The site serves nothing at the well-known path: the Exchange's own
	// fetch answers manifest_missing, and the console names the repair.
	f.walkToDocument(t, "stoa.example")

	out := f.ok(t, "/api/domain/check-manifest", map[string]any{"domain": "stoa.example"})
	if out["passed"] != false {
		t.Fatalf("a domain serving no document passed: %v", out)
	}
	reasons := reasonsOf(t, out)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "/.well-known/fora.json") {
		t.Fatalf("the check does not name the file to publish: %v", reasons)
	}
	f.assertNoDomains(t, "checking a document")
}

// TestConsole_TheDocumentCheckPassesOnceItIsPublished is the other half, and
// the one the resume rests on: a publisher whose document is already in place
// is not asked to do it again.
func TestConsole_TheDocumentCheckPassesOnceItIsPublished(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	// The document is on the site before the domain is even typed in.
	f.registerBusiness(t)
	f.publish(t)
	out := f.ok(t, "/api/domain/lookup", map[string]any{"domain": "stoa.example"})

	// The domain step already carries the answer, so a publisher who published
	// the document before starting arrives at the next step with it passing.
	manifest, _ := out["manifest"].(map[string]any)
	if manifest["passed"] != true {
		t.Fatalf("a published document was not reported at the domain step: %v", out)
	}
	again := f.ok(t, "/api/domain/check-manifest", map[string]any{"domain": "stoa.example"})
	if again["passed"] != true {
		t.Fatalf("the check did not pass: %v", again)
	}
	f.assertNoDomains(t, "a passing check")
}

// TestConsole_TellsTheEdgeAnswersApart is the one thing about the setup the
// console can find out for itself rather than asking the publisher to run a
// command and believe the result.
//
// The package identifies itself when it refuses an agent, so asking as one and
// reading the reply is the evidence. What matters is that the answers are told
// apart: ours refused, nothing refused, something else refused, and the site
// did not answer. The middle two look alike from a status code and mean
// opposite things.
func TestConsole_TellsTheEdgeAnswersApart(t *testing.T) {
	cases := []struct {
		name   string
		reader stubPageReader
		state  string
		says   string
	}{
		{
			name: "our package refused the agent",
			reader: stubPageReader{probe: pagefetch.Probe{
				Status: http.StatusForbidden,
				Header: http.Header{"X-Content-Rules": []string{
					"https://stoa.example/.well-known/fora.json",
				}},
			}},
			state: "deployed",
			says:  "your package answered",
		},
		{
			name: "it answered but sells through another exchange",
			reader: stubPageReader{probe: pagefetch.Probe{
				Status: http.StatusForbidden,
				Header: http.Header{
					"X-Content-Rules": []string{"https://stoa.example/.well-known/fora.json"},
					"X-Fora-Exchange": []string{"https://someone-elses-exchange.example"},
				},
			}},
			state: "other_exchange",
			says:  "different exchange",
		},
		{
			name: "the page was served as usual",
			reader: stubPageReader{probe: pagefetch.Probe{
				Status: http.StatusOK, Header: http.Header{},
			}},
			state: "absent",
			says:  "not in front of this path yet",
		},
		{
			name: "something else refused",
			reader: stubPageReader{probe: pagefetch.Probe{
				Status: http.StatusForbidden, Header: http.Header{"Server": []string{"nginx"}},
			}},
			state: "blocked_by_other",
			says:  "not your package",
		},
		{
			name:   "the site did not answer",
			reader: stubPageReader{probeErr: errUnreadable},
			state:  "unknown",
			says:   "could not reach",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startConsole(t, verifiedClaims(), withPages(tc.reader))
			f.signIn(t)
			f.walkToDocument(t, "stoa.example")

			out := f.ok(t, "/api/domain/check-edge", map[string]any{
				"domain": "stoa.example", "content_paths": "/articles/",
			})
			if out["state"] != tc.state {
				t.Fatalf("the check reports %v, want %q", out["state"], tc.state)
			}
			msg, _ := out["message"].(string)
			if !strings.Contains(msg, tc.says) {
				t.Fatalf("the check does not say %q: %q", tc.says, msg)
			}
			if tc.state != "deployed" && out["passed"] == true {
				t.Fatalf("the step unlocked on %q: %v", tc.state, out)
			}
			f.assertNoDomains(t, "checking the package")
		})
	}
}

// TestConsole_TheEdgeCheckAsksTheLicensedPath pins where the check looks. A
// publisher who licensed one section has the package in front of that section;
// asking the home page would report it missing on every such domain. The
// address is the site reader's, in the scheme it reads sites with: the reader
// here reads over plain http, as a local stack's does, and a check that built
// its own https address would never reach such a site.
func TestConsole_TheEdgeCheckAsksTheLicensedPath(t *testing.T) {
	asked := make(chan string, 4)
	f := startConsole(t, verifiedClaims(), withPages(recordingProber{asked: asked}))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	f.ok(t, "/api/domain/check-edge", map[string]any{
		"domain": "stoa.example", "content_paths": "/articles/",
	})

	var asks []string
	for len(asked) > 0 {
		asks = append(asks, <-asked)
	}
	want := []string{
		// As an agent, then as an ordinary visitor, at the licensed path.
		"http://stoa.example/articles/", "http://stoa.example/articles/",
		// The discovery document, on the same site.
		"http://stoa.example/.well-known/fora.json",
	}
	if !slices.Equal(asks, want) {
		t.Fatalf("the check asked %q, want %q: the licensed path and the document, "+
			"at the reader's addresses", asks, want)
	}
}

// recordingProber answers nothing and records what it was asked. It reads sites
// over plain http, so the address it was asked at shows whose scheme it carries.
type recordingProber struct {
	stubPageReader
	asked chan string
}

func (p recordingProber) SiteURL(domain, path string) string {
	return "http://" + domain + path
}

func (p recordingProber) ProbeAs(_ context.Context, rawURL, _ string) (pagefetch.Probe, error) {
	select {
	case p.asked <- rawURL:
	default:
	}
	return pagefetch.Probe{Status: http.StatusOK, Header: http.Header{}}, nil
}

// TestConsole_SaysWhatAnOrdinaryVisitorGets covers the second question the
// check asks, which the written instructions used to hand to the publisher as a
// curl command.
//
// The package is meant to leave people and search crawlers alone, so a refusal
// here is the failure that costs a publisher readers. What matters is that the
// two kinds of refusal are told apart. Our own package refusing a browser is a
// fault in how it was attached. Anything else refusing us is most likely a bot
// filter reacting to a request from a server, which says nothing about what a
// reader in a browser gets -- so that answer sends the publisher to look, not
// to fix.
func TestConsole_SaysWhatAnOrdinaryVisitorGets(t *testing.T) {
	cases := []struct {
		name    string
		visitor pagefetch.Probe
		says    string
		// passed is whether the step lets the publisher on. An unclear answer
		// does: the package refused the agent, and what refuses a browser is
		// something of theirs we cannot see past. A failed one does not.
		passed bool
	}{
		{
			name:    "the page is served as usual",
			visitor: servedNormally(),
			says:    "asked as an ordinary browser and were not refused",
			passed:  true,
		},
		{
			name:    "the package refuses people too",
			visitor: refusedByPackage("stoa.example"),
			says:    "refused an ordinary browser",
			passed:  false,
		},
		{
			name: "something else refuses us",
			visitor: pagefetch.Probe{
				Status: http.StatusForbidden, Header: http.Header{"Server": []string{"nginx"}},
			},
			says:   "open the page in your own browser",
			passed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := startConsole(t, verifiedClaims(), withPages(scriptedProber{
				answer: func(_, userAgent string) (pagefetch.Probe, error) {
					if asAgent(userAgent) {
						return refusedByPackage("stoa.example"), nil
					}
					return tc.visitor, nil
				},
			}))
			f.signIn(t)
			f.walkToDocument(t, "stoa.example")
			out := f.ok(t, "/api/domain/check-edge", map[string]any{
				"domain": "stoa.example", "content_paths": "/articles/",
			})

			detail := checkDetail(t, out, "People and search crawlers are let through")
			if !strings.Contains(detail, tc.says) {
				t.Fatalf("the check does not say %q: %q", tc.says, detail)
			}
			if out["passed"] != tc.passed {
				t.Fatalf("passed = %v, want %v: %v", out["passed"], tc.passed, out)
			}
		})
	}
}

// TestConsole_NoticesTheDocumentStoppedBeingServed covers the third question,
// and the reason it belongs to this step rather than the one before it.
//
// The package sits in front of the whole hostname. A deployment whose routes do
// not cover the well-known paths takes the discovery document away from the one
// address every agent reads it at -- and the check that passed at the previous
// step ran before the package existed, so nothing else would notice.
func TestConsole_NoticesTheDocumentStoppedBeingServed(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(scriptedProber{
		answer: func(rawURL, userAgent string) (pagefetch.Probe, error) {
			switch {
			case strings.Contains(rawURL, "/.well-known/fora.json"):
				return pagefetch.Probe{Status: http.StatusNotFound, Header: http.Header{}}, nil
			case asAgent(userAgent):
				return refusedByPackage("stoa.example"), nil
			default:
				return servedNormally(), nil
			}
		},
	}))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	out := f.ok(t, "/api/domain/check-edge", map[string]any{
		"domain": "stoa.example", "content_paths": "/articles/",
	})
	detail := checkDetail(t, out, "Your discovery document is served")
	if !strings.Contains(detail, "routes have to cover the well-known paths") {
		t.Fatalf("the check does not name the repair: %q", detail)
	}
	if out["passed"] == true {
		t.Fatalf("the step unlocked with the document missing: %v", out)
	}
}

// checkDetail finds one of the edge check's rows by its label and returns what
// it said. A label that is not there fails the test rather than returning an
// empty string, because a question that was never asked and one that passed
// silently are different things.
func checkDetail(t *testing.T, out map[string]any, label string) string {
	t.Helper()
	rows, _ := out["checks"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if got, _ := row["label"].(string); strings.Contains(got, label) {
			detail, _ := row["detail"].(string)
			return detail
		}
	}
	t.Fatalf("the check never asked %q: %v", label, out)
	return ""
}
