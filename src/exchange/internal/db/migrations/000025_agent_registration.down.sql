-- Reverse of 000025_agent_registration.up.sql.

ALTER TABLE fora.agents
    DROP COLUMN billing_ref;

ALTER TABLE fora.tenants
    DROP COLUMN activate_new_agents_by_default;
