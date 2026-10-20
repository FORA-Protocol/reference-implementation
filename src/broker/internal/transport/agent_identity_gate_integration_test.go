//go:build integration

package transport_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// The Broker derives the agent identity for Resolve from the verified
// Signature-Agent directory alone, reduced to a canonical host. requester.id is a
// free wire label: it is never compared with the caller, never used as the budget
// or audit key, and is forwarded to the Exchange exactly as the agent sent it.
//
// Every case is a signed Connect round-trip — signing client → RFC 9421 httpsig
// middleware → registered Resolve handler → validateResolveRequest →
// authenticatedAgentID → resolve core — observed through that same Resolve RPC
// and the mock Exchange's call counters and captured requester.id. The one
// exception, the selection_log read, is marked where it happens.
//
// The budget half — one counter per verified agent, whatever label or directory
// spelling it sends — is how this Broker computes a budget. The pinned protocol
// leaves that computation to the Broker, so no e2e obligation grades it.

// TestResolve_AcceptsAnyRequesterLabel covers the labels the old self-action gate
// refused: a bare token, a URN, a host that is not the caller, and a value that
// does not parse as a host at all. Each one must reach the Exchange unchanged.
//
// Needs an e2e test: a free-label requester.id on Resolve, read back unchanged at a real Exchange.
func TestResolve_AcceptsAnyRequesterLabel(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	const host = "label-agent.example"
	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client := signingBrokerClient(srv.base, srv.server.URL, "https://"+host, priv)

	labels := []string{"parse-probe", "urn:parse:probe", "unrelated.example", "//" + host}
	for i, label := range labels {
		resp, err := client.Resolve(ctx, connect.NewRequest(
			buildDiscoveryRequest(label, reqOpts{query: "FORA intro"})))
		if err != nil {
			t.Fatalf("requester.id %q was refused: %v", label, err)
		}
		if len(resp.Msg.GetOfferGroups()) == 0 {
			t.Fatalf("requester.id %q: no offer_groups (absence_reason=%v)",
				label, resp.Msg.GetAbsenceReason())
		}
		if srv.exchange.discoverCalls != i+1 {
			t.Fatalf("requester.id %q: discover calls = %d, want %d",
				label, srv.exchange.discoverCalls, i+1)
		}
		if got := srv.exchange.lastDiscoverRequesterID; got != label {
			t.Errorf("forwarded requester.id = %q, want the agent's label %q unchanged", got, label)
		}
	}
}

// TestResolve_CallerDirectoryThatIsNotAnHTTPSOriginIsUnauthenticated: a
// Signature-Agent member names the signer's key directory, and the profile
// admits it only as an https origin. A caller whose member is the plaintext
// http origin of its host, or no origin at all, has not established who it is,
// so the BrokerService gate refuses it as an authentication failure, answers
// with the Accept-Signature value a signature here must follow, and nothing
// upstream fires. The member is rewritten after an otherwise valid signature,
// because no SDK signer will put such a member on the wire.
func TestResolve_CallerDirectoryThatIsNotAnHTTPSOriginIsUnauthenticated(t *testing.T) {
	ctx := context.Background()
	const host = "dict-form.example"
	// Every refusal here is one the caller can fix by signing again as the
	// profile requires, so each names what a signature here must follow: a
	// Signature-Agent the profile cannot parse, and a well-formed member whose
	// value is not an https origin, as the protocol's shared verify vectors pin.
	for name, member := range map[string]string{
		"http origin":   `sig1="http://` + host + `"`,
		"bare host":     `sig1="` + host + `"`,
		"dictionary of": `sig1="agent2=https://` + host + `"`,
		"v1.0.8 token":  "https://" + host,
	} {
		t.Run(name, func(t *testing.T) {
			fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})
			pub, priv := newBrokerKeyPair(t)
			srv := startBrokerConnectServer(t, fx, pub)
			signing := brokerSigningTransport(memberRewrite{next: srv.base, member: member}, "https://"+host, priv)
			client := foraconnect.NewBrokerServiceClient(&http.Client{Transport: signing},
				srv.server.URL, connect.WithProtoJSON())

			_, err := client.Resolve(ctx, connect.NewRequest(
				buildDiscoveryRequest(host, reqOpts{query: "FORA intro"})))
			assertConnectCodeLocal(t, err, connect.CodeUnauthenticated)
			var cerr *connect.Error
			if !errors.As(err, &cerr) {
				t.Fatalf("refusal %v is not a Connect error", err)
			}
			if got, want := cerr.Meta().Get(helpers.AcceptSignatureHeader), helpers.AcceptSignature(false); got != want {
				t.Errorf("refusal carries %s = %q, want %q", helpers.AcceptSignatureHeader, got, want)
			}
			assertNothingSent(t, fx.wire)
		})
	}
}

// memberRewrite sits under the signing transport and replaces the
// Signature-Agent header of the request it has just signed, standing in for a
// signer that names a member the profile refuses.
type memberRewrite struct {
	next   http.RoundTripper
	member string
}

func (m memberRewrite) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(helpers.SignatureAgentHeader, m.member)
	return m.next.RoundTrip(req)
}

// TestResolve_SelectionLogRecordsVerifiedAgent pins the audit half of the
// accounting identity: the selection_log row names the verified caller, not the
// requester.id label.
//
// Needs an e2e test: the selection audit of a real-Exchange resolve; it first needs a public read surface.
func TestResolve_SelectionLogRecordsVerifiedAgent(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	const host = "audited-agent.example"
	const reqID = "req-selection-agent-identity-0001"
	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client := signingBrokerClient(srv.base, srv.server.URL, "https://"+host, priv)

	req := connect.NewRequest(buildDiscoveryRequest("parse-probe", reqOpts{query: "FORA intro"}))
	req.Header().Set("X-Request-ID", reqID)
	if _, err := client.Resolve(ctx, req); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Tier-2 read through the production repository: the Broker has no public
	// read surface for its selection audit (the same gap
	// TestResolve_RecordsSelectionAudit records).
	entries, err := repo.NewSelectionLogRepo(fx.pool).ByRequestID(ctx, reqID)
	if err != nil {
		t.Fatalf("ByRequestID: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("selection_log entries = %d, want 1", len(entries))
	}
	if got := entries[0].AgentID; got != host {
		t.Errorf("selection_log agent_id = %q, want the verified agent %q", got, host)
	}
}
