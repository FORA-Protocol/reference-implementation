//go:build integration

package transport_test

// The Broker's refusals of the request itself, each answered before anything is
// sent: the request signature (checked by the verify seam, which every
// ExecuteTransaction enters, signed or not), the binding of requester.domain to
// the signing directory, and a malformed body. A signature refusal carries
// request_auth_failure exactly as at an Exchange. Every test here runs in front
// of a sink, which fails the test if the Broker sends the Exchange anything.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// unsignedBrokerClient is a BrokerService client that signs nothing.
func unsignedBrokerClient(baseURL string) foraconnect.BrokerServiceClient {
	return foraconnect.NewBrokerServiceClient(http.DefaultClient, baseURL, connect.WithProtoJSON())
}

// assertRequestAuthFailure checks err is UNAUTHENTICATED carrying
// request_auth_failure with the named reason.
func assertRequestAuthFailure(t *testing.T, err error, reason string) {
	t.Helper()
	ce, ok := asConnectError(err)
	if !ok {
		t.Fatalf("answer %v, want an UNAUTHENTICATED refusal", err)
	}
	if ce.Code() != connect.CodeUnauthenticated {
		t.Fatalf("code = %v, want unauthenticated: %v", ce.Code(), ce)
	}
	if got := purchaseErrorDetail(t, ce).GetRequestAuthFailure().GetReason().String(); got != reason {
		t.Errorf("request_auth_failure = %s, want %s", got, reason)
	}
}

// asConnectError unwraps a Connect error.
func asConnectError(err error) (*connect.Error, bool) {
	var ce *connect.Error
	ok := errors.As(err, &ce)
	return ce, ok
}

// TestPurchase_ARequesterThatIsNotTheSignerIsRefused pins the requester binding.
// The signature verifies, but requester.domain names another agent, and every
// Exchange resolves the acceptance keys from requester.domain. The Broker refuses
// it first, as a signature that does not verify for the requester the body
// claims: UNAUTHENTICATED with request_auth_failure SIGNATURE_INVALID.
func TestPurchase_ARequesterThatIsNotTheSignerIsRefused(t *testing.T) {
	env := newPurchaseTestEnv(t)

	req := env.purchaseRequestAs(t, "tx-not-the-signer", "someone-else.example", line{"offer-1", env.exchangeDom})
	_, err := env.client.ExecuteTransaction(t.Context(), connect.NewRequest(req))

	assertNothingSent(t, env.exchange)
	assertRequestAuthFailure(t, err, "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID")
	ce, _ := asConnectError(err)
	if got := purchaseErrorDetail(t, ce).GetMetadata()["field"]; got != "requester.domain" {
		t.Errorf("metadata[field] = %q, want requester.domain", got)
	}
	assertPurchaseAudit(t, env, "REJECTED_AUTHZ", "WARN", "")
}

// TestPurchase_AMalformedRequestIsInvalidArgument drives each malformed shape the
// protocol names, plus two offers presenting one offer_id. Every one is refused
// INVALID_ARGUMENT with nothing sent. A shape the purchase service refuses leaves
// one REJECTED_INVALID audit record; a shape the strict wire validation refuses
// never reaches the service, so it carries protovalidate's violations and leaves
// no purchase record.
func TestPurchase_AMalformedRequestIsInvalidArgument(t *testing.T) {
	cases := map[string]struct {
		mutate func(req *forav1.TransactionRequest)
		wire   bool // refused by the strict wire validation, before the purchase service
	}{
		"an item without its acceptance": {mutate: func(req *forav1.TransactionRequest) {
			req.Items[1].AgentAcceptance = nil
		}},
		"no request acceptance": {mutate: func(req *forav1.TransactionRequest) {
			req.AgentRequestAcceptance = nil
		}},
		"a request acceptance listing the items in another order": {mutate: func(req *forav1.TransactionRequest) {
			req.Items[0], req.Items[1] = req.Items[1], req.Items[0]
		}},
		"a request acceptance naming another idempotency key": {mutate: func(req *forav1.TransactionRequest) {
			req.IdempotencyKey = "tx-some-other-key"
		}},
		"two items presenting one offer": {mutate: func(req *forav1.TransactionRequest) {
			req.Items[1].Offer.OfferId = req.Items[0].GetOffer().GetOfferId()
		}},
		"no items": {mutate: func(req *forav1.TransactionRequest) {
			req.Items = nil
		}, wire: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newPurchaseTestEnv(t)
			req := env.purchaseRequest(t, "tx-malformed",
				line{"offer-1", env.exchangeDom}, line{"offer-2", env.exchangeDom})
			tc.mutate(req)

			ce := env.refused(t, req)

			assertNothingSent(t, env.exchange)
			if ce.Code() != connect.CodeInvalidArgument {
				t.Errorf("code = %v, want invalid_argument: %v", ce.Code(), ce)
			}
			if !tc.wire {
				assertPurchaseAudit(t, env, "REJECTED_INVALID", "WARN", "")
				return
			}
			if len(testutil.ValidationViolations(t, ce)) == 0 {
				t.Errorf("a wire-level refusal carries no protovalidate violations: %v", ce)
			}
			if recs := env.logs.Records(t, "broker.execute_transaction"); len(recs) != 0 {
				t.Errorf("a request the wire validation refused left purchase records: %v", recs)
			}
		})
	}
}

