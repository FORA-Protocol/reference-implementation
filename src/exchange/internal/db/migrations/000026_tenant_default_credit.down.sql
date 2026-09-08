-- Reverse of 000026_tenant_default_credit.up.sql.

ALTER TABLE fora.tenants
    DROP COLUMN default_agent_credit;
