-- The transactions that belong to each publisher, defined once.
--
-- A transaction belongs to the publisher its catalog entry names as resource
-- owner. transaction_log carries no publisher column, so every publisher read
-- joins it to catalog. Before this view the join and its tenant condition were
-- written out in each query that needed it, and a change to one copy would
-- have let the transaction list, the transaction detail and the revenue totals
-- disagree about which sales are a publisher's.
--
-- The join matches on tenant_id as well as resource_id, so a catalog row of one
-- seat can never vouch for a transaction of another. Readers still filter on
-- tenant_id and publisher_id themselves: the view names the relationship, the
-- caller names the scope.
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
