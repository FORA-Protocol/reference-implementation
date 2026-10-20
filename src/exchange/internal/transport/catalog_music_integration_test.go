//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/catalogfeed"
)

// Real Postgres and signed public PushResources -> DiscoverResources RPCs.
// Customer/use declarations must not hide alternatives: the same agent receives
// all three, reads their terms and chooses. This does not execute purchases or
// claim that an LLM has made the correct decision.
func TestDemoMusic_ThreeSignedAlternatives(t *testing.T) {
	entry := musicFeedEntry(t)
	h := newPushHarnessWith(t, pushHarnessOptions{publisherDomain: entry.GetDomain()})
	const contributor = "music-contributor.example"
	client := setupTermContributor(t, h, contributor)
	pushCanonicalEntry(t, h, client, contributor, h.tenantID, entry)
	key := fetchExchangeOfferKey(t, h)
	uri := "https://" + entry.GetDomain() + entry.GetPath()
	for _, scenario := range []struct{ user, use string }{
		{"individual", "stream"}, {"broadcaster", "broadcast"},
	} {
		spec := requesterWithExt("same-music-agent", scenario.user, "", scenario.use)
		offers := discoverOffersAs(t, h, uri, spec)
		if len(offers) != 3 {
			t.Fatalf("%s: %d offers, want all three alternatives", scenario.user, len(offers))
		}
		ids := make(map[string]bool)
		for i, offer := range offers {
			if ids[offer.GetOfferId()] || offer.GetOfferId() == "" {
				t.Fatal("missing or duplicate offer identity")
			}
			ids[offer.GetOfferId()] = true
			if !sellsTerm(offer, entry.GetTerms()[i]) {
				t.Fatalf("offer %d does not sell feed term %d: the term copy lost restrictions, quotas, "+
					"obligations or document reference, carries pricing, or the offer's price differs", i, i)
			}
			requireMusicOffer(t, offer, key)
			requireMusicExt(t, offer, entry, key)
			if offer.GetIdentity().GetCanonicalUrl() != uri || !proto.Equal(offer.GetIdentity(), offers[0].GetIdentity()) {
				t.Fatal("alternatives must sell the same resource")
			}
			t.Logf("%s: verified %s, EUR %s, metering NONE", scenario.user, offer.GetOfferId(), offer.GetPricing().GetRate())
		}
	}
	requireServedMusicDocument(t, entry.GetTerms()[0].GetLicense())
}

func musicFeedEntry(t *testing.T) *forav1.ResourceEntry {
	t.Helper()
	f, err := os.Open("../../../../deploy/fixtures/demo/music.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	records, err := catalogfeed.ParseJSONL(f)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := catalogfeed.MapRecords(records)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.GetPath() == "/tracks/mesmerizing-galaxy-loop.mp3" {
			verdict := helpers.ValidateResourceEntry(entry)
			if len(verdict.Violations) != 0 || len(verdict.Warnings) != 0 {
				t.Fatalf("music entry does not validate cleanly: %+v", verdict)
			}
			return entry
		}
	}
	t.Fatal("demo music fixture missing")
	return nil
}

func requireMusicOffer(t *testing.T, offer *forav1.Offer, key ed25519.PublicKey) {
	t.Helper()
	p := offer.GetPricing()
	if p.GetModel() != forav1.PricingModel_PRICING_MODEL_FLAT || p.GetCurrency() != "EUR" ||
		p.GetMetering() != forav1.PricingMetering_PRICING_METERING_NONE {
		t.Fatalf("unexpected music pricing: %v", p)
	}
	// Discovery fills derived unit_cost, and the term copy carries no price of
	// its own: the offer states its price once.
	if offer.GetTerms()[0].GetPricing() != nil || p.GetUnitCost() != p.GetRate() || p.GetUnit() != "" {
		t.Fatal("the offer prices its term copy, or its unit_cost or unit differs from a flat price")
	}
	if err := helpers.VerifyOffer(offer, offer.GetSignature(), key); err != nil {
		t.Fatalf("offer signature: %v", err)
	}
	// A changed license binding must not verify under the original signature.
	tampered := proto.Clone(offer).(*forav1.Offer)
	tampered.Terms[0].License.UriDigest = proto.String("sha256:" + strings.Repeat("0", 64))
	if err := helpers.VerifyOffer(tampered, tampered.GetSignature(), key); err == nil {
		t.Fatal("tampered license digest still verifies")
	}
}

// The Exchange advertises fora-music-v1 as pass-through: the offer carries
// every music.<field> key of the feed's ext unchanged, and the offer signature
// covers them.
func requireMusicExt(t *testing.T, offer *forav1.Offer, entry *forav1.ResourceEntry, key ed25519.PublicKey) {
	t.Helper()
	var n int
	for k, want := range entry.GetExt().GetFields() {
		if !strings.HasPrefix(k, "music.") {
			continue
		}
		n++
		if got := offer.GetExt().GetFields()[k]; !proto.Equal(got, want) {
			t.Fatalf("offer ext %q = %v, want the feed's %v unchanged", k, got, want)
		}
	}
	if n == 0 {
		t.Fatal("feed entry has no music.* key")
	}
	tampered := proto.Clone(offer).(*forav1.Offer)
	tampered.Ext.Fields["music.bpm"] = structpb.NewNumberValue(60)
	if err := helpers.VerifyOffer(tampered, tampered.GetSignature(), key); err == nil {
		t.Fatal("offer with a changed music.bpm still verifies")
	}
}

// Serve the actual document bytes over local HTTP and verify their digest.
// This exercises the artifact, not deployed nginx/edge routing or live DNS.
func requireServedMusicDocument(t *testing.T, license *forav1.License) {
	t.Helper()
	origin := httptest.NewServer(http.FileServer(http.Dir("../../../../deploy/content/demo/harmonia-records")))
	t.Cleanup(origin.Close)
	u, err := url.Parse(license.GetUri())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin.URL+u.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := origin.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || fmt.Sprintf("sha256:%x", sha256.Sum256(body)) != license.GetUriDigest() {
		t.Fatal("served license document does not match its signed digest")
	}
}
