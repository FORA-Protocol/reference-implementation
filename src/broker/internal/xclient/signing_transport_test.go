package xclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil/record"
)

// brokerOrigin is the Broker's own https origin, the Signature-Agent member every
// request it signs names (BROKER_PUBLIC_ORIGIN in production).
const brokerOrigin = "https://broker.fixture.test"

// TestSigningTransport_SignsAloneAsTheBroker pins what every Broker→Exchange
// request carries: exactly one signature, the Broker's, naming the Broker's own
// origin as its Signature-Agent member, which the SDK verifier accepts when it
// resolves the key in that origin's directory. The Broker originates or
// re-packages every request it sends, so there is no agent signature for it to
// cover.
func TestSigningTransport_SignsAloneAsTheBroker(t *testing.T) {
	brokerPub, brokerPriv := mustGenerateKey(t)
	server, capturedReqs := record.CaptureServer(t)
	defer server.Close()

	body := []byte(`{"url":"https://publisher.example/content"}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		server.URL+"/fora.v1.ExchangeService/ExecuteTransaction", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	brokerKey := &RelayKey{KeyID: rwtestutil.MustThumbprint(t, brokerPub), Private: brokerPriv}
	resp := sendThroughSigningTransport(t, req, brokerKey, now)
	defer func() { _ = resp.Body.Close() }()

	// Extract and validate the single captured request.
	snap := capturedReqs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("captured %d requests; want 1", len(snap))
	}
	snap[0].CheckReadErr(t)
	captured := snap[0].Req

	if got := captured.Header.Get(helpers.SignatureAgentHeader); got != `sig1="`+brokerOrigin+`"` {
		t.Fatalf("Signature-Agent = %q, want the one Broker member", got)
	}
	if in := captured.Header.Get("Signature-Input"); strings.Contains(in, "sig2=") {
		t.Fatalf("Signature-Input carries a second signature: %q", in)
	}
	resolver := keyInDirectory(brokerOrigin, brokerPub)
	got, err := io.ReadAll(captured.Body)
	if err != nil {
		t.Fatal(err)
	}
	v, err := helpers.VerifyRequestResolved(context.Background(), captured, got, resolver, helpers.VerifyOptions{Now: now})
	if err != nil {
		t.Fatalf("the Broker's signature did not verify in its own directory: %v", err)
	}
	if v.SignatureAgent != brokerOrigin {
		t.Errorf("verified Signature-Agent = %q, want %q", v.SignatureAgent, brokerOrigin)
	}
}

// TestSigningTransport_BurstIsNeverSignedAheadOfTheClock pins that a burst of
// requests in one second, the shape of a Resolve fanning out to every Exchange,
// is signed with created equal to the clock's now on every request. A window
// that moved created forward per request would sign the tail of the burst in
// the future, which a verifier allowing no future skew refuses.
func TestSigningTransport_BurstIsNeverSignedAheadOfTheClock(t *testing.T) {
	brokerPub, brokerPriv := mustGenerateKey(t)
	brokerKey := &RelayKey{KeyID: rwtestutil.MustThumbprint(t, brokerPub), Private: brokerPriv}
	server, capturedReqs := record.CaptureServer(t)
	defer server.Close()
	now := time.Unix(1_900_000_000, 0)
	transport, err := NewSigningTransport(nil, brokerKey, brokerOrigin, 30*time.Second, clock.NewDeterministic(now))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport}
	want := fmt.Sprintf(";created=%d;", now.Unix())
	for i := range 5 {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
			server.URL+"/fora.v1.ExchangeService/DiscoverResources", bytes.NewReader([]byte(`{}`)))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		snap := capturedReqs.Snapshot()
		if len(snap) != i+1 {
			t.Fatalf("request %d of the burst: captured %d requests; want %d", i, len(snap), i+1)
		}
		snap[i].CheckReadErr(t)
		if in := snap[i].Req.Header.Get("Signature-Input"); !strings.Contains(in, want) {
			t.Fatalf("request %d of the burst: Signature-Input %q, want created at the clock's now", i, in)
		}
	}
}

// TestNewSigningTransport_RefusesAnOriginThatIsNotHTTPS pins that the relay
// transport is never built over a Signature-Agent every verifier refuses.
func TestNewSigningTransport_RefusesAnOriginThatIsNotHTTPS(t *testing.T) {
	brokerPub, brokerPriv := mustGenerateKey(t)
	key := &RelayKey{KeyID: rwtestutil.MustThumbprint(t, brokerPub), Private: brokerPriv}
	for _, origin := range []string{"broker.fixture.test", "http://broker.fixture.test", "https://broker.fixture.test/"} {
		if _, err := NewSigningTransport(nil, key, origin, 30*time.Second, nil); !errors.Is(err, helpers.ErrSignatureAgentNotOrigin) {
			t.Errorf("origin %q: err = %v, want helpers.ErrSignatureAgentNotOrigin", origin, err)
		}
	}
}

// keyInDirectory resolves a keyid only when the signature's own member names
// origin, the way a Web Bot Auth verifier resolves it.
func keyInDirectory(origin string, pub ed25519.PublicKey) helpers.KeyResolver {
	return resolverFunc(func(ctx context.Context, _ string) (ed25519.PublicKey, error) {
		if helpers.SignatureAgentFromContext(ctx) != origin {
			return nil, helpers.ErrUnknownKey
		}
		return pub, nil
	})
}

type resolverFunc func(ctx context.Context, keyID string) (ed25519.PublicKey, error)

func (f resolverFunc) Resolve(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
	return f(ctx, keyID)
}

// mustGenerateKey generates an ed25519 keypair or fails the test.
func mustGenerateKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// sendThroughSigningTransport creates a broker signing transport and sends
// the request through it, returning the HTTP response.
func sendThroughSigningTransport(
	t *testing.T, req *http.Request, brokerKey *RelayKey, now time.Time,
) *http.Response {
	t.Helper()
	clk := clock.NewDeterministic(now)
	transport, err := NewSigningTransport(nil, brokerKey, brokerOrigin, 30*time.Second, clk)
	if err != nil {
		t.Fatalf("build signing transport: %v", err)
	}
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("transport roundtrip: %v", err)
	}
	return resp
}

// TestSigningTransport_SetsGetBody guards the retry defect: the relay signing
// transport MUST re-seat req.GetBody after draining the body so net/http can
// replay it on a retried request. A signer that leaves GetBody nil silently
// drops the body on the second attempt (empty content-digest → verify failure).
func TestSigningTransport_SetsGetBody(t *testing.T) {
	_, brokerPriv := mustGenerateKey(t)
	brokerPub := brokerPriv.Public().(ed25519.PublicKey)
	brokerKey := &RelayKey{KeyID: rwtestutil.MustThumbprint(t, brokerPub), Private: brokerPriv}

	// A captured inner transport lets us inspect the request AFTER signing without
	// making a network hop.
	var seen *http.Request
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})

	transport, err := NewSigningTransport(inner, brokerKey, brokerOrigin, 30*time.Second,
		clock.NewDeterministic(time.Now()))
	if err != nil {
		t.Fatalf("build signing transport: %v", err)
	}

	body := []byte(`{"url":"https://publisher.example/content"}`)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"https://exchange.example/fora.v1.ExchangeService/CreateOffer", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer agent-token")

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	_ = resp.Body.Close()

	if seen == nil {
		t.Fatal("inner transport never saw the request")
	}
	if seen.GetBody == nil {
		t.Fatal("GetBody is nil after signing: a retried request would lose its body")
	}
	// GetBody must reproduce the exact signed bytes so a retry re-sends them.
	rc, err := seen.GetBody()
	if err != nil {
		t.Fatalf("GetBody: %v", err)
	}
	replayed, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read replayed body: %v", err)
	}
	if !bytes.Equal(replayed, body) {
		t.Errorf("GetBody replayed %q, want %q", replayed, body)
	}
}

// roundTripFunc adapts a function to http.RoundTripper for the capture test.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestLoadRelayKey_ValidFile verifies loading a valid broker relay key file.
func TestLoadRelayKey_ValidFile(t *testing.T) {
	// Generate a test key.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Extract seed from private key.
	seed := priv.Seed()

	// Create a temporary file with the relay key. After the WBA split the file
	// carries no kid — the keyid is derived as the public key's thumbprint.
	keyFile := relayKeyFile{
		PrivateKey: base64.RawURLEncoding.EncodeToString(seed),
		PublicKey:  base64.RawURLEncoding.EncodeToString(pub),
	}

	data, err := json.Marshal(keyFile)
	if err != nil {
		t.Fatalf("marshal key file: %v", err)
	}

	// Write to temp file.
	tmpFile := t.TempDir() + "/broker-relay-key.json"
	if err := writeFile(tmpFile, data); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	// Load the key.
	relayKey, err := LoadRelayKey(tmpFile)
	if err != nil {
		t.Fatalf("load relay key: %v", err)
	}

	// KeyID is the RFC 7638 thumbprint of the derived public key.
	if want := rwtestutil.MustThumbprint(t, pub); relayKey.KeyID != want {
		t.Errorf("keyid: want %s, got %s", want, relayKey.KeyID)
	}

	// Verify private key matches.
	if !bytes.Equal(relayKey.Private, priv) {
		t.Error("private key mismatch")
	}
}

// TestLoadRelayKey_BadSeedLength verifies rejection of a private_key whose
// decoded length is not a valid Ed25519 seed.
func TestLoadRelayKey_BadSeedLength(t *testing.T) {
	keyFile := relayKeyFile{
		PrivateKey: base64.RawURLEncoding.EncodeToString([]byte("too-short")),
		PublicKey:  "",
	}

	data, err := json.Marshal(keyFile)
	if err != nil {
		t.Fatalf("marshal key file: %v", err)
	}

	tmpFile := t.TempDir() + "/bad-key.json"
	if err := writeFile(tmpFile, data); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	_, err = LoadRelayKey(tmpFile)
	if err == nil {
		t.Fatal("want error for bad seed length, got nil")
	}
	if !strings.Contains(err.Error(), "private_key length") {
		t.Errorf("error should mention private_key length, got: %v", err)
	}
}

// writeFile is a test helper to write bytes to a file.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
