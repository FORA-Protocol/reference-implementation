// Package purchase is the Broker's purchase service: the business half of
// BrokerService.ExecuteTransaction. An agent sends the Broker one
// TransactionRequest for offers issued by any number of Exchanges, signed for
// the Broker. The transport has already verified that request signature; this
// package decides everything after it.
//
// The order is fixed, and every refusal comes before anything is sent:
//
//  1. requester.domain must be the host of the directory the request was signed
//     from, because every Exchange resolves the agent's acceptance keys from it;
//  2. the request must be well formed: every item has an offer and an
//     AgentAcceptance, and the AgentRequestAcceptance lists exactly the
//     request's items, in order, for the request's requester and key;
//  3. every Exchange the items name must be one this Broker routes to and
//     approves for purchases.
//
// Then the items are grouped by their signed offer.exchange, and each group goes
// to its Exchange as a NEW ExchangeService.ExecuteTransaction the Broker signs
// with its own key. The agent's acceptances, requester and idempotency key
// travel unchanged, so each Exchange still verifies the agent's consent, and a
// retry is answered from each Exchange's stored result.
//
// The answers are combined into one BrokerTransactionResponse. Once anything has
// been sent the call succeeds: an Exchange that refuses its whole sub-request,
// or does not answer, is reported on each of its items as an UpstreamRefusal,
// and the other Exchanges' results come back unchanged.
package purchase

import (
	"context"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// Registry reads the Broker's Exchange registry row for a domain. The row holds
// both answers admission needs: whether the operator approves the Exchange for
// purchases, and whether its last health probe found it routable.
// repo.ExchangeRepo satisfies it.
type Registry interface {
	GetByDomain(ctx context.Context, domain string) (repo.Exchange, error)
}

// EndpointResolver resolves an Exchange domain to the ExchangeService origin the
// Exchange advertises in its own /.well-known/fora.json. The registry never
// supplies the endpoint: it is a trust allowlist, and the address always comes
// from the Exchange itself.
type EndpointResolver interface {
	ResolveEndpoint(ctx context.Context, host string) (string, error)
}

// ExchangeCaller sends one Broker-signed ExecuteTransaction to an Exchange
// endpoint. A refusal comes back as the Connect error the Exchange answered
// with, possibly wrapped. xclient.Pool satisfies it.
type ExchangeCaller interface {
	ExecuteTransaction(
		ctx context.Context, endpoint string, req *forav1.TransactionRequest,
	) (*forav1.TransactionResponse, error)
}

// Service buys offers across Exchanges for one verified agent per call.
type Service struct {
	registry  Registry
	endpoints EndpointResolver
	exchanges ExchangeCaller
}

// New wires the purchase service.
func New(registry Registry, endpoints EndpointResolver, exchanges ExchangeCaller) *Service {
	return &Service{registry: registry, endpoints: endpoints, exchanges: exchanges}
}

// Execute runs one purchase for the agent whose request signature verified
// against the directory callerDirectory names. A returned error is the Broker's
// own refusal, a *broker.Error, and means nothing was sent. A response means the
// sub-requests were sent, whatever each Exchange answered.
func (s *Service) Execute(
	ctx context.Context, callerDirectory string, req *forav1.TransactionRequest,
) (*forav1.BrokerTransactionResponse, error) {
	if err := bindRequester(req.GetRequester().GetDomain(), callerDirectory); err != nil {
		audit(ctx, outcomeRejectedAuthz, "", err)
		return nil, err
	}
	if err := validateRequest(req); err != nil {
		audit(ctx, outcomeRejectedInvalid, "", err)
		return nil, err
	}
	groups := groupByExchange(req.GetItems())
	for _, g := range groups {
		if err := s.admit(ctx, g); err != nil {
			return nil, err
		}
	}
	answers := s.send(ctx, req, groups)
	resp := combine(ctx, req.GetItems(), groups, answers)
	audit(ctx, outcomeValidated, "", nil, "exchanges", len(groups))
	return resp, nil
}