// TestPurchase_AnUnsignedPurchaseIsRefusedAsUnsigned pins the verify gate: a
// purchase always enters the seam, so an unsigned one is refused the way an
// Exchange refuses it rather than reaching the handler.
func TestPurchase_AnUnsignedPurchaseIsRefusedAsUnsigned(t *testing.T) {
	env := newPurchaseTestEnv(t)
	req := env.purchaseRequest(t, "tx-unsigned", line{"offer-1", env.exchangeDom})

	_, err := unsignedBrokerClient(env.brokerURL).ExecuteTransaction(t.Context(), connect.NewRequest(req))

	assertNothingSent(t, env.exchange)
	assertRequestAuthFailure(t, err, "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_MISSING")
}

// TestPurchase_ATamperedSignatureIsRefused flips one byte of the signature after
// the agent signed: the request no longer verifies.
func TestPurchase_ATamperedSignatureIsRefused(t *testing.T) {
	env := newPurchaseTestEnv(t)
	client := &http.Client{Transport: brokerSigningTransport(
		tamperSignature{base: http.DefaultTransport}, purchaseAgentDirectory, env.agentPriv)}
	broker := foraconnect.NewBrokerServiceClient(client, env.brokerURL, connect.WithProtoJSON())

	_, err := broker.ExecuteTransaction(t.Context(),
		connect.NewRequest(env.purchaseRequest(t, "tx-tampered", line{"offer-1", env.exchangeDom})))

	assertNothingSent(t, env.exchange)
	assertRequestAuthFailure(t, err, "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID")
}

// tamperSignature changes one character of the Signature header's value on the
// way out, after the signing transport above it signed.
type tamperSignature struct{ base http.RoundTripper }

func (t tamperSignature) RoundTrip(req *http.Request) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	sig := r2.Header.Get("Signature")
	if i := strings.Index(sig, ":") + 1; i > 0 && i < len(sig) {
		flipped := byte('A')
		if sig[i] == 'A' {
			flipped = 'B'
		}
		r2.Header.Set("Signature", sig[:i]+string(flipped)+sig[i+1:])
	}
	return t.base.RoundTrip(r2)
}

// TestPurchase_AReplayedSignatureIsRefused resends a purchase byte for byte,
// signature included. The first send passes the verify seam, which records its
// signature, and is then refused by admission because it names an Exchange
// nobody registered. That refusal is FAILED_PRECONDITION, so it can only come
// after the signature verified. The replay of the same bytes is refused as
// stale by the seam's replay store, before admission runs at all.
func TestPurchase_AReplayedSignatureIsRefused(t *testing.T) {
	env := newPurchaseTestEnv(t)
	rec := &recordRequest{base: http.DefaultTransport}
	client := &http.Client{Transport: brokerSigningTransport(rec, purchaseAgentDirectory, env.agentPriv)}
	broker := foraconnect.NewBrokerServiceClient(client, env.brokerURL, connect.WithProtoJSON())
	_, err := broker.ExecuteTransaction(t.Context(),
		connect.NewRequest(env.purchaseRequest(t, "tx-replayed", line{"offer-1", "nobody.example"})))
	if ce, ok := asConnectError(err); !ok || ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("first purchase answered %v, want the admission refusal that follows a verified signature", err)
	}

	replay, err := http.NewRequestWithContext(t.Context(), http.MethodPost, rec.url, bytes.NewReader(rec.body))
	if err != nil {
		t.Fatalf("build the replay: %v", err)
	}
	replay.Header = rec.header.Clone()
	resp, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatalf("send the replay: %v", err)
	}
	body := readAndCloseBody(t, resp)

	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "SIGNATURE_STALE") {
		t.Errorf("replay answered %d %s, want 401 with request_auth_failure SIGNATURE_STALE", resp.StatusCode, body)
	}
	assertNothingSent(t, env.exchange)
}

// recordRequest keeps the last request it carried, headers and body, so a test
// can resend it unchanged.
type recordRequest struct {
	base   http.RoundTripper
	url    string
	header http.Header
	body   []byte
}

func (r *recordRequest) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	_ = req.Body.Close()
	r.url, r.header, r.body = req.URL.String(), req.Header.Clone(), body
	r2 := req.Clone(req.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	return r.base.RoundTrip(r2)
}

