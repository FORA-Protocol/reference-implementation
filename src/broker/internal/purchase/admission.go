package purchase

import (
	"context"
	"errors"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/broker"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// group is one Exchange's share of a purchase: the Exchange, the endpoint it
// advertises once admission has resolved it, and the positions of its items in
// the request, in request order.
type group struct {
	exchange string
	endpoint string
	indexes  []int
}

// groupByExchange groups the items by their signed offer.exchange, in the order
// each Exchange first appears among the items, keeping request order inside each
// group.
//
// Two spellings of one Exchange — a different letter case, or an explicit :443 —
// land in one group, by the recipient-identity rule helpers.CheckAudience owns.
// That is the rule each Exchange applies when it checks its sub-request against
// the agent's request acceptance, so splitting one Exchange's items across two
// sub-requests would have each of them refused there. The group is named by the
// first spelling; a value that is not a bare domain never matches anything and
// gets a group of its own, which admission then refuses.
func groupByExchange(items []*forav1.TransactionItem) []*group {
	var groups []*group
	for i, item := range items {
		exchange := item.GetOffer().GetExchange()
		if g := findGroup(groups, exchange); g != nil {
			g.indexes = append(g.indexes, i)
			continue
		}
		groups = append(groups, &group{exchange: exchange, indexes: []int{i}})
	}
	return groups
}

// findGroup returns the group whose Exchange exchange names, or nil.
func findGroup(groups []*group, exchange string) *group {
	for _, g := range groups {
		if g.exchange == exchange {
			return g
		}
		verdict, err := helpers.CheckAudience(g.exchange, exchange)
		if err == nil && verdict == helpers.AudienceAccepted {
			return g
		}
	}
	return nil
}

// admit decides whether the Broker may send this group's Exchange a purchase at
// all, and then resolves where that Exchange is. Every refusal is
// FAILED_PRECONDITION naming the Exchange under metadata["exchange"], and the
// caller runs admit for every group before it sends anything, so one Exchange
// the Broker cannot use refuses the whole purchase rather than buying the rest.
//
// The order matters. Every verdict the registry row can give is read before the
// Broker fetches anything from the Exchange: a withdrawn Exchange gets no
// traffic at all, its manifest fetch included.
//
// A down Exchange is refused here as well, although its state may clear on its
// own. The protocol gives a Broker's routing refusals one code, and an agent
// that retries later gets the answer the next health probe supports.
func (s *Service) admit(ctx context.Context, g *group) error {
	if !helpers.IsBareDomain(g.exchange) {
		return refuseExchange(ctx, g.exchange, "is not a bare domain the Broker can route to")
	}
	ex, err := s.registry.GetByDomain(ctx, g.exchange)
	if errors.Is(err, repo.ErrNotFound) {
		return refuseExchange(ctx, g.exchange, "is not a registered exchange")
	}
	if err != nil {
		// The Broker's own fault, so it is not recorded against the Exchange.
		rerr := broker.Wrapf(broker.KindInternal, err, "exchange registry")
		audit(ctx, outcomeRejectedInternal, g.exchange, rerr)
		return rerr
	}
	if reason := refusalReason(ex); reason != "" {
		return refuseExchange(ctx, g.exchange, reason)
	}
	endpoint, err := s.endpoints.ResolveEndpoint(ctx, g.exchange)
	if err != nil {
		return refuseExchange(ctx, g.exchange, "advertises no endpoint the Broker can use: "+err.Error())
	}
	// Canonicalized so one Exchange keys one pooled client whether or not its
	// manifest advertises a trailing slash. The generated Connect client trims
	// the base URL itself, so the slash never reaches the wire or the signature.
	g.endpoint = repo.CanonicalEndpoint(endpoint)
	return nil
}

// refusalReason is why the registry row refuses its Exchange for purchases, or
// "" when it admits it.
//
// A blocked row reads as unregistered: both are settled refusals, and telling
// them apart would report whether an operator deliberately withdrew trust. The
// trust level is checked separately from routability, because an Exchange the
// Broker only discovered is live and may quote prices, which costs nothing, but
// may not be paid until an operator approves it. Otherwise anyone who can edit a
// publisher's fora.json could put themselves in the payment path.
func refusalReason(ex repo.Exchange) string {
	switch ex.Admission() {
	case repo.AdmissionLive:
	case repo.AdmissionDown:
		return "is registered but its last health check failed"
	case repo.AdmissionRefused:
		return "is registered but its manifest was refused at the last probe"
	default:
		return "is not a registered exchange"
	}
	if ex.TrustLevel != repo.TrustLevelVerified && ex.TrustLevel != repo.TrustLevelPreferred {
		return "is registered but not approved for purchases"
	}
	return ""
}

// refuseExchange records and returns the refusal of one Exchange.
func refuseExchange(ctx context.Context, exchange, reason string) error {
	err := broker.Newf(broker.KindExchangeNotAdmitted, "offer.exchange %q %s", exchange, reason).
		WithMeta("exchange", exchange)
	audit(ctx, outcomeRejectedExchange, exchange, err)
	return err
}
