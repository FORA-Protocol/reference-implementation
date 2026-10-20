//go:build integration

package app_test

import (
	"strings"
	"testing"
)

// The page a publisher comes back to: one registered domain, its network and
// its licensing. The wizard is how they got here and is tested in the sibling
// files; everything this page used to ask the site is asked there now, before
// the domain is stored.

// TestConsole_DomainPageShowsWhatTheDomainIsRegisteredWith pins what the page
// says: the network and the licensing, in one line.
func TestConsole_DomainPageShowsWhatTheDomainIsRegisteredWith(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f)

	body := f.get2(t, "/domains/stoa.example")
	for _, want := range []string{"stoa.example", "cloudflare", "0.02 EUR"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the domain page does not say %q: %s", want, head(body))
		}
	}
}

// TestConsole_OpeningTheDomainPageAsksNothing pins that a visit is not a
// question. The Exchange is not asked to re-read the document and the site is
// not asked anything.
func TestConsole_OpeningTheDomainPageAsksNothing(t *testing.T) {
	asked := make(chan string, 8)
	f := startConsole(t, verifiedClaims(), withPages(recordingProber{asked: asked}))
	f.signIn(t)
	addDomain(t, f)
	before := f.exchange.Manifests.Calls()
	drain(asked)

	f.get2(t, "/domains/stoa.example")
	if got := f.exchange.Manifests.Calls(); got != before {
		t.Fatalf("opening the page read the published file %d times", got-before)
	}
	if len(asked) != 0 {
		t.Fatalf("opening the page asked the domain %d times", len(asked))
	}
}

// drain empties a recorder between the setup and what a test is about.
func drain(c chan string) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}
