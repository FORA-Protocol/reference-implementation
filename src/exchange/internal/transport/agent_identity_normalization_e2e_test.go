//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// An agent's identity is the HOST of the directory it names in its covered
// Signature-Agent member, not the origin verbatim. Under the Web Bot Auth profile
// that member is always an https origin, so the identity is the host — with any
// non-default port — and a member that is not an https origin is refused before
// any identity is derived.
//
// These drive the property through the public Connect router only: Register
// creates the account, GetAccountStatus reads it back. Both legs traverse
// transport → service → repo → Postgres, and the account is observed through the
// same RPC a client would use, so no test here reaches past a layer (Testing
// Doctrine §9). The billing_ref is the observable: it is minted once per account,
// so two spellings returning one ref is proof they resolved to one row, and it
// comes from the stored row rather than anything the caller sent.
//
// The harness reuses newRegisterHarness / newAgent wholesale; only the
// Signature-Agent the client signs with varies, which is exactly the variable
// under test.

// TestAgentIdentity_OneHostIsOneAccount pins the property: one agent, one key,
// and the spellings of its directory a signer may use — the origin and the host
// the test signer expands to that same origin — land on the account the first
// call created. A billing_ref is minted once per account, so one ref across the
// spellings proves they resolved to one row.
func TestAgentIdentity_OneHostIsOneAccount(t *testing.T) {
	h := newRegisterHarness(t)
	const host = "collapse-agent.example"
	a := h.newAgent(t, host)

	reg, err := a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(testutil.RegistrationStruct(t, map[string]any{"legal_entity": "Collapse AI Ltd"}))))
	if err != nil {
		t.Fatalf("Register as %q: %v", host, err)
	}
	want := reg.Msg.GetBillingRef()
	if want == "" {
		t.Fatal("Register returned an empty billing_ref; the rest of this test would assert nothing")
	}

	for _, directory := range []string{"https://" + host, host} {
		t.Run(directory, func(t *testing.T) {
			client := h.clientFor(directory, a.priv)
			resp, err := client.GetAccountStatus(h.ctx,
				connect.NewRequest(newAccountStatusRequest()))
			if err != nil {
				t.Fatalf("GetAccountStatus signing Signature-Agent %q: %v", directory, err)
			}
			if got := resp.Msg.GetBillingRef(); got != want {
				t.Errorf("billing_ref = %q, want %q — %q resolved to a different account than %q",
					got, want, directory, host)
			}
			if !resp.Msg.GetActive() {
				t.Error("active = false, want true (the account Register created)")
			}
		})
	}
}

// TestAgentIdentity_HTTPSignatureAgentIsRefused is the negative half: a
// Signature-Agent member naming the agent's directory over plaintext http is
// refused as an authentication failure, with the Accept-Signature value a
// signature here must follow, since the caller can fix it by signing again as
// the profile requires, and it reaches no account — not the one the agent holds
// under its https origin, and not a second one. No scheme or
// port setting on the Exchange can make an http member acceptable. The member is
// rewritten after an otherwise valid signature, because no SDK signer emits it.
func TestAgentIdentity_HTTPSignatureAgentIsRefused(t *testing.T) {
	h := newRegisterHarness(t)
	const host = "plaintext-agent.example"
	a := h.newAgent(t, host)

	for _, member := range []string{`sig1="http://` + host + `"`, `sig1="http://` + host + `:443"`} {
		client := h.memberRewritingClient(host, member, a.priv)
		_, err := client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
		if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
			t.Fatalf("member %s: code = %v, want Unauthenticated (err=%v)", member, got, err)
		}
		var cerr *connect.Error
		if !errors.As(err, &cerr) {
			t.Fatalf("member %s: refusal %v is not a Connect error", member, err)
		}
		if got, want := cerr.Meta().Get(helpers.AcceptSignatureHeader), helpers.AcceptSignature(false); got != want {
			t.Errorf("member %s: %s = %q, want %q", member, helpers.AcceptSignatureHeader, got, want)
		}
	}
	// Nothing was registered for the host the refused member named: the agent's
	// own https client finds no account.
	_, err := a.client.GetAccountStatus(h.ctx, connect.NewRequest(newAccountStatusRequest()))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound — an http member registered an account (err=%v)", got, err)
	}
}

