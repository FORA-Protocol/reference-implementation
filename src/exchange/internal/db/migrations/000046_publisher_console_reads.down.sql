-- CREATE OR REPLACE cannot remove a column, and the catalog's domain column
-- cannot be dropped while the view reads it, so the view is dropped and
-- recreated in its 000041 shape first.
DROP VIEW fora.publisher_transactions;

CREATE VIEW fora.publisher_transactions AS
SELECT tl.transaction_id,
       tl.tenant_id,
       c.resource_owner_id AS publisher_id,
       tl.resource_id,
       c.uri AS resource_uri,
       tl.agent_id,
       tl.unit_cost,
       tl.currency,
       tl.created_at
  FROM fora.transaction_log tl
  JOIN fora.catalog c
    ON c.resource_id = tl.resource_id AND c.tenant_id = tl.tenant_id;

DROP INDEX fora.catalog_tenant_domain_idx;
ALTER TABLE fora.catalog DROP COLUMN domain;
