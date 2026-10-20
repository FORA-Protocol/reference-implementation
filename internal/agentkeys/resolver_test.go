package agentkeys_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentkeys"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

var clockNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// The fixture directory is a TLS origin serving a signed key directory: the only
// form a signer's directory is ever fetched in. Its Signature-Agent member is
// the origin itself, https://127.0.0.1:<port>.

func resolverFor(t *testing.T, o *testutil.Origin) helpers.KeyResolver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return agentkeys.New(ctx, agentkeys.Config{
		Client: o.HTTPClient(),
		Clk:    clock.NewDeterministic(clockNow),
	})
}

// resolve invokes r with dir carried as the signed Signature-Agent directory and
// keyid as the RFC 9421 keyid (an RFC 7638 thumbprint).
func resolve(r helpers.KeyResolver, dir, keyid string) (ed25519.PublicKey, error) {
	return r.Resolve(helpers.WithSignatureAgent(context.Background(), dir), keyid)
}

func mustKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 gen: %v", err)
	}
	return pub, priv
}

// directoryOrigin serves a directory listing pub over [from, until), signed by
// priv, and returns the origin and the key's RFC 7638 thumbprint.
func directoryOrigin(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, from, until time.Time) (*testutil.Origin, string) {
	t.Helper()
	key := forawellknown.NewKey(pub, from, until)
	tp, err := forawellknown.Thumbprint(key)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	o := testutil.NewTLSOrigin(nil)
	t.Cleanup(o.Close)
	o.SetWBA(testutil.MarshalWBA(testutil.WBAFile(key)))
	o.SetWBASigners(priv)
	return o, tp
}

func TestResolver_PublishedKeyResolves(t *testing.T) {
	pub, priv := mustKey(t)
	o, keyid := directoryOrigin(t, pub, priv, clockNow.Add(-time.Hour), clockNow.Add(24*time.Hour))

	got, err := resolve(resolverFor(t, o), o.DirectoryOrigin(), keyid)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !bytes.Equal(got, pub) {
		t.Fatal("resolved key != published key")
	}
}

func TestResolver_NoSignatureAgentSkippedNoFetch(t *testing.T) {
	pub, priv := mustKey(t)
	o, keyid := directoryOrigin(t, pub, priv, clockNow, clockNow.Add(time.Hour))

	// No Signature-Agent directory on the context — the resolver cannot know
	// which directory to fetch, so it reports the keyid unknown WITHOUT a fetch.
	_, err := resolverFor(t, o).Resolve(context.Background(), keyid)
	if !errors.Is(err, helpers.ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey without Signature-Agent, got %v", err)
	}
	if hits := o.WBAHits(); hits != 0 {
		t.Fatalf("missing Signature-Agent must not trigger a fetch; got %d hits", hits)
	}
}

// TestResolver_NonHTTPSMemberRefusedWithoutFetch pins that no form of the
// signer's directory other than an https origin reaches the network: the http
// form of the very origin that serves the directory, a bare host, and an origin
// carrying a path all resolve no key and fetch nothing.
func TestResolver_NonHTTPSMemberRefusedWithoutFetch(t *testing.T) {
	pub, priv := mustKey(t)
	o, keyid := directoryOrigin(t, pub, priv, clockNow.Add(-time.Hour), clockNow.Add(time.Hour))
	r := resolverFor(t, o)
	for _, member := range []string{
		strings.Replace(o.DirectoryOrigin(), "https://", "http://", 1),
		strings.TrimPrefix(o.DirectoryOrigin(), "https://"),
		o.DirectoryOrigin() + "/",
	} {
		if _, err := resolve(r, member, keyid); !errors.Is(err, helpers.ErrUnknownKey) {
			t.Errorf("member %q: want ErrUnknownKey, got %v", member, err)
		}
	}
	if hits := o.WBAHits(); hits != 0 {
		t.Fatalf("a non-https member reached the directory %d times", hits)
	}
}

// TestResolver_UnsignedDirectoryResolvesNoKey pins that a directory whose
// response no listed key signed hands out no key, however valid its body.
func TestResolver_UnsignedDirectoryResolvesNoKey(t *testing.T) {
	pub, priv := mustKey(t)
	o, keyid := directoryOrigin(t, pub, priv, clockNow.Add(-time.Hour), clockNow.Add(time.Hour))
	o.SetWBASigners()
	assertUnknown(t, resolverFor(t, o), o.DirectoryOrigin(), keyid)
}

func TestResolver_DirectoryMissing(t *testing.T) {
	o := testutil.NewTLSOrigin(nil)
	t.Cleanup(o.Close)
	o.SetWBAStatus(http.StatusNotFound)
	assertUnknown(t, resolverFor(t, o), o.DirectoryOrigin(), "any-thumbprint")
}

func TestResolver_OriginUnreachable(t *testing.T) {
	o := testutil.NewTLSOrigin(nil)
	r := resolverFor(t, o)
	dir := o.DirectoryOrigin()
	o.Close()
	assertUnknown(t, r, dir, "any-thumbprint")
}