// TestPurchase_AnOversizedBodyIsRefusedBeforeVerification pins the pre-auth read
// bound the mount sets. A body past it is refused before any signature is
// checked, so an unauthenticated caller cannot make the Broker buffer it.
func TestPurchase_AnOversizedBodyIsRefusedBeforeVerification(t *testing.T) {
	env := newPurchaseTestEnv(t)
	body := bytes.Repeat([]byte(" "), 65*1024)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		env.brokerURL+foraconnect.BrokerServiceExecuteTransactionProcedure, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Signature-Input", `sig1=("@method");created=1;keyid="k"`)
	req.Header.Set("Signature", "sig1=:AAAA:")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	_ = readAndCloseBody(t, resp)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
	assertNothingSent(t, env.exchange)
}

// TestPurchase_BehindATLSTerminatingProxyTheForwardedSchemeVerifies is the
// proxied topology: the agent signs the https URL, the proxy forwards plain HTTP
// with X-Forwarded-Proto, and with proxy trust wired the signature verifies.
//
// The purchase names an Exchange nobody registered, so a verified request is
// answered by admission with FAILED_PRECONDITION. That answer can only come
// after the signature verified, and nothing is sent to any Exchange. The
// directly exposed twin below gets UNAUTHENTICATED for the same request.
func TestPurchase_BehindATLSTerminatingProxyTheForwardedSchemeVerifies(t *testing.T) {
	env := newPurchaseTestEnvShaped(t, true)
	broker := proxiedAgentClient(t, env)

	_, err := broker.ExecuteTransaction(t.Context(),
		connect.NewRequest(env.purchaseRequest(t, "tx-proxied", line{"offer-1", "nobody.example"})))

	ce, ok := asConnectError(err)
	if !ok {
		t.Fatalf("proxied purchase answered %v, want the admission refusal", err)
	}
	assertNotAdmitted(t, ce, "nobody.example", "not a registered exchange")
	assertNothingSent(t, env.exchange)
}

// TestPurchase_ADirectlyExposedBrokerIgnoresAForwardedScheme is the other side:
// without proxy trust, a caller-supplied X-Forwarded-Proto must not pick the
// scheme its signature is verified against, so the https-signed request fails.
func TestPurchase_ADirectlyExposedBrokerIgnoresAForwardedScheme(t *testing.T) {
	env := newPurchaseTestEnv(t)
	broker := proxiedAgentClient(t, env)

	_, err := broker.ExecuteTransaction(t.Context(),
		connect.NewRequest(env.purchaseRequest(t, "tx-spoofed", line{"offer-1", "nobody.example"})))

	assertNothingSent(t, env.exchange)
	assertRequestAuthFailure(t, err, "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID")
}

// proxiedAgentClient signs over the https variant of the Broker's URL and sends
// the request downgraded to plain HTTP with X-Forwarded-Proto, the wire shape a
// TLS-terminating proxy forwards.
func proxiedAgentClient(t *testing.T, env purchaseTestEnv) foraconnect.BrokerServiceClient {
	t.Helper()
	client := &http.Client{Transport: brokerSigningTransport(
		testutil.TLSTerminatingProxyTransport{Base: http.DefaultTransport}, purchaseAgentDirectory, env.agentPriv)}
	return foraconnect.NewBrokerServiceClient(client, testutil.HTTPSVariant(t, env.brokerURL), connect.WithProtoJSON())
}

// TestPurchase_AnAbsoluteFormRequestLineIsRefused pins that a directly exposed
// Broker refuses an absolute-form request line outright, before any verify
// seam: that form lets a caller choose the scheme and host its signature is
// verified against without touching a header.
func TestPurchase_AnAbsoluteFormRequestLineIsRefused(t *testing.T) {
	env := newPurchaseTestEnv(t)
	body := []byte(`{}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		testutil.HTTPSVariant(t, env.brokerURL)+foraconnect.BrokerServiceExecuteTransactionProcedure,
		bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	signer, err := helpers.NewEd25519Signer(mustThumbprintPub(t, env), env.agentPriv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	now := time.Now().Unix()
	if err := helpers.SignRequest(t.Context(), req, body, signer, helpers.SignOptions{
		Created: now, Expires: now + 60, SignatureAgent: purchaseAgentDirectory,
	}); err != nil {
		t.Fatalf("sign: %v", err)
	}

	resp := testutil.SendAbsoluteForm(t, strings.TrimPrefix(env.brokerURL, "http://"), req, body)
	_ = readAndCloseBody(t, resp)

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	assertNothingSent(t, env.exchange)
}

// mustThumbprintPub is the agent key's RFC 7638 thumbprint, the keyid the seam
// resolves.
func mustThumbprintPub(t *testing.T, env purchaseTestEnv) string {
	t.Helper()
	thumb, err := helpers.Thumbprint(env.agentPub)
	if err != nil {
		t.Fatalf("thumbprint: %v", err)
	}
	return thumb
}
