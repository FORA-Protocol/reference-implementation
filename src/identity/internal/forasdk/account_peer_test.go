package forasdk_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	connectrpc "connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/forasdk"
)

// The two SDK properties the account legs rely on and do not set, driven against
// a real peer because neither can be observed from a hand-built error.
//
// WHICH LEG THESE TRAVERSE, stated plainly. Both drive Resolve, which dials the
// Broker's operator-configured origin. The account legs are NOT drivable against
// a loopback peer: they route off the recipient named in the request, and the
// SDK requires the advertised endpoint to sit on that recipient's own host AND
// its scheme's default port, which no test server can bind. Adding a seam to get
// past that would weaken the very rule it routed around, so these tests reach the
// shared code from the leg that can reach it.
//
// What they cover is exactly the shared code. NewClient and NewBrokerClient are
// both assembled by the SDK's plumbing(), so the response read cap and the
// interceptor chain are one construction, and a peer's error is classified by one
// function for every verb. What they do NOT cover is the account legs' own
// routing, which has its own tests.

// TestSDK_ClassifiesAPeersRefusalAsRefusedNotMalformed pins the assumption the
// local-versus-peer split rests on.
//
// The SDK's own pre-check refusal and a peer's refusal of the same payload arrive
// carrying the SAME typed detail: a RegistrationFailure naming the offending
// members. Only the SDK's transport class separates them, and fieldsRefused reads
// that class to decide which of the two happened, treating CallMalformed as "this
// service refused it locally, nothing was signed or sent".
//
// If a peer's refusal were classified CallMalformed, that decision inverts: the
// agent is told nothing was sent about a request that was signed and sent, and
// the operator log line is suppressed, because a LOCAL refusal names the
// operator's own business details and is deliberately kept out of the log. Every
// existing test stays green through that, because each one builds the CallError
// itself.
func TestSDK_ClassifiesAPeersRefusalAsRefusedNotMalformed(t *testing.T) {
	t.Parallel()
	peer := startBrokerPeer(t, refusingBroker{})

	_, err := brokerClientAgainst(t, peer.URL).Resolve(context.Background(), discoveryRequest())
	if err == nil {
		t.Fatal("the peer refused the call, so it must not have succeeded")
	}

	var callErr *connect.CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("err = %v carries no SDK call error, so the leg has no class to read", err)
	}
	if callErr.Kind == connect.CallMalformed {
		t.Errorf("the SDK classifies a peer's invalid_argument as %v, the class the account "+
			"leg reads as its OWN pre-check refusal -- the class alone can no longer tell a "+
			"local refusal from a remote one and that leg needs a stronger signal",
			callErr.Kind)
	}
	if callErr.Kind != connect.CallRefused {
		t.Errorf("a peer that answered was classified %v, want %v", callErr.Kind, connect.CallRefused)
	}
	// The typed detail survives the classification. It is what the tool layer
	// renders member names from, and an agent told only "invalid" cannot correct
	// the submission in one retry.
	if callErr.Detail.GetRegistrationFailure().GetFieldErrors() == nil {
		t.Error("the peer's typed detail did not survive the call, so the members it " +
			"named never reach the agent")
	}
}

// TestSDK_BoundsAResponseByItsOwnReadCap pins a bound this repository relies on
// and no longer sets.
//
// The outbound client the account legs replaced applied a 1 MiB read cap at
// construction, naming the threat: a hostile or misconfigured peer buffering an
// unbounded body into this process. The cap did not disappear with it -- the SDK
// applies its own default in the plumbing every one of its clients is built from
// -- but nothing here held that fact, so a later re-pin could drop it silently
// and no test in this tree would notice.
func TestSDK_BoundsAResponseByItsOwnReadCap(t *testing.T) {
	t.Parallel()
	if connect.DefaultMaxRPCReadBytes > 1<<20 {
		t.Errorf("the SDK's default response read cap is %d bytes, larger than the 1 MiB "+
			"bound the client this replaced applied", connect.DefaultMaxRPCReadBytes)
	}
	peer := startBrokerPeer(t, oversizedBroker{bytes: connect.DefaultMaxRPCReadBytes * 2})

	_, err := brokerClientAgainst(t, peer.URL).Resolve(context.Background(), discoveryRequest())
	if err == nil {
		t.Fatal("a response past the read cap was accepted, so nothing bounds what a peer " +
			"can buffer into this process")
	}
	var callErr *connect.CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("err = %v carries no SDK call error", err)
	}
	if callErr.Kind != connect.CallTooLarge {
		t.Errorf("a response past the read cap failed as %v, want %v -- the call failed for "+
			"some other reason, so this no longer proves the cap is applied",
			callErr.Kind, connect.CallTooLarge)
	}
}

func brokerClientAgainst(t *testing.T, brokerURL string) *forasdk.Client {
	t.Helper()
	cfg := completeConfig(t)
	cfg.BrokerURL = brokerURL
	client, err := forasdk.New(cfg)
	if err != nil {
		t.Fatalf("forasdk.New: %v", err)
	}
	return client
}

func discoveryRequest() *forav1.DiscoveryRequest {
	return &forav1.DiscoveryRequest{
		Ver: helpers.ProtocolVersion,
		Requester: &forav1.Requester{
			Id:     "https://agent.example",
			Domain: "agent.example",
			Type:   forav1.RequesterType_REQUESTER_TYPE_AGENT,
		},
		Uris: []string{"https://publisher.example/item"},
	}
}

// refusingBroker answers the way a conformant peer refusing a payload does:
// invalid_argument carrying the typed detail that names the offending members.
type refusingBroker struct {
	forav1connect.UnimplementedBrokerServiceHandler
}

func (refusingBroker) Resolve(
	context.Context, *connectrpc.Request[forav1.DiscoveryRequest],
) (*connectrpc.Response[forav1.DiscoveryResponse], error) {
	err := connectrpc.NewError(connectrpc.CodeInvalidArgument,
		errors.New("registration data does not conform"))
	detail, derr := connectrpc.NewErrorDetail(&forav1.ErrorDetail{
		Message: "registration data does not conform",
		Reason: &forav1.ErrorDetail_RegistrationFailure{
			RegistrationFailure: &forav1.RegistrationFailure{
				Reason: forav1.RegistrationFailureReason_REGISTRATION_FAILURE_REASON_INVALID_REGISTRATION_DATA,
				FieldErrors: []*forav1.RegistrationFieldError{
					{Path: "/vat_id", Error: "must match ^[A-Z]{2}[0-9]+$"},
				},
			},
		},
	})
	if derr != nil {
		return nil, derr
	}
	err.AddDetail(detail)
	return nil, err
}

// oversizedBroker answers successfully with a body past the read cap, standing
// for a peer that returns more than the caller agreed to buffer.
type oversizedBroker struct {
	forav1connect.UnimplementedBrokerServiceHandler
	bytes int
}

func (o oversizedBroker) Resolve(
	context.Context, *connectrpc.Request[forav1.DiscoveryRequest],
) (*connectrpc.Response[forav1.DiscoveryResponse], error) {
	return connectrpc.NewResponse(&forav1.DiscoveryResponse{
		Ver: helpers.ProtocolVersion,
		OfferGroups: []*forav1.OfferGroup{
			{Uri: "https://publisher.example/" + strings.Repeat("a", o.bytes)},
		},
	}), nil
}

func startBrokerPeer(t *testing.T, svc forav1connect.BrokerServiceHandler) *httptest.Server {
	t.Helper()
	path, handler := forav1connect.NewBrokerServiceHandler(svc,
		connectrpc.WithCodec(connectserver.EmitUnpopulatedJSONCodec()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
