package resolve

import (
	"context"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/probe"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/selection"
)

// discover.go — the S2 route-per-URL discovery dispatch. Each requested URL is
// routed ONLY to the exchange its publisher's /.well-known/fora.json names
// (buildRoutePlan, routing.go); each routed exchange is queried with ONLY its
// URL subset; the per-URL OfferGroups returned are CONCATENATED (no
// cross-exchange merge). The EXA-query path keeps the legacy broadcast.

// discover routes the resolve to the right discovery strategy and returns the
// per-URL OfferGroups (response order). The URL path (route-per-URL) routes
// each URL ONLY to the exchange(s) its publisher manifest names and concatenates
// the per-URL groups each exchange returns. It serves URLs the agent named and
// URLs the Broker's own catalog search chose (searchQuery). The EXA-query path
// (no URLs, catalog search not enabled) keeps the legacy broadcast (relay the
// free-text query to every manifest-named exchange). A non-nil *Response is a
// request-level refusal the caller returns verbatim (no healthy exchange / no
// route).
func (h *Service) discover(
	ctx context.Context, req Request, manifests map[string]probe.Manifest,
) ([]OfferGroup, discoverFlags, *Response) {
	if uris := requestURIs(req); len(uris) > 0 {
		method := forav1.DiscoveryMethod_DISCOVERY_METHOD_EXCHANGE
		if req.searched {
			method = forav1.DiscoveryMethod_DISCOVERY_METHOD_SEARCH
		}
		groups, flags := h.discoverByRoute(ctx, req, uris, manifests)
		return stampMethod(groups, method), flags, nil
	}
	exchanges, hasHealthy := h.exchangesFor(ctx, manifests)
	if !hasHealthy {
		return nil, discoverFlags{}, noHealthyExchangeResponse()
	}
	groups, flags := h.broadcastQuery(ctx, req, exchanges)
	return stampMethod(groups, forav1.DiscoveryMethod_DISCOVERY_METHOD_SEARCH), flags, nil
}

// stampMethod records how the Broker found these URIs, on every group the
// strategy produced. This is the single place the value is decided for the
// response BrokerService/Resolve returns, and it sits here because discover is
// the only function that knows which strategy ran.
//
// The Broker is also the only component that CAN know. It either received the
// URIs from the agent or chose them itself; the exchange it then queries is
// handed a URI list and cannot tell those apart. URIs the agent named are
// EXCHANGE, including a synthesised absence group for a named URI that no
// exchange would serve. URIs the Broker chose are SEARCH, both from the search
// provider's broadcast and from the Broker's own catalog search, because in
// either case the Broker searched on the agent's behalf.
//
// A Broker-side discovery source that is neither — syndication,
// recommendation — adds a branch to discover and nothing else. The exchange
// never changes.
//
// Resolve is the Broker's only discovery surface. An agent that queries an
// Exchange directly reads the method the Exchange stated, so the same field
// means two things by design: on Resolve the Broker vouches for it, and on a
// direct query the Exchange does.
func stampMethod(groups []OfferGroup, method forav1.DiscoveryMethod) []OfferGroup {
	for i := range groups {
		groups[i].DiscoveryMethod = method
	}
	return groups
}

// discoverByRoute implements the route-per-URL model: build the
// publisher-manifest-derived plan (URL -> manifest-named exchange), query each
// exchange with ONLY its routed URL subset, and CONCATENATE the per-URL groups
// each exchange returns in request order. Unroutable URLs (no
// registered+healthy exchange named) surface as typed-absence groups, so a batch
// where no URL routes anywhere still yields a per-URL absence response rather
// than a bare refusal. It never produces a request-level refusal of its own —
// an unroutable batch is still a per-URL answer — so unlike discover it returns
// no *Response. A failed exchange query is logged and counted toward
// allUpstreamFailed, never returned.
func (h *Service) discoverByRoute(
	ctx context.Context, req Request, uris []string, manifests map[string]probe.Manifest,
) ([]OfferGroup, discoverFlags) {
	plan, scan, anyRouted := h.buildRoutePlan(ctx, uris, manifests)
	flags := discoverFlags{namedExchangeDown: scan.sawTransientDecline}
	byURL := make(map[string]OfferGroup, len(uris))
	failures := 0
	for _, domain := range plan.exchangeOrder {
		ex := plan.exchanges[domain]
		resp, err := h.queryExchange(ctx, req, ex, plan.exchangeURLs[domain])
		if err != nil {
			reqctx.FromContext(ctx).WarnContext(ctx, "broker.discover",
				"exchange", ex.Domain, "err", err)
			failures++
			continue
		}
		h.collectGroups(ctx, byURL, exchangeRefOf(ex), resp, &flags)
	}
	flags.allUpstreamFailed = anyRouted && failures > 0 && failures == len(plan.exchangeOrder)
	return plan.assemble(ctx, byURL), flags
}

