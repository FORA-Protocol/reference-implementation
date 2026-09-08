package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/agentid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// RejectionReasonNotInContributors is the machine-readable reason emitted in
// CatalogPushRejection when the caller is authenticated but is not listed
// as a contributor in the publisher's fora.json.
const RejectionReasonNotInContributors = "caller_not_in_catalog_contributors"

// RejectionReasonInvalidTerms is the machine-readable reason emitted when an
// entry carries a license term the SDK's ingest tier refuses
// (helpers.ValidateLicenseTerm over the canonicalized term). The Exchange does
// not branch on which rule failed: validateEntryTerms maps EVERY violation that
// face returns to this one reason and carries the SDK's message as the
// rejection's Detail. Which rules each tier owns is ADR-014's "Validation rules
// (normative)" table, and stating the inventory here as well would be a second
// copy of it to keep in step.
//
// The wire tier is refused earlier by the protovalidate interceptor and never
// reaches this reason. Disjointness is the one rule stated at both tiers, and
// the two are not one rule repeated: the wire rule compares the tokens as
// received, and since several tokens have more than one accepted spelling — a
// registered alias beside its canonical form, or either in any ASCII case — two
// spellings of one token clear it and become one token when the fold runs,
// which is what the ingest-tier rule reads.
const RejectionReasonInvalidTerms = "invalid_license_terms"

// RejectionReasonUnknownPublisherDomain is emitted when an entry's domain has no
// tenant on this Exchange — it cannot own a catalog row here.
const RejectionReasonUnknownPublisherDomain = "unknown_publisher_domain"

// RejectionReasonManifestVersionRefused is emitted when an entry's owner
// manifest was fetched and read but carries a document version this Exchange
// does not accept (forawellknown.ErrVersionRefused): another major, or no
// version at all. It is the publisher's own document and a verdict about it —
// the manifest will say the same thing on every push — so it is reported to the
// caller as a rejection naming the entry, never as an Exchange-internal error.
const RejectionReasonManifestVersionRefused = "publisher_manifest_version_refused"

// RejectionReasonMissingResourceOwner is emitted when an entry's owner manifest
// does not attest a resource_owner_id for this Exchange. The settlement payee is
// never inferred (no tenant_id fallback), so an un-attested entry cannot own a
// catalog row.
const RejectionReasonMissingResourceOwner = "missing_resource_owner_id"

// RejectionReasonTenantMismatch is emitted when the caller-supplied req.tenant_id
// disagrees with the tenant the entry's domain resolves to; the server-derived
// tenant is authoritative and the disagreeing client value is rejected.
const RejectionReasonTenantMismatch = "tenant_mismatch"

// CatalogPushRejection captures a single rejected entry along with its
// machine-readable reason. Under all-or-nothing PushResources the
// collected rejections are enumerated into the InvalidArgument error returned
// to the caller; nothing is persisted.
type CatalogPushRejection struct {
	URI    string
	Reason string
	// Detail is the human-readable cause behind Reason, when the refusing check
	// has one: for RejectionReasonInvalidTerms it is the SDK's
	// RuleViolation.Message — the string every SDK's client-side check reports
	// for the same finding, so a publisher's pre-check and the Exchange's
	// refusal read alike. Empty for the other reasons.
	Detail string
}

// reject builds a per-entry rejection. It is the ONLY place one is built:
// centralizing the construction keeps the gate chains readable, keeps the
// rejection shape consistent, and means logRejections below sees every refusal
// there is rather than only the ones one file remembered to route through it.
func reject(uri, reason string) *CatalogPushRejection {
	return &CatalogPushRejection{URI: uri, Reason: reason}
}

// catalogRejectMsg is the audit message key for one refused catalog entry, in
// the dotted vocabulary the request path emits beside exchange.httpsig.reject
// and exchange.admin.ip_reject.
const catalogRejectMsg = "exchange.catalog_push.reject"

// logRejections records every refused entry of one push, at the point the
// refusals are collected rather than at each gate that produces one.
//
// It runs once over the whole set for the reason reject exists: a push can be
// refused for seven reasons across two files, and a log call per gate is a list
// that goes out of date the first time an eighth is added. Here there is one
// call, and it sees whatever partitionByContributor collected.
//
// It carries the machine-readable reason and the URI, which is what an operator
// needs to place a refusal a publisher is reporting. Detail is carried when the
// refusing check has one; only the ingest-tier term check does. That check ALSO
// logs its own line, one gate deeper, because it holds the rule id, the
// entry-relative path and the offending token — fields this vocabulary does not
// carry and which are what an operator names the bad value from. The two lines
// are not redundant: this one says which entries were refused and why, that one
// says which value inside a term did it.
func logRejections(ctx context.Context, rejections []CatalogPushRejection) {
	if len(rejections) == 0 {
		return
	}
	log := reqctx.FromContext(ctx)
	for _, r := range rejections {
		attrs := []any{"uri", r.URI, "reason", r.Reason}
		if r.Detail != "" {
			attrs = append(attrs, "detail", r.Detail)
		}
		log.WarnContext(ctx, catalogRejectMsg, attrs...)
	}
}

// formatRejections renders per-entry rejections as "uri (reason)" — or
// "uri (reason: detail)" when the rejection carries a Detail — joined by "; ",
// for the all-or-nothing InvalidArgument message, so a publisher sees exactly
// which URL failed and why.
func formatRejections(rejections []CatalogPushRejection) string {
	parts := make([]string, len(rejections))
	for i, r := range rejections {
		if r.Detail == "" {
			parts[i] = fmt.Sprintf("%s (%s)", r.URI, r.Reason)
			continue
		}
		parts[i] = fmt.Sprintf("%s (%s: %s)", r.URI, r.Reason, r.Detail)
	}
	return strings.Join(parts, "; ")
}

// resolveManifestGates fetches the entry's owner manifest once and runs the two
// manifest-backed gates in order: contributor authorization (Gate 2), then
// resource-owner attestation. It returns the attested resource_owner_id payee on
// success. A missing manifest or an unlisted caller yields
// RejectionReasonNotInContributors; a manifest at a document version this
// Exchange does not accept yields RejectionReasonManifestVersionRefused, a
// verdict on the publisher's own document rather than an Exchange failure; a
// manifest that does not attest a resource_owner_id for this Exchange yields
// RejectionReasonMissingResourceOwner (the payee is never inferred — there is no
// tenant_id fallback). Any other fetch error aborts the batch.
func (s *CatalogService) resolveManifestGates(
	ctx context.Context,
	e *forav1.ResourceEntry,
	callerID, uri string,
) (string, *CatalogPushRejection, error) {
	manifest, err := s.manifests.Get(ctx, e.GetDomain())
	if err != nil {
		if errors.Is(err, forawellknown.ErrNoDocument) {
			return "", reject(uri, RejectionReasonNotInContributors), nil //nolint:nilerr // err->rejection
		}
		if errors.Is(err, forawellknown.ErrVersionRefused) {
			return "", reject(uri, RejectionReasonManifestVersionRefused), nil //nolint:nilerr // err->rejection
		}
		return "", nil, exchange.Wrap(exchange.KindInternal, err, "fetch publisher manifest")
	}
	if !forawellknown.AuthorizesContributor(manifest, callerID, agentid.FromDirectory) {
		return "", reject(uri, RejectionReasonNotInContributors), nil
	}
	ownerID, ok := forawellknown.ResourceOwnerID(manifest, s.exchangeDomain)
	if !ok {
		return "", reject(uri, RejectionReasonMissingResourceOwner), nil
	}
	return ownerID, nil, nil
}
