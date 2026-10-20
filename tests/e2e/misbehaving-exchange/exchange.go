package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/connect"
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/fora/v1/forav1connect"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

const (
	// offerRate and offerCurrency are what every offer the impostor issues costs.
	offerRate     = "1.25"
	offerCurrency = "USD"
	// offerLifetime is how long an issued offer stays valid.
	offerLifetime = 15 * time.Minute
	// stallFor is how long modeStall holds a sub-request. The Broker's relay
	// client gives up after 10 seconds, so this is comfortably past it.
	stallFor = 20 * time.Second
)

// impostor answers ExchangeService for the identities it serves.
type impostor struct {
	forav1connect.UnimplementedExchangeServiceHandler

	key        ed25519.PrivateKey
	clk        clock.Clock
	book       *faultBook
	identities map[string]bool
}

// DiscoverResources answers every requested URI with one offer, issued by the
// identity the query is addressed to and signed with the impostor's key.
func (x *impostor) DiscoverResources(
	_ context.Context, req *connect.Request[forav1.ResourceQuery],
) (*connect.Response[forav1.ResourceResponse], error) {
	exchange := req.Msg.GetExchange()
	if !x.identities[exchange] {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("misbehaving-exchange: does not answer as %q", exchange))
	}
	groups := make([]*forav1.OfferGroup, 0, len(req.Msg.GetUris()))
	for _, uri := range req.Msg.GetUris() {
		offer, err := x.offer(exchange, uri)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		groups = append(groups, &forav1.OfferGroup{Uri: uri, Offers: []*forav1.Offer{offer}})
	}
	return connect.NewResponse(&forav1.ResourceResponse{
		Ver:         helpers.ProtocolVersion,
		Exchange:    exchange,
		OfferGroups: groups,
	}), nil
}

// offer is one flat-priced offer for uri, signed by the SDK's offer signer.
func (x *impostor) offer(exchange, uri string) (*forav1.Offer, error) {
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("offer id: %w", err)
	}
	offer := &forav1.Offer{
		OfferId: "offer-" + hex.EncodeToString(nonce),
		Title:   proto.String("Fault fixture item"),
		Pricing: &forav1.Pricing{
			Model:    forav1.PricingModel_PRICING_MODEL_FLAT,
			Rate:     offerRate,
			Currency: offerCurrency,
		},
		DeliveryMethod: forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS,
		ExpiresAt:      timestamppb.New(x.clk.Now().Add(offerLifetime)),
		Identity: &forav1.ResourceIdentity{
			CanonicalUrl:       proto.String(uri),
			ResourceMutability: forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC,
		},
		Exchange: exchange,
		// One term: the agent agrees to it by accepting, and nothing here ever
		// checks a use against it.
		Terms: []*forav1.LicenseTerm{{Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED}},
	}
	signature, err := helpers.SignOffer(x.key, offer)
	if err != nil {
		return nil, fmt.Errorf("sign offer: %w", err)
	}
	offer.Signature = signature
	offer.SignatureAlgorithm = helpers.OfferSignatureAlgorithm
	return offer, nil
}

// ExecuteTransaction records the sub-request and answers it in the fault mode
// set for its requester.
func (x *impostor) ExecuteTransaction(
	ctx context.Context, req *connect.Request[forav1.TransactionRequest],
) (*connect.Response[forav1.TransactionResponse], error) {
	sub := req.Msg
	requester := sub.GetRequester().GetDomain()
	x.book.record(requester, arrivalOf(sub))
	f := x.book.faultFor(requester)
	switch f.mode {
	case modeRefuse:
		return nil, f.refusal()
	case modeDrop:
		// net/http closes the connection without writing a response when a
		// handler panics with ErrAbortHandler, and logs nothing. The Broker's
		// call fails at the transport, as against an Exchange that went away.
		panic(http.ErrAbortHandler)
	case modeStall:
		select {
		case <-ctx.Done():
			return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		case <-x.clk.After(stallFor):
		}
	case modeHonest, modeFewer, modeMore:
	}
	answer := x.answer(sub)
	switch f.mode {
	case modeFewer:
		answer.Items = answer.Items[:len(answer.Items)-1]
	case modeMore:
		extra := proto.CloneOf(answer.Items[len(answer.Items)-1])
		extra.OfferId = "offer-nobody-sent"
		answer.Items = append(answer.Items, extra)
	case modeHonest, modeRefuse, modeDrop, modeStall:
	}
	return connect.NewResponse(answer), nil
}

// arrivalOf is what the harness reads back about one sub-request.
func arrivalOf(sub *forav1.TransactionRequest) arrival {
	a := arrival{IdempotencyKey: sub.GetIdempotencyKey(), OfferIDs: []string{}}
	for _, item := range sub.GetItems() {
		a.Exchange = item.GetOffer().GetExchange()
		a.OfferIDs = append(a.OfferIDs, item.GetOffer().GetOfferId())
	}
	return a
}

// answer buys every item sent: one result item per item, in the order sent.
// Each id is derived from the requester, the idempotency key and the offer, so
// a repeated sub-request is answered with the same transaction, the way an
// Exchange answers a replay from its stored result.
func (x *impostor) answer(sub *forav1.TransactionRequest) *forav1.TransactionResponse {
	items := make([]*forav1.TransactionResultItem, 0, len(sub.GetItems()))
	for _, item := range sub.GetItems() {
		offer := item.GetOffer()
		transaction := stableID("tx", sub, offer.GetOfferId())
		items = append(items, &forav1.TransactionResultItem{
			OfferId:       offer.GetOfferId(),
			TransactionId: transaction,
			BillingId:     stableID("bill", sub, offer.GetOfferId()),
			Cost: &forav1.Cost{
				Amount:   offer.GetPricing().GetRate(),
				Currency: offer.GetPricing().GetCurrency(),
			},
			DeliveryMethod:    offer.GetDeliveryMethod(),
			ExpiresAt:         timestamppb.New(x.clk.Now().Add(offerLifetime)),
			RetrievalEndpoint: proto.String("http://" + offer.GetExchange() + "/delivery/" + transaction),
		})
	}
	return &forav1.TransactionResponse{Ver: helpers.ProtocolVersion, Items: items}
}

// stableID derives an id from the requester, the idempotency key and the offer.
func stableID(prefix string, sub *forav1.TransactionRequest, offerID string) string {
	sum := sha256.Sum256([]byte(sub.GetRequester().GetDomain() + "\n" + sub.GetIdempotencyKey() + "\n" + offerID))
	return prefix + "-" + hex.EncodeToString(sum[:12])
}
