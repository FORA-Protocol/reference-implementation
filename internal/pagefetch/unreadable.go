package pagefetch

// Why a page could not be read, and what the visitor is told about it.
//
// A site that answers without serving a page has still answered: the response
// carries the headers that name the network in front of it, so the onboarding
// path is worth showing even though the article is not there. This file decides
// which case it was and how to say it.

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Why a page could not be read, when the site answered but not with a page.
const (
	// ReasonBotProtection means a network in front of the site turned the
	// request away as automated traffic. The visitor's own browser reaches the
	// page; this service does not, and no amount of retrying changes that.
	ReasonBotProtection = "bot_protection"
	// ReasonRefused means the site declined the request without any sign of a
	// bot check: a login wall, an allowlist, a paywall.
	ReasonRefused = "refused"
	// ReasonNotFound means there is nothing at that address.
	ReasonNotFound = "not_found"
	// ReasonServerError means the site's own software failed.
	ReasonServerError = "server_error"
	// ReasonUnexpectedStatus is the residual class.
	ReasonUnexpectedStatus = "unexpected_status"
)

// Unreadable says why a page could not be read.
//
// It is set when the site answered and the answer was not a page. Reaching the
// site at all is what separates this from a failed preview: the response
// carries the headers that name the network in front of the site, so the
// onboarding path can still be shown even though the article cannot.
type Unreadable struct {
	// Reason is one of the constants above.
	Reason string `json:"reason"`
	// Status is what the site answered with.
	Status int `json:"status"`
}

// protectionHeaders are set by the networks that run bot checks. Their presence
// on a refusal is what separates "we were turned away as a robot" from "this
// site does not want to serve this address to anyone".
var protectionHeaders = []string{
	"cf-mitigated",
	"cf-chl-bypass",
	"x-datadome",
	"x-datadome-cid",
	"x-sucuri-block",
	"x-iinfo",
	"server-timing-bot",
}

// classifyUnreadable names why the site did not serve a page.
//
// The classification is a hint for what the visitor is told, never a decision
// this service acts on, so it stays deliberately plain: the status says most of
// it, and a header a bot check leaves behind says the rest.
func classifyUnreadable(result Result) string {
	switch result.Status {
	case http.StatusNotFound, http.StatusGone:
		return ReasonNotFound
	case http.StatusTooManyRequests:
		return ReasonBotProtection
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable:
		if looksLikeBotCheck(result) {
			return ReasonBotProtection
		}
		if result.Status == http.StatusServiceUnavailable {
			return ReasonServerError
		}
		return ReasonRefused
	}
	if result.Status >= 500 {
		return ReasonServerError
	}
	return ReasonUnexpectedStatus
}

// looksLikeBotCheck reports whether a refusal carries the marks of an automated
// traffic check rather than an ordinary denial.
func looksLikeBotCheck(result Result) bool {
	for _, name := range protectionHeaders {
		if result.Header.Get(name) != "" {
			return true
		}
	}
	// Cloudflare and Akamai identify themselves on the responses they generate
	// for a challenge, which an origin's own denial does not carry.
	switch strings.ToLower(result.Header.Get("Server")) {
	case "cloudflare", "akamaighost":
		return true
	}
	return IsChallengePage(result)
}

// challengeMarkers are the scripts and endpoints a bot check loads.
//
// A marker on its own settles nothing. Cloudflare's JavaScript Detections puts
// /cdn-cgi/challenge-platform/ into ORDINARY responses, so the mere presence of
// one of these says the site is behind a check, not that this response is one.
// What it is worth is corroboration; IsChallengePage says what corroborates it.
var challengeMarkers = []string{
	"/cdn-cgi/challenge-platform/",
	"cf_chl_opt",
	"cf-browser-verification",
	"challenges.cloudflare.com/turnstile",
	"_incapsula_resource",
	"captcha-delivery.com",
	"px-captcha",
	"/_sec/cp_challenge/",
}

// challengeTitles are what these pages call themselves.
var challengeTitles = []string{
	"just a moment",
	"attention required!",
	"please wait...",
	"security check",
	"access denied",
	"verify you are human",
	"verifying you are human",
	"are you a robot",
	"checking your browser",
}

// maxChallengeText bounds the readable text a document may carry and still be
// an interstitial. These pages show a sentence or two; anything a publisher
// would want an offer for carries far more. Counted after scripts and markup
// are removed, because a challenge is mostly script and a page behind one is
// mostly words.
const maxChallengeText = 400

// IsChallengePage reports whether a response is an interstitial from a bot
// check rather than the page that was asked for.
//
// The status is not part of the question. A check that answers 403 is already
// unreadable by its status alone; this exists for the one that answers 200,
// where nothing but the body says the document is a waiting room. Without it an
// offer would be built for a resource titled "Just a moment…", priced and
// listed as if it were the publisher's article.
func IsChallengePage(result Result) bool {
	if !isHTML(result.Header) || len(result.Body) == 0 {
		return false
	}
	body := strings.ToLower(string(result.Body))
	if !hasChallengeMarker(body) {
		return false
	}
	// A status that is not a success already says the page was not served, so
	// the marker is enough to say what kind of refusal it was.
	if result.Status < 200 || result.Status > 299 {
		return true
	}
	// On a success the marker alone would condemn every page behind a network
	// that injects its detection script — which is most pages on those
	// networks. What makes this response an interstitial rather than a page
	// that merely carries the script is that there is nothing to read: it names
	// itself as a check, or it has no content under all that script.
	return hasChallengeTitle(body) || visibleTextLength(body) <= maxChallengeText
}

// hasChallengeMarker reports whether the document loads a known check.
func hasChallengeMarker(lowerBody string) bool {
	for _, marker := range challengeMarkers {
		if strings.Contains(lowerBody, marker) {
			return true
		}
	}
	return false
}

// visibleTextLength counts the characters a reader would see: markup, scripts
// and styles removed, runs of whitespace collapsed.
func visibleTextLength(lowerBody string) int {
	stripped := scriptOrStyle.ReplaceAllString(lowerBody, " ")
	stripped = markup.ReplaceAllString(stripped, " ")
	return len(strings.Join(strings.Fields(stripped), " "))
}

var (
	scriptOrStyle = regexp.MustCompile(`(?s)<(script|style|noscript)\b[^>]*>.*?</(script|style|noscript)>`)
	markup        = regexp.MustCompile(`(?s)<[^>]*>`)
)

// hasChallengeTitle reports whether the document names itself as a check.
func hasChallengeTitle(lowerBody string) bool {
	start := strings.Index(lowerBody, "<title")
	if start < 0 {
		return false
	}
	open := strings.Index(lowerBody[start:], ">")
	if open < 0 {
		return false
	}
	rest := lowerBody[start+open+1:]
	end := strings.Index(rest, "</title>")
	if end < 0 {
		return false
	}
	title := strings.TrimSpace(rest[:end])
	for _, candidate := range challengeTitles {
		if strings.Contains(title, candidate) {
			return true
		}
	}
	return false
}

// unreadableWarning is what the visitor reads about it.
func unreadableWarning(reason string, status int) string {
	switch reason {
	case ReasonBotProtection:
		return "the page could not be read because the network in front of your site turned " +
			"this check away as automated traffic"
	case ReasonRefused:
		return fmt.Sprintf("the page could not be read because your site answered %d", status)
	case ReasonNotFound:
		return "the page could not be read because there is nothing at that address"
	case ReasonServerError:
		return fmt.Sprintf("the page could not be read because your site answered %d", status)
	}
	return fmt.Sprintf("the page could not be read because your site answered %d rather than "+
		"serving a page", status)
}