// broadcastQuery is the EXA-query discovery path. The agent named no URL, so
// candidateDomains has already asked EXA which publisher domains to try; this
// queries every exchange those domains name and concatenates the groups they
// return. There is no per-URL routing because there are no URLs.
//
// The free-text query does NOT reach the exchanges. ResourceQuery carries no
// query field at all, so buildResourceQuery sends one with an empty uris list,
// and an Exchange that requires at least one uri rejects it — against a
// compliant Exchange this path returns no groups today, and a batch with no
// groups answers TEMPORARILY_UNAVAILABLE. Only a test double answers a
// uri-less query. The path is kept wired, and stamped SEARCH by discover,
// because the Broker searched. When catalog search is enabled, searchQuery
// turns the query into URIs first and this path is not reached.
func (h *Service) broadcastQuery(
	ctx context.Context, req Request, exchanges []repo.Exchange,
) ([]OfferGroup, discoverFlags) {
	flags := discoverFlags{}
	byURL := make(map[string]OfferGroup)
	var order []string
	failures := 0
	for _, ex := range exchanges {
		resp, err := h.queryExchange(ctx, req, ex, nil)
		if err != nil {
			reqctx.FromContext(ctx).WarnContext(ctx, "broker.discover",
				"exchange", ex.Domain, "err", err)
			failures++
			continue
		}
		order = appendGroupOrder(order, byURL, resp)
		h.collectGroups(ctx, byURL, exchangeRefOf(ex), resp, &flags)
	}
	flags.allUpstreamFailed = failures > 0 && failures == len(exchanges)
	return groupsInOrder(ctx, order, byURL), flags
}

// queryExchange issues one DiscoverResources call to a single
// exchange, scoped to the supplied URL subset (nil for the broadcast-query
// path). The route-per-URL caller passes ONLY the URLs routed to this exchange.
func (h *Service) queryExchange(
	ctx context.Context, req Request, ex repo.Exchange, uris []string,
) (*forav1.ResourceResponse, error) {
	// The registry row's domain becomes the recipient this leg names, so it is
	// held to the shape the wire admits before it is stamped — the same check
	// every other sender in this repository runs on a value bound for the
	// recipient field. A row holding something else is a registration fault, and
	// saying so here is the difference between an operator reading it and an
	// exchange that silently returns nothing: the far end would refuse the leg
	// as malformed, and that refusal names the Broker's registry nowhere.
	if !helpers.IsBareDomain(ex.Domain) {
		return nil, fmt.Errorf(
			"registered exchange %q is not a bare domain, so no request can be addressed to it",
			ex.Domain)
	}
	rq := buildResourceQuery(ctx, req, h.deps.Clk, ex.Domain, uris)
	return h.deps.Exchange.DiscoverResources(ctx, ex.Endpoint, rq)
}

// collectGroups folds one exchange's per-URL OfferGroups into byURL: each group
// is keyed on its requested URL and carries the offers tagged with the
// originating exchange (the exchange already emits one group per URL with offers
// or a NOT_IN_CATALOG absence_reason). Every offer is sorted through the SDK
// Verifier FIRST (Core Invariant): only verified offers become
// candidates; a doctored/unverifiable offer is logged and dropped before it can
// reach selection. No cross-exchange merge — under route-per-URL each URL is
// routed to exactly one exchange, so a URL key is seen once; multiple offers for
// one URL are Dedup+Ranked within the group at assembly. flags accumulate the
// upstream signals for the all-empty fallback.
func (h *Service) collectGroups(
	ctx context.Context, byURL map[string]OfferGroup, ref selection.ExchangeRef,
	resp *forav1.ResourceResponse, flags *discoverFlags,
) {
	for _, group := range resp.GetOfferGroups() {
		uri := group.GetUri()
		acc := byURL[uri]
		acc.URI = uri
		sorted := h.deps.Verifier.Sort(ctx, group.GetOffers())
		for _, rej := range sorted.Rejected {
			reqctx.FromContext(ctx).WarnContext(ctx, "broker.discover.offer_rejected",
				"exchange", ref.Domain, "uri", uri,
				"offer_id", rej.Offer.GetOfferId(), "reason", rej.Reason)
		}
		for _, offer := range sorted.Verified {
			acc.cands = append(acc.cands, selection.Candidate{Offer: offer, Exchange: ref})
		}
		if reason := group.GetAbsenceReason(); reason != forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_UNSPECIFIED {
			acc.AbsenceReason = reason
			flags.upstreamReason = reason
		}
		byURL[uri] = acc
	}
}

// appendGroupOrder records first-seen URL order for the broadcast-query path,
// which has no request-order to seed from.
func appendGroupOrder(order []string, byURL map[string]OfferGroup, resp *forav1.ResourceResponse) []string {
	for _, group := range resp.GetOfferGroups() {
		uri := group.GetUri()
		if _, seen := byURL[uri]; !seen {
			order = append(order, uri)
		}
	}
	return order
}

// groupsInOrder finalises the broadcast-query groups (Dedup+Rank within each).
func groupsInOrder(ctx context.Context, order []string, byURL map[string]OfferGroup) []OfferGroup {
	out := make([]OfferGroup, 0, len(order))
	for _, uri := range order {
		out = append(out, byURL[uri].finalize(ctx))
	}
	return out
}

func exchangeRefOf(ex repo.Exchange) selection.ExchangeRef {
	return selection.ExchangeRef{
		Domain:   ex.Domain,
		Endpoint: ex.Endpoint,
		Trust:    ex.TrustLevel,
		Priority: ex.Priority,
	}
}
