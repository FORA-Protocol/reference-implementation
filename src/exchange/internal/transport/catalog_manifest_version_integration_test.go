//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service"
)

// TestPushResources_PublisherManifestVersionRejectsOnlyWhatItCannotRead proves
// the Exchange answers a publisher whose manifest it cannot read with a rejection
// naming that document, not with an internal error — and that the entry does
// not reach the catalog behind that rejection.
//
// The publisher serves a manifest at a document version this Exchange does not
// implement — another major, and none at all. Every other member is exactly what
// production serves, so the refusal can only be the version. It is the
// publisher's own document and a verdict about it: the manifest says the same
// thing on the next push, so the caller has to be told which document to fix,
// and an Exchange-internal error would instead read as "we broke, try again".
//
// The third case is the control, and the other two assert nothing without it.
// Persistence is observed as an offer count through DiscoverResources, and an
// entry that carries no priced term yields zero offers whether or not it
// persisted — so a fixture without terms makes "offers = 0" true by
// construction, and the refusal cases would pass with the version gate deleted.
// Every entry here carries a priced term, and the accepted-version case shows
// the same probe reading a 1 through the same code path.
//
// Round-trip: a signed push over the CatalogService RPC → the Exchange fetches
// the entry's owner manifest over real HTTP → the verdict → the assertion reads
// the RPC's own error and the DiscoverResources RPC. Nothing reads the table.
func TestPushResources_PublisherManifestVersionRejectsOnlyWhatItCannotRead(t *testing.T) {
	for name, tc := range map[string]struct {
		ver      string
		accepted bool
	}{
		"another major":                        {ver: "2.0"},
		"no version":                           {ver: ""},
		"the version this Exchange implements": {ver: forawellknown.Version, accepted: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newPushHarness(t)
			callerID := "caller.example"
			// Authorized as a contributor, so the gate that precedes this one
			// passes and a refusal can only come from the version.
			h.publisher.setContributors(callerID)
			h.publisher.serveVersion(tc.ver)

			_, priv, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("gen: %v", err)
			}
			h.publishAgent(t, callerID, priv)

			const path = "/articles/manifest-version"
			uri := "https://" + h.publisherDom + path
			_, err = h.signedCat(callerID, priv).PushResources(h.ctx,
				connect.NewRequest(newPushRequest(h.tenantID, callerID, []*forav1.ResourceEntry{
					// The priced term is what makes the offer count a persistence
					// probe rather than a restatement of the fixture.
					{Domain: h.publisherDom, Path: path, Terms: []*forav1.LicenseTerm{seedPricedTerm()}},
				})))

			if tc.accepted {
				if err != nil {
					t.Fatalf("the push was refused against a manifest this Exchange reads: %v", err)
				}
				if got := discoverOfferCount(t, h, uri); got != 1 {
					t.Fatalf("offers = %d, want 1 — the probe the refusal cases rely on "+
						"cannot read an entry that DID land, so their zero proves nothing", got)
				}
				return
			}

			if err == nil {
				t.Fatal("the push was accepted against a manifest this Exchange cannot read")
			}
			assertConnectCode(t, err, connect.CodeInvalidArgument)
			if !strings.Contains(err.Error(), service.RejectionReasonManifestVersionRefused) {
				t.Fatalf("error %q does not name %s — the caller cannot tell which document to fix",
					err.Error(), service.RejectionReasonManifestVersionRefused)
			}
			if got := discoverOfferCount(t, h, uri); got != 0 {
				t.Fatalf("offers = %d, want 0 — the entry landed behind a refused manifest", got)
			}
		})
	}
}
