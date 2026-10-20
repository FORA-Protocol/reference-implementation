package service

// The ingest tier of the license-term rules: the checks the SDK owns, run over
// one pushed entry's canonicalized terms. Split from catalog.go because the push
// flow there calls them but does not define them, and because the SDK is the
// authority on what a term may say.

import (
	"context"
	"errors"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// validateEntryTerms runs the SDK's ingest-tier checks
// (helpers.ValidateLicenseTerm) over every term on the entry. It returns either
// the accumulated lint warnings — each RuleWarning.Message verbatim, in term
// order and, within a term, in the SDK's order, the strings that become
// PushResourcesResponse.warnings[] — or, on the first hard violation, the
// RejectionReasonInvalidTerms rejection that drops the whole entry, carrying
// the SDK's message as its Detail. The terms were canonicalized in place by
// helpers.NormalizeResourceEntry at the top of classifyEntry, so the checks see
// canonical tokens.
//
// The per-term face is deliberate: the Exchange never calls
// helpers.ValidateResourceEntry, for three reasons. That face re-runs
// protovalidate over the whole entry, which the validate interceptor already
// did once per request. It normalizes a proto.Clone and returns a verdict,
// while the Exchange must persist canonical tokens and so needs the in-place
// face. And it reports every violation, for a publisher fixing a feed, whereas
// the Exchange stops at the first hard violation per entry (all-or-nothing),
// which is ValidateLicenseTerm's contract.
//
// A refusal is logged at Warn with the entry URI and the violation's rule,
// path and token, so an operator can name the offending value without the
// publisher's feed. The path is made entry-relative ("terms[i].<path>"), the
// form the SDK's entry verdict reports.
//
// This is the SECOND line a term refusal produces, and it is deliberate. Every
// refused entry, whatever refused it, gets one line from logRejections carrying
// the URI and the machine-readable reason. That vocabulary has no place for a
// rule id, a field path or a token, and those three are what an operator names
// the bad value from — so the gate that holds them writes them here, one level
// deeper, rather than widening the shape every refusal shares.
func validateEntryTerms(
	ctx context.Context, uri string, e *forav1.ResourceEntry,
) ([]string, *CatalogPushRejection) {
	var warnings []string
	for i, term := range e.GetTerms() {
		termWarnings, err := helpers.ValidateLicenseTerm(term)
		if err != nil {
			violation := asRuleViolation(err)
			reqctx.FromContext(ctx).WarnContext(
				ctx, "license term refused at ingest",
				"uri", uri,
				"rule", violation.Rule,
				"path", fmt.Sprintf("terms[%d].%s", i, violation.Path),
				"token", violation.Token,
			)
			rej := reject(uri, RejectionReasonInvalidTerms)
			rej.Detail = violation.Message
			return nil, rej
		}
		for _, w := range termWarnings {
			warnings = append(warnings, w.Message)
		}
	}
	return warnings, nil
}

// asRuleViolation unwraps the error helpers.ValidateLicenseTerm returns into the
// *helpers.RuleViolation it documents. Any other error type is still a refusal
// — the entry must not persist — carried with its message alone.
func asRuleViolation(err error) *helpers.RuleViolation {
	violation, ok := errors.AsType[*helpers.RuleViolation](err)
	if ok {
		return violation
	}
	return &helpers.RuleViolation{Message: err.Error()}
}
