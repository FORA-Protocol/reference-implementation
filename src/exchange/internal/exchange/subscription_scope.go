package exchange

import (
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/subscriptionscope"
)

// ValidateSubscriptionScope is the shared concrete-scope rule
// (subscriptionscope.Validate) as a KindInvalidRequest, the shape catalog
// ingest reports against the field.
func ValidateSubscriptionScope(s string) error {
	if err := subscriptionscope.Validate(s); err != nil {
		return Newf(KindInvalidRequest, "%s", err.Error())
	}
	return nil
}

// SubscriptionScopeOf returns the one subscription scope of a term's scopes and
// true, or "" and false when the term is not gated. A term with more than one
// subscription scope, or one that fails ValidateSubscriptionScope, is refused:
// the error names the fault so catalog ingest can report it against the field,
// and term selection hides the term for every requester.
func SubscriptionScopeOf(scopes []string) (string, bool, error) {
	found := ""
	for _, s := range scopes {
		if !subscriptionscope.Is(s) {
			continue
		}
		if found != "" {
			return "", true, Newf(KindInvalidRequest, "a term carries at most one subscription scope; found %q and %q", found, s)
		}
		if err := ValidateSubscriptionScope(s); err != nil {
			return "", true, err
		}
		found = s
	}
	return found, found != "", nil
}