// memberRewritingClient is a client that signs as https://<host> with priv and
// then replaces the Signature-Agent header with member, standing in for a
// signer that names a member the profile refuses. It speaks the Connect
// protocol, not gRPC: the signature gate answers a refusal with a plain HTTP
// 401 carrying Accept-Signature, and a gRPC client reads that status without
// surfacing the response headers, which would hide the header under test.
func (h *registerHarness) memberRewritingClient(
	host, member string, priv ed25519.PrivateKey,
) foraconnect.ExchangeServiceClient {
	rewrite := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = req.Clone(req.Context())
		req.Header.Set(helpers.SignatureAgentHeader, member)
		return h.baseTransport.RoundTrip(req)
	})
	return foraconnect.NewExchangeServiceClient(
		&http.Client{Transport: exchangetest.SigningTransport(rewrite, host, priv)},
		h.server, connect.WithProtoJSON())
}

// TestAgentIdentity_SchemeIsNotAnIdentityBoundary is the same property seen from
// the other side, and it is the one that matters for authorization: a caller must
// not be able to pick which identity it registers as by choosing a scheme. Here
// the FIRST contact is over https and the follow-up is bare, so the row is created
// from a schemed directory and read back without one. Registering twice would mint
// a second billing_ref; sharing the account is what proves the scheme never
// reached the stored key.
func TestAgentIdentity_SchemeIsNotAnIdentityBoundary(t *testing.T) {
	h := newRegisterHarness(t)
	const host = "https-first-agent.example"
	a := h.newAgent(t, host)

	// Self-sign up and register under the https spelling.
	httpsClient := h.clientFor("https://"+host, a.priv)
	reg, err := httpsClient.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
	if err != nil {
		t.Fatalf("Register as %q: %v", "https://"+host, err)
	}
	want := reg.Msg.GetBillingRef()

	// A second Register under the bare spelling must be the already-registered fast
	// path returning the SAME ref, not a fresh account (ADR-021 D4: the stored id
	// wins). A new ref here would mean the bare spelling is a separate identity.
	again, err := a.client.Register(h.ctx, connect.NewRequest(newRegisterRequest(nil)))
	if err != nil {
		t.Fatalf("Register as bare %q after registering as https: %v", host, err)
	}
	if got := again.Msg.GetBillingRef(); got != want {
		t.Fatalf("billing_ref = %q, want %q — the bare spelling minted a second account", got, want)
	}
}

// TestAgentIdentity_UnusableDirectoryIsRejected is the negative path (Testing
// Doctrine §10). A Signature-Agent that names no host cannot be an identity and
// cannot be a fetch target, so it must be refused rather than stored.
//
// The value used is a dictionary whose only member is keyed agent2 while the
// signature covers the member keyed sig1: the signature names no directory at
// all, so no identity can be derived from it.
//
// The refused call is a REGISTER, deliberately, and that choice is what makes the
// side-effect half of this test able to fail at all. Register is the only RPC that
// mints a billing_ref; GetAccountStatus answers NotFound for any row without one
// and lazily creates the row it reads. So a refused GetAccountStatus leaves
// "no account exists" true no matter what the code does — it asserts nothing. A
// refused Register leaves it true only while the refusal actually holds: an
// over-lenient parser that unwrapped the dictionary form would mint a ref under
// the bare host, and the follow-up below would find it.
func TestAgentIdentity_UnusableDirectoryIsRejected(t *testing.T) {
	h := newRegisterHarness(t)
	const host = "dict-form-agent.example"
	a := h.newAgent(t, host)

	client := h.memberRewritingClient(host, `agent2="https://`+host+`"`, a.priv)
	_, err := client.Register(h.ctx, connect.NewRequest(newRegisterRequest(testutil.RegistrationStruct(t, map[string]any{"legal_entity": "Dict Form AI Ltd"}))))
	if err == nil {
		t.Fatal("Register succeeded with a Signature-Agent that names no host; want rejection")
	}
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want Unauthenticated (err=%v)", got, err)
	}

	// No account was created for the host named INSIDE the refused value. Observed
	// through the public read RPC: NotFound is the answer for an agent with no
	// billing_ref, and only Register mints one.
	_, err = a.client.GetAccountStatus(h.ctx,
		connect.NewRequest(newAccountStatusRequest()))
	if got := connect.CodeOf(err); got != connect.CodeNotFound {
		t.Fatalf("code = %v, want NotFound — the refused Register minted an account for %q anyway (err=%v)",
			got, host, err)
	}
}
