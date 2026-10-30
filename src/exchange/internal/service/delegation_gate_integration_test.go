//go:build integration

package service

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo/repotest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/service/servicetest"
)

// sharedChainCorpus is the repository's verbatim copy of the pinned module's
// sdk/go/helpers/testdata/delegation-chains.json; the SDK pin gate keeps the
// two byte-identical at every re-pin.
const sharedChainCorpus = "../../../../testdata/delegation-chains.json"

// chainCase is one entry of the protocol's shared delegation-chain corpus: a
// signed token, the issuers an Exchange trusts, the audience, holder and clock
// the verifier runs with, and the declared outcome.
type chainCase struct {
	Name    string `json:"name"`
	Token   string `json:"token"`
	Trusted []struct {
		Identity string `json:"identity"`
		KeyX     string `json:"key_x"`
	} `json:"trusted"`
	Audience string    `json:"audience"`
	Holder   string    `json:"holder"`
	Now      time.Time `json:"now"`
	Expect   struct {
		Valid     bool      `json:"valid"`
		Failure   string    `json:"failure"`
		Scopes    []string  `json:"scopes"`
		ExpiresAt time.Time `json:"expires_at"`
	} `json:"expect"`
}

// TestVerifySubscription_SharedChainCorpus replays every case of the protocol's
// delegation-chain corpus through the Exchange's verifier adapter with the
// trusted keys arranged on a real subscription record: the record is read
// through the repository, the chain is verified for the case's holder, audience
// and clock, and the outcome matches the corpus, failure name included. On a
// valid case the exposed expiry is the chain's signed exp, not exp plus the
// verifier's clock leeway, which the exp_inside_leeway case pins (59s past exp
// verifies and still reports the past expiry).
//
// Persistence round-trip: the record is written through SubscriptionTrustRepo.
// No request path writes one; the admin surface is owned by later work.
func TestVerifySubscription_SharedChainCorpus(t *testing.T) {
	ctx := context.Background()
	pool := servicetest.AcquireTestDB(t, ctx)
	const tenantID = "t-chain-corpus"
	repotest.SeedSeat(t, ctx, pool, repotest.Seat{ID: tenantID, Domain: "exchange.example", Ed25519KeyRef: "k"})
	trust := repo.NewSubscriptionTrustRepo(sqlc.New(pool))

	var corpus struct {
		Cases []chainCase `json:"cases"`
	}
	raw, err := os.ReadFile(sharedChainCorpus)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatal(err)
	}
	for i, tc := range corpus.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			scope := "subscription:corpus:" + itoa(i)
			keys, identity := corpusKeys(t, tc)
			if len(keys) != 0 {
				if _, err := trust.Create(ctx, repo.SubscriptionRecord{
					TenantID: tenantID, Scope: scope, IssuerIdentity: identity, IssuerKeys: keys,
				}); err != nil {
					t.Fatalf("Create record: %v", err)
				}
			}
			svc := NewExchangeService(ExchangeDeps{
				Subscriptions: trust,
				Clk:           clock.NewDeterministic(tc.Now),
				Config:        ExchangeConfig{Exchange: tc.Audience},
			})
			got, verr := svc.verifySubscription(ctx, tenantID, scope, []byte(tc.Token), tc.Holder)
			assertCorpusVerdict(t, tc, got, verr, len(keys) != 0)
		})
	}
}

// corpusKeys decodes the case's trusted root keys and names their one identity.
func corpusKeys(t *testing.T, tc chainCase) ([]ed25519.PublicKey, string) {
	t.Helper()
	keys := make([]ed25519.PublicKey, 0, len(tc.Trusted))
	identity := ""
	for _, tr := range tc.Trusted {
		x, err := base64.RawURLEncoding.DecodeString(tr.KeyX)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, ed25519.PublicKey(x))
		identity = tr.Identity
	}
	return keys, identity
}

// assertCorpusVerdict compares the adapter's verdict with the case's declared
// outcome. A case with no trusted key has no record to name a failure under, so
// only the refusal itself is checked there.
func assertCorpusVerdict(t *testing.T, tc chainCase, got *verifiedSubscription, verr *exchange.Error, hasRecord bool) {
	t.Helper()
	if !tc.Expect.Valid {
		if verr == nil {
			t.Fatalf("verified, want failure %q", tc.Expect.Failure)
		}
		if verr.Kind != exchange.KindDelegationInvalid {
			t.Fatalf("kind %v, want KindDelegationInvalid", verr.Kind)
		}
		if got, want := verr.Metadata["delegation_failure"], tc.Expect.Failure; hasRecord && got != want {
			t.Fatalf("failure %q, want %q", got, want)
		}
		return
	}
	if verr != nil {
		t.Fatalf("refused: %v", verr)
	}
	if !got.ExpiresAt.Equal(tc.Expect.ExpiresAt) {
		t.Fatalf("ExpiresAt %v, want the signed exp %v (no leeway)", got.ExpiresAt, tc.Expect.ExpiresAt)
	}
	if got.Holder != tc.Holder || got.RecordID == "" {
		t.Fatalf("holder %q record %q", got.Holder, got.RecordID)
	}
}

func itoa(i int) string { return string(rune('a'+i%26)) + string(rune('a'+i/26)) }
