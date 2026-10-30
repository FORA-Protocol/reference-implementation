package exchange

import (
	"errors"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/subscriptionscope"
)

// TestValidateSubscriptionScope pins each clause of the concrete-scope rule with
// one string that breaks only that clause. A parser is a permitted unit-test
// subject.
func TestValidateSubscriptionScope(t *testing.T) {
	ok := []string{"subscription:publisher.example:premium", "subscription:a", "subscription:a:b:c:d"}
	for _, s := range ok {
		if err := ValidateSubscriptionScope(s); err != nil {
			t.Errorf("%q: unexpected %v", s, err)
		}
	}
	bad := []string{
		"publisher:news:read", // wrong prefix
		"subscription:",       // no segment after the prefix
		"subscription::x",     // empty leading segment
		"subscription:x:",     // empty trailing segment
		"subscription:a::b",   // doubled ':'
		"subscription:a:*",    // wildcard segment
		"subscription:a*b",    // '*' inside a segment
		"subscription: a",     // whitespace
		"subscription:a\tb",   // control character
		"subscription:ä",      // non-ASCII
		" subscription:a",     // never trimmed
		"Subscription:a",      // case-sensitive prefix
		"subscription:" + string(make([]byte, subscriptionscope.MaxLen)), // too long
	}
	for _, s := range bad {
		err := ValidateSubscriptionScope(s)
		if err == nil {
			t.Errorf("%q: accepted, want rejection", s)
			continue
		}
		var de *Error
		if !errors.As(err, &de) || de.Kind != KindInvalidRequest {
			t.Errorf("%q: %v, want KindInvalidRequest", s, err)
		}
	}
}

func TestSubscriptionScopeOf(t *testing.T) {
	if s, gated, err := SubscriptionScopeOf([]string{"publisher:news:read"}); s != "" || gated || err != nil {
		t.Errorf("non-gated term: %q %v %v", s, gated, err)
	}
	if s, gated, err := SubscriptionScopeOf([]string{"region:EU", "subscription:p:x"}); s != "subscription:p:x" || !gated || err != nil {
		t.Errorf("gated term: %q %v %v", s, gated, err)
	}
	if _, gated, err := SubscriptionScopeOf([]string{"subscription:p:x", "subscription:p:y"}); !gated || err == nil {
		t.Errorf("two subscription scopes: gated=%v err=%v, want gated and refused", gated, err)
	}
	if _, gated, err := SubscriptionScopeOf([]string{"subscription:p:*"}); !gated || err == nil {
		t.Errorf("wildcard subscription scope: gated=%v err=%v, want gated and refused", gated, err)
	}
}
