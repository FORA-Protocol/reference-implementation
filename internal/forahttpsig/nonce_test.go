package forahttpsig

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// fixedWindow returns the same (created, expires) pair on every call. With it,
// two signatures over identical requests by the same key can differ only by
// something the signer adds per request.
func fixedWindow() core.Window {
	now := time.Now().Unix()
	return func() (int64, int64) { return now, now + 30 }
}

// TestRoundTrip_IdenticalRequestsGetDistinctSignatures pins the nonce: Ed25519
// is deterministic and the window repeats, so without a fresh nonce two
// identical requests would carry one signature and the second would be refused
// as a replay by the peer's (keyid, signature) replay store.
func TestRoundTrip_IdenticalRequestsGetDistinctSignatures(t *testing.T) {
	srv, seen := captureServer(t)
	key, pub := agentKeyFor(t, 0x1a, "https://alice.foramcp.org")
	transport, err := New(nil, "", nil, fixedWindow(), staticSource(key))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		resp, err := (&http.Client{Transport: transport}).Do(
			newRequest(t, http.MethodPost, srv.URL+foraTarget, []byte(`{"q":"same"}`)))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}

	requests := captureRequests(t, seen)
	first, second := requests[0], requests[1]
	if first.req.Header.Get("Signature") == second.req.Header.Get("Signature") {
		t.Fatal("identical requests carried the same signature; the second would be refused as a replay")
	}
	keys := map[string]ed25519.PublicKey{key.Directory: pub}
	for i, got := range []captured{first, second} {
		if _, err := verifyFORA(t, got, keys); err != nil {
			t.Errorf("request %d did not verify: %v", i, err)
		}
	}
	if !strings.Contains(first.req.Header.Get("Signature-Input"), "nonce=") {
		t.Errorf("Signature-Input %q carries no nonce", first.req.Header.Get("Signature-Input"))
	}
}

// TestRoundTrip_IncomingSignatureIsKeptAndEachVerifiesInItsOwnDirectory pins
// what happens to a request that arrives already signed: the SDK transport
// appends its own signature, with its own Signature-Agent member, and leaves
// the earlier one byte for byte. Each then verifies on its own, against the
// directory its own member names.
func TestRoundTrip_IncomingSignatureIsKeptAndEachVerifiesInItsOwnDirectory(t *testing.T) {
	srv, seen := captureServer(t)
	agent, agentPub := agentKeyFor(t, 0x2a, "https://alice.foramcp.org")
	relay, relayPub := agentKeyFor(t, 0x3a, "https://relay.example")
	body := []byte(`{"q":"x"}`)

	req := newRequest(t, http.MethodPost, srv.URL+foraTarget, body)
	agentKey, err := CompleteKey(agent)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := helpers.NewEd25519Signer(agentKey.KeyID, agentKey.Private)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := helpers.SignRequest(context.Background(), req, body, signer, helpers.SignOptions{
		Created: now, Expires: now + 60, Nonce: "bm9uY2U", SignatureAgent: agent.Directory,
	}); err != nil {
		t.Fatal(err)
	}
	incoming := req.Header.Get("Signature")

	transport, err := New(nil, "", nil, fixedWindow(), staticSource(relay))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	got := captureRequests(t, seen)[0]
	if !strings.HasPrefix(got.req.Header.Get("Signature"), incoming) {
		t.Fatalf("the incoming signature was not kept byte for byte: %q", got.req.Header.Get("Signature"))
	}
	keys := map[string]ed25519.PublicKey{agent.Directory: agentPub, relay.Directory: relayPub}
	all, err := helpers.VerifyMultisigRequestResolved(context.Background(), got.req, got.body,
		directoryKeys(keys), helpers.VerifyOptions{})
	if err != nil {
		t.Fatalf("the two signatures did not each verify in their own directory: %v", err)
	}
	if len(all) != 2 || all[0].SignatureAgent != agent.Directory || all[1].SignatureAgent != relay.Directory {
		t.Errorf("verified signers = %+v, want the agent then the relay", all)
	}
}
