// Package transportconnect adapts verify-seam rejections to Connect-over-HTTP
// responses and the service audit vocabulary. It has two kinds of user. The
// Exchange's own signed surfaces (the catalog capture and the console surface,
// which verify through the SDK's pure face rather than its connectserver mount)
// use it to classify an over-cap body and write the refusal. The reject logger
// in internal/reqctx (NewRejectLogger) uses its audit outcome, and the
// Exchange's and the Broker's SDK mounts log every refused request through that
// logger.
//
// It does NOT own how a refusal is written. The SDK's server binding exports
// that — connectserver.RejectCode, IsBodyTooLarge and WriteReject — and every
// function here that answers a protocol-shaped question defers to it. What
// stays is the part the SDK cannot know: the audit token for a body past the
// read cap, which the SDK's reject classifier has no value for.
package transportconnect

import (
	"net/http"

	connect "connectrpc.com/connect"
	"github.com/FORA-Protocol/protocol/sdk/go/connectserver"
)

// RejectCode maps a rejection at the verify seam to the Connect code returned to
// the client. It is the SDK's judgement (connectserver.RejectCode), the over-cap
// body included, so the Exchange's own surfaces and the SDK's mounts answer
// alike.
func RejectCode(err error) connect.Code {
	return connectserver.RejectCode(err)
}

// IsBodyTooLarge reports whether err is net/http's over-cap signal from a read
// bounded by http.MaxBytesReader.
//
// It is the SDK's predicate under this package's name, because the branch it
// decides has callers here: a middleware that must tell, before it can answer,
// whether the read it just failed was a size refusal or a malformed request.
// Those two answer differently — one is a resource limit this package writes,
// the other a bad request the caller writes itself — so the predicate the
// branch turns on has to be the one the writer turns on.
func IsBodyTooLarge(err error) bool {
	return connectserver.IsBodyTooLarge(err)
}

// WriteError emits a Connect-compatible error response so Connect clients see a
// proper code (and matching HTTP status) instead of a raw status. The body and
// the code→status split are the SDK's: a body past the read cap is 413 (send
// less), a hop budget is 429 (send fewer, or slower), and anything else is 401.
// The 413 is a deliberate departure from Connect's own table, which is why one
// place decides it.
//
// The Exchange's catalog capture calls it for the over-cap read. It answers the
// two resource verdicts and the authentication refusal only — a caller holding
// any other, such as a malformed read, writes that response itself.
func WriteError(w http.ResponseWriter, err error) {
	connectserver.WriteReject(w, RejectCode(err), err)
}

// OutcomeBodyTooLarge is the audit token for a request refused because its body
// passed the read cap, in the lowercase vocabulary the reject loggers emit
// beside the SDK's own signature / replay / broken_chain / hop_budget.
//
// The SDK's RejectReason has no value for it. That enum names the four
// authentication outcomes an RFC 9421 gate produces and defaults anything else
// to signature, which is the right default for an error the gate cannot place —
// but an over-cap body is not an authentication outcome at all, and the SDK's
// own response side says so, answering it 413 resource_exhausted rather than
// 401. Auditing it as a signature failure would send an operator after a key
// rotation for a caller that sent too much, which is the same misreport the
// response side was changed to stop making.
const OutcomeBodyTooLarge = "body_too_large"

// RejectAuditOutcome is the audit token for a verify-gate rejection, in the
// vocabulary the reject loggers emit. The four authentication outcomes come
// from the SDK's own classifier — the gate owns that judgement and no copy of
// it is kept here — and the one case the SDK's enum cannot express, a body past
// the read cap, is named rather than folded into the default.
func RejectAuditOutcome(err error) string {
	if IsBodyTooLarge(err) {
		return OutcomeBodyTooLarge
	}
	return connectserver.ClassifyReject(err).String()
}
