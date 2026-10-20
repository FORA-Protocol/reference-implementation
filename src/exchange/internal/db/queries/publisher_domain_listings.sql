-- name: GetPublisherDomainListing :one
-- Reads one seat's listing of one site: whether the site may sell here, and the
-- commercial arrangement the generated artifacts are built from. Tenant-scoped.
SELECT * FROM fora.publisher_domain_listings
 WHERE tenant_id = $1 AND domain = $2;

-- name: ListPublisherDomainListings :many
-- Every site one payee has listed on one seat, for the console's domain list.
-- Joins to fora.publisher_domains because the listing is keyed by domain and the
-- publisher is a fact of the site, not of the relationship. Tenant-scoped.
SELECT l.* FROM fora.publisher_domain_listings l
  JOIN fora.publisher_domains d ON d.domain = l.domain
 WHERE l.tenant_id = $1 AND d.publisher_id = $2
 ORDER BY l.domain;

-- name: CreatePublisherDomainListing :one
-- Lists a site on a seat. The state is left to the column default,
-- PENDING_MANIFEST: a listing is never born proven, and taking the state from
-- the caller would let registration create an ACTIVE listing without a manifest.
-- No ON CONFLICT, so listing the same site twice on one seat surfaces the
-- duplicate instead of silently overwriting the first listing's path scope or
-- the licensing it agreed.
INSERT INTO fora.publisher_domain_listings (
    tenant_id, domain, content_path_scope, terms, sitemap_url,
    import_replaces_manual_terms
) VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ActivatePublisherDomainListing :one
-- Records a manifest check that passed: the listing goes ACTIVE and both
-- timestamps move to the moment of the check. verified_at is set from the same
-- NOW() as last_checked_at so a reader can never see a verification that appears
-- to predate the check that produced it. Tenant-scoped.
--
-- Returns the row rather than a count, so a caller can tell a listing that is
-- not there (no rows) from one it wrote, and can read what it wrote inside the
-- same transaction. CheckDomain, the service's one caller, needs only the
-- first: it reads the listing back after its transaction commits.
UPDATE fora.publisher_domain_listings
   SET state = 'ACTIVE',
       verified_at = NOW(),
       last_checked_at = NOW(),
       updated_at = NOW()
 WHERE tenant_id = $1 AND domain = $2
RETURNING *;

-- name: RecordPublisherDomainListingCheck :execrows
-- Records a manifest check that did NOT pass. Only last_checked_at moves; the
-- state and verified_at are left alone deliberately, so the row keeps saying
-- when the site last proved itself. The proof is continuous and the push path is
-- what stops a site whose manifest has gone stale, so a failed check does not
-- revoke an active listing from here. Tenant-scoped.
UPDATE fora.publisher_domain_listings
   SET last_checked_at = NOW(),
       updated_at = NOW()
 WHERE tenant_id = $1 AND domain = $2;

-- name: ListImportEligibleListings :many
-- The listings one seat imports automatically: ACTIVE, with a sitemap
-- recorded. Every ACTIVE listing carries its licensing -- the table's
-- publisher_domain_listings_active_has_terms CHECK refuses one without it, and
-- registration validated it -- so the sitemap is the one setting that can be
-- missing, and an empty one means "not recorded": that listing is skipped,
-- never imported from a guess. The site row is joined for the payee, which the
-- importer logs with every outcome. Tenant-scoped: the importer runs for one
-- seat, the deployment's own.
SELECT l.tenant_id, l.domain, l.content_path_scope, l.sitemap_url, l.terms,
       l.import_replaces_manual_terms, d.publisher_id
  FROM fora.publisher_domain_listings l
  JOIN fora.publisher_domains d ON d.domain = l.domain
 WHERE l.tenant_id = $1
   AND l.state = 'ACTIVE'
   AND l.sitemap_url <> ''
 ORDER BY l.domain;

-- name: RecordListingImportOutcome :execrows
-- Writes what one import run did to one listing. The attempt timestamp always
-- moves; the success timestamp moves only when the outcome is complete, so the
-- row keeps saying when the catalog last matched the sitemap. The six counts
-- are written together with the outcome and replace the previous run's whole.
-- Tenant-scoped, and only the named row: a run for one seat's listing can
-- never touch another seat's listing of the same site.
UPDATE fora.publisher_domain_listings
   SET last_import_attempt_at = NOW(),
       last_import_success_at = CASE
           WHEN sqlc.arg(outcome)::fora.import_outcome = 'complete' THEN NOW()
           ELSE last_import_success_at
       END,
       last_import_outcome = sqlc.arg(outcome)::fora.import_outcome,
       last_import_failure_reason = sqlc.narg(failure_reason),
       last_import_entries = sqlc.arg(entries),
       last_import_accepted = sqlc.arg(accepted),
       last_import_refused = sqlc.arg(refused),
       last_import_unconfirmed = sqlc.arg(unconfirmed),
       last_import_unsent = sqlc.arg(unsent),
       last_import_skipped = sqlc.arg(skipped),
       updated_at = NOW()
 WHERE tenant_id = sqlc.arg(tenant_id) AND domain = sqlc.arg(domain);

-- name: CountPendingPublisherDomainListings :one
-- How many sites one payee has saved on one seat and not verified yet: the
-- listings still PENDING_MANIFEST. Registration reads it, after locking the
-- payee's row, to cap how many hostnames one payee can hold without proving any
-- of them. Joins the site row because the listing is keyed by domain and the
-- payee is a fact of the site. Tenant-scoped.
SELECT count(*) FROM fora.publisher_domain_listings l
  JOIN fora.publisher_domains d ON d.domain = l.domain
 WHERE l.tenant_id = $1 AND d.publisher_id = $2 AND l.state = 'PENDING_MANIFEST';

-- name: SetPendingPublisherDomainListingTerms :one
-- Sets the terms of a listing that is not verified yet: the licensing, the
-- sitemap and the import's take-over answer, which are chosen together on the
-- console's licensing step. The state is in the WHERE clause rather than only
-- checked beforehand, so a listing activated between the check and this write
-- is not changed: an ACTIVE listing is sold under its terms, and nothing here
-- changes them. No rows means the listing is not there or is ACTIVE.
-- Tenant-scoped.
UPDATE fora.publisher_domain_listings
   SET terms = sqlc.arg(terms),
       sitemap_url = sqlc.arg(sitemap_url),
       import_replaces_manual_terms = sqlc.arg(import_replaces_manual_terms),
       updated_at = NOW()
 WHERE tenant_id = sqlc.arg(tenant_id) AND domain = sqlc.arg(domain)
   AND state = 'PENDING_MANIFEST'
RETURNING *;
