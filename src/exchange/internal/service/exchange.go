package service

import (
	"context"
	"errors"
	"math/big"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/google/uuid"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/regschema"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
)

// ExchangeConfig bundles wiring knobs for the service.
type ExchangeConfig struct {
	Exchange     string        // canonical exchange domain (echoed in ResourceResponse)
	OfferTTL     time.Duration // default DefaultOfferTTL
	URLTTL       time.Duration // default DefaultURLTTL
	ReportWindow time.Duration // default 24h
	// SupportedProfiles is the Exchange's advertised extension-profile set (the
	// same list published in WellKnownManifest.supported_profiles). It gates
	// which profiles DiscoverResources will project (ADR-014).
	SupportedProfiles []string
	// DefaultTenantDomain names the single tenant a Register call reads its
	// activate_new_agents_by_default policy from (ADR-021 §5 decision 1). The
	// RegisterRequest carries no tenant field and billing_ref is per-Exchange, so
	// v1 resolves one default tenant by domain at construction time rather than
	// from the request. Populated in cmd/server from EXCHANGE_DEFAULT_TENANT
	// (falling back to EXCHANGE_DOMAIN); handling per-publisher activation defaults
	// is deferred past v1.
	DefaultTenantDomain string
	// LedgerCurrencies are the currencies this deployment bills in (ISO 4217
	// alpha), in configuration order. There is deliberately NO default:
	// cmd/server threads the selected billing backend's currencies here, and the
	// billing gate rejects a Credit in a currency the adapter does not keep, so
	// wiring that leaves this empty fails the first Register loudly. A default
	// would substitute a plausible currency for missing wiring and misbook money
	// instead.
	//
	// The order matters outside this struct: the well-known document advertises
	// the first entry as base_currency. Nothing in the service reads the order.
	LedgerCurrencies []string
	// DefaultAgentCredits is the one-time welcome credit a freshly registered
	// agent receives at its first Register, keyed by ISO 4217 alpha code. It is
	// parsed once at boot from EXCHANGE_DEFAULT_AGENT_CREDIT, which validates
	// every entry (a currency this deployment bills in, a non-negative amount
	// exact at ledger asset scale 8), and is held here for the life of the
	// process. A currency absent from the map, and an entry of zero, both mean
	// no grant in that currency; nil or empty disables the grant entirely.
	//
	// It is configuration, not seat state, so it applies from the first
	// Register whenever the default seat was created: a seat inserted after the
	// process started grants it exactly like one that existed at boot.
	// Server-side commercial term, never on the wire.
	DefaultAgentCredits map[string]*big.Rat
	// TermsDigest is the digest of the licensing terms this Exchange currently
	// publishes — the SAME value the manifest serves as
	// WellKnownManifest.terms_digest, threaded from the one place the operator's
	// registration settings are read. Publishing a digest is what switches the
	// Register terms gate on: a registration must then name this exact digest, and
	// one that names a different digest or none is refused as stale. Empty means
	// no terms are versioned, so a submitted digest is ignored and none is
	// recorded — the behaviour every deployment had before terms versioning
	// existed.
	TermsDigest string
	// BrokerHost is the canonical directory host of the Broker this Exchange
	// accepts relayed transactions from — derived at boot from
	// EXCHANGE_BROKER_WELLKNOWN_URL, the same document the transport's
	// revocation resolver is bound to, so the Broker the transport trusts and the
	// Broker the service recognises are one party. A verified caller signing under
	// this host is a relay, and the agent is then read from Requester.domain
	// rather than from the header (see resolveAgent). Empty means no Broker is
	// configured: every caller is then direct, and a relay cannot be recognised at
	// all. Production refuses to boot without the URL, so empty is the harness and
	// single-tenant-direct case.
	BrokerHost string
}

func (c ExchangeConfig) withDefaults() ExchangeConfig {
	if c.OfferTTL == 0 {
		c.OfferTTL = DefaultOfferTTL
	}
	if c.URLTTL == 0 {
		c.URLTTL = DefaultURLTTL
	}
	if c.ReportWindow == 0 {
		c.ReportWindow = DefaultReportWindow
	}
	return c
}

