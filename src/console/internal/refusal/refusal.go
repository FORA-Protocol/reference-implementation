// Package refusal is the console's own refusals: what the console declines to
// do on its own account, with a sentence written for the publisher. The wizard
// and the dashboard both refuse through it, and the web package answers every
// refusal from one mapping, on a page and in the wizard's JSON alike.
//
// A failure the Exchange reported is not one of these. It arrives as the
// Exchange client's own error, which already says whether the Exchange was
// unreachable or refused the caller, and a second classification here would
// be one more mapping to keep in step with it. The one exception is the
// Exchange's not_found on a dashboard read, which every page answers the same
// way and so is classified as KindNotFound.
package refusal

// Kind classifies a refusal.
type Kind int

const (
	// KindInvalid means something the person gave cannot be used: an answer
	// in the wizard, or a date, a period or a status in a page's address.
	KindInvalid Kind = iota + 1
	// KindNotRegistered means a step that needs the publisher's payee was
	// reached before the business name was registered.
	KindNotRegistered
	// KindNotFound means what was asked about is not this person's, or does
	// not exist. The two are answered alike, so the answer says nothing about
	// anybody else's.
	KindNotFound
	// KindBuildFailed means the setup files could not be produced.
	KindBuildFailed
)

// Error is a refusal. Message is written for the publisher and is safe to show
// them; Err, when set, is the cause, and is for the log.
type Error struct {
	Kind    Kind
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// New is a refusal of kind, saying message.
func New(kind Kind, message string) *Error {
	return &Error{Kind: kind, Message: message}
}

// Wrap is New with the cause that led to it, for the log.
func Wrap(kind Kind, message string, cause error) *Error {
	return &Error{Kind: kind, Message: message, Err: cause}
}
