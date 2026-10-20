-- Index the transaction log on billing_id.
--
-- A totals read narrowed to one resource or one domain traces the ledger's
-- settlements back to their sales: it holds the settlements back in batches
-- and asks the log which of a batch's billing ids are charges inside the
-- narrowing (billing_id = ANY(...)). No index covered billing_id, so each
-- batch read the seat's whole transaction history. Partial on NOT NULL: a
-- transaction that charged nothing has no billing id, and no settlement can
-- name one.
--
-- Built blocking, not CONCURRENTLY, for the reason 000030 gives: migrations
-- run at service start, and a concurrent build that fails part way leaves an
-- INVALID index and a dirty schema_migrations row that the boot-time migrator
-- cannot repair on its own. The blocking build fails cleanly. Its cost is a
-- SHARE lock on transaction_log for the duration, which blocks new
-- transactions while it runs.
CREATE INDEX transaction_log_billing_id_idx
    ON fora.transaction_log (billing_id)
    WHERE billing_id IS NOT NULL;
