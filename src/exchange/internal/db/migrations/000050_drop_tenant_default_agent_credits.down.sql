-- Restore the per-seat welcome-credit column in the shape 000037 created.
--
-- No value is carried back, because the up migration kept none: every seat comes
-- back with the empty map, which means no grant. A build that reads the column
-- writes EXCHANGE_DEFAULT_AGENT_CREDIT into the default seat at its next boot,
-- so rolling back to such a build restores the grant once it restarts.

ALTER TABLE fora.tenants
    ADD COLUMN default_agent_credits JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(default_agent_credits) = 'object');
