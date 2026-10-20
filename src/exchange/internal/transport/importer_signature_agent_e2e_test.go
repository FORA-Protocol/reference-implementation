//go:build integration

package transport_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/ingest/ingesttest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sitemapwalk/sitemaptest"
)

// The Signature-Agent member every catalog push names, read off the wire. Both
// push paths -- the automatic import's job (importer.RunAll) and fora-ingest's
// own pipeline (ingest.Run) -- run against the real Exchange through the rig's
// verdictProxy, which records the headers of every push it forwards. The
// member is checked on the wire only: the Exchange's catalog gate resolves the
// signing key by the push's caller_id, not by the member, so nothing it answers
// would show a wrong member. A Web Bot Auth verifier follows the member, which
// is why every push must name the contributor's own directory, https://<kid>.

// wantContributorMember checks that every push named exactly one Signature-Agent
// member, the contributor's own origin https://<kid>.
func wantContributorMember(t *testing.T, pushes []http.Header, kid string) {
	t.Helper()
	if len(pushes) == 0 {
		t.Fatal("no push reached the proxy")
	}
	want := `sig1="https://` + kid + `"`
	for i, h := range pushes {
		if got := h.Values(helpers.SignatureAgentHeader); len(got) != 1 || got[0] != want {
			t.Errorf("push %d named Signature-Agent %q, want exactly %s", i+1, got, want)
		}
	}
}

// TestCatalogPush_NamesTheContributorsOwnOrigin: a run of the job and a push by
// hand are both accepted, and every push either one makes names https://<kid>
// as its Signature-Agent member.
func TestCatalogPush_NamesTheContributorsOwnOrigin(t *testing.T) {
	r := newImportRig(t, nil)
	r.sitemap.serve(sitemaptest.Urlset(sitePages(2)...))
	var job pushRecorder
	r.viaProxy(job.hook)
	if _, err := r.runJob(); err != nil {
		t.Fatalf("the job returned %v; want nil", err)
	}
	want(t, r.recorded(), repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 2, Accepted: 2})
	wantContributorMember(t, job.seen(), r.kid)

	var byHand pushRecorder
	r.pushManual(newVerdictProxy(t, r.h.server.URL, byHand.hook).URL, handRecord("/news/by-hand-member"))
	wantContributorMember(t, byHand.seen(), r.kid)
}

// TestCatalogPush_RefusesAKidThatIsNotAnHTTPSOrigin: a keypair whose kid does
// not make an https origin -- here an upper-case host -- is refused before
// anything is signed or sent, by the job and by fora-ingest alike, and no
// request reaches the Exchange.
func TestCatalogPush_RefusesAKidThatIsNotAnHTTPSOrigin(t *testing.T) {
	r := newImportRig(t, nil)
	r.sitemap.serve(sitemaptest.Urlset(sitePages(1)...))
	proxy := r.viaProxy(nil)
	r.keyPath = ingesttest.WriteContributorKeyFile(t, "Catalog-Contributor.fora.test")

	if _, err := r.runJob(); !errors.Is(err, helpers.ErrSignatureAgentNotOrigin) {
		t.Fatalf("the job returned %v; want a refusal wrapping helpers.ErrSignatureAgentNotOrigin", err)
	}
	if err := r.runIngest(r.h.ctx, proxy.URL, handRecord("/news/by-hand-refused")); !errors.Is(err, helpers.ErrSignatureAgentNotOrigin) {
		t.Fatalf("fora-ingest returned %v; want a refusal wrapping helpers.ErrSignatureAgentNotOrigin", err)
	}
	if n := proxy.calls.Load(); n != 0 {
		t.Fatalf("%d requests reached the Exchange; a refused kid must send none", n)
	}
}