// ExchangeService implements DiscoverResources, ExecuteTransaction, and
// ReportUsage. It depends on narrow repos, a billing adapter, a URL-signer
// dispatcher, and an offer signer.
type ExchangeService struct {
	tx      db.TxRunner
	catalog *CatalogService
	tenants repo.TenantReadRepo
	// sites resolves the registered publisher site a delivery URL is minted for.
	// The scheme the site's edge verifies, and its CloudFront key-pair id, are
	// read from there; the private key the URL is signed with comes from the seat
	// (per ADR-026 D5).
	sites        repo.PublisherDomainReadRepo
	agents       repo.AgentRepo
	agentReg     agentreg.Registry
	transactions repo.TransactionRepo
	obligations  repo.ObligationRepo
	evidence     repo.EvidenceRepo
	events       repo.TransactionEventRepo
	feeOverrides repo.FeeOverrideRepo
	billing      billing.Adapter
	offerSigner  *signing.Ed25519Signer
	keyStore     signing.KeyStore
	clk          clock.Clock
	cfg          ExchangeConfig
	// lifetimes shortens the offer and delivery-URL lifetimes of one tenant; nil
	// in production. See Lifetimes.
	lifetimes Lifetimes
	// sor is the System of Record the Register flow creates and reads agent
	// accounts through (ADR-021), and the paid charge path reads the account's
	// active flag from before reserving money (checkAccountActive). billingRefGen
	// mints the candidate billing_ref the Exchange passes to the SoR (ADR-021 D2).
	sor           sor.Adapter
	billingRefGen BillingRefGen
	// regSchema is the operator-configured registration schema the Register gate
	// checks an incoming registration_data against. It is the SAME loaded value
	// the manifest publishes, threaded in by the composition root; nil means no
	// schema is configured, and every method on it is safe on nil, so the
	// pass-through case needs no branch.
	regSchema *regschema.Schema
	// audit is the control-plane audit log. A first registration appends one row
	// through it, in the same transaction as the account link it records.
	audit repo.AuditRepo
}

