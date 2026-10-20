package forahttpsig

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/keypolicy"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil/record"
)

const (
	foraTarget  = "/fora.v1.ExchangeService/DiscoverResources"
	plainTarget = "/article/2026/07"
)

// agentKeyFor builds a deterministic AgentKey with the directory an agent of that
// seed would publish. The keyid is left empty so the transport derives it — which
// is what a KeySource backed by key custody does.
func agentKeyFor(t *testing.T, seed byte, directory string) (AgentKey, ed25519.PublicKey) {
	t.Helper()
	s := make([]byte, ed25519.SeedSize)
	for i := range s {
		s[i] = seed
	}
	priv := ed25519.NewKeyFromSeed(s)
	return AgentKey{Directory: directory, Private: priv}, priv.Public().(ed25519.PublicKey)
}

func thumbprintOf(t *testing.T, pub ed25519.PublicKey) string {
	t.Helper()
	tp, err := helpers.Thumbprint(pub)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	return tp
}

// captured is one request exactly as the server received it, body included.
type captured struct {
	req  *http.Request
	body []byte
}

// captureServer records every request it receives, body restored, so a test can
// verify the signature against the bytes that actually crossed the wire rather
// than the ones the transport thinks it sent.
func captureServer(t *testing.T) (*httptest.Server, *record.Recorder[*record.CaptureResult]) {
	return record.CaptureServer(t)
}

// captureRequests returns every request the server has received so far, each
// checked for a body read error and paired with its body. A body that could not
// be read makes signature verification fail misleadingly; the check fails fast
// with a clear message instead. The SDK verifier takes the body as an argument
// rather than reading it off the request, so each body is read out here. Reading
// drains it, so it is put back as a fresh reader over the same bytes: a second
// call would otherwise pair every request with an empty body.
func captureRequests(t *testing.T, rec *record.Recorder[*record.CaptureResult]) []captured {
	t.Helper()
	snap := rec.Snapshot()
	out := make([]captured, 0, len(snap))
	for _, r := range snap {
		r.CheckReadErr(t)
		body, err := io.ReadAll(r.Req.Body)
		if err != nil {
			t.Fatalf("read captured body: %v", err)
		}
		r.Req.Body = io.NopCloser(bytes.NewReader(body))
		out = append(out, captured{req: r.Req, body: body})
	}
	return out
}

// send drives one request through a transport built with opts; what the server
// received is read off the capture recorder.
func send(t *testing.T, method, url string, body []byte, opts ...Option) {
	t.Helper()
	transport, err := New(nil, "", nil, core.ClockWindow(clock.System{}.Now, 30*time.Second), opts...)
	if err != nil {
		t.Fatalf("build transport: %v", err)
	}
	resp, err := (&http.Client{Transport: transport}).Do(newRequest(t, method, url, body))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	_ = resp.Body.Close()
}

