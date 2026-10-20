//go:build integration

package agentsign_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forahttpsig"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/keypolicy"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil/record"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/agentsign"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/keystore"
)

// These cases drive the whole custody-to-wire path with a REAL Vault holding the
// keys and a REAL HTTP origin receiving the request: mint a key through the
// KeyStore, sign an outbound request through forahttpsig's per-request key seam
// (WithKeySource) over Source(), and verify the signature at the origin.
// Production does not sign through that transport: its outbound legs build one
// SDK client per call from the same Source(). The resolver under test is the
// same either way.
//
// What each leg traverses, named honestly: the write leg is
// KeyStore → Vault; the read leg is transport → agentsign → KeyStore → Vault →
// signature → origin. The public key the origin checks against is read back
// through the KeyStore (the same surface the agent's published WBA directory is
// built from), NOT through an HTTP fetch of that directory — so this is a CUSTODY
// round-trip, not a directory round-trip. The directory↔custody link is covered by
// the publisher package's own suite.

const (
	alice = "alice.foramcp.org"
	bob   = "bob.foramcp.org"
)

// signedPath is a FORA RPC path: the requests the identity service signs as an
// agent are FORA RPCs, signed under the Web Bot Auth profile.
const signedPath = "/fora.v1.ExchangeService/DiscoverResources"

// origin is a FORA receiver: it records the requests it is handed, with their
// bodies, so a test verifies the signature against the bytes that crossed the
// wire.
type origin struct {
	srv  *httptest.Server
	reqs *record.Recorder[*record.CaptureResult]
	tb   testing.TB
}

func newOrigin(t *testing.T) *origin {
	t.Helper()
	srv, reqs := record.CaptureServer(t)
	return &origin{srv: srv, reqs: reqs, tb: t}
}

// requests returns a point-in-time copy of every request the origin received.
func (o *origin) requests() []*http.Request {
	snap := o.reqs.Snapshot()
	reqs := make([]*http.Request, len(snap))
	for i, r := range snap {
		r.CheckReadErr(o.tb) // Fail fast if body read failed
		reqs[i] = r.Req
	}
	return reqs
}

// verify checks the recorded request with the SDK verifier, resolving the key
// only in the directory of the agent whose subdomain is given — what a FORA
// receiver does once it has fetched that agent's directory. A signature naming
// any other directory resolves no key.
func (o *origin) verify(t *testing.T, i int, subdomain string, pub ed25519.PublicKey) error {
	t.Helper()
	reqs := o.requests()
	if i >= len(reqs) {
		t.Fatalf("verify request index %d out of bounds (saw %d)", i, len(reqs))
	}
	resolver := keypolicy.ResolverFunc(func(ctx context.Context, _ string) (ed25519.PublicKey, error) {
		if helpers.SignatureAgentFromContext(ctx) != "https://"+subdomain {
			return nil, helpers.ErrUnknownKey
		}
		return pub, nil
	})
	body := receivedBody(t, reqs[i])
	_, err := helpers.VerifyRequestResolved(context.Background(), reqs[i], body, resolver, helpers.VerifyOptions{})
	return err
}

// receivedBody returns the bytes of a recorded request's body, which the SDK
// verifier takes as an argument rather than reading off the request. Reading
// drains the body, so it is put back as a fresh reader over the same bytes. A
// test verifies one request several times; with a drained body every check after
// the first fails on the content digest, and a negative check would then pass
// without ever testing the key or the directory it names.
func receivedBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read recorded body: %v", err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body
}

