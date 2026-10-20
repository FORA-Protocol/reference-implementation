package transport

import (
	"errors"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
)

// brokerDetail builds the ErrorDetail envelope of every broker fault but one:
// the non-authoritative developer Message, the stable Domain, and any structured
// field metadata a broker.Error recorded via WithField/WithMeta. The envelope
// body itself is the shared SDK build half (connectserver.NewErrorDetail) — the
// canonical ADR-019 §1 shape: the offending field identity travels
// machine-readably, never only in the message string. These faults are the
// generic transport class, so no reason oneof is set — Domain + Message +
// optional Metadata only. The Connect sink attaches this via the shared SDK
// connectserver.AttachDetail. The one exception is a purchase whose requester
// is not the signer: executeFaultError builds that detail with the SDK's
// request_auth_failure helper (reason SIGNATURE_INVALID) and copies the
// error's metadata onto it.
func brokerDetail(err error) *forav1.ErrorDetail {
	var meta map[string]string
	var be *broker.Error
	if errors.As(err, &be) {
		meta = be.Metadata
	}
	return connectserver.NewErrorDetail(brokerServiceDomain, err.Error(), meta)
}
