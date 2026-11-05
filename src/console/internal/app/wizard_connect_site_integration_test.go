//go:build integration

package app_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/setupsteps"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
)

// What the last step's check reads off the publisher's own live site, task by
// task. Each answer is the server's; nothing in the browser decides whether a
// task is done.

// TestConsole_TellsTheSitesAnswersApart is the one thing about the setup the
// console can find out for itself rather than asking the publisher to run a
// command and believe the result.
//
// The package identifies itself when it refuses an agent, so asking as one and
// reading the reply is the evidence. What matters is that the answers are told
// apart: ours refused, nothing refused, something else refused, and the site
// did not answer. The middle two look alike from a status code and mean
// opposite things.
func TestConsole_TellsTheSitesAnswersApart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader stubPageReader
		state  string
		says   string
	}{
		{
			name: "our package refused the agent",
			reader: stubPageReader{probe: pagefetch.Probe{
				Status: http.StatusForbidden,
				Header: http.Header{"X-Content-Rules": []string{"https://stoa.example/.well-known/fora.json"}},
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
			name:   "the page was served as usual",
			reader: stubPageReader{probe: servedNormally()},
			state:  "absent",
			says:   "not in front of it yet",
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
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := task(t, askTheSite(t, tc.reader, false), "package")
			if pkg["state"] != tc.state {
				t.Fatalf("the check reports %v, want %q", pkg["state"], tc.state)
			}
			if status, _ := pkg["status"].(string); !strings.Contains(status, tc.says) {
				t.Fatalf("the package task does not say %q: %q", tc.says, status)
			}
			if tc.state != "deployed" && pkg["passed"] == true {
				t.Fatalf("the package task passed on %q: %v", tc.state, pkg)
			}
		})
	}
}