// ExchangeDeps bundles the wiring dependencies.
type ExchangeDeps struct {
	// TxRunner opens transactions for multi-statement writes. Wiring passes
	// db.PoolRunner{Pool: pool}; the service depends only on this narrow
	// port, and multi-statement writes run inside one transaction.
	TxRunner db.TxRunner
	Catalog  *CatalogService
	Tenants  repo.TenantReadRepo
	// Sites resolves the registered publisher site a delivery URL is minted for,
	// which carries the signing scheme and the CloudFront key-pair id. Required:
	// every successful ExecuteTransaction mints a URL, so a nil value would panic
	// the hot path.
	Sites  repo.PublisherDomainReadRepo
	Agents repo.AgentRepo
	// AgentReg drives ADR-009 D2 lazy registration: when resolveCaller meets
	// a keyID with no fora.agents row, the service pulls the caller's own Web
	// Bot Auth key directory, pins the currently-valid key published there,
	// persists the row, and proceeds. nil disables lazy registration (an unknown
	// keyID stays Unauthenticated) — used by tests that pre-seed every agent.
	AgentReg     agentreg.Registry
	Transactions repo.TransactionRepo
	Obligations  repo.ObligationRepo
	// Evidence persists the append-once transaction_evidence row (full signed
	// offer + both-party signatures + both verifying keys) inside the same
	// ExecuteTransaction commit as the transaction_log + obligation writes.
	// Required: every successful ExecuteTransaction writes one; a nil value would
	// panic the hot path.
	Evidence repo.EvidenceRepo
	// Events is the transaction event journal the attributed denials are
	// recorded in (ADR-027). Required: every attributed denial of a request that
	// proved possession writes one row; a nil value would panic the denial path.
	Events repo.TransactionEventRepo
	// FeeOverrides resolves the per-(tenant, resource_owner) commission override
	// at Authorize. Required: every ExecuteTransaction resolves the effective fee
	// rate so it can be frozen on the hold; a nil value would panic the hot path.
	FeeOverrides repo.FeeOverrideRepo
	Billing      billing.Adapter
	OfferSigner  *signing.Ed25519Signer
	KeyStore     signing.KeyStore
	// SoR is the account System of Record the Register flow and the paid charge
	// path's active-flag gate depend on (ADR-021 D2/D4). The boot path
	// (cmd/server) threads the selected adapter here — in production wrapped in
	// the 30-second read-through cache, which is what keeps the per-transaction
	// active check off the SoR itself.
	SoR sor.Adapter
	// BillingRefGen mints the candidate billing_ref (ADR-021 D2). nil defaults to
	// uuid.NewString in NewExchangeService; tests inject a deterministic
	// generator.
	BillingRefGen BillingRefGen
	// RegSchema is the registration schema the Register gate enforces. It MUST be
	// the value loadRegistrationConfig already produced for the manifest, never a
	// second load: publishing a schema commits the Exchange to enforcing that
	// schema, and an Exchange advertising one document while refusing on another
	// is wrong in a way neither reader could detect from where it stands.
	// Structural guards under internal/guards keep every route to a second value
	// closed. nil is the no-schema-configured case and passes payloads through
	// uninspected.
	RegSchema *regschema.Schema
	// Audit is the control-plane audit log a first registration appends to.
	// Required: every successful first Register writes one row, so a nil value
	// would panic the registration path.
	Audit repo.AuditRepo
	// Clk is the time source consulted by the offer-expiry, signed-URL
	// expiry and reporting-grace deadlines. nil defaults to clock.System{};
	// integration tests pass a DeterministicClock so the gates are driven
	// without sleeping. See ADR-008 D1.
	Clk clock.Clock
	// Lifetimes shortens the offer and delivery-URL lifetimes of a tenant. nil
	// in production, where every tenant gets Config.OfferTTL and Config.URLTTL;
	// the e2e build wires its control service here (ADR-028 D6).
	Lifetimes Lifetimes
	// Every ExecuteTransaction / ReportUsage outcome (success or rejection)
	// emits one log line through the REQUEST-SCOPED logger that
	// RequestIDMiddleware binds onto the context (reqctx.IntoContext), so the
	// line carries request_id + caller_keyid + outcome attributes. The
	// service therefore takes no construction-time
	// logger; logOutcome retrieves the logger via reqctx.FromContext(ctx),
	// which falls back to slog.Default() when no middleware ran.
	Config ExchangeConfig
}

// NewExchangeService assembles the service.
func NewExchangeService(d ExchangeDeps) *ExchangeService {
	clk := d.Clk
	if clk == nil {
		clk = clock.System{}
	}
	gen := d.BillingRefGen
	if gen == nil {
		gen = uuid.NewString
	}
	return &ExchangeService{
		tx:            d.TxRunner,
		catalog:       d.Catalog,
		tenants:       d.Tenants,
		sites:         d.Sites,
		agents:        d.Agents,
		agentReg:      d.AgentReg,
		transactions:  d.Transactions,
		obligations:   d.Obligations,
		evidence:      d.Evidence,
		events:        d.Events,
		feeOverrides:  d.FeeOverrides,
		billing:       d.Billing,
		offerSigner:   d.OfferSigner,
		keyStore:      d.KeyStore,
		clk:           clk,
		cfg:           d.Config.withDefaults(),
		lifetimes:     d.Lifetimes,
		sor:           d.SoR,
		billingRefGen: gen,
		regSchema:     d.RegSchema,
		audit:         d.Audit,
	}
}

// ExchangeDomain reports this Exchange's published identity — the domain it
// stamps into the offers it issues and serves its manifest under. It exists so
// the transport can name the refusing Exchange on a transaction denial without
// reading the environment a second time or being wired with the value
// separately: a denial that named a different domain than the offers it refuses
// would be worse than one that named none.
func (s *ExchangeService) ExchangeDomain() string { return s.cfg.Exchange }

