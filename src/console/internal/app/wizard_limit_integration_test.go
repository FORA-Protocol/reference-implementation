//go:build integration

package app_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
)

// TestConsole_SaysWhenTheUnverifiedLimitIsReached: a publisher who already
// holds as many domains saved and not verified as one may is told so at the
// domain step, before anything is looked up about the domain they typed. One
// of the domains they hold is not another: it is answered as theirs. The last
// button for a new domain is refused with the Exchange's own sentence, and
// nothing is saved.
func TestConsole_SaysWhenTheUnverifiedLimitIsReached(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.registerBusiness(t)
	for i := range consoleapi.MaxUnverifiedDomains {
		f.holdDomain(t, fmt.Sprintf("site%d.stoa.example", i))
	}
	fetched := f.exchange.Manifests.Calls()

	out := f.ok(t, "/api/domain/lookup", map[string]any{"domain": "harmonia.example"})
	message, _ := out["message"].(string)
	if out["limit_reached"] != true || !strings.Contains(message, "not verified") {
		t.Fatalf("the lookup at the limit answered %v; want the limit, said", out)
	}
	if n := f.exchange.Manifests.Calls() - fetched; n != 0 {
		t.Fatalf("the lookup at the limit read the domain's document %d times; it asks nothing", n)
	}
	if held := f.ok(t, "/api/domain/lookup", map[string]any{"domain": "site0.stoa.example"}); held["limit_reached"] != false ||
		held["yours"] != true {
		t.Fatalf("a domain the publisher already holds answered %v; want it named as theirs, not as one more", held)
	}

	status, refused := f.api(t, "/api/domain", map[string]any{
		"domain": "harmonia.example", "cdn": "cloudflare", "content_paths": "",
		"sitemap_url": "", "terms": addDomainTerms(),
	})
	reason, _ := refused["error"].(string)
	if status != http.StatusBadRequest || !strings.Contains(reason, "not verified") {
		t.Fatalf("the last button past the limit = %d %v; want 400 with the Exchange's sentence", status, refused)
	}
	if n := len(f.mine(t).Sites); n != consoleapi.MaxUnverifiedDomains {
		t.Fatalf("the publisher holds %d domains after the refused save, want %d", n, consoleapi.MaxUnverifiedDomains)
	}
}
