// Package service holds the Exchange business logic. Services accept narrow
// repo interfaces and return proto types at the boundary so the transport
// layer stays a thin adapter.
package service

import (
	"context"
	"errors"
	"sync/atomic"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/jackc/pgx/v5"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/agentreg"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// defaultCatalogURIScheme is the scheme used to materialize a CatalogEntry
// URI from the `(domain, path)` pair in a ResourceEntry message. Production
// always uses "https"; the e2e compose stack overrides via
// EXCHANGE_CATALOG_URI_SCHEME so catalog URIs can route through the in-network
// http edge worker. Exported via SetCatalogURIScheme for test overrides; the
// Exchange binary calls it once at startup from runhttp.EnvOr.
const defaultCatalogURIScheme = "https"

var catalogURIScheme atomic.Value // stores string

// SetCatalogURIScheme overrides the scheme used when materializing
// catalog URIs in entryFromProto and uriFromEntry. Call once at startup
// (or in a test setup) with the value from EXCHANGE_CATALOG_URI_SCHEME.
// Empty scheme falls back to defaultCatalogURIScheme.
func SetCatalogURIScheme(scheme string) {
	if scheme == "" {
		scheme = defaultCatalogURIScheme
	}
	catalogURIScheme.Store(scheme)
}

func currentCatalogURIScheme() string {
	if v, ok := catalogURIScheme.Load().(string); ok && v != "" {
		return v
	}
	return defaultCatalogURIScheme
}

// LookupVerdict describes how a catalog lookup resolved.
type LookupVerdict int

const (
	// LookupMiss means no catalog entry matches the requested URI.
	LookupMiss LookupVerdict = iota
	// LookupHitPublic means an entry matched. The v1 surface emits a
	// per-request offer.
	LookupHitPublic
)

// Lookup returns the single catalog entry that matches uri by longest prefix.
// Returns (entry, LookupHitPublic) on hit; (zero, LookupMiss) on miss.
func (s *CatalogSnapshot) Lookup(uri string) (repo.CatalogEntry, LookupVerdict) {
	if s == nil || s.trie == nil {
		return repo.CatalogEntry{}, LookupMiss
	}
	_, raw, ok := s.trie.LongestPrefix(uri)
	if !ok {
		return repo.CatalogEntry{}, LookupMiss
	}
	entry, typed := raw.(repo.CatalogEntry)
	if !typed {
		return repo.CatalogEntry{}, LookupMiss
	}
	return entry, LookupHitPublic
}

// ManifestCache is the narrow port the CatalogService needs from the shared
// publisher-manifest cache: fetch a host's manifest (a 404 surfaces as
// forawellknown.ErrNoDocument, and a document version this build does not accept
// as forawellknown.ErrVersionRefused — both are caller-facing rejections rather
// than internal faults). Composed from forawellknown.Cache at wiring time so the
// service depends only on the read it makes: ports stay narrow.
type ManifestCache interface {
	Get(ctx context.Context, host string) (*forawellknown.Manifest, error)
}

// CatalogService handles the CatalogService.PushResources RPC and owns the
// in-memory radix trie rebuilt on every push.
type CatalogService struct {
	repo      repo.CatalogRepo
	tenants   repo.TenantReadRepo
	sites     repo.PublisherDomainReadRepo
	listings  repo.PublisherListingReadRepo
	registry  agentreg.Registry
	manifests ManifestCache
	tx        db.TxRunner
	// exchangeDomain is this Exchange's own canonical domain (EXCHANGE_DOMAIN).
	// It does two things at push. It selects the AuthorizedExchange entry in a
	// site's manifest from which the resource_owner_id payee is read, and it
	// names the SEAT this deployment sells as — the fora.tenants row every
	// pushed entry is stored under. Required: an empty value matches no manifest
	// entry and no seat, so every push would be refused.
	exchangeDomain string
	snapshot       atomic.Pointer[CatalogSnapshot]
	// clk drives any time-dependent CatalogService state. Defaults to
	// clock.System{} per ADR-008 D1; integration tests inject a DeterministicClock
	// via SetClock. Retained for future per-entry clock hooks (e.g. valid_until).
	clk clock.Clock
	// supportedProfiles is the Exchange's advertised extension-profile set, set
	// via SetSupportedProfiles before Bootstrap. rebuild() pre-renders the CoMP
	// projection for these profiles into the snapshot; empty = no
	// pre-render (no comp ext). Single source = cmd/server exchangeSupportedProfiles().
	supportedProfiles []string
}

// CatalogDeps bundles the CatalogService wiring dependencies. Every field is
// required; a zero value is a programmer error, not a supported mode.
type CatalogDeps struct {
	// Catalog persists the accepted entries.
	Catalog repo.CatalogRepo
	// Tenants resolves this deployment's single seat from ExchangeDomain.
	Tenants repo.TenantReadRepo
	// Sites resolves a pushed entry's domain to the registered site, which names
	// the publisher the entry settles to (per ADR-026 D1).
	Sites repo.PublisherDomainReadRepo
	// Listings answers whether the seat actually sells that site: a push is
	// accepted only for a listing in the ACTIVE state.
	Listings repo.PublisherListingReadRepo
	// Registry enforces caller-signature auth (Gate 1).
	Registry agentreg.Registry
	// Manifests serves each site's own /.well-known/fora.json, which carries the
	// contributor list (Gate 2) and the attested payee.
	Manifests ManifestCache
	// TxRunner opens the transaction the accepted batch commits in.
	TxRunner db.TxRunner
	// ExchangeDomain is this Exchange's own canonical domain (EXCHANGE_DOMAIN):
	// the seat's hostname and the manifest entry the payee is read from.
	ExchangeDomain string
}

// NewCatalogService constructs the service from its wiring dependencies.
func NewCatalogService(d CatalogDeps) *CatalogService {
	return &CatalogService{
		repo:           d.Catalog,
		tenants:        d.Tenants,
		sites:          d.Sites,
		listings:       d.Listings,
		registry:       d.Registry,
		manifests:      d.Manifests,
		tx:             d.TxRunner,
		exchangeDomain: d.ExchangeDomain,
		clk:            clock.System{},
	}
}

// SetClock replaces the clock used for time-dependent CatalogService state.
// Test-only seam so integration tests can drive deadline gates without
// sleeping; production wires clock.System{} via NewCatalogService and never
// calls this.
func (s *CatalogService) SetClock(c clock.Clock) {
	s.clk = c
}

// SetSupportedProfiles sets the extension-profile set rebuild() pre-renders into
// the snapshot. Call BEFORE Bootstrap so the first rebuild renders.
// Both production (cmd/server) and the integration harness must call it with the
// SAME list the Exchange advertises, else the CoMP cache is empty.
func (s *CatalogService) SetSupportedProfiles(profiles []string) {
	s.supportedProfiles = profiles
}

// Bootstrap loads existing rows into the snapshot so that discovery works
// after a service restart without requiring a PushResources first.
func (s *CatalogService) Bootstrap(ctx context.Context) error {
	return s.rebuild(ctx)
}

// Snapshot returns the current catalog snapshot (never nil after Bootstrap).
func (s *CatalogService) Snapshot() *CatalogSnapshot {
	snap := s.snapshot.Load()
	if snap == nil {
		empty := newEmptySnapshot()
		s.snapshot.Store(empty)
		return empty
	}
	return snap
}

// PushResources upserts the entries and swaps the in-memory snapshot. Gate 1
// (caller-signature) is enforced by the transport layer before invocation;
// this method enforces Gate 2 (contributor authorization) per entry. source is
// which client made the push, as the transport read it; every row the push
// stores records it.
func (s *CatalogService) PushResources(
	ctx context.Context,
	req *forav1.PushResourcesRequest,
	source PushSource,
) (*forav1.PushResourcesResponse, error) {
	if req == nil {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "nil request")
	}
	if req.GetTenantId() == "" {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "tenant_id required")
	}
	if req.GetCallerId() == "" {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "caller_id required")
	}
	if len(req.GetEntries()) == 0 {
		return nil, exchange.Newf(exchange.KindInvalidRequest, "entries required")
	}

	accepted, rejections, warnings, err := s.partitionByContributor(ctx, req)
	if err != nil {
		return nil, err
	}
	// All-or-nothing: a submission is accepted only if EVERY entry is
	// valid. Any per-entry rejection => persist NOTHING and reject the
	// whole submission with per-item reasons, so the publisher fixes and
	// resubmits the whole set. No partial acceptance. Term rules run in two
	// tiers before this point: the wire tier (the protovalidate interceptor —
	// term shape and cross-field rules, refused before this method runs) and
	// the ingest tier (the SDK helpers, in classifyEntry, over canonicalized
	// terms). The gates the Exchange owns — contributor authorization,
	// resource-owner attestation, site and listing registration, URI ownership —
	// run in the same per-entry chain, and this refusal of the whole set is the
	// Exchange's.
	if len(rejections) > 0 {
		return nil, exchange.Newf(exchange.KindInvalidRequest,
			"push rejected, nothing persisted; fix and resubmit: %s", formatRejections(rejections))
	}
	// The accepted entries upsert atomically: a mid-batch failure rolls the
	// whole batch back rather than leaving the catalog half-written (Arch rule 7).
	// The snapshot rebuild reads committed state, so it runs after the commit.
	if len(accepted) > 0 {
		stampSource(accepted, source)
		if err := s.persistAccepted(ctx, accepted); err != nil {
			return nil, err
		}
		if err := s.rebuild(ctx); err != nil {
			return nil, exchange.Wrap(exchange.KindInternal, err, "rebuild catalog snapshot")
		}
	}
	return &forav1.PushResourcesResponse{
		Ver:      helpers.ProtocolVersion,
		Accepted: int32(len(accepted)), //nolint:gosec // per-request batch size fits in int32
		Warnings: warnings,
	}, nil
}

