//go:build integration

package transport_test

// ADR-019 §1 metadata-ride tests for the broker CONNECT sink (BrokerService/
// Resolve). Every broker input-validation reject must carry its offending field
// (or other machine-readable axis) as TYPED forav1.ErrorDetail.metadata read back
// through the public Connect error envelope — NEVER string-matched on the
// non-authoritative Message (the anti-pattern ADR-019 retired on the exchange
// side, mirrored here onto the broker).
//
// These tests live in a sibling file (not resolve_integration_test.go, which is
// near the 800-line test cap) so coverage is split by scenario, never trimmed
// (Testing Doctrine: split, never shrink).
//
// WHY they are RED on HEAD: broker.Error carries only Kind/Message/Err — no
// Metadata field — and resolveFaultError builds the ErrorDetail with Message +
// Domain only (broker_connect_handler.go), so GetMetadata() is empty for every
// fault. The metadata["field"] / metadata["required_one_of"] assertions therefore
// fail until broker.Error gains Metadata/WithField/WithMeta and the field-bearing
// call sites stamp it.
//
// Round-trip honesty: each leg is a genuine PROTOCOL round-trip — signed Connect
// client → real RFC 9421 signature → real httpsig middleware → registered Connect
// handler → validateResolveRequest / authorize / resolve core → the typed Connect
// transport error the client decodes. The Exchange address is a sink that fails
// the test if the Broker sends it anything, which pins the absence of upstream
// side effects without emulating an Exchange.

import (
	"context"
	"testing"

	connect "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// faultCallerID is the signer of every signedResolveFault call. A test whose
// fault depends on requester.id matching the signer names it for that field.
const faultCallerID = "agent-1"

// signedResolveFault spins up the signed Connect harness for faultCallerID (its
// key is registered in the resolver), issues the given DiscoveryRequest, and returns
// the transport error. It asserts the call DID fault (the negative-path tests
// expect a non-nil error). Mirrors resolveOverConnect's wiring but returns the
// error rather than fatal-ing on it.
func signedResolveFault(t *testing.T, fx *fixture, req *forav1.DiscoveryRequest) error {
	t.Helper()
	pub, priv := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)
	client := signingBrokerClient(srv.base, srv.server.URL, faultCallerID, priv)
	_, err := client.Resolve(context.Background(), connect.NewRequest(req))
	if err == nil {
		t.Fatal("expected a fault, got nil error")
	}
	return err
}

// TestResolve_RequesterIDMissing_CarriesFieldMetadata pins the empty-id refusal.
// At the pinned protocol Requester.id is a wire rule (1 to 255 characters), so
// the validate interceptor refuses an empty requester.id as InvalidArgument with
// a violation on requester.id under string.min_len, before any handler guard or
// the caller derivation runs. The request is signed with a valid registered key
// but names no requester, and nothing is sent on.
func TestResolve_RequesterIDMissing_CarriesFieldMetadata(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	// requester.id deliberately empty; sign with a valid registered key.
	req := &forav1.DiscoveryRequest{
		Ver:       helpers.ProtocolVersion,
		Requester: &forav1.Requester{Domain: requesterDomain, Type: forav1.RequesterType_REQUESTER_TYPE_AGENT},
		Query:     ptr("FORA intro"),
	}
	err := signedResolveFault(t, fx, req)

	assertConnectCodeLocal(t, err, connect.CodeInvalidArgument)
	assertRequesterIDViolation(t, err)
	assertNothingSent(t, fx.wire)
}

// assertRequesterIDViolation requires the wire tier's violation of the
// Requester.id length rule, read from the refusal's Violations detail.
func assertRequesterIDViolation(t *testing.T, err error) {
	t.Helper()
	for _, v := range testutil.ValidationViolations(t, err) {
		if testutil.ViolationFieldPath(v) == "requester.id" && v.GetRuleId() == "string.min_len" {
			return
		}
	}
	t.Fatalf("no wire violation on requester.id under string.min_len (err=%v)", err)
}

// TestResolve_QueryAndURIBothEmpty_CarriesRequiredOneOfMetadata pins the
// search-input guard: a request with no query, no uri and no search filter is
// rejected InvalidArgument carrying
// metadata["required_one_of"]=="query,uri,search_filters" (the settled
// composite key — there is no single offending field, so no "field" key; ADR-019
// §1 metadata is a free map of axes). requester.id == the signing key so the
// query/uri guard (before authorize) is the cause, not impersonation.
func TestResolve_QueryAndURIBothEmpty_CarriesRequiredOneOfMetadata(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	req := &forav1.DiscoveryRequest{
		Ver: helpers.ProtocolVersion,
		Requester: &forav1.Requester{
			Id: faultCallerID, Domain: requesterDomain, Type: forav1.RequesterType_REQUESTER_TYPE_AGENT,
		},
		// no Query, no Uris
	}
	err := signedResolveFault(t, fx, req)

	assertConnectCodeLocal(t, err, connect.CodeInvalidArgument)
	assertBrokerErrorField(t, err, "required_one_of", "query,uri,search_filters")
	assertNothingSent(t, fx.wire)
}

// TestResolve_UnparseableURI_CarriesFieldMetadata pins resolve.go:198
// (urisToDomains → exa.DomainOf): a caller-supplied uri that url.Parse cannot
// parse fails the WHOLE resolve InvalidArgument with metadata["field"]=="uri".
// "http://[::1" is a deterministic url.Parse failure ("missing ']' in host").
// The request clears the agent_id + query/uri guards and authorize (id == signer)
// so the uri-parse fault is the cause.
func TestResolve_UnparseableURI_CarriesFieldMetadata(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	req := &forav1.DiscoveryRequest{
		Ver: helpers.ProtocolVersion,
		Requester: &forav1.Requester{
			Id: faultCallerID, Domain: requesterDomain, Type: forav1.RequesterType_REQUESTER_TYPE_AGENT,
		},
		Uris: []string{"http://[::1"},
	}
	err := signedResolveFault(t, fx, req)

	assertConnectCodeLocal(t, err, connect.CodeInvalidArgument)
	assertBrokerErrorField(t, err, "field", "uri")
	assertNothingSent(t, fx.wire)
}

// TestResolve_BareFault_CarriesNoMetadata is the negative control for the
// metadata ride: a fault with no single offending field (here an UNSIGNED Resolve
// → Unauthenticated, the same path TestResolve_FaultCarriesErrorDetail drives)
// must carry a typed ErrorDetail with Domain but EMPTY metadata. This proves the
// ride is conditional (only field-bearing rejects stamp metadata) rather than a
// blanket stamp. It already passes for the Domain leg on HEAD; the metadata-empty
// leg stays true after the fix (Unauthenticated is left bare), so this guards
// against a future over-eager stamp.
func TestResolve_BareFault_CarriesNoMetadata(t *testing.T) {
	ctx := context.Background()
	fx := newSinkFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	const callerID = "agent-1"
	pub, _ := newBrokerKeyPair(t)
	srv := startBrokerConnectServer(t, fx, pub)

	// PLAIN unsigned client — no httpsig context → Unauthenticated, a field-less
	// fault.
	client := foraconnect.NewBrokerServiceClient(srv.server.Client(), srv.server.URL, connect.WithGRPC())
	_, err := client.Resolve(ctx, connect.NewRequest(buildDiscoveryRequest(callerID, reqOpts{query: "FORA intro"})))

	assertConnectCodeLocal(t, err, connect.CodeUnauthenticated)
	assertBrokerErrorMetadataAbsent(t, err)
}

// ptr returns a pointer to the given string (for optional proto string fields).
func ptr(s string) *string { return &s }
