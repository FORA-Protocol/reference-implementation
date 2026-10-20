-- Publisher-scoped reads for the console. Which transactions are a publisher's
-- is decided once, by the fora.publisher_transactions view. Every query here
-- filters on the seat and on the publisher.

-- name: ListPublisherTransactionsPage :many
-- One page of the publisher's transactions in [period_start, period_end),
-- newest first. The keyset (created_at, transaction_id) is strictly below the
-- cursor, so a page never repeats the last row of the one before it. The caller
-- asks for one row more than the page size and reads the extra row as "there is
-- a next page". The obligation columns are the transaction's latest
-- obligation: an empty state and a NULL deadline when it minted none. The state
-- is read as text because the lateral join can leave it NULL, which the enum
-- type sqlc would otherwise generate cannot hold.
--
-- match_status narrows the page to the transactions whose latest obligation
-- meets one of three conditions: there is none (match_none), its state is in
-- match_open and its deadline has not passed as_of, or its state is in
-- match_past and the deadline has passed. The query decides no reporting
-- status. Which conditions make up a status is the service's rule, the same one
-- it labels each row with, and it hands this query the conditions it derived.
-- deadline is NOT NULL on the obligations table, so for a transaction with an
-- obligation neither comparison is null.
SELECT pt.transaction_id, pt.created_at, pt.resource_id, pt.resource_uri,
       COALESCE(pt.resource_title, '')::text AS resource_title, pt.agent_id,
       pt.unit_cost, pt.currency,
       COALESCE(ro.state::text, '')::text AS obligation_state, ro.deadline AS obligation_deadline
  FROM fora.publisher_transactions pt
  LEFT JOIN LATERAL (
        SELECT o.state, o.deadline
          FROM fora.reporting_obligations o
         WHERE o.transaction_id = pt.transaction_id
         ORDER BY o.created_at DESC
         LIMIT 1
       ) ro ON TRUE
 WHERE pt.tenant_id = @tenant_id
   AND pt.publisher_id = @publisher_id
   AND pt.created_at >= @period_start
   AND pt.created_at < @period_end
   AND (sqlc.narg(resource_id)::text IS NULL OR pt.resource_id = sqlc.narg(resource_id)::text)
   AND (sqlc.narg(domain)::text IS NULL OR pt.resource_domain = sqlc.narg(domain)::text)
   AND (NOT @match_status::boolean
        OR (ro.state IS NULL AND @match_none::boolean)
        OR (ro.deadline >= @as_of::timestamptz AND ro.state::text = ANY(@match_open::text[]))
        OR (ro.deadline < @as_of::timestamptz AND ro.state::text = ANY(@match_past::text[])))
   AND (sqlc.narg(key_created_at)::timestamptz IS NULL
        OR (pt.created_at, pt.transaction_id)
           < (sqlc.narg(key_created_at)::timestamptz, sqlc.narg(key_transaction_id)::text))
 ORDER BY pt.created_at DESC, pt.transaction_id DESC
 LIMIT @row_limit;

-- name: ListPublisherTransactionsPageBefore :many
-- ListPublisherTransactionsPage read the other way, for a Previous link: the
-- rows strictly above the keyset, oldest first, so the caller takes the ones
-- nearest to it. It takes the same arguments and returns the same columns;
-- only the keyset comparison and the order are turned around. Its filters must
-- stay the forward query's word for word, which the console-read tests hold
-- by walking every filtered list forward and back and comparing the pages.
SELECT pt.transaction_id, pt.created_at, pt.resource_id, pt.resource_uri,
       COALESCE(pt.resource_title, '')::text AS resource_title, pt.agent_id,
       pt.unit_cost, pt.currency,
       COALESCE(ro.state::text, '')::text AS obligation_state, ro.deadline AS obligation_deadline
  FROM fora.publisher_transactions pt
  LEFT JOIN LATERAL (
        SELECT o.state, o.deadline
          FROM fora.reporting_obligations o
         WHERE o.transaction_id = pt.transaction_id
         ORDER BY o.created_at DESC
         LIMIT 1
       ) ro ON TRUE
 WHERE pt.tenant_id = @tenant_id
   AND pt.publisher_id = @publisher_id
   AND pt.created_at >= @period_start
   AND pt.created_at < @period_end
   AND (sqlc.narg(resource_id)::text IS NULL OR pt.resource_id = sqlc.narg(resource_id)::text)
   AND (sqlc.narg(domain)::text IS NULL OR pt.resource_domain = sqlc.narg(domain)::text)
   AND (NOT @match_status::boolean
        OR (ro.state IS NULL AND @match_none::boolean)
        OR (ro.deadline >= @as_of::timestamptz AND ro.state::text = ANY(@match_open::text[]))
        OR (ro.deadline < @as_of::timestamptz AND ro.state::text = ANY(@match_past::text[])))
   AND (sqlc.narg(key_created_at)::timestamptz IS NULL
        OR (pt.created_at, pt.transaction_id)
           > (sqlc.narg(key_created_at)::timestamptz, sqlc.narg(key_transaction_id)::text))
 ORDER BY pt.created_at ASC, pt.transaction_id ASC
 LIMIT @row_limit;

