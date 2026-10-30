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
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
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
// It is reported at the domain step, by the lookup the step's Next runs before
// it saves: the publisher reads it before anything is saved or done for a
// hostname they will never be able to register.
//
// The domain is taken the way it is taken in production: somebody else walks
// the wizard first, through the same console and the same Exchange.
func TestConsole_SaysWhenADomainBelongsToAnotherPublisher(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	addDomain(t, f.otherPerson(t, otherClaims()))
	f.signIn(t)

	out := f.lookUp(t, "stoa.example")
	if out["taken"] != true {
		t.Fatalf("a domain another publisher holds was not reported: %v", out)
	}
	if msg, _ := out["message"].(string); !strings.Contains(msg, "registered to another publisher") {
		t.Fatalf("the domain step does not say why: %v", out)
	}
	f.assertNoDomains(t, "a domain another publisher holds")
}

// TestConsole_ShowsTheExchangesRefusal is the domain step's save sent anyway,
// on a domain somebody else registered. The console's own checks all pass --
// the domain is well formed and the network is supported -- so the refusal is
// the Exchange's, and the publisher reads the Exchange's own sentence with a
// status that says the request was refused rather than failed. Nothing is
// saved for them, and the owner's domain is as it was.
func TestConsole_ShowsTheExchangesRefusal(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	other := f.otherPerson(t, otherClaims())
	addDomain(t, other)
	f.signIn(t)
	f.lookUp(t, "stoa.example")

	status, out := f.api(t, web.APIRegisterPath, map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("saving somebody else's domain = %d, want %d: %v", status, http.StatusBadRequest, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "registered to another publisher") {
		t.Fatalf("the publisher is not shown the Exchange's refusal: %q", msg)
	}
	f.assertNoDomains(t, "a refused save")
	if n := f.exchange.CountAuditActions(t, "RegisterDomain"); n != 1 {
		t.Fatalf("the Exchange recorded %d saves; want the owner's one", n)
	}
	if got := other.mine(t).Sites; len(got) != 1 || got[0].Domain != "stoa.example" {
		t.Fatalf("the refusal disturbed the owner's registration: %+v", got)
	}
}

// TestConsole_SaysWhenTheExchangeCannotBeReached covers the Exchange being
// down. Nothing is known about the other side, so the publisher is told nothing
// changed and invited to try again -- on the wizard's calls, the business
// step's among them, and on a page.
func TestConsole_SaysWhenTheExchangeCannotBeReached(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.exchange.Server.Close()

	status, out := f.api(t, "/api/domain/lookup", map[string]any{"domain": "stoa.example"})
	if status != http.StatusBadGateway {
		t.Fatalf("a call with the Exchange down = %d, want %d", status, http.StatusBadGateway)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "Nothing was changed") {
		t.Fatalf("the call does not say nothing changed: %v", out)
	}
	status, out = f.api(t, web.APIPublisherPath, map[string]any{"display_name": "Stoa Press"})
	if msg, _ := out["error"].(string); status != http.StatusBadGateway || !strings.Contains(msg, "Nothing was changed") {
		t.Fatalf("the business step with the Exchange down = %d: %v", status, out)
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