// TestConsole_TheCheckAsksTheLicensedPath pins where the check looks. A
// publisher who licensed one section has the package in front of that section;
// asking the home page would report it missing on every such domain. The
// address is the site reader's, in the scheme it reads sites with: the reader
// here reads over plain http, as a local stack's does, and a check that built
// its own https address would never reach such a site. Once the package has
// answered, the check also asks for the discovery document on the same site.
func TestConsole_TheCheckAsksTheLicensedPath(t *testing.T) {
	asked := make(chan probeAsked, 4)
	askTheSite(t, recordingProber{asked: asked}, false)

	var asks []string
	for len(asked) > 0 {
		asks = append(asks, (<-asked).url)
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

// TestConsole_ThePackageInstructionsCheckWhatTheConsoleChecks holds the checks
// the written instructions give to the questions the last step's check asks
// the site itself. The instructions travel in each package and are read where
// the console is not open. A question the console asks that they leave out is
// one nobody asks there, and a check they add tests something the domain's
// verification never looks at.
//
// One leg goes through the console's own check route to the site; the other
// is the network's edge step as the onboarding steps carry it, compiled in.
// The two lists are compared position by position: the same client, by the
// first word of its user agent, at the same kind of address (a page, or the
// discovery document), on the same site.
func TestConsole_ThePackageInstructionsCheckWhatTheConsoleChecks(t *testing.T) {
	for _, network := range cdn.SelfServe() {
		t.Run(string(network), func(t *testing.T) {
			asked := make(chan probeAsked, 8)
			askTheSite(t, recordingProber{asked: asked}, false, withDetector(newStubDetector(network)))
			var console []siteQuestion
			for len(asked) > 0 {
				console = append(console, questionAsked(<-asked))
			}
			var written []siteQuestion
			for _, c := range setupsteps.EdgeStep(network, "stoa.example").Checks() {
				written = append(written, questionWritten(t, c.Run))
			}
			if len(console) == 0 || !slices.Equal(console, written) {
				t.Fatalf("the console asked %+v,\nthe instructions check %+v", console, written)
			}
		})
	}
}

// siteQuestion is one question put to the publisher's site: who asks, by the
// first word of the user agent; whether it asks for the discovery document or
// for a page; and which site.
type siteQuestion struct {
	client   string
	document bool
	host     string
}

// questionAsked is the question one of the console's requests put.
func questionAsked(p probeAsked) siteQuestion {
	client, _, _ := strings.Cut(p.userAgent, " ")
	return siteQuestion{client: client, document: atDocument(p.url), host: hostOf(p.url)}
}

// questionWritten is the question one written curl command puts: its -A value
// and the https address it asks.
func questionWritten(t *testing.T, run string) siteQuestion {
	t.Helper()
	fields := strings.Fields(strings.ReplaceAll(run, "\\\n", " "))
	var q siteQuestion
	for i, f := range fields {
		switch {
		case f == "-A" && i+1 < len(fields):
			q.client = strings.Trim(fields[i+1], `"'`)
		case strings.HasPrefix(f, "https://"):
			q.document, q.host = atDocument(f), hostOf(f)
		}
	}
	if q.client == "" || q.host == "" {
		t.Fatalf("cannot read who asks, and where, out of %q", run)
	}
	return q
}

// recordingProber is a site with the package in front of it, which records
// every request it was sent: the address, and the user agent it was sent
// under. It reads sites over plain http, so the address it was asked at shows
// whose scheme it carries.
type recordingProber struct {
	stubPageReader
	asked chan probeAsked
}

// probeAsked is one request the console sent the site.
type probeAsked struct {
	url, userAgent string
}

func (p recordingProber) SiteURL(domain, path string) string {
	return "http://" + domain + path
}

func (p recordingProber) ProbeAs(_ context.Context, rawURL, userAgent string) (pagefetch.Probe, error) {
	select {
	case p.asked <- probeAsked{url: rawURL, userAgent: userAgent}:
	default:
	}
	if asAgent(userAgent) && !atDocument(rawURL) {
		return refusedByPackage(hostOf(rawURL)), nil
	}
	return servedNormally(), nil
}

// TestConsole_SaysWhatAnOrdinaryVisitorGets covers the second question the
// check asks. The console asks it for a signed-in publisher; the instructions
// in each package write it as a curl command for whoever deploys without the
// console open.
//
// The package is meant to leave people and search crawlers alone, so a refusal
// here is the failure that costs a publisher readers. What matters is that the
// two kinds of refusal are told apart. Our own package refusing a browser is a
// fault in how it was attached. Anything else refusing us is most likely a bot
// filter reacting to a request from a server, which says nothing about what a
// reader in a browser gets -- so that answer sends the publisher to look, not
// to fix.
func TestConsole_SaysWhatAnOrdinaryVisitorGets(t *testing.T) {
	for _, tc := range []struct {
		name    string
		visitor pagefetch.Probe
		says    string
		// passed is whether the package task is done. An unclear answer lets
		// it be: the package refused the agent, and what refuses a browser is
		// something of theirs we cannot see past. A failed one does not.
		passed bool
	}{
		{
			name: "the page is served as usual", visitor: servedNormally(),
			says: "asked as an ordinary browser and were not refused", passed: true,
		},
		{
			name: "the package refuses people too", visitor: refusedByPackage("stoa.example"),
			says: "refused an ordinary browser", passed: false,
		},
		{name: "something else refuses us", visitor: pagefetch.Probe{
			Status: http.StatusForbidden, Header: http.Header{"Server": []string{"nginx"}},
		}, says: "open the page in your own browser", passed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := task(t, askTheSite(t, scriptedProber{
				answer: func(rawURL, userAgent string) (pagefetch.Probe, error) {
					switch {
					case atDocument(rawURL):
						// The package passes the document through, and the site serves it.
						return servedNormally(), nil
					case asAgent(userAgent):
						return refusedByPackage("stoa.example"), nil
					default:
						return tc.visitor, nil
					}
				},
			}, false), "package")
			if detail := checkDetail(t, pkg, "People and search crawlers are let through"); !strings.Contains(detail, tc.says) {
				t.Fatalf("the check does not say %q: %q", tc.says, detail)
			}
			if pkg["passed"] != tc.passed {
				t.Fatalf("passed = %v, want %v", pkg["passed"], tc.passed)
			}
		})
	}
}

// TestConsole_SaysWhatAnAgentGetsAtTheDiscoveryAddress covers the third
// question, asked once the package answers.
//
// The discovery document is the publisher's own file, and the package passes
// its address through to their site. The package sends every agent it refuses
// there, so the question is asked as an agent, and what an agent gets is the
// answer. A site that serves the file to the Exchange and refuses it to AI
// agents, or has nothing there for them, leaves those agents with nothing to
// read: the file task says so and is not done. What a browser gets there
// changes nothing, and an answer that settles nothing is not held against the
// publisher.
func TestConsole_SaysWhatAnAgentGetsAtTheDiscoveryAddress(t *testing.T) {
	refused := pagefetch.Probe{Status: http.StatusForbidden, Header: http.Header{"Server": []string{"nginx"}}}
	missing := pagefetch.Probe{Status: http.StatusNotFound, Header: http.Header{}}
	limited := pagefetch.Probe{Status: http.StatusTooManyRequests, Header: http.Header{}}
	answers := func(agent, browser pagefetch.Probe) func(string) pagefetch.Probe {
		return func(userAgent string) pagefetch.Probe {
			if asAgent(userAgent) {
				return agent
			}
			return browser
		}
	}
	for _, tc := range []struct {
		name     string
		document func(userAgent string) pagefetch.Probe
		state    string
	}{
		{name: "the site refuses AI agents there", document: answers(refused, servedNormally()), state: "refused"},
		{name: "the site has nothing there for AI agents", document: answers(missing, servedNormally()), state: "refused"},
		{name: "agents are served and browsers are not", document: answers(servedNormally(), refused), state: "served"},
		{name: "agents are asked to slow down", document: answers(limited, servedNormally()), state: "served"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := askTheSite(t, packageServingTheDocument(tc.document), true)
			file := task(t, out, "file")
			if file["state"] != tc.state || file["passed"] != (tc.state == "served") {
				t.Fatalf("the file task answered %v, want %s", file, tc.state)
			}
			if out["verified"] != (tc.state == "served") {
				t.Fatalf("the check answered verified %v with the file task %s", out["verified"], tc.state)
			}
		})
	}
}

// askTheSite saves stoa.example with its terms on a console whose site reader
// answers as reader does, publishes the discovery document the Exchange
// accepts when publish is set, and returns what the last step's check
// answered. opts tune the console further, such as the network its lookup
// reports.
func askTheSite(t *testing.T, reader app.SiteReader, publish bool, opts ...option) map[string]any {
	t.Helper()
	f := startConsole(t, verifiedClaims(), append([]option{withPages(reader)}, opts...)...)
	f.signIn(t)
	f.walkToConnect(t)
	if publish {
		f.publish(t)
	}
	return f.check(t, "stoa.example")
}

// checkDetail finds one of the package task's rows by its label and returns
// what it said. A label that is not there fails the test rather than returning
// an empty string, because a question that was never asked and one that passed
// silently are different things.
func checkDetail(t *testing.T, pkg map[string]any, label string) string {
	t.Helper()
	rows, _ := pkg["checks"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if got, _ := row["label"].(string); strings.Contains(got, label) {
			detail, _ := row["detail"].(string)
			return detail
		}
	}
	t.Fatalf("the check never asked %q: %v", label, pkg)
	return ""
}
