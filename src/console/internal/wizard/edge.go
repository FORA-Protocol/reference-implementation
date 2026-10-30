package wizard

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// EdgeState is what we can tell about a site's edge package from outside.
type EdgeState string

const (
	// EdgeUnknown means we asked and the site did not answer.
	EdgeUnknown EdgeState = "unknown"
	// EdgeDeployed means the package answered: it refused an agent with its
	// own headers, which nothing else on the internet produces.
	EdgeDeployed EdgeState = "deployed"
	// EdgeAbsent means the licensed path served an agent as it serves anyone,
	// so nothing is guarding it.
	EdgeAbsent EdgeState = "absent"
	// EdgeBlockedByOther means something refused the request, but not our
	// package. A bot firewall in front of the site does this, and it is worth
	// telling apart: the publisher's content is unreachable to agents either
	// way, but only one of the two can sell it.
	EdgeBlockedByOther EdgeState = "blocked_by_other"
	// EdgeOtherExchange means our package answered, and it is configured to
	// sell through a different exchange than this one. A publisher who rebuilt
	// the package from an older configuration gets this, and it is the one
	// failure that looks completely correct from every other angle: the site
	// is confirmed, agents are refused, and none of the money arrives here.
	EdgeOtherExchange EdgeState = "other_exchange"
)

// contentRulesHeader is what the package names its discovery document in
// every refusal. It is the signal this check reads, because it is ours: a
// status code alone proves nothing, since a bot firewall answers 403 too.
const contentRulesHeader = "X-Content-Rules"

// exchangeHeader is the second thing the package says: which exchange it sends
// refused agents to. It is read only after the first header has identified the
// package as ours, never on its own.
const exchangeHeader = "X-FORA-Exchange"

// browserUserAgent is what the second check calls itself: an ordinary visitor.
//
// The package is supposed to let this through untouched, so a refusal here is
// the failure that costs a publisher readers rather than agents. It is also the
// check most likely to be wrong for a reason that is not the publisher's fault,
// which is why its refusal is reported as something to look at rather than as a
// fault -- see browserCheck.
const browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36"

// wellKnownPath is where a publisher's discovery document is served.
const wellKnownPath = "/.well-known/fora.json"

// agentUserAgent is what the agent check and the document check call
// themselves.
//
// The agent check needs a name the package refuses, because that refusal IS
// the evidence. GPTBot is in the package's own default deny list and is the one
// the written instructions tell a publisher to test with, so a site that
// answers this differently from the instructions is a site where the
// instructions were followed differently. The document check needs the same
// name because the package sends every agent it refuses to the discovery
// document, and what matters there is what such an agent gets.
const agentUserAgent = "GPTBot/1.0 (+https://openai.com/gptbot)"

// AgentProber makes one request as an agent would and reports the answer.
type AgentProber interface {
	ProbeAs(ctx context.Context, rawURL, userAgent string) (pagefetch.Probe, error)
}

// Outcome is what one check found. Three values rather than two, because the
// middle one is real: a request from a datacentre can be refused by a bot
// filter that has no opinion about the publisher's setup, and calling that a
// failure sends the publisher to fix something that is not broken.
type Outcome string

const (
	// OutcomePassed means the site answered the way the setup requires.
	OutcomePassed Outcome = "passed"
	// OutcomeFailed means it answered a way that needs fixing.
	OutcomeFailed Outcome = "failed"
	// OutcomeUnclear means the answer does not settle the question.
	OutcomeUnclear Outcome = "unclear"
)

// EdgeCheck is one question asked of the publisher's site and the answer, in
// the words the publisher reads.
//
// These replace the curl commands the written instructions used to end with.
// Each one is a request to a public address, so the console can make it and
// report the result rather than ask a publisher to run a command and interpret
// a status code.
type EdgeCheck struct {
	Label   string  `json:"label"`
	Outcome Outcome `json:"outcome"`
	Detail  string  `json:"detail"`
}

// EdgeReport is everything one check found out about the site.
//
// State is what asking as an agent found, and drives the package task's badge
// and sentence. Checks are the two questions asked at the licensed path: as an
// agent, and as an ordinary visitor. Document is what an agent got at the
// site's discovery document. It is asked only once the package answers,
// because the package is what sends agents there, and is empty before that.
type EdgeReport struct {
	State    EdgeState
	Checks   []EdgeCheck
	Document Outcome
}

