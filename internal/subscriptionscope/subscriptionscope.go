// Package subscriptionscope is the one definition of the 'subscription:' scope
// marker that the Exchange, the Broker and the RSL mapping all read. A licensing
// term carrying a scope with this prefix is subscription-gated: the Exchange
// presents and sells it only through a verified delegation chain, and the Broker
// lets such an item travel without an AgentAcceptance so the Exchange can deny
// it per item. Both services must agree on the marker byte for byte: a Broker
// that forwards an item the Exchange's envelope check does not recognise as
// gated turns one item's denial into a refusal of the whole relayed batch. The
// pinned protocol module exports no constant for it yet; this package is the
// marker's home until it does.
package subscriptionscope

import (
	"errors"
	"fmt"
	"strings"
)

// Prefix marks a licensing-term scope as subscription-gated.
const Prefix = "subscription:"

// MaxLen bounds one subscription scope. The protocol bounds the scopes list (64
// entries) but not one entry; this is the storage bound of the subscription
// record that owns the scope.
const MaxLen = 256

// ErrInvalid is the sentinel every Validate failure wraps.
var ErrInvalid = errors.New("invalid subscription scope")

// Is reports whether s names a subscription scope, valid or not.
func Is(s string) bool { return strings.HasPrefix(s, Prefix) }

// Validate checks the concrete-scope rule every subscription scope must satisfy,
// on a record, in a pushed term, and at term selection: the string starts with
// the prefix; at least one segment follows it; segments are ':'-separated and
// none is empty; '*' appears nowhere; every byte is visible ASCII (0x21-0x7E);
// the whole string fits MaxLen. Comparison against a record is exact, so nothing
// here trims or folds case. The error wraps ErrInvalid and names the clause.
func Validate(s string) error {
	if !Is(s) {
		return fmt.Errorf("%w: %q must start with %q", ErrInvalid, s, Prefix)
	}
	if len(s) > MaxLen {
		return fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, MaxLen)
	}
	rest := s[len(Prefix):]
	if rest == "" {
		return fmt.Errorf("%w: %q names no subscription", ErrInvalid, s)
	}
	for _, seg := range strings.Split(rest, ":") {
		if seg == "" {
			return fmt.Errorf("%w: %q has an empty segment", ErrInvalid, s)
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '*':
			return fmt.Errorf("%w: %q must be concrete: no '*'", ErrInvalid, s)
		case c < 0x21 || c > 0x7E:
			return fmt.Errorf("%w: %q has a byte outside visible ASCII at %d", ErrInvalid, s, i)
		}
	}
	return nil
}