-- name: CountPublisherTransactionsByBucket :many
-- How many of the publisher's transactions in the currency each bucket of a
-- totals read holds. bucket_starts is the sorted start of every bucket, and the
-- first one is period_start, so width_bucket numbers the buckets from 1 and a
-- bucket that holds no transaction has no row. Free transactions are counted:
-- they are sales of nothing, and the count says so. resource_id and domain,
-- when given, count one resource's or one domain's transactions only.
SELECT width_bucket(pt.created_at, @bucket_starts::timestamptz[])::int AS bucket,
       count(*) AS transactions
  FROM fora.publisher_transactions pt
 WHERE pt.tenant_id = @tenant_id
   AND pt.publisher_id = @publisher_id
   AND pt.currency = @currency
   AND pt.created_at >= @period_start
   AND pt.created_at < @period_end
   AND (sqlc.narg(resource_id)::text IS NULL OR pt.resource_id = sqlc.narg(resource_id)::text)
   AND (sqlc.narg(domain)::text IS NULL OR pt.resource_domain = sqlc.narg(domain)::text)
 GROUP BY 1;

-- name: PublisherScopedBillingIDs :many
-- Which of billing_ids are charges for the publisher on this seat, narrowed to
-- one resource and to one domain when those are given. The ledger hands the
-- revenue read the billing id of each settlement and nothing about what was
-- sold; this is how the read keeps only one resource's or one domain's money.
-- A billing id of another publisher, outside the narrowing, or of no
-- transaction at all is simply not returned.
SELECT pt.billing_id::text AS billing_id
  FROM fora.publisher_transactions pt
 WHERE pt.tenant_id = @tenant_id
   AND pt.publisher_id = @publisher_id
   AND (sqlc.narg(resource_id)::text IS NULL OR pt.resource_id = sqlc.narg(resource_id)::text)
   AND (sqlc.narg(domain)::text IS NULL OR pt.resource_domain = sqlc.narg(domain)::text)
   AND pt.billing_id = ANY(@billing_ids::text[]);

-- name: PublisherOwnsTransaction :one
-- Whether the transaction is one of the publisher's on this seat. A read of
-- another publisher's transaction and a read of a transaction that does not
-- exist get the same answer.
SELECT EXISTS (
    SELECT 1
      FROM fora.publisher_transactions pt
     WHERE pt.tenant_id = @tenant_id
       AND pt.publisher_id = @publisher_id
       AND pt.transaction_id = @transaction_id
) AS owned;

-- name: ListPublisherCatalogPage :many
-- One page of the publisher's resources, newest first, keyset on
-- (created_at, resource_id) as the transaction list is. The price of a resource
-- is on each of its terms; the legacy catalog.pricing column is not read.
-- resource_id and domain, when given, narrow the page the same way they narrow
-- the transaction reads.
SELECT c.resource_id, c.uri, c.title, c.terms, c.created_at, c.updated_at
  FROM fora.catalog c
 WHERE c.tenant_id = @tenant_id
   AND c.resource_owner_id = @publisher_id
   AND (sqlc.narg(resource_id)::text IS NULL OR c.resource_id = sqlc.narg(resource_id)::text)
   AND (sqlc.narg(domain)::text IS NULL OR c.domain = sqlc.narg(domain)::text)
   AND (sqlc.narg(key_created_at)::timestamptz IS NULL
        OR (c.created_at, c.resource_id)
           < (sqlc.narg(key_created_at)::timestamptz, sqlc.narg(key_resource_id)::text))
 ORDER BY c.created_at DESC, c.resource_id DESC
 LIMIT @row_limit;

-- name: ListPublisherCatalogPageBefore :many
-- ListPublisherCatalogPage read the other way, for a Previous link, as
-- ListPublisherTransactionsPageBefore is for the transactions. Its filters
-- must stay the forward query's word for word.
SELECT c.resource_id, c.uri, c.title, c.terms, c.created_at, c.updated_at
  FROM fora.catalog c
 WHERE c.tenant_id = @tenant_id
   AND c.resource_owner_id = @publisher_id
   AND (sqlc.narg(resource_id)::text IS NULL OR c.resource_id = sqlc.narg(resource_id)::text)
   AND (sqlc.narg(domain)::text IS NULL OR c.domain = sqlc.narg(domain)::text)
   AND (sqlc.narg(key_created_at)::timestamptz IS NULL
        OR (c.created_at, c.resource_id)
           > (sqlc.narg(key_created_at)::timestamptz, sqlc.narg(key_resource_id)::text))
 ORDER BY c.created_at ASC, c.resource_id ASC
 LIMIT @row_limit;

-- name: GetPublisherCatalogResource :one
-- One of the publisher's resources. Scoped on the seat and the publisher like
-- every read here, so naming another publisher's resource returns no row
-- rather than that publisher's data.
SELECT c.resource_id, c.uri, c.title, c.terms, c.created_at, c.updated_at
  FROM fora.catalog c
 WHERE c.tenant_id = @tenant_id
   AND c.resource_owner_id = @publisher_id
   AND c.resource_id = @resource_id;