// DiscoverResources resolves URIs against the catalog, signs offers, returns
// them grouped per URI:
//   - catalog hit  → per-request offer in the OfferGroup
//   - catalog miss → empty group, OFFER_ABSENCE_REASON_NOT_IN_CATALOG
//
// The canonical v1 path is batch-shaped: ResourceQuery.uris is always a list
// (the requester is identity-only), and the response carries one OfferGroup
// per requested URI.
// The flat `offers` field mirrors the assembled per-request offers as a
// convenience aggregate; per fora.proto §ResourceResponse, callers are
// expected to read OfferGroup.
func (s *ExchangeService) DiscoverResources(
	ctx context.Context,
	req *forav1.ResourceQuery,
) (*forav1.ResourceResponse, error) {
	_ = ctx // unused; kept for interface symmetry with ExecuteTransaction
	if req == nil || req.GetRequester() == nil {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "requester required")
	}
	if req.GetRequester().GetId() == "" {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "requester.id required")
	}
	uris := req.GetUris()
	if len(uris) == 0 {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "at least one uri required")
	}
	snap := s.catalog.Snapshot()
	// Only project profiles the Exchange advertises (single source of truth =
	// cfg.SupportedProfiles == WellKnownManifest.supported_profiles).
	profiles := effectiveProfiles(req.GetSupportedProfiles(), s.cfg.SupportedProfiles)
	flatOffers := make([]*forav1.Offer, 0, len(uris))
	groups := make([]*forav1.OfferGroup, 0, len(uris))
	for _, uri := range uris {
		entry, verdict := snap.Lookup(uri)
		group, groupOffers, err := s.groupFor(uri, entry, verdict, req.GetRequester(), profiles)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
		flatOffers = append(flatOffers, groupOffers...)
	}
	resp := &forav1.ResourceResponse{
		Ver:         helpers.ProtocolVersion,
		Exchange:    s.cfg.Exchange,
		Offers:      flatOffers,
		OfferGroups: groups,
	}
	// Per-URI absence reasons are surfaced via OfferGroup.absence_reason,
	// populated by groupFor(). The canonical fora.proto carries no
	// request-level absence-reason field.
	return resp, nil
}

// ExecuteTransaction verifies the caller's identity, each item's offer and
// billing authorization, then writes one transaction_log row per item and
// returns the signed URLs. There is exactly ONE pipeline — the items[] batch
// loop (executeBatch); N=1 is just a one-element batch. Write-before-sign is
// enforced per item: each signed-URL hash lands in the DB in the same
// transaction as the rest of that item's fields, and the URL is only returned
// after commit. Per-item business denials stay in-body (HTTP 200,
// TransactionResultItem.denial_reason); an envelope/internal failure aborts the
// whole batch with the corresponding connect.Code.
func (s *ExchangeService) ExecuteTransaction(
	ctx context.Context,
	req *forav1.TransactionRequest,
) (*forav1.TransactionResponse, error) {
	return s.executeBatch(ctx, req)
}

// logBillingRecordFailed emits the best-effort billing-Record-failure line
// through the request-scoped logger so it carries request_id, same
// rationale as logOutcome. The message is the short,
// low-cardinality event name; the specific condition lives in the "event" attr
// so this line shares logOutcome's greppable style instead of embedding the
// condition in a free-text message. Shared by every batch item's
// billing path.
func (s *ExchangeService) logBillingRecordFailed(ctx context.Context, txID, billingID string, rErr error) {
	reqctx.FromContext(ctx).ErrorContext(ctx, "exchange.execute_transaction",
		"event", "billing_record_failed_best_effort",
		"transaction_id", txID,
		"billing_id", billingID,
		"err", rErr.Error())
}

// releaseHold returns a previously-held reservation on the ExecuteTransaction
// hot-path failure (URL signing failed, persist failed). The caller is already
// returning an error to the agent; Release's own failure cannot fail the
// request again. ErrUnknownBillingID is treated as a successful no-op
// (idempotent — Release was already called or the reservation was already
// consumed by Record). An empty billingID means no reservation was ever taken
// (the price-zero free path, ADR-009 D2), so there is nothing to release.
func (s *ExchangeService) releaseHold(ctx context.Context, billingID, idempotencyKey, reason string) {
	if hasNoReservation(billingID) {
		return
	}
	err := s.billing.Release(ctx, billingID, idempotencyKey)
	if err != nil && !errors.Is(err, billing.ErrUnknownBillingID) {
		// Through the request-scoped logger so the line carries request_id
		// for correlation, same rationale as logOutcome. The message
		// is the short, low-cardinality event name; the specific condition lives
		// in the "event" attr so this line shares logOutcome's greppable style
		// instead of embedding the condition in a free-text message.
		reqctx.FromContext(ctx).ErrorContext(ctx, "exchange.execute_transaction",
			"event", "release_hold_failed",
			"billing_id", billingID,
			"reason", reason,
			"err", err.Error())
	}
}

