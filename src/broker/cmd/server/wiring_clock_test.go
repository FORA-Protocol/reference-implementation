package main

// Wiring-level test: the offer Verifier the wiring builds makes its expiry
// decisions from the INJECTED clock, not an inline time.Now. The round trip is
// real: the verifier resolves the exchange's offer-signing key from a genuine,
// signed WBA directory served over https (the exchange is the external system —
// the adapter boundary), verifies a genuinely signed offer, and the verdict
// flips from verified to rejected purely by pinning the injected clock past the
// offer's expiry. The directory is fetched through the TLS server's own client,
// because a key directory is only ever fetched over https.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

func TestNewOfferVerifier_ExpiryDrivenByInjectedClock(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	// Built through the shared key builder, which stamps the RFC 8037 JWK header
	// quartet in exactly one place, and served signed by the key it lists.
	srv := rwtestutil.NewTLSOrigin(nil)
	defer srv.Close()
	srv.SetWBA(rwtestutil.MarshalWBA(rwtestutil.WBAFile(forawellknown.NewKey(
		pub,
		time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2100, time.January, 1, 0, 0, 0, 0, time.UTC),
	))))
	srv.SetWBASigners(priv)

	t0 := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	offer := &forav1.Offer{
		OfferId:   "wiring-clock-offer",
		Exchange:  srv.Host(),
		ExpiresAt: timestamppb.New(t0.Add(time.Hour)),
	}
	sig, err := helpers.SignOffer(priv, offer)
	if err != nil {
		t.Fatalf("sign offer: %v", err)
	}
	offer.Signature = sig

	// Clock pinned BEFORE expiry: the genuinely signed offer verifies.
	freshVerifier := offerVerifierOver(clock.NewDeterministic(t0), srv.HTTPClient())
	fresh := freshVerifier.Sort(context.Background(), []*forav1.Offer{offer})
	if len(fresh.Verified) != 1 || len(fresh.Rejected) != 0 {
		t.Fatalf("clock before expiry: verified=%d rejected=%d (want 1/0); rejected=%+v",
			len(fresh.Verified), len(fresh.Rejected), fresh.Rejected)
	}

	// Same offer, same key, clock pinned PAST expiry: rejected as expired. The
	// only variable is the injected clock — proving the expiry decision is
	// driven by it, not by the wall clock.
	staleVerifier := offerVerifierOver(clock.NewDeterministic(t0.Add(2*time.Hour)), srv.HTTPClient())
	stale := staleVerifier.Sort(context.Background(), []*forav1.Offer{offer})
	if len(stale.Verified) != 0 || len(stale.Rejected) != 1 {
		t.Fatalf("clock past expiry: verified=%d rejected=%d (want 0/1)",
			len(stale.Verified), len(stale.Rejected))
	}
	if !errors.Is(stale.Rejected[0].Reason, core.ErrOfferExpired) {
		t.Fatalf("rejection reason = %v; want core.ErrOfferExpired", stale.Rejected[0].Reason)
	}
}
