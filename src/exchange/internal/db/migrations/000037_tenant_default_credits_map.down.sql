-- Restore the single-currency welcome credit column.
--
-- The per-currency amounts are NOT carried back: one NUMERIC cannot hold a map,
-- and picking one currency's amount to keep would be a guess. The column comes
-- back at its original default of 0, and the next boot writes whatever
-- EXCHANGE_DEFAULT_AGENT_CREDIT holds — which is the same channel that owns the
-- setting in either direction.

ALTER TABLE fora.tenants
    DROP COLUMN default_agent_credits;

ALTER TABLE fora.tenants
    ADD COLUMN default_agent_credit NUMERIC(20,8) NOT NULL DEFAULT 0
        CHECK (default_agent_credit >= 0 AND default_agent_credit <> 'NaN'::numeric);
