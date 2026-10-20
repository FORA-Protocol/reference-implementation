package resolve

import (
	"context"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/tagsearch"
)

// QuerySearcher resolves a free-text query and the request's search filters to
// catalog URIs, best first. It is wired only when query discovery is enabled
// in the Broker configuration (*tagsearch.Searcher satisfies it). An error
// means the search could not run.
type QuerySearcher interface {
	Search(ctx context.Context, query string, filters tagsearch.Filters) ([]string, error)
}

// requireCatalogSearch refuses search filters when query discovery is off. The
// search-provider fallback cannot apply them, and results that ignore the
// filters would answer a different question. The request itself is valid, so
// the refusal is Unimplemented rather than InvalidArgument.
func (h *Service) requireCatalogSearch(req Request) error {
	if h.deps.Query != nil || req.Filters.Empty() || len(requestURIs(req)) > 0 {
		return nil
	}
	return broker.Newf(broker.KindUnimplemented,
		"search_filters need the Broker's catalog search, which is not enabled").WithField("search_filters")
}

// searchQuery turns a request that names no URI into a URI request, when query
// discovery is enabled. The search uses the query and the search filters. The
// matched URIs then follow the same per-URL route as URIs the agent names:
// probing, manifest routing, offer verification and ranking. It runs before
// candidate-domain probing, so an enabled search never falls back to the search
// provider.
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
