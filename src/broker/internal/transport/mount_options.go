package transport

import (
	"net/http"

	foraconnect "github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	sdkconnect "github.com/FORA-Protocol/protocol/sdk/go/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraaudience"
)

// BrokerMountOptions returns the ServerOption set the BrokerService mount runs
// with. Production and the integration harness both call it, so the mount those
// tests drive is the mount that ships.
//
// Written out twice instead, the two lists came apart: the harness never wired
// the recipient check that production mounts, while its own comment said it
// wired the production set "no more, no less". Nothing failed, because no
// agent-facing BrokerService request names a recipient today — which is exactly
// why the gap could sit there, and exactly why the day an RPC gains one is the
// wrong day to discover it.
//
// audience is required rather than optional, matching the Exchange's mount
// options and the refusal buildBrokerMux already made: a nil interceptor mounts
// cleanly and checks nothing.
func BrokerMountOptions(
	resolver helpers.KeyResolver,
	replayStore core.ReplayStore,
	audience *foraaudience.Interceptor,
) ([]connectserver.ServerOption, error) {
	if err := foraaudience.Require(audience, "broker mount options"); err != nil {
		return nil, err
	}
	// MaxSignatures is deliberately unset (0 = unbounded): the hop bound is an
	// Exchange-terminal policy, and bounding it at the Broker too would
	// double-count the relay hop the Broker is about to add.
	return []connectserver.ServerOption{
		connectserver.WithKeyResolver(resolver),
		connectserver.WithReplayStore(replayStore),
		connectserver.WithValidation(sdkconnect.ValidationStrict),
		// Zero-valued response fields stay on the wire (platform JSON contract).
		connectserver.WithEmitUnpopulated(),
		// The same recipient check the Exchange mounts, from the one shared
		// definition. Nothing this interceptor sees names the Broker today:
		// DiscoveryRequest carries no recipient field, and the items of a
		// TransactionRequest sent to ExecuteTransaction name the Exchanges the
		// Broker routes to, which the interceptor knows not to compare. It is
		// mounted so an agent-facing RPC that gains a recipient later is guarded
		// the day it does rather than the day somebody remembers.
		connectserver.WithInterceptors(audience),
		// A purchase always clears the seam, signed or not, so an unsigned
		// ExecuteTransaction is refused the way an Exchange refuses one:
		// UNAUTHENTICATED with request_auth_failure SIGNATURE_MISSING.
		//
		// Any other /fora. request clears the seam iff it PRESENTS a signature:
		// an unsigned Resolve reaches the handler, which finds no verified
		// context and returns the typed Unauthenticated fault (ADR-019 error
		// detail) — the no-caller negative path the contract tests drive.
		connectserver.WithVerifyGate(func(r *http.Request) bool {
			return r.URL.Path == foraconnect.BrokerServiceExecuteTransactionProcedure ||
				r.Header.Get("Signature-Input") != ""
		}),
		// The pre-auth read bound every agent-facing body gets. The seam
		// buffers a signed body to check its Content-Digest before anything
		// verifies it, so without this cap an unauthenticated caller could make
		// the Broker hold the SDK's default 4 MiB per request.
		connectserver.WithMaxRequestBytes(maxAgentBodyBytes),
		// Audit-log every gate rejection with its outcome: the four the SDK's
		// own classifier names (replay / broken_chain / hop_budget / signature)
		// plus body_too_large, which its enum has no value for: a body past the
		// cap set above, refused before verification.
		//
		// The cap bounds every request on this mount (the SDK applies it outside
		// the verify seam), but this observer runs inside the seam, so it records
		// only what the gate above sends through it: every ExecuteTransaction,
		// signed or not, and any other request that presents a signature. An
		// unsigned Resolve past the cap is refused by the same bound and audited
		// by nothing; the runbook records that gap rather than the code pretending
		// it is not there.
		connectserver.WithOnReject(LogHTTPSigReject),
	}, nil
}