// PackageAnswers reports whether the package is in front of the site and doing
// its job: it refused us as an agent and named this exchange, and no question
// came back failed.
//
// An unclear answer is let through, and a failed one never is. An unclear
// visitor answer is the origin or a bot filter refusing a server, which the
// publisher is told to confirm by hand. Holding them on the step would not
// change that answer.
func (r EdgeReport) PackageAnswers() bool {
	if r.State != EdgeDeployed {
		return false
	}
	for _, c := range r.Checks {
		if c.Outcome == OutcomeFailed {
			return false
		}
	}
	return true
}

// inspectEdge asks the publisher's site everything the written instructions
// used to ask them to ask it themselves.
//
// Three requests at most, all to public addresses on their own site: what an
// agent gets at the licensed path, what a visitor gets there, and, once the
// package answers, what an agent gets at the site's discovery document. That
// last read and the Exchange's own answer different questions: the Exchange's
// says the document is right, and this one says the agents the package refuses
// can read it.
func (s *Service) inspectEdge(ctx context.Context, domain string, paths []string) EdgeReport {
	if s.pages == nil {
		return EdgeReport{State: EdgeUnknown}
	}
	target := s.pages.SiteURL(domain, licensedProbePath(paths))
	state := s.checkEdge(ctx, target, domain)
	report := EdgeReport{State: state, Checks: []EdgeCheck{agentCheck(state), s.browserCheck(ctx, target, domain)}}
	if state == EdgeDeployed {
		report.Document = s.documentOutcome(ctx, domain)
	}
	return report
}

// agentCheck says what the agent probe found, as one line.
//
// It repeats the package task's own sentence in fewer words on purpose: the
// list of checks is the record of what was asked, and a record with one row
// missing reads as a check that did not run.
func agentCheck(state EdgeState) EdgeCheck {
	c := EdgeCheck{Label: "Agents without a licence are refused and sent to buy access"}
	switch state {
	case EdgeDeployed:
		c.Outcome, c.Detail = OutcomePassed,
			"Your package refused us and named your discovery file."
	case EdgeAbsent:
		c.Outcome, c.Detail = OutcomeFailed,
			"The page was served as usual, so nothing is guarding this path."
	case EdgeOtherExchange:
		c.Outcome, c.Detail = OutcomeFailed,
			"Your package refused us, but it sends agents to a different exchange."
	case EdgeBlockedByOther:
		c.Outcome, c.Detail = OutcomeUnclear,
			"Something refused us and it was not your package."
	default:
		c.Outcome, c.Detail = OutcomeUnclear, "The site did not answer."
	}
	return c
}

// browserCheck asks the same address as an ordinary visitor, which the package
// is supposed to serve untouched.
//
// A refusal is read two ways, and the difference is what the package says about
// itself. Our own header on a refusal means the package is refusing people, and
// that is a fault in how it was attached. A refusal without it is far more
// likely to be a bot filter reacting to a request from a datacentre address,
// which says nothing about what a reader in a browser gets -- so it is reported
// as something to confirm by hand, not as a fault.
func (s *Service) browserCheck(ctx context.Context, target, domain string) EdgeCheck {
	c := EdgeCheck{Label: "People and search crawlers are let through"}
	probe, err := s.pages.ProbeAs(ctx, target, browserUserAgent)
	if err != nil {
		reqctx.FromContext(ctx).InfoContext(ctx, "console.browser_probe_unavailable",
			"domain", domain, "url", target, "err", err)
		c.Outcome, c.Detail = OutcomeUnclear, "The site did not answer."
		return c
	}
	switch {
	case probe.Status >= 200 && probe.Status < 400:
		// Any answer that is not a refusal counts, including a redirect. A
		// site that sends visitors from the bare hostname to www answers 301
		// here, and that is a visitor being served, not a visitor stopped.
		c.Outcome, c.Detail = OutcomePassed,
			"We asked as an ordinary browser and were not refused."
	case probe.Header.Get(contentRulesHeader) != "":
		c.Outcome, c.Detail = OutcomeFailed,
			"Your package refused an ordinary browser. Check which paths it is attached to."
	default:
		c.Outcome, c.Detail = OutcomeUnclear,
			"We were refused, but not by your package. A bot filter often refuses requests "+
				"from a server, so open the page in your own browser to be sure."
	}
	return c
}

