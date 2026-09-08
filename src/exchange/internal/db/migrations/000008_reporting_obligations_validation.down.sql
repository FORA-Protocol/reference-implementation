-- Mirror of 000008.up.sql in reverse order: drop the index, the obligation
-- columns, the tenant column, then the enum type.
--
-- NOTE: PostgreSQL has no ALTER TYPE … DROP VALUE; the BROKER label added to
-- fora.requester_type by the up migration cannot be removed by this down
-- without rebuilding the type and rewriting every dependent column. Since
-- the value is additive and harmless (no rows reference it unless the
-- application chooses to), we deliberately leave it in place on rollback.

DROP INDEX IF EXISTS fora.reporting_obligations_source_report_idx;

ALTER TABLE fora.reporting_obligations
    DROP COLUMN IF EXISTS required_fields,
    DROP COLUMN IF EXISTS estimated_quantity,
    DROP COLUMN IF EXISTS quantity_tolerance,
    DROP COLUMN IF EXISTS validation_outcome,
    DROP COLUMN IF EXISTS validated_at,
    DROP COLUMN IF EXISTS source_report_id,
    DROP COLUMN IF EXISTS issued_report_id;

ALTER TABLE fora.tenants
    DROP COLUMN IF EXISTS allow_broker_relay;

DROP TYPE IF EXISTS fora.validation_outcome;