// sendExpectingFailure drives a signed-path request the transport is expected to
// refuse and returns the error, closing any response body the client did hand
// back.
func sendExpectingFailure(t *testing.T, transport http.RoundTripper, url string) error {
	t.Helper()
	resp, err := (&http.Client{Transport: transport}).Do(newRequest(t, http.MethodPost, url, []byte(`{"q":"x"}`)))
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

func newRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

// staticSource is the KeySource a single-identity caller wires: the same key for
// every request.
func staticSource(key AgentKey) Option {
	return WithKeySource(func(context.Context) (AgentKey, error) { return key, nil })
}

// directoryKeys resolves a keyid only in the directory its signature's own
// Signature-Agent member names — the way a Web Bot Auth verifier resolves it.
func directoryKeys(keys map[string]ed25519.PublicKey) helpers.KeyResolver {
	return keypolicy.ResolverFunc(func(ctx context.Context, keyID string) (ed25519.PublicKey, error) {
		pub, ok := keys[helpers.SignatureAgentFromContext(ctx)]
		if !ok || thumbprintOrEmpty(pub) != keyID {
			return nil, helpers.ErrUnknownKey
		}
		return pub, nil
	})
}

func thumbprintOrEmpty(pub ed25519.PublicKey) string {
	tp, _ := helpers.Thumbprint(pub)
	return tp
}

// verifyFORA checks a received request with the SDK verifier — the same verify
// face the Broker's and the Exchange's gates run — resolving the key in the
// directory the signature's member names.
func verifyFORA(t *testing.T, got captured, keys map[string]ed25519.PublicKey) (*helpers.VerifiedRequest, error) {
	t.Helper()
	return helpers.VerifyRequestResolved(context.Background(), got.req, got.body, directoryKeys(keys), helpers.VerifyOptions{})
}

// TestRoundTrip_FORATargetSignsUnderTheWebBotAuthProfile pins what a FORA
// request carries: a signature the SDK verifier accepts, resolved in the
// directory its own Signature-Agent member names, with tag="web-bot-auth" and
// the member in the dictionary form.
func TestRoundTrip_FORATargetSignsUnderTheWebBotAuthProfile(t *testing.T) {
	srv, seen := captureServer(t)
	key, pub := agentKeyFor(t, 0x11, "https://alice.foramcp.org")
	send(t, http.MethodPost, srv.URL+foraTarget, []byte(`{"q":"x"}`), staticSource(key))

	got := captureRequests(t, seen)[0]
	v, err := verifyFORA(t, got, map[string]ed25519.PublicKey{key.Directory: pub})
	if err != nil {
		t.Fatalf("SDK verifier refused the signature: %v", err)
	}
	if v.SignatureAgent != key.Directory {
		t.Errorf("verified Signature-Agent = %q, want %q", v.SignatureAgent, key.Directory)
	}
	if sa := got.req.Header.Get(helpers.SignatureAgentHeader); sa != `sig1="https://alice.foramcp.org"` {
		t.Errorf("Signature-Agent = %q, want the one-member dictionary", sa)
	}
	if !bytes.Contains([]byte(got.req.Header.Get("Signature-Input")), []byte(`tag="web-bot-auth"`)) {
		t.Errorf("Signature-Input %q carries no web-bot-auth tag", got.req.Header.Get("Signature-Input"))
	}
}

// TestRoundTrip_BodylessFORATargetStaysUnsigned pins the skip rule: a bodiless
// request has nothing to bind a Content-Digest to, so it passes through.
func TestRoundTrip_BodylessFORATargetStaysUnsigned(t *testing.T) {
	srv, seen := captureServer(t)
	key, _ := agentKeyFor(t, 0x22, "https://alice.foramcp.org")
	send(t, http.MethodGet, srv.URL+foraTarget, nil, staticSource(key))

	if sig := captureRequests(t, seen)[0].req.Header.Get("Signature"); sig != "" {
		t.Errorf("bodiless request was signed: Signature=%q", sig)
	}
}

// TestNew_RejectsUnusableKeyMaterial pins that a misconfigured composition root
// fails at construction rather than on the first request: a key of the wrong
// size, and a directory that is not an https origin, which every verifier would
// refuse as a Signature-Agent member.
func TestNew_RejectsUnusableKeyMaterial(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		directory string
		priv      ed25519.PrivateKey
	}{
		{"short key", "https://console.example", []byte("short")},
		{"http directory", "http://console.example", priv},
		{"bare host directory", "console.example", priv},
		{"directory with a path", "https://console.example/", priv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(nil, tc.directory, tc.priv, core.ClockWindow(clock.System{}.Now, 30*time.Second))
			if !errors.Is(err, ErrUnusableKey) {
				t.Fatalf("New = %v, want ErrUnusableKey", err)
			}
		})
	}
}

// TestRoundTrip_NonFORATargetUnsigned pins that traffic no rule claims leaves
// this transport untouched.
func TestRoundTrip_NonFORATargetUnsigned(t *testing.T) {
	srv, seen := captureServer(t)
	key, _ := agentKeyFor(t, 0x33, "https://alice.foramcp.org")
	send(t, http.MethodPost, srv.URL+plainTarget, []byte("x"), staticSource(key))

	if sig := captureRequests(t, seen)[0].req.Header.Get("Signature"); sig != "" {
		t.Errorf("non-FORA request was signed: Signature=%q", sig)
	}
}