// documentOutcome asks, as an AI agent, whether the site's discovery document
// answers through the package. What the answer means is the file task's to say.
//
// The document is the publisher's own file on its origin, and the package
// passes its address through to it for every client, so the answer is the
// site's own. It is asked under the agent's name because the package sends
// every agent it refuses to this address. Asked as a browser, a site that
// refuses AI agents here would read as serving its document.
//
// It is asked only once the package answers, from the same server and under
// the same name the package has just answered at the licensed path. So a
// missing file or a refusal here is a failure, where a refusal of the visitor
// check is only unclear (see Outcome): every agent the package refuses is sent
// to this address, from wherever it asks.
func (s *Service) documentOutcome(ctx context.Context, domain string) Outcome {
	target := s.pages.SiteURL(domain, wellKnownPath)
	probe, err := s.pages.ProbeAs(ctx, target, agentUserAgent)
	if err != nil {
		reqctx.FromContext(ctx).InfoContext(ctx, "console.document_probe_unavailable",
			"domain", domain, "url", target, "err", err)
		return OutcomeUnclear
	}
	switch probe.Status {
	case http.StatusOK:
		return OutcomePassed
	case http.StatusNotFound, http.StatusForbidden:
		return OutcomeFailed
	default:
		return OutcomeUnclear
	}
}

// checkEdge asks the site what an agent gets, and reads the answer.
//
// Only one path is asked about: the first the publisher licensed, or the home
// page when they licensed the whole site. Walking every path would multiply
// requests against somebody's site to answer a question the first one answers.
func (s *Service) checkEdge(ctx context.Context, target, domain string) EdgeState {
	probe, err := s.pages.ProbeAs(ctx, target, agentUserAgent)
	if err != nil {
		reqctx.FromContext(ctx).InfoContext(ctx, "console.edge_probe_unavailable",
			"domain", domain, "url", target, "err", err)
		return EdgeUnknown
	}
	switch {
	case probe.Header.Get(contentRulesHeader) != "":
		// Ours. The header names the discovery document, which nothing else
		// serving this site has a reason to emit. What is left to check is
		// which exchange it sends agents to, because a package built from an
		// older configuration is correct in every other way and sells
		// somewhere else.
		if named := probe.Header.Get(exchangeHeader); named != "" && !namesExchange(named, s.exchangeDomain) {
			return EdgeOtherExchange
		}
		return EdgeDeployed
	case probe.Status == http.StatusForbidden || probe.Status == http.StatusTooManyRequests:
		return EdgeBlockedByOther
	case probe.Status >= 200 && probe.Status < 400:
		return EdgeAbsent
	default:
		// A server error, a redirect we did not follow off the site, anything
		// else: the site answered, but not about this.
		return EdgeUnknown
	}
}

// namesExchange reports whether the address the package announces is this
// deployment's exchange.
//
// The comparison is on host, because the package announces an origin and the
// console knows a hostname. A port is part of the host and is compared with it:
// two exchanges on one host and different ports are two exchanges.
func namesExchange(announced, exchangeDomain string) bool {
	if exchangeDomain == "" {
		// Nothing to compare against. Reporting a mismatch here would tell a
		// publisher their package is wrong on the strength of a setting we did
		// not have.
		return true
	}
	host := announced
	if u, err := url.Parse(announced); err == nil && u.Host != "" {
		host = u.Host
	}
	return strings.EqualFold(strings.TrimSpace(host), strings.TrimSpace(exchangeDomain))
}

// licensedProbePath picks the path to ask about.
func licensedProbePath(paths []string) string {
	path := "/"
	for _, p := range paths {
		if p = strings.TrimSpace(p); p != "" {
			path = p
			break
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

// edgeMessage says what the state means, in the publisher's terms.
//
// Each sentence names the next thing to do, because each state has a different
// one: deploy it, look at what is in front of the site, or wait for the next
// check.
func edgeMessage(state EdgeState) string {
	switch state {
	case EdgeDeployed:
		return "We asked your site as an AI agent would and your package answered."
	case EdgeAbsent:
		return "We asked your site as an AI agent would and it served the page. " +
			"The package is not in front of it yet."
	case EdgeOtherExchange:
		return "Your package answered, but it sends agents to a different exchange than this one. " +
			"Deploy the package from this page again."
	case EdgeBlockedByOther:
		return "Something at your site refused us, but it was not your package. " +
			"A bot firewall in front of the site does this, and it blocks paying agents too."
	default:
		return "We could not reach your site to check just now."
	}
}
