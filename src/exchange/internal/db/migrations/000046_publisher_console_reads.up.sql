-- What the publisher console's reads need beyond the 000041 shape: the domain
-- each catalog row belongs to, and each sale's resource title, domain and
-- billing id on the publisher transactions view.
--
-- Domain: the publisher domain a catalog row was pushed under. The catalog
-- push stores the registered domain it matched the entry against, so every
-- read that narrows by domain -- the console's lists and totals, and the
-- automatic import's read of one site -- compares one stored value instead of
-- parsing the address. Registered domains are normalized, lower-case, so the
-- stored value is too. Rows that exist when this runs are backfilled once from
-- their address, whose host is the domain the push built it from; every later
-- row is written by the push.
ALTER TABLE fora.catalog ADD COLUMN domain text;
UPDATE fora.catalog SET domain = lower(split_part(split_part(uri, '://', 2), '/', 1));
ALTER TABLE fora.catalog ALTER COLUMN domain SET NOT NULL;
-- An empty string would satisfy NOT NULL and then match no filter at all, so a
-- write path that forgot the domain is refused instead.
ALTER TABLE fora.catalog ADD CONSTRAINT catalog_domain_not_empty CHECK (domain <> '');

-- The console narrows a seat's catalog by domain on every page, and the
-- automatic import reads one site of one seat.
CREATE INDEX IF NOT EXISTS catalog_tenant_domain_idx ON fora.catalog (tenant_id, domain);

-- Title: the transaction list shows each sale by the headline of the page that
-- was sold, not by its long address. catalog.title is nullable: a resource
-- ingested without one has none, and the page falls back to the address.
--
-- Billing id: the ledger hold a transaction's charge was reserved on and later
-- posted from. The ledger records whose account a settlement credited and
-- nothing about what was sold, so this id is how a settlement is traced back to
-- its transaction and, through it, to the resource that sold. NULL for a
-- transaction that charged nothing.
--
-- The new columns are appended: CREATE OR REPLACE VIEW only accepts new columns
-- at the end of the list. Readers name their columns, so the order is of no
-- consequence to them.
CREATE OR REPLACE VIEW fora.publisher_transactions AS
SELECT tl.transaction_id,
       tl.tenant_id,
       c.resource_owner_id AS publisher_id,
       tl.resource_id,
       c.uri AS resource_uri,
       tl.agent_id,
       tl.unit_cost,
       tl.currency,
       tl.created_at,
       c.title AS resource_title,
       c.domain AS resource_domain,
       tl.billing_id
  FROM fora.transaction_log tl
  JOIN fora.catalog c
    ON c.resource_id = tl.resource_id AND c.tenant_id = tl.tenant_id;