// persistAccepted upserts the accepted entries in one transaction (Arch rule
// 7: a mid-batch failure rolls the whole batch back). The guarded upsert
// refuses a URI move for an existing resource_id — the race-safe backstop
// behind the rejectURIConflicts precheck — and a push that loses that race is
// a caller-fixable conflict, not an infrastructure failure.
func (s *CatalogService) persistAccepted(ctx context.Context, accepted []repo.CatalogEntry) error {
	err := s.tx.WithTx(ctx, func(tx pgx.Tx) error {
		for _, entry := range accepted {
			if _, err := s.repo.UpsertTx(ctx, tx, entry); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		return nil
	}
	if errors.Is(err, repo.ErrCatalogURIImmutable) {
		return exchange.Wrap(exchange.KindInvalidRequest, err,
			"push rejected, nothing persisted: catalog uri is immutable for an existing resource")
	}
	return exchange.Wrap(exchange.KindInternal, err, "upsert catalog entries")
}

// partitionByContributor walks req.Entries, classifying each through the full
// per-entry gate chain (classifyEntry). Rejected entries are appended to the
// rejections slice; accepted entries are converted to the repo shape and
// returned in accepted order. A non-nil error is an infrastructure failure that
// aborts the whole batch.
func (s *CatalogService) partitionByContributor(
	ctx context.Context,
	req *forav1.PushResourcesRequest,
) ([]repo.CatalogEntry, []CatalogPushRejection, []string, error) {
	seat, err := s.resolveSeat(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	accepted := make([]repo.CatalogEntry, 0, len(req.GetEntries()))
	rejections := make([]CatalogPushRejection, 0)
	var warnings []string
	for _, e := range req.GetEntries() {
		entry, rej, entryWarnings, err := s.classifyEntry(ctx, seat, req, e)
		if err != nil {
			return nil, nil, nil, err
		}
		if rej != nil {
			rejections = append(rejections, *rej)
			continue
		}
		warnings = append(warnings, entryWarnings...)
		accepted = append(accepted, entry)
	}
	// URI-ownership gate: drop any accepted entry whose URI is already
	// owned by a different resource_id (in the catalog or earlier in this batch).
	// Runs after per-entry classification so it sees the materialized URIs and the
	// tenant-scoped resource_ids, and keeps the failure a per-entry rejection
	// rather than the batch-aborting UNIQUE(uri) violation it backstops.
	accepted, uriRejections, err := s.rejectURIConflicts(ctx, accepted)
	if err != nil {
		return nil, nil, nil, err
	}
	rejections = append(rejections, uriRejections...)
	logRejections(ctx, rejections)
	return accepted, rejections, warnings, nil
}

// resolveSeat reads the one Exchange seat this deployment sells as: the
// fora.tenants row whose domain is EXCHANGE_DOMAIN. Every entry of every push
// is stored under it (per ADR-026 D1).
//
// It is read once per push, not once per entry, because the seat does not
// depend on the entry. Resolving the seat from the request Host — which is what
// a deployment running a second seat would need — is deliberately not done: one
// deployment sells as exactly one seat, and a second seat is a second
// deployment.
//
// A missing seat row is an operator-fixable deployment state, not a caller
// mistake, so it surfaces as FailedPrecondition for the whole push rather than
// as a per-entry rejection. No entry could succeed under it, and reporting it
// per entry would tell a publisher to fix their feed.
func (s *CatalogService) resolveSeat(ctx context.Context) (repo.Tenant, error) {
	seat, err := s.tenants.ByDomain(ctx, s.exchangeDomain)
	if err != nil {
		if errors.Is(err, repo.ErrTenantNotFound) {
			return repo.Tenant{}, exchange.Newf(exchange.KindFailedPrecondition,
				"this Exchange has no seat registered for its own domain %q; "+
					"seed it before accepting catalog pushes", s.exchangeDomain)
		}
		return repo.Tenant{}, exchange.Wrap(exchange.KindInternal, err, "resolve exchange seat")
	}
	return seat, nil
}

// classifyEntry runs the full per-entry gate chain and returns exactly one of:
// an accepted entry (rej == nil) or a rejection (zero entry, rej != nil). A
// non-nil error is an infrastructure failure that aborts the whole batch.
//
// Chain order: token canonicalization (the SDK's NormalizeResourceEntry, in
// place); the seat check against any caller-supplied tenant_id; proto→repo
// conversion with a seat-scoped resource_id; the site lookup, which refuses an
// unregistered domain; contributor authorization (Gate 2) and resource-owner
// attestation, both read off the site's manifest; the remaining registration
// gates, which check that the attested payee is the one the site is registered
// under and that this seat lists the site as active; the SDK's ingest-tier term
// checks (validateEntryTerms).
//
// Two ordering decisions, and they pull in opposite directions.
//
// The site lookup runs FIRST, before the manifest fetch, because the fetch goes
// to the host the entry names. Without a database precondition ahead of it, any
// caller who clears signature authorization can make the Exchange issue an
// outbound request per entry to a host of their choosing. The cost is that the
// refusal for an unregistered domain is visible to a caller who is not a
// contributor anywhere — the same bit the pre-registration deriveTenantID
// refused on, so this discloses nothing that was hidden before.
//
// Contributor authorization still runs BEFORE the payee comparison and the
// listing check. A caller outside the site's catalog_contributors[] is refused
// as an unauthorized contributor whether or not the payee and the listing check
// out, so a stranger pushing at a REGISTERED site learns nothing about who that
// site settles to or whether this seat sells it.
func (s *CatalogService) classifyEntry(
	ctx context.Context,
	seat repo.Tenant,
	req *forav1.PushResourcesRequest,
	e *forav1.ResourceEntry,
) (repo.CatalogEntry, *CatalogPushRejection, []string, error) {
	// The terms[] length is not checked here. ResourceEntry.terms carries its
	// cap on the wire, so the validate interceptor refuses an over-cap entry —
	// and with it the whole submission — at the RPC boundary, before this chain
	// runs; a per-entry check here would be unreachable.
	//
	// Canonicalize tokens in place before conversion and validation, so the
	// persisted row and the DiscoverResources→Offer.terms projection carry
	// canonical tokens and the ingest-tier checks compare canonical values.
	helpers.NormalizeResourceEntry(e)
	// A caller-supplied tenant_id that disagrees with this deployment's seat is
	// rejected rather than honoured: the seat is server-side and authoritative.
	if req.GetTenantId() != "" && req.GetTenantId() != seat.ID {
		return repo.CatalogEntry{}, reject(uriFromEntry(e), RejectionReasonTenantMismatch), nil, nil
	}
	entry, err := entryFromProto(seat.ID, e)
	if err != nil {
		// A malformed entry drops as a per-entry rejection, not a batch abort.
		rej := reject(uriFromEntry(e), RejectionReasonInvalidEntry)
		return repo.CatalogEntry{}, rej, nil, nil //nolint:nilerr // err->rejection
	}
	// The site comes from the URI that was just materialized, through the same
	// siteDomainFromURI the delivery path uses, so push and delivery cannot
	// disagree about which site a stored row belongs to. The row stores the
	// registered domain it matched, which every read that narrows the catalog
	// by domain compares.
	domain, err := siteDomainFromURI(entry.URI)
	if err != nil {
		rej := reject(uriFromEntry(e), RejectionReasonInvalidEntry)
		return repo.CatalogEntry{}, rej, nil, nil //nolint:nilerr // err->rejection
	}
	// The site lookup precedes the manifest fetch, so an unregistered domain is
	// refused without any outbound request. See resolveSite.
	site, rej, err := s.resolveSite(ctx, domain, entry.URI)
	if err != nil || rej != nil {
		return repo.CatalogEntry{}, rej, nil, err
	}
	// Contributor authorization (Gate 2) and resource-owner attestation both read
	// the site's manifest; resolveManifestGates fetches it once and runs both. A
	// missing manifest, unlisted caller, or un-attested payee drops the entry.
	ownerID, rej, err := s.resolveManifestGates(ctx, domain, req.GetCallerId(), entry.URI)
	if err != nil || rej != nil {
		return repo.CatalogEntry{}, rej, nil, err
	}
	rej, err = s.checkRegistration(ctx, registrationCheck{
		seatID: seat.ID, site: site, attestedOwnerID: ownerID, uri: entry.URI,
	})
	if err != nil || rej != nil {
		return repo.CatalogEntry{}, rej, nil, err
	}
	entry.ResourceOwnerID = ownerID
	entry.Domain = site.Domain
	// One term failing the SDK's ingest-tier checks drops the whole entry, with
	// the SDK's message as the rejection detail; lint warnings on accepted
	// terms surface in PushResourcesResponse.warnings[].
	entryWarnings, rej := validateEntryTerms(ctx, entry.URI, e)
	if rej != nil {
		return repo.CatalogEntry{}, rej, nil, nil
	}
	return entry, nil, entryWarnings, nil
}

// rebuild reloads the WHOLE catalog (all tenants) into the in-memory radix trie.
//
// admin:cross_tenant — the ListAll read is DELIBERATELY un-tenant-filtered
// and carries the explicit cross-tenant marker. Discovery spans every
// publisher, so the trie is a single global index. Isolation is preserved at the
// WRITE boundary: tenant_id is the seat resolved server-side from
// EXCHANGE_DOMAIN, never a caller-supplied value, and a URI belongs to exactly
// one row (UNIQUE(uri)), so the global read cannot surface a cross-tenant
// shadowing. Lookup() inherits this — it returns the single owning row, never
// another tenant's.
func (s *CatalogService) rebuild(ctx context.Context) error {
	rows, err := s.repo.ListAll(ctx) // admin:cross_tenant — intentional global read; see doc above
	if err != nil {
		return err
	}
	snap := newEmptySnapshot()
	for _, row := range rows {
		snap.trie.Insert(row.URIPrefix, row)
		snap.byURI[row.URI] = row
		snap.tenant[row.ResourceID] = row.TenantID
		// Decode the row's terms ONCE here. A row whose stored JSONB fails
		// to decode is cached as zero terms (decodeRowTerms logs + returns nil), so
		// one bad row cannot poison the snapshot or crash rebuild.
		snap.terms[row.ResourceID] = decodeRowTerms(ctx, row)
		// Decode the row's metadata ONCE here too (same rationale). A row without
		// metadata or whose document fails to decode caches nil and yields no
		// metadata on the Offer.
		if md := decodeRowMetadata(ctx, row); md != nil {
			snap.metadata[row.ResourceID] = md
		}
		// Pre-render the CoMP projection for each stored term x supported profile
		// ONCE here, so the discovery hot path looks up a blob rather
		// than rendering per request. Runs AFTER terms+metadata are cached for the row.
		renderRowProfiles(ctx, snap, row.ResourceID, s.supportedProfiles)
	}
	s.snapshot.Store(snap)
	return nil
}
