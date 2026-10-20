package exchangeclient

import (
	"encoding/json"
	"fmt"
	"slices"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/consoleapi"
)

// Error is what a failed call carries back: the status the Exchange answered,
// the kind of refusal it named, the message it gave, and the cause for the log.
//
// The message is written for a publisher when the kind is a caller fault,
// because the Exchange writes it that way -- "site X is registered to another
// publisher", "cdn Y is not one of the supported integrations". The console
// shows it rather than translating it: a second wording of the same refusal is
// a second thing to keep in step, and the one closer to the decision is the one
// that stays right. For any other kind the message is not shown.
//
// Err never reaches a page. It holds the transport failure or the decode
// failure, which name the console's own plumbing.
type Error struct {
	Status int
	// Kind is the token the Exchange classified the refusal with, one of the
	// consoleapi.ErrorKind values. Empty when no answer came back or the answer
	// was not the Exchange's.
	Kind    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("exchange %d %s: %s: %v", e.Status, e.Kind, e.Message, e.Err)
	}
	return fmt.Sprintf("exchange %d %s: %s", e.Status, e.Kind, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Unreachable reports whether the call never got an answer. It is the one
// failure a page should invite the person to retry, because the state on the
// other side is unknown rather than decided.
func (e *Error) Unreachable() bool { return e.Status == 0 }

// CallerFault reports whether the Exchange refused something about the request
// itself: a malformed domain, an unsupported integration, a site that belongs
// to somebody else. The page shows the message. Anything else is ours to fix,
// and the page says so without repeating an internal message to the visitor.
//
// It is decided from the kind the Exchange named, not from the HTTP status: a
// status says how to retry, and several kinds share one status.
func (e *Error) CallerFault() bool {
	return slices.Contains(consoleapi.CallerFaultKinds(), e.Kind)
}

// errorFrom builds the error for a non-200 answer.
//
// A body that does not decode is not itself reported to the caller: it means
// something other than the Exchange answered -- a proxy, a load balancer -- and
// echoing its HTML into a publisher-facing page would say nothing useful and
// could carry markup. The status is kept, because that is what distinguishes a
// refusal from an outage, and the kind is left empty, so nothing that is not
// the Exchange's own answer can be read as a caller fault.
func errorFrom(status int, raw []byte) *Error {
	var body consoleapi.ErrorBody
	if err := json.Unmarshal(raw, &body); err != nil || body.Error == "" {
		return &Error{Status: status, Message: "the Exchange refused the request"}
	}
	return &Error{Status: status, Kind: body.Kind, Message: body.Error}
}