// resolvedOffer carries the outcome of binding the presented offer's signed
// canonical URL to a catalog entry, with pricing rebuilt to reflect the
// selected variant.
// offerCanonicalBytes is what verifyPresentedOffer checked the Exchange signature
// over, carried from there so the evidence row stores the verified bytes rather
// than a second derivation of them.
type resolvedOffer struct {
	entry               repo.CatalogEntry
	pricing             PricingDoc
	offerCanonicalBytes []byte
}

// resolveOfferForTx verifies the PRESENTED reflected Offer statelessly, then
// maps its SIGNED Identity.canonical_url to a catalog entry for
// delivery/resource binding and derives the charge from the SIGNED offer's
// pricing.
//
// Trust model:
//   - The SIGNED offer's Identity.canonical_url is the catalog binding: it is
//     set inside the offer before signing, so it carries the same trust as
//     every other signed field, and catalog.uri is globally UNIQUE, so the
//     exact-match lookup is well-defined. offer_id is an opaque per-offer
//     identifier with no resource semantics and takes no part in binding.
//   - Verification is over the PRESENTED bytes via verifyPresentedOffer (signature
//   - expiry), never a reconstruct-from-catalog. A post-discovery tamper of any
//     covered field breaks the signature.
//   - Billing reads the VERIFIED offer's signed pricing (the agent pays exactly
//     what it signed; catalog price drift between discover and execute is the
//     publisher's problem). The catalog entry is used only for delivery/resource
//     binding (entry.URI → SignURL, tenant, tx_log keys).
func (s *ExchangeService) resolveOfferForTx(req *forav1.TransactionRequest) (resolvedOffer, error) {
	// Items-only: the offer is presented in items[0] (executeBatchItem
	// re-projects each item onto a 1-item synthetic request).
	presented := req.GetItems()[0].GetOffer()
	// Verify the presented offer (signature over presented bytes + signed expiry)
	// BEFORE trusting any of its fields for binding or billing. The canonical
	// bytes it checked ride along to persistence.
	canonical, err := s.verifyPresentedOffer(presented)
	if err != nil {
		return resolvedOffer{}, err
	}
	// The signed canonical URL is REQUIRED at execute: without it the offer binds
	// to nothing. Its presence is checked at envelope validation, BEFORE the
	// request claims its idempotency key, so a malformed offer cannot consume the
	// key and leave the agent unable to retry with a corrected one.
	canonicalURL := presented.GetIdentity().GetCanonicalUrl()
	snap := s.catalog.Snapshot()
	entry, ok := snap.byURI[canonicalURL]
	if !ok {
		// The offer is authentic but the resource behind it is gone from the
		// catalog. That is a per-item DENIAL (DENIAL_REASON_CONTENT_UNAVAILABLE),
		// not a batch abort: the agent keeps its other items, and the batch
		// finalizes a complete response so an exact retry replays that denial
		// instead of re-running against a claim that never stored one.
		return resolvedOffer{}, exchange.Newf(exchange.KindContentUnavailable,
			"offer resource %q not found in catalog", canonicalURL)
	}
	// Charge the SIGNED offer's pricing — the price the agent verifiably accepted.
	// Not a recompute from the live catalog (which may have drifted since
	// discovery): the signature authenticates this exact price (MEDIUM1).
	pricing, err := pricingDocFromPricing(presented.GetPricing())
	if err != nil {
		return resolvedOffer{}, exchange.Wrap(exchange.KindInvalidRequest, err, "parse signed offer pricing")
	}
	return resolvedOffer{entry: entry, pricing: pricing, offerCanonicalBytes: canonical}, nil
}
