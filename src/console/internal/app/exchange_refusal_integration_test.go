//go:build integration

package app_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// What a publisher reads when the Exchange says no, or does not answer at all.
// Each case is produced by the real Exchange: a refusal its service writes, a
// signature its middleware rejects, a server that is gone.

// otherClaims is a second person, who registers a domain before the fixture's
// person tries to.
func otherClaims() oidcup.Claims {
	return claimsFor("sub-other", "editor@elsewhere.example", "Grace")
}

// TestConsole_SaysWhenADomainBelongsToAnotherPublisher covers the one refusal a
// publisher cannot repair by editing a file.
//
// It is reported at the domain step rather than at the last button on purpose.
// Everything between the two is work -- publishing a document, installing a
// package -- and asking someone to do it for a hostname they will never be able
// to register is the worst possible time to tell them.
//
// The domain is taken the way it is taken in production: somebody else walks
// the wizard to the end first, through the same console and the same Exchange.
func TestConsole_SaysWhenADomainBelongsToAnotherPublisher(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	addDomain(t, f.otherPerson(t, otherClaims()))
	f.signIn(t)

	out := f.walkToDocument(t, "stoa.example")
	if out["taken"] != true {
		t.Fatalf("a domain another publisher holds was not reported: %v", out)
	}
	manifest, _ := out["manifest"].(map[string]any)
	reasons := reasonsOf(t, manifest)
	if len(reasons) != 1 || !strings.Contains(reasons[0], "registered to another publisher") {
		t.Fatalf("the domain step does not say why: %v", reasons)
	}
	f.assertNoDomains(t, "a domain another publisher holds")
}

// TestConsole_ShowsTheExchangesRefusal is the last button pressed anyway, on a
// domain somebody else registered. The console's own checks all pass -- the
// domain is well formed, the network is supported, the terms can be offered --
// so the refusal is the Exchange's, and the publisher reads the Exchange's own
// sentence with a status that says the request was refused rather than failed.
func TestConsole_ShowsTheExchangesRefusal(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	other := f.otherPerson(t, otherClaims())
	addDomain(t, other)
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	status, out := f.api(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "",
		"sitemap_url": "",
		"terms": map[string]any{
			"pricing_model": "free", "permitted_functions": []string{"search"},
		},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("registering somebody else's domain = %d, want %d: %v", status, http.StatusBadRequest, out)
	}
	msg, _ := out["error"].(string)
	if !strings.Contains(msg, "registered to another publisher") {
		t.Fatalf("the publisher is not shown the Exchange's refusal: %q", msg)
	}
	f.assertNoDomains(t, "a refused registration")
	if got := other.mine(t).Sites; len(got) != 1 || got[0].Domain != "stoa.example" {
		t.Fatalf("the refusal disturbed the owner's registration: %+v", got)
	}
}

// TestConsole_SaysWhenTheExchangeCannotBeReached covers the Exchange being
// down. Nothing is known about the other side, so the publisher is told nothing
// changed and invited to try again -- on the wizard's calls and on a page.
func TestConsole_SaysWhenTheExchangeCannotBeReached(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.openWizard(t)
	f.exchange.Server.Close()

	status, out := f.api(t, "/api/publisher", map[string]any{"display_name": "Stoa Press"})
	if status != http.StatusBadGateway {
		t.Fatalf("a call with the Exchange down = %d, want %d", status, http.StatusBadGateway)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "Nothing was changed") {
		t.Fatalf("the call does not say nothing changed: %v", out)
	}
	status, body := f.get(t, "/")
	if status != http.StatusBadGateway || !strings.Contains(body, "could not reach the exchange") {
		t.Fatalf("the home page with the Exchange down = %d: %s", status, head(body))
	}
}

// TestConsole_TakesTheBlameWhenTheExchangeRefusesItsSignature covers a console
// the Exchange does not trust: its signing key is not the one its directory
// publishes, which is a deployment fault on our side. The Exchange refuses
// every call, and the publisher is told the problem is ours without being shown
// anything about signatures.
func TestConsole_TakesTheBlameWhenTheExchangeRefusesItsSignature(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withUnpublishedKey(t))

	// Signing in lands on the home page, which is the first page that asks the
	// Exchange anything.
	status, body := f.get(t, "/login")
	if status != http.StatusInternalServerError {
		t.Fatalf("the home page under a refused signature = %d, want %d", status, http.StatusInternalServerError)
	}
	if !strings.Contains(body, "on our side") {
		t.Fatalf("the page does not say the fault is ours: %s", head(body))
	}
	if strings.Contains(body, "signature") {
		t.Fatalf("the page repeats the Exchange's internal refusal: %s", head(body))
	}
}

// withUnpublishedKey makes the console sign with a key its directory does not
// publish.
func withUnpublishedKey(t *testing.T) option {
	t.Helper()
	return func(c *app.Config, ex *exchangetest.Exchange) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("unpublished ed25519 gen: %v", err)
		}
		client, err := exchangeclient.New(exchangeclient.Config{
			BaseURL: ex.URL, Directory: "https://console.example", SigningKey: priv,
		})
		if err != nil {
			t.Fatalf("exchange client: %v", err)
		}
		c.Exchange, c.SigningKey = client, priv
	}
}
