// Package publicerr is the error the shared page-reading and terms packages
// raise: internal/pagefetch and internal/termspreset.
//
// Those packages serve two services, the onboarding preview and the publisher
// console, so they cannot raise either service's own error. Each service reads
// this one instead and decides for itself what status it answers with.
//
// The error keeps two texts apart. Public is written for the person who typed
// the input, and is the only text a page may show. Error() is for the log: it
// carries the kind and the cause, which name this service's own plumbing and are
// never shown to anyone.
package publicerr

import (
	"errors"
	"fmt"
)

// Kind classifies a failure. The zero value classifies nothing.
type Kind int

const (
	// KindUnspecified is the zero value and names no class.
	KindUnspecified Kind = iota
	// KindInvalidRequest is a fault in what the person gave: a price that is
	// not a decimal amount, a use the vocabulary has no token for, an address
	// on another site.
	KindInvalidRequest
	// KindUpstream means the publisher's own site could not be read: the
	// origin answered an error or a redirect, or sent more than the cap.
	KindUpstream
	// KindTimeout means the publisher's site did not answer in time.
	KindTimeout
	// KindInternal is a fault in this code, not in the input.
	KindInternal
)

// String names the kind for the log.
func (k Kind) String() string {
	switch k {
	case KindInvalidRequest:
		return "invalid_request"
	case KindUpstream:
		return "upstream"
	case KindTimeout:
		return "timeout"
	case KindInternal:
		return "internal"
	case KindUnspecified:
		return "unspecified"
	default:
		return "unspecified"
	}
}

// Error is the one error type the shared packages produce.
type Error struct {
	// Kind decides how a service answers.
	Kind Kind
	// Public is the sentence a person may be shown. It names what is wrong
	// and what to change, and never carries the text of a cause.
	Public string
	// Err is the cause, for the log. It never reaches a person.
	Err error
}

// Newf builds an error with a formatted public sentence and no cause.
func Newf(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Public: fmt.Sprintf(format, args...)}
}

// Wrap builds an error carrying a cause. The public sentence is what a person
// sees; the cause is what the log sees.
func Wrap(kind Kind, cause error, public string) *Error {
	return &Error{Kind: kind, Public: public, Err: cause}
}

// Error renders the error for the log.
func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Kind, e.Public)
	}
	return fmt.Sprintf("%s: %s: %v", e.Kind, e.Public, e.Err)
}

// Unwrap exposes the cause so a caller can test it with errors.Is.
func (e *Error) Unwrap() error { return e.Err }

// KindOf classifies any error. An error this package did not produce is
// internal, because nothing can be said about it to the person.
func KindOf(err error) Kind {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Kind
	}
	return KindInternal
}

// Message returns the sentence a person may be shown for err. It reports false
// for an error this package did not produce, whose own text must not be shown.
func Message(err error) (string, bool) {
	if e, ok := errors.AsType[*Error](err); ok && e.Public != "" {
		return e.Public, true
	}
	return "", false
}
