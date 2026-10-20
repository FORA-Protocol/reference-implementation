package server_test

import (
	"crypto/ed25519"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/server"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// liveKey is a key valid around the real clock, because the SDK reader checks
// the response signatures against it.
func liveKey(seed string) (ed25519.PrivateKey, *forawellknown.Key) {
	now := time.Now()
	return testutil.NewSigningKey(seed, now.Add(-time.Hour), now.Add(time.Hour))
}

// serveDirectory mounts a WBA handler listing keys, signed by privs, on a TLS
// server, the way every service in this repository serves its directory.
func serveDirectory(t *testing.T, keys []*forawellknown.Key, privs ...ed25519.PrivateKey) *httptest.Server {
	t.Helper()
	signers := make([]helpers.Signer, 0, len(privs))
	for _, p := range privs {
		signers = append(signers, testutil.DirectorySigner(p))
	}
	h, err := server.NewWBAHandler(server.WBAConfig{Keys: server.StaticKeys(keys...), Signers: signers})
	if err != nil {
		t.Fatalf("NewWBAHandler: %v", err)
	}
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestWBAHandler_ServesASignedDirectoryEveryReaderAccepts drives the served
// directory through the SDK's own reader (resolvers.ReadWBADirectory), which
// refuses a redirect, a media type other than the directory one, and a response
// any listed key did not sign. Two keys, so the response must carry a signature
// by each.
func TestWBAHandler_ServesASignedDirectoryEveryReaderAccepts(t *testing.T) {
	t.Parallel()
	p1, k1 := liveKey("signed-1")
	p2, k2 := liveKey("signed-2")
	srv := serveDirectory(t, []*forawellknown.Key{k1, k2}, p1, p2)

	doc, err := resolvers.ReadWBADirectory(t.Context(), srv.URL+forawellknown.WBAPath,
		resolvers.ReadOptions{Client: srv.Client()})
	if err != nil {
		t.Fatalf("the SDK reader refused the served directory: %v", err)
	}
	if doc.MediaType != server.DirectoryMediaType || len(doc.Message.GetKeys()) != 2 {
		t.Fatalf("read media type %q with %d keys, want %s with 2", doc.MediaType,
			len(doc.Message.GetKeys()), server.DirectoryMediaType)
	}
}

// TestWBAHandler_ResponseSignatureCoversTheRequestAuthority pins that the
// response signature is made for the authority the request named: it verifies
// (helpers.VerifyDirectoryResponse) under that authority and under no other, so
// a served directory cannot be replayed under a different host.
func TestWBAHandler_ResponseSignatureCoversTheRequestAuthority(t *testing.T) {
	t.Parallel()
	priv, key := liveKey("authority")
	srv := serveDirectory(t, []*forawellknown.Key{key}, priv)

	for _, host := range []string{"agent.example", "other.example:8443"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+forawellknown.WBAPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d", resp.StatusCode)
		}
		pub, _ := forawellknown.PublicKey(key)
		tp := testutil.MustThumbprintKey(t, key)
		verified, err := helpers.VerifyDirectoryResponse(host, resp.Header, body, []ed25519.PublicKey{pub}, time.Now())
		if err != nil || !verified[tp] {
			t.Fatalf("response for %s did not verify under its own authority: %v %v", host, verified, err)
		}
		other, err := helpers.VerifyDirectoryResponse("elsewhere.example", resp.Header, body, []ed25519.PublicKey{pub}, time.Now())
		if err == nil && other[tp] {
			t.Fatalf("response for %s verified under another authority", host)
		}
	}
}

// TestWBAHandler_DefaultHTTPSPortIsNotPartOfTheAuthority pins the authority a
// client derives for https://host:443: the host alone, which is what the
// response signature must cover.
func TestWBAHandler_DefaultHTTPSPortIsNotPartOfTheAuthority(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://Agent.Example:443/.well-known/x", nil)
	req.TLS = &tls.ConnectionState{}
	got, err := server.RequestAuthority(req)
	if err != nil || got != "agent.example" {
		t.Fatalf("RequestAuthority = %q, %v; want agent.example", got, err)
	}
}

// TestNewWBAHandler_RefusesAListedKeyNoSignerHolds pins the boot refusal: a
// directory whose response a listed key cannot sign would be read without that
// key by every verifier, so the handler is never built.
func TestNewWBAHandler_RefusesAListedKeyNoSignerHolds(t *testing.T) {
	t.Parallel()
	_, listed := liveKey("listed")
	other, _ := liveKey("other")
	_, err := server.NewWBAHandler(server.WBAConfig{
		Keys: server.StaticKeys(listed), Signers: []helpers.Signer{testutil.DirectorySigner(other)},
	})
	if err == nil || !strings.Contains(err.Error(), "no signer holds listed key") {
		t.Fatalf("NewWBAHandler = %v, want the listed-key refusal", err)
	}
}
