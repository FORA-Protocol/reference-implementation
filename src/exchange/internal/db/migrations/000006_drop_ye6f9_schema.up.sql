-- Drop the 000005_offers_and_reports schema.
--
-- The proto-rename epic renames the local repo to upstream proto
-- (DiscoverResources / ExecuteTransaction / ReportUsage). The legacy
-- ListOffers / AcceptOffer / Report bindings backed by fora.offers,
-- fora.report_tokens, fora.transaction_reports, and the
-- transaction_log.lifecycle column are removed in W1. This migration drops
-- the DB schema those RPCs depended on so the catalog / discover-and-execute
-- path becomes the sole supported flow.
--
-- Mirror of 000005_offers_and_reports.up.sql in reverse order: drop the
-- tables (transaction_reports → report_tokens → offers) before the enum
-- types they reference, restore the transaction_log resource_id FK that
-- 000005 relaxed to allow the legacy AcceptOffer path to write offer-id-bearing rows,
-- then drop the lifecycle column and its enum.

DROP TABLE IF EXISTS fora.transaction_reports CASCADE;
DROP TABLE IF EXISTS fora.report_tokens CASCADE;
DROP TABLE IF EXISTS fora.offers CASCADE;

-- Restore the FK relaxed in 000005 (replaces the transaction_log_resource_id_idx
-- index 000005 added in lieu of the constraint).
DROP INDEX IF EXISTS fora.transaction_log_resource_id_idx;
ALTER TABLE fora.transaction_log
    ADD CONSTRAINT transaction_log_resource_id_fkey
    FOREIGN KEY (resource_id) REFERENCES fora.catalog (resource_id) ON DELETE RESTRICT;

ALTER TABLE fora.transaction_log DROP COLUMN IF EXISTS lifecycle;

DROP TYPE IF EXISTS fora.offer_type;
DROP TYPE IF EXISTS fora.transaction_lifecycle;
