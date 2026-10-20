package pagefetch

import (
	"net/http"
	"strings"
	"testing"
)

func htmlResult(status int, body string) Result {
	return Result{
		Status: status,
		Header: http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:   []byte(body),
	}
}

// article is a page a publisher would want an offer for: real words, and the
// detection script the network in front of the site injects into everything.
func article(extraHead string) string {
	return `<!doctype html><html><head><title>What the harbour bill changes</title>` +
		extraHead +
		`</head><body><article><p>` +
		strings.Repeat("The council voted on Tuesday, and the bill now goes to committee. ", 40) +
		`</p></article></body></html>`
}

// Cloudflare's JavaScript Detections injects the challenge-platform script into
// ORDINARY responses. Treating that as a challenge takes the offer away from
// every page behind that setting, which is most pages on that network.
func TestAPageCarryingTheDetectionScriptIsStillAPage(t *testing.T) {
	body := article(`<script src="/cdn-cgi/challenge-platform/scripts/jsd/main.js"></script>`)
	if IsChallengePage(htmlResult(http.StatusOK, body)) {
		t.Fatal("an article was read as a challenge because of the injected detection script")
	}
}

// A short page is not a challenge either, as long as it has something to read.
func TestAShortPageWithTheScriptIsStillAPage(t *testing.T) {
	body := `<!doctype html><html><head><title>Contact</title>` +
		`<script src="/cdn-cgi/challenge-platform/scripts/jsd/main.js"></script></head>` +
		`<body><p>` + strings.Repeat("Write to us at the address below. ", 20) + `</p></body></html>`
	if IsChallengePage(htmlResult(http.StatusOK, body)) {
		t.Fatal("a short but real page was read as a challenge")
	}
}

// The interstitial itself is still caught: it names what it is.
func TestAnInterstitialThatNamesItselfIsCaught(t *testing.T) {
	body := `<!doctype html><html><head><title>Just a moment...</title>` +
		`<script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1"></script></head>` +
		`<body><p>` + strings.Repeat("Checking your browser before accessing the site. ", 30) +
		`</p></body></html>`
	if !IsChallengePage(htmlResult(http.StatusOK, body)) {
		t.Fatal("an interstitial titled 'Just a moment...' was not caught")
	}
}

// And so is one that does not name itself but has nothing to read.
func TestAnInterstitialWithNothingToReadIsCaught(t *testing.T) {
	body := `<!doctype html><html><head><title>www.example.com</title>` +
		`<script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1"></script></head>` +
		`<body><div id="challenge-running"></div></body></html>`
	if !IsChallengePage(htmlResult(http.StatusOK, body)) {
		t.Fatal("an interstitial with no content was not caught")
	}
}

// A refusal carrying the marker needs no further evidence: the status already
// says the page was not served.
func TestARefusalCarryingTheMarkerIsAChallenge(t *testing.T) {
	body := `<!doctype html><html><head><title>www.example.com</title></head><body>` +
		`<script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1"></script>` +
		strings.Repeat("<p>Please stand by while we check your browser and then continue.</p>", 20) +
		`</body></html>`
	if !IsChallengePage(htmlResult(http.StatusForbidden, body)) {
		t.Fatal("a refusal carrying a challenge marker was not read as one")
	}
}

// No marker, no challenge, however the page is titled.
func TestAPageTitledLikeAChallengeWithoutTheScriptIsAPage(t *testing.T) {
	body := `<!doctype html><html><head><title>Just a moment...</title></head>` +
		`<body><p>A short post about waiting.</p></body></html>`
	if IsChallengePage(htmlResult(http.StatusOK, body)) {
		t.Fatal("a page was read as a challenge with no marker at all")
	}
}

func TestAnythingThatIsNotHTMLIsNotAChallenge(t *testing.T) {
	result := Result{
		Status: http.StatusOK,
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   []byte(`{"note":"/cdn-cgi/challenge-platform/"}`),
	}
	if IsChallengePage(result) {
		t.Fatal("a JSON document was read as a challenge")
	}
}

func TestVisibleTextIgnoresScriptsAndMarkup(t *testing.T) {
	body := `<html><head><style>body{color:red}</style>` +
		`<script>var a = "a very long string that nobody reads on the page at all";</script></head>` +
		`<body><div class="x"><p>Two words.</p></div></body></html>`
	if got := visibleTextLength(strings.ToLower(body)); got != len("two words.") {
		t.Fatalf("visibleTextLength = %d, want %d", got, len("two words."))
	}
}
