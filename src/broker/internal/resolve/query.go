package resolve

import (
	"context"
	"errors"
	"maps"
	"slices"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/catalogsearch"
)

// admitSearch decides, before any search or probe runs, whether the request's
// search inputs can be answered, in the order the fora-music-v1 profile
// publishes. Explicit URIs win, so a request naming any is admitted without its
// search_filters being read. Otherwise the configured vocabulary parses the
// filters, and a refused key stops the request. A key the vocabulary ignores, as
// fora-music-v1 ignores a key it does not define, is no filter from here on, so
// it is never refused for want of a search. Then a request with no query, no URI
// and no filter is refused; then filters with no catalog search to apply them
// are refused.
//
// It runs in the service rather than in transport because the vocabulary is a
// dependency of the configured provider, and mapping a provider's FilterError
// to a Broker error is a domain decision.
func (h *Service) admitSearch(req *Request) error {
	if len(requestURIs(*req)) > 0 {
		return nil
	}
	f, err := h.deps.Vocabulary.Parse(req.RawFilters)
	if err != nil {
		return filterRefusal(err)
	}
	req.Filters = f
	if req.Query == "" && f.Empty() {
		// No single offending field — record the composite requirement so a client
		// learns which one-of set was unsatisfied (ADR-019 §1 free-form metadata).
		return broker.Newf(broker.KindInvalidArgument, "either query, uri or a search filter is required").
			WithMeta("required_one_of", "query,uri,search_filters")
	}
	if !f.Empty() && h.deps.Query == nil {
		// Results that ignore the parsed filters would answer a different question,
		// which rules out the search-provider fallback. A key Parse ignored is not
		// among them, so a query beside only such keys takes that fallback, as the
		// profile's order of checks says. The request itself is valid, so the
		// refusal is Unimplemented rather than InvalidArgument.
		return broker.Newf(broker.KindUnimplemented,
			"search_filters need a catalog search that applies them; this Broker's catalog search does not").
			WithField("search_filters")
	}
	return nil
}

// filterRefusal maps a vocabulary's refusal to the Broker error the agent gets.
// A key the vocabulary does not apply is Unimplemented, because the request is
// valid; a malformed value is InvalidArgument. Either names the key's field, or
// search_filters itself when the whole object was refused. Any other error
// breaks the Vocabulary contract.
func filterRefusal(err error) error {
	var ferr *catalogsearch.FilterError
	if !errors.As(err, &ferr) {
		return broker.Wrapf(broker.KindInternal, err, "parse search_filters")
	}
	kind := broker.KindInvalidArgument
	if ferr.Unsupported {
		kind = broker.KindUnimplemented
	}
	field := "search_filters"
	if ferr.Key != "" {
		field += "." + ferr.Key
	}
	return broker.Newf(kind, "%s", ferr).WithField(field)
}

// unsupportedVocabulary is the fail-closed default for Deps.Vocabulary: until a
// vocabulary is wired, every search_filters key is refused as one this Broker
// does not apply. A wiring omission therefore surfaces as a loud refusal,
// never as filters silently dropped.
type unsupportedVocabulary struct{}

func (unsupportedVocabulary) Parse(raw map[string]any) (catalogsearch.Filters, error) {
	if len(raw) == 0 {
		return noFilters{}, nil
	}
	return nil, &catalogsearch.FilterError{
		Key:         slices.Min(slices.Collect(maps.Keys(raw))),
		Unsupported: true,
		Reason:      "this Broker applies no search filter",
	}
}

// noFilters is the parsed value of an absent or empty search_filters object.
type noFilters struct{}

func (noFilters) Empty() bool { return true }

// searchQuery turns a request that names no URI into a URI request, when a
// catalog search is configured. The search gets the query and the filters
// admitSearch parsed. The matched URIs then follow the same per-URL route as
// URIs the agent names: probing, manifest routing, offer verification and
// ranking. It runs before candidate-domain probing, so an enabled search never
// falls back to the search provider.
//
// It returns a request-level refusal when nothing can be resolved: no match is
// NOT_IN_CATALOG, and a failed search is TEMPORARILY_UNAVAILABLE. Both carry
// no groups.
func (h *Service) searchQuery(ctx context.Context, req *Request) *Response {
	if h.deps.Query == nil || len(requestURIs(*req)) > 0 {
		return nil
	}
	uris, err := h.deps.Query.Search(ctx, req.Query, req.Filters)
	if err != nil {
		reqctx.FromContext(ctx).WarnContext(ctx, "broker.resolve.query_search_failed", "err", err)
		return &Response{AbsenceReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE}
	}
	if len(uris) == 0 {
		return &Response{AbsenceReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG}
	}
	req.URIs = uris
	req.searched = true
	return nil
}