func TestResolver_MalformedDirectory(t *testing.T) {
	_, priv := mustKey(t)
	o := testutil.NewTLSOrigin(nil)
	t.Cleanup(o.Close)
	o.SetWBA([]byte("{not valid jwk set"))
	o.SetWBASigners(priv)
	assertUnknown(t, resolverFor(t, o), o.DirectoryOrigin(), "any-thumbprint")
}

func TestResolver_UnknownThumbprint(t *testing.T) {
	pub, priv := mustKey(t)
	o, _ := directoryOrigin(t, pub, priv, clockNow.Add(-time.Hour), clockNow.Add(time.Hour))
	// A thumbprint the directory does not publish resolves to unknown.
	other, _ := mustKey(t)
	otherTP, err := helpers.Thumbprint(other)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	assertUnknown(t, resolverFor(t, o), o.DirectoryOrigin(), otherTP)
}

func TestResolver_AllKeysExpired(t *testing.T) {
	pub, priv := mustKey(t)
	o, keyid := directoryOrigin(t, pub, priv, clockNow.Add(-48*time.Hour), clockNow.Add(-time.Hour))
	assertUnknown(t, resolverFor(t, o), o.DirectoryOrigin(), keyid)
}

func TestResolver_CachesDirectory(t *testing.T) {
	pub, priv := mustKey(t)
	o, keyid := directoryOrigin(t, pub, priv, clockNow.Add(-time.Hour), clockNow.Add(24*time.Hour))
	r := resolverFor(t, o)

	for range 3 {
		if _, err := resolve(r, o.DirectoryOrigin(), keyid); err != nil {
			t.Fatalf("resolve: %v", err)
		}
	}
	if hits := o.WBAHits(); hits != 1 {
		t.Fatalf("WBA directory should be fetched once and cached; got %d hits", hits)
	}
}

// TestResolver_PollerAppliesRevocation proves New starts the resolver's
// background revocation poller: a key that resolves cleanly on first contact is
// rejected once a newer revocation snapshot is published, WITHOUT the directory
// TTL expiring (TTL is set far past the test). The revocation list is the one
// the signer's own directory names, on the signer's own host.
func TestResolver_PollerAppliesRevocation(t *testing.T) {
	priv, key := testutil.NewSigningKey("agent-k1", clockNow.Add(-time.Hour), clockNow.Add(1000*time.Hour))
	tp, err := forawellknown.Thumbprint(key)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	o := testutil.NewTLSOrigin(nil)
	t.Cleanup(o.Close)
	wba := testutil.WBAFile(key)
	wba.RevocationUrl = testutil.Ptr(o.RevocationURL())
	o.SetWBA(testutil.MarshalWBA(wba))
	o.SetWBASigners(priv)
	o.SetRevocation(testutil.MarshalRevocation(clockNow.Add(-time.Hour))) // nothing revoked yet

	const pollInterval = 10 * time.Second
	clk := clock.NewDeterministic(clockNow)
	poll := testutil.NewPollSignals()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := agentkeys.New(ctx, agentkeys.Config{
		Client:       o.HTTPClient(),
		Clk:          clk,
		TTL:          100 * time.Hour, // never re-fetch the directory → isolate the poller
		PollInterval: pollInterval,
		OnPollArmed:  poll.Armed,
		OnPollCycle:  poll.Cycled,
	})

	if _, err := resolve(r, o.DirectoryOrigin(), tp); err != nil {
		t.Fatalf("prime resolve: %v", err)
	}
	o.SetRevocation(testutil.MarshalRevocation(clockNow, tp))
	poll.CrossOne(t, clk, 2*pollInterval)

	if _, err := resolve(r, o.DirectoryOrigin(), tp); !errors.Is(err, helpers.ErrUnknownKey) {
		t.Fatalf("running poller did not apply the revocation: got %v", err)
	}
}

// captureTransport records the outbound directory fetch so a test can assert
// the URL the resolver built, then fails the request — the captured URL is the
// assertion, no live directory is needed.
type captureTransport struct{ scheme, host string }

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.scheme, c.host = req.URL.Scheme, req.URL.Host
	return nil, errors.New("captured")
}

// TestNewService_NoDeploymentSettingReshapesTheDirectory pins that the
// well-known shaping knobs the manifest fetches read cannot reach a key
// directory: with FORA_WELLKNOWN_SCHEME=http and FORA_WELLKNOWN_PORT set, the
// directory a signature names is still fetched over https at exactly the origin
// the member names.
func TestNewService_NoDeploymentSettingReshapesTheDirectory(t *testing.T) {
	t.Setenv("FORA_WELLKNOWN_SCHEME", "http")
	t.Setenv("FORA_WELLKNOWN_PORT", "8123")
	ct := &captureTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	r := agentkeys.NewService(ctx, &http.Client{Transport: ct}, slog.Default())
	_, _ = resolve(r, "https://agent.example", "some-thumbprint")
	if ct.scheme != "https" || ct.host != "agent.example" {
		t.Fatalf("directory fetched at %s://%s, want https://agent.example", ct.scheme, ct.host)
	}
}

func assertUnknown(t *testing.T, r helpers.KeyResolver, dir, keyid string) {
	t.Helper()
	_, err := resolve(r, dir, keyid)
	if !errors.Is(err, helpers.ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
}