// signingClient builds one transport whose key is resolved per request from
// custody, through Source().
//
// The signature window runs on the SYSTEM clock even though custody runs on a
// deterministic one, because the two instants are judged by different parties.
// Custody's clock decides which key is active, and only this suite reads it. The
// signature's window is judged by the origin's verifier against the wall clock.
func signingClient(t *testing.T, custody agentsign.Custody) *http.Client {
	t.Helper()
	resolver, err := agentsign.New(agentsign.Config{Keys: custody, BaseDomain: "foramcp.org"})
	if err != nil {
		t.Fatalf("new resolver: %v", err)
	}
	transport, err := forahttpsig.New(nil, "", nil, core.ClockWindow(clock.System{}.Now, 5*time.Minute),
		forahttpsig.WithKeySource(resolver.Source()))
	if err != nil {
		t.Fatalf("build transport: %v", err)
	}
	return &http.Client{Transport: transport}
}

// fetchAs drives one request as the named agent and returns the transport error,
// if any. A nil subdomain-carrying context is the unauthenticated case.
func fetchAs(t *testing.T, client *http.Client, url, subdomain string) error {
	t.Helper()
	ctx := t.Context()
	if subdomain != "" {
		ctx = agentsign.WithSubdomain(ctx, subdomain)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{"q":"x"}`)))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, doErr := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return doErr
}

// TestSigningTransport_SignsWithTheAgentsCustodiedKey is the ticket's core
// property: a request the registry sends on an agent's behalf carries a signature
// the origin can check against that agent's own key, and names that agent's own
// directory so the origin knows where to look.
func TestSigningTransport_SignsWithTheAgentsCustodiedKey(t *testing.T) {
	custody, _ := newCustody(t)
	minted, err := custody.Create(t.Context(), alice, liveWindow())
	if err != nil {
		t.Fatalf("mint key: %v", err)
	}
	o := newOrigin(t)

	if err := fetchAs(t, signingClient(t, custody), o.srv.URL+signedPath, alice); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	reqs := o.requests()
	if len(reqs) != 1 {
		t.Fatalf("origin saw %d requests, want 1", len(reqs))
	}
	if err := o.verify(t, 0, alice, minted.Public); err != nil {
		t.Fatalf("origin could not verify against the agent's custodied key: %v", err)
	}
	got := reqs[0]
	if want := `sig1="https://` + alice + `"`; got.Header.Get(helpers.SignatureAgentHeader) != want {
		t.Errorf("Signature-Agent = %s, want %s", got.Header.Get(helpers.SignatureAgentHeader), want)
	}
	if !strings.Contains(got.Header.Get("Signature-Input"), `keyid="`+minted.Ref.Thumbprint+`"`) {
		t.Errorf("Signature-Input %q does not name the custodied key's thumbprint %q",
			got.Header.Get("Signature-Input"), minted.Ref.Thumbprint)
	}
}

// TestSigningTransport_SignsEachAgentWithItsOwnKey is the impersonation guard. One
// transport carries every agent, so a key resolved once and reused would sign the
// second agent's request as the first — and the origin would believe it.
func TestSigningTransport_SignsEachAgentWithItsOwnKey(t *testing.T) {
	custody, _ := newCustody(t)
	aliceKey, err := custody.Create(t.Context(), alice, liveWindow())
	if err != nil {
		t.Fatalf("mint alice: %v", err)
	}
	bobKey, err := custody.Create(t.Context(), bob, liveWindow())
	if err != nil {
		t.Fatalf("mint bob: %v", err)
	}
	o := newOrigin(t)
	client := signingClient(t, custody)

	for _, subdomain := range []string{alice, bob} {
		if err := fetchAs(t, client, o.srv.URL+signedPath, subdomain); err != nil {
			t.Fatalf("fetch as %s: %v", subdomain, err)
		}
	}

	if err := o.verify(t, 0, alice, aliceKey.Public); err != nil {
		t.Errorf("first request did not verify as alice: %v", err)
	}
	if err := o.verify(t, 1, bob, bobKey.Public); err != nil {
		t.Errorf("second request did not verify as bob: %v", err)
	}
	// Cross-check: bob's request must NOT verify under alice's key or through
	// alice's directory, or the two agents are not distinguishable at the origin.
	if err := o.verify(t, 1, bob, aliceKey.Public); err == nil {
		t.Error("bob's request verified under alice's key")
	}
	if err := o.verify(t, 1, alice, bobKey.Public); err == nil {
		t.Error("bob's request resolved through alice's directory")
	}
}

// TestSigningTransport_FollowsARotation pins that the key is resolved per request
// rather than cached: once custody retires the old key and a newer one is active,
// the very next request is signed with the new one. A cached key would keep signing
// with a retired — or revoked — key for as long as the process lived.
func TestSigningTransport_FollowsARotation(t *testing.T) {
	custody, clk := newCustody(t)
	old, err := custody.Create(t.Context(), alice, liveWindow())
	if err != nil {
		t.Fatalf("mint old: %v", err)
	}
	o := newOrigin(t)
	client := signingClient(t, custody)
	if err := fetchAs(t, client, o.srv.URL+signedPath, alice); err != nil {
		t.Fatalf("fetch before rotation: %v", err)
	}

	// Rotate: mint the successor, then retire the predecessor.
	fresh, err := custody.Create(t.Context(), alice, keystore.Window{
		NotBefore: anchor, NotAfter: anchor.Add(60 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("mint successor: %v", err)
	}
	if err := custody.Expire(t.Context(), old.Ref, anchor.Add(time.Hour)); err != nil {
		t.Fatalf("retire predecessor: %v", err)
	}
	clk.Advance(2 * time.Hour)

	if err := fetchAs(t, client, o.srv.URL+signedPath, alice); err != nil {
		t.Fatalf("fetch after rotation: %v", err)
	}

	if err := o.verify(t, 1, alice, fresh.Public); err != nil {
		t.Errorf("post-rotation request did not verify under the new key: %v", err)
	}
	if err := o.verify(t, 1, alice, old.Public); err == nil {
		t.Error("post-rotation request still verified under the retired key")
	}
}

// TestSigningTransport_RefusesAnUnauthenticatedRequest pins the fail-closed rule:
// with no authenticated agent on the context there is nobody to sign as, and the
// request must not leave unsigned — an origin would read it as an anonymous bot.
func TestSigningTransport_RefusesAnUnauthenticatedRequest(t *testing.T) {
	custody, _ := newCustody(t)
	if _, err := custody.Create(t.Context(), alice, liveWindow()); err != nil {
		t.Fatalf("mint key: %v", err)
	}
	o := newOrigin(t)

	err := fetchAs(t, signingClient(t, custody), o.srv.URL+signedPath, "")

	if !errors.Is(err, agentsign.ErrNoIdentity) {
		t.Fatalf("err = %v, want ErrNoIdentity", err)
	}
	if reqs := o.requests(); len(reqs) != 0 {
		t.Errorf("origin saw %d requests; an unidentified request must not be sent", len(reqs))
	}
}

// TestSigningTransport_RefusesWhenTheAgentHasNoActiveKey covers the states custody
// reports as "nothing to sign with": never provisioned, expired, or revoked. The
// error keeps the keystore sentinel so a caller can tell it from an outage.
func TestSigningTransport_RefusesWhenTheAgentHasNoActiveKey(t *testing.T) {
	custody, _ := newCustody(t)
	// A key whose window closed an hour before the clock's instant — the shape a
	// retired or revoked key leaves behind.
	if _, err := custody.Create(t.Context(), alice, keystore.Window{
		NotBefore: anchor.Add(-48 * time.Hour), NotAfter: anchor.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("mint expired key: %v", err)
	}
	o := newOrigin(t)

	err := fetchAs(t, signingClient(t, custody), o.srv.URL+signedPath, alice)

	if !errors.Is(err, keystore.ErrNotFound) {
		t.Fatalf("err = %v, want the keystore not-found sentinel", err)
	}
	if reqs := o.requests(); len(reqs) != 0 {
		t.Errorf("origin saw %d requests; want none", len(reqs))
	}
}