// TestRoundTrip_KeySourceSignsAsEachAgent is the registry's case: ONE transport,
// MANY agents, each request signed by the key its context resolves, each naming
// its own directory — and each verifies only in that directory.
func TestRoundTrip_KeySourceSignsAsEachAgent(t *testing.T) {
	srv, seen := captureServer(t)
	alice, alicePub := agentKeyFor(t, 0x44, "https://alice.foramcp.org")
	bob, bobPub := agentKeyFor(t, 0x55, "https://bob.foramcp.org")
	type agentCtx struct{}
	src := WithKeySource(func(ctx context.Context) (AgentKey, error) {
		if ctx.Value(agentCtx{}) == "bob" {
			return bob, nil
		}
		return alice, nil
	})
	transport, err := New(nil, "", nil, core.ClockWindow(clock.System{}.Now, 30*time.Second), src)
	if err != nil {
		t.Fatal(err)
	}
	for _, who := range []string{"alice", "bob"} {
		req := newRequest(t, http.MethodPost, srv.URL+foraTarget, []byte(`{"q":"`+who+`"}`))
		req = req.WithContext(context.WithValue(req.Context(), agentCtx{}, who))
		resp, err := (&http.Client{Transport: transport}).Do(req)
		if err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		_ = resp.Body.Close()
	}

	requests := captureRequests(t, seen)
	keys := map[string]ed25519.PublicKey{alice.Directory: alicePub, bob.Directory: bobPub}
	for i, want := range []string{alice.Directory, bob.Directory} {
		v, err := verifyFORA(t, requests[i], keys)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if v.SignatureAgent != want {
			t.Errorf("request %d signed as %q, want %q", i, v.SignatureAgent, want)
		}
	}
	// A signature resolved through another agent's directory does not verify.
	if _, err := verifyFORA(t, requests[1], map[string]ed25519.PublicKey{bob.Directory: alicePub}); err == nil {
		t.Error("bob's request verified under alice's key")
	}
}

// TestRoundTrip_KeySourceWithAnHTTPDirectoryNeverSends pins that a per-request
// key whose directory is not an https origin is refused by the SDK signer and
// never sent: a Signature-Agent every verifier refuses would present the agent
// as unauthenticated.
func TestRoundTrip_KeySourceWithAnHTTPDirectoryNeverSends(t *testing.T) {
	srv, seen := captureServer(t)
	key, _ := agentKeyFor(t, 0x66, "http://alice.foramcp.org")
	transport, err := New(nil, "", nil, core.ClockWindow(clock.System{}.Now, 30*time.Second), staticSource(key))
	if err != nil {
		t.Fatal(err)
	}
	err = sendExpectingFailure(t, transport, srv.URL+foraTarget)

	if !errors.Is(err, helpers.ErrSignatureAgentNotOrigin) {
		t.Fatalf("err = %v, want helpers.ErrSignatureAgentNotOrigin", err)
	}
	if seen.Len() != 0 {
		t.Errorf("server received %d requests; want none", seen.Len())
	}
}

// TestRoundTrip_WithFORATargetsSignsANonFORAPathAsFORA is the option's reason for
// existing: the Exchange's console API is not a /fora.* path but is verified
// exactly like one.
func TestRoundTrip_WithFORATargetsSignsANonFORAPathAsFORA(t *testing.T) {
	srv, seen := captureServer(t)
	key, pub := agentKeyFor(t, 0x77, "https://console.example")
	send(t, http.MethodPost, srv.URL+plainTarget, []byte(`{"q":"x"}`), staticSource(key),
		WithFORATargets(func(r *http.Request) bool { return r.URL.Path == plainTarget }))

	got := captureRequests(t, seen)[0]
	if _, err := verifyFORA(t, got, map[string]ed25519.PublicKey{key.Directory: pub}); err != nil {
		t.Fatalf("admitted target was not signed as FORA: %v", err)
	}
}

// TestRoundTrip_WithFORATargetsNeverRemovesFORAPaths pins that the predicate can
// only add targets.
func TestRoundTrip_WithFORATargetsNeverRemovesFORAPaths(t *testing.T) {
	srv, seen := captureServer(t)
	key, pub := agentKeyFor(t, 0x88, "https://console.example")
	send(t, http.MethodPost, srv.URL+foraTarget, []byte(`{"q":"x"}`), staticSource(key),
		WithFORATargets(func(*http.Request) bool { return false }))

	got := captureRequests(t, seen)[0]
	if _, err := verifyFORA(t, got, map[string]ed25519.PublicKey{key.Directory: pub}); err != nil {
		t.Fatalf("a /fora.* path lost its signature: %v", err)
	}
}

// TestRoundTrip_WithFORATargetsBodylessStaysUnsigned pins that an admitted
// target follows the same bodiless skip rule.
func TestRoundTrip_WithFORATargetsBodylessStaysUnsigned(t *testing.T) {
	srv, seen := captureServer(t)
	key, _ := agentKeyFor(t, 0x99, "https://console.example")
	send(t, http.MethodGet, srv.URL+plainTarget, nil, staticSource(key),
		WithFORATargets(func(*http.Request) bool { return true }))

	if sig := captureRequests(t, seen)[0].req.Header.Get("Signature"); sig != "" {
		t.Errorf("bodiless admitted request was signed: Signature=%q", sig)
	}
}
