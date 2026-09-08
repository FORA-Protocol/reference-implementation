DROP TABLE IF EXISTS fora.transaction_reports;
DROP TABLE IF EXISTS fora.report_tokens;
DROP TABLE IF EXISTS fora.offers;
DROP INDEX IF EXISTS fora.transaction_log_resource_id_idx;
ALTER TABLE fora.transaction_log
    ADD CONSTRAINT transaction_log_resource_id_fkey
    FOREIGN KEY (resource_id) REFERENCES fora.catalog (resource_id) ON DELETE RESTRICT;
ALTER TABLE fora.transaction_log DROP COLUMN IF EXISTS lifecycle;
DROP TYPE IF EXISTS fora.offer_type;
DROP TYPE IF EXISTS fora.transaction_lifecycle;
