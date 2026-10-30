// Subscription-gated terms: the delegation verification both the discovery
// path (selectTerms) and the execute path run before a term carrying a
// 'subscription:' scope is presented or sold.
//
// The trust anchor is the subscription record for (tenant, scope): its Ed25519
// root keys are the only issuers a chain may be rooted in. The chain itself is
// the holder-bound JWT profile the pinned protocol module defines and
// helpers.VerifyDelegation checks: every signature, the attenuation of scopes,
// audience and expiry down the chain, and the terminal cnf.jkt binding to the
// key the request proved. Nothing in the plaintext Delegation mirror fields
// (scopes, expires_at, issuer, principal_domain, caps) is read for trust; when
// they disagree with the signed chain, the chain decides.
//
// Who the holder is differs by path. On discovery the holder is the key that
// signed the request itself, so only a DIRECT request (one signature, not the
// configured Broker's) can prove it; a Broker-originated query hides gated
// terms. On execute the holder is the key the item's AgentAcceptance verified
// under, on direct and relayed purchases alike, never the Broker's transport key.

package service

import (
	"context"
	"errors"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// verifiedSubscription is the evidence a verified chain leaves behind: the
// record it was verified against and the SDK's verdict. ExpiresAt is the chain's
// earliest exp as signed, without the verifier's clock leeway, so a caller that
// bounds an offer's lifetime by it never extends past what the issuer signed.
type verifiedSubscription struct {
	RecordID string
	helpers.VerifiedDelegation
}

// verifySubscription verifies the delegation token against the record that owns
// scope in tenantID, for a holder whose key thumbprint is holder. It returns the
// evidence on success, or a KindDelegationInvalid error whose message names the
// failure and the record for the journal. The token is the raw
// Delegation.token; an empty one is a failure like any other. An empty holder
// means the request proved no key the chain could be bound to.
func (s *ExchangeService) verifySubscription(
	ctx context.Context, tenantID, scope string, token []byte, holder string,
) (*verifiedSubscription, *exchange.Error) {
	if s.subscriptions == nil {
		return nil, exchange.Newf(exchange.KindDelegationInvalid, "no subscription records wired")
	}
	trust, err := s.subscriptions.TrustedIssuersFor(ctx, tenantID, scope)
	if err != nil {
		return nil, exchange.Wrap(exchange.KindInternal, err, "read subscription record")
	}
	if trust == nil {
		return nil, exchange.Newf(exchange.KindDelegationInvalid, "no subscription record for scope %q", scope)
	}
	if holder == "" {
		return nil, exchange.Newf(exchange.KindDelegationInvalid,
			"record %s: request proved no holder key", trust.RecordID)
	}
	vd, err := helpers.VerifyDelegation(helpers.DelegationInput{
		Token:          token,
		TrustedIssuers: trust.Issuers,
		Audience:       s.cfg.Exchange,
		Holder:         holder,
		Now:            s.clk.Now(),
	})
	if err != nil {
		var de *helpers.DelegationError
		failure := "invalid"
		if errors.As(err, &de) {
			failure = string(de.Failure)
		}
		return nil, exchange.Wrap(exchange.KindDelegationInvalid, err,
			fmt.Sprintf("record %s: delegation %s", trust.RecordID, failure)).
			WithMeta("delegation_failure", failure).WithMeta("subscription_record", trust.RecordID)
	}
	return &verifiedSubscription{RecordID: trust.RecordID, VerifiedDelegation: *vd}, nil
}

// discoveryGate is what one DiscoverResources call can prove about its
// requester for gated terms. It verifies once per (tenant, scope) and remembers
// the verdict for the rest of the request, so a query naming many URIs of the
// same subscription reads the record and checks the chain one time.
type discoveryGate struct {
	s      *ExchangeService
	ctx    context.Context
	caller Caller
	token  []byte
	// holder is the thumbprint of the key that signed the request, or "" when
	// the request is not direct (unsigned, or Broker-originated) and no gated
	// term can be presented.
	holder   string
	verdicts map[string]*verifiedSubscription
}

// newDiscoveryGate reads the verified transport identity off ctx. Discovery
// does not resolve registrations, billing refs or relayed requester domains:
// the only thing it needs is the key that signed the request, and only when
// exactly one party signed and that party is not the configured Broker.
func (s *ExchangeService) newDiscoveryGate(ctx context.Context, requester *forav1.Requester) *discoveryGate {
	g := &discoveryGate{
		s: s, ctx: ctx, token: requester.GetDelegation().GetToken(), verdicts: map[string]*verifiedSubscription{},
	}
	v := helpers.FromContext(ctx)
	if v == nil || v.KeyID == "" {
		return g
	}
	g.caller = Caller{KeyID: v.KeyID, Kind: CallerAgent, PublicKey: v.PublicKey}
	if relay := s.classifyRequestTransport(ctx, v); relay.hasBroker {
		g.caller.Kind = CallerBroker
		return g
	}
	if thumb, err := helpers.Thumbprint(v.PublicKey); err == nil {
		g.holder = thumb
	}
	return g
}

// verified returns the evidence for the gated term's subscription scope, or nil
// when the term must be hidden. A refusal is journaled once per (tenant, scope)
// with the failure name and record; the response never carries it.
func (g *discoveryGate) verified(tenantID, scope string) *verifiedSubscription {
	key := tenantID + "\x00" + scope
	if vs, seen := g.verdicts[key]; seen {
		return vs
	}
	var vs *verifiedSubscription
	if g.holder != "" {
		var err *exchange.Error
		vs, err = g.s.verifySubscription(g.ctx, tenantID, scope, g.token, g.holder)
		if err != nil {
			g.s.logOutcome(g.ctx, "discover_resources", "DELEGATION_REFUSED", g.caller, nil, "", "", err)
		}
	}
	g.verdicts[key] = vs
	return vs
}

// hideMalformedTerm journals a stored term whose subscription scopes break the
// concrete-scope rule. Ingest refuses such a term; this is the backstop for a
// row that reached storage anyway, hidden for every requester.
func (g *discoveryGate) hideMalformedTerm(tenantID string, err error) {
	g.s.logOutcome(g.ctx, "discover_resources", "TERM_HIDDEN", g.caller, nil, "", "",
		exchange.Wrap(exchange.KindInvalidRequest, err, "tenant "+tenantID+": gated term hidden"))
}

// authorizeDelegation runs the execute-side gate for one item. A non-gated item
// passes untouched. For a gated item the chain in Requester.delegation is
// verified against the record that owns the term's subscription scope, with
// the holder being the thumbprint of the key the item's acceptance verified
// under, and the verified scopes must cover every scope of the term. Every
// failure is the same per-item denial, KindDelegationInvalid; the failure name
// goes to the audit line, never to the response.
func (s *ExchangeService) authorizeDelegation(
	ctx context.Context, itemReq *forav1.TransactionRequest, item *forav1.TransactionItem,
	tenantID string, caller Caller, binding agentBinding,
) error {
	for _, term := range item.GetOffer().GetTerms() {
		scope, gated, err := exchange.SubscriptionScopeOf(term.GetScopes())
		if err != nil {
			// Ingest refuses such a term; a stored one is unsellable.
			return exchange.Wrap(exchange.KindDelegationInvalid, err, "gated term is not sellable")
		}
		if !gated {
			continue
		}
		token := itemReq.GetRequester().GetDelegation().GetToken()
		vs, verr := s.verifySubscription(ctx, tenantID, scope, token, binding.thumbprint)
		if verr == nil && !helpers.ScopesCover(vs.Scopes, term.GetScopes()) {
			verr = exchange.Newf(exchange.KindDelegationInvalid,
				"record %s: verified scopes do not cover the term", vs.RecordID).WithMeta("subscription_record", vs.RecordID)
		}
		if verr != nil {
			s.logOutcome(ctx, "execute_transaction", "DELEGATION_REFUSED", caller, nil, caller.AgentID, "", verr)
			return verr
		}
	}
	return nil
}
