-- The transaction event journal: an append-only record of transaction decisions,
-- starting with the one no other table can hold — a denied item.
--
-- A denied item is answered in the ExecuteTransaction response and was written
-- nowhere. transaction_log cannot hold it: a row there is an issued URL, its
-- signed_url_hash, expiry and agent_identity_hash are NOT NULL, and its UNIQUE
-- idempotency_key is what replay and the double-charge backstop read as "this
-- item sold". So a denial is an event in its own table.
--
-- One row is one BILLING_AUTHORIZATION decision that refused an item. Every row
-- carries its denial_reason; authorized=false is implied, and is not a column,
-- because nothing else is journaled. Journaling approvals later needs a column
-- that says which decision a row records, and denial_reason then stops being
-- NOT NULL.
--
-- WHICH DENIALS ARE WRITTEN. Only those the Exchange can attribute to a
-- publisher: the Exchange signature on the offer verified AND the offer's
-- catalog entry resolved. An offer whose signature fails, and an offer whose
-- resource is gone from the catalog, write nothing. So does any request that did
-- not prove possession of the agent key. The service enforces both rules; the
-- table cannot.
--
-- DECISION-TIME VALUES. resource_id and resource_owner_id are copied from the
-- catalog entry when the item is refused. They are values, not references: a
-- catalog re-push can move a resource to another owner, and the denial stays
-- with the publisher that owned the resource when it was refused. created_at is
-- that decision time on the Exchange's own clock, so it has no default.
--
-- ONE ROW PER (agent, item key). idempotency_key is the derived per-item key —
-- the request's idempotency key, ':', and the offer id — the value
-- transaction_log and the billing adapter use for the same item. The same denial
-- can be raised again for one key (an older client's retry re-executes, an
-- unfinalized request re-runs), and the insert is ON CONFLICT DO NOTHING against
-- the UNIQUE below. It is scoped per agent, as the protocol scopes idempotency:
-- per authenticated caller, never globally.
--
-- APPEND-ONCE. UPDATE, DELETE and TRUNCATE are refused for every role by the
-- triggers below, exactly as for fora.transaction_evidence. REVOKE is omitted
-- for the same reason as there: every environment connects as the database
-- owner, which REVOKE does not restrict. The refusal function is shared: the
-- evidence table's function is renamed and generalized here so both tables name
-- themselves in the error, and the evidence triggers stay bound to it, raising
-- the same message they always have.
--
-- RETENTION. Rows are never removed: the triggers refuse DELETE and TRUNCATE,
-- the table is not partitioned, and the only statement that removes a row is
-- DROP TABLE. Every text column is therefore length-bounded or copied from a
-- bounded source: offer_id is minted by this Exchange as a UUID and journaled
-- only when the Exchange signature over it verified, and idempotency_key is at
-- most the 256-byte request-key cap, the separator and the offer id.

CREATE TYPE fora.transaction_event_type AS ENUM ('BILLING_AUTHORIZATION');

CREATE TABLE fora.transaction_events (
    event_id          TEXT PRIMARY KEY                    -- UUIDv7, minted by the Exchange
        CHECK (event_id <> ''),
    event_type        fora.transaction_event_type NOT NULL,
    created_at        TIMESTAMPTZ NOT NULL,               -- decision time, Exchange clock
    tenant_id         TEXT NOT NULL CHECK (tenant_id <> '')
        REFERENCES fora.tenants (tenant_id) ON DELETE RESTRICT,
    idempotency_key   TEXT NOT NULL
        CHECK (octet_length(idempotency_key) BETWEEN 1 AND 512),
    offer_id          TEXT NOT NULL
        CHECK (octet_length(offer_id) BETWEEN 1 AND 255),
    agent_id          TEXT NOT NULL CHECK (agent_id <> '')
        REFERENCES fora.agents (agent_id) ON DELETE RESTRICT,
    resource_id       TEXT NOT NULL CHECK (resource_id <> ''),
    resource_owner_id TEXT NOT NULL CHECK (resource_owner_id <> ''),
    denial_reason     fora.denial_reason NOT NULL,
    CONSTRAINT transaction_events_one_per_agent_key
        UNIQUE (event_type, agent_id, idempotency_key)
);

-- The one read: a publisher's denials on one seat, over a period.
CREATE INDEX transaction_events_publisher_idx
    ON fora.transaction_events (tenant_id, resource_owner_id, created_at);

-- One refusal function for every append-once table. Renaming keeps the
-- function's identity, so the two transaction_evidence triggers stay bound to it
-- without being touched; the new body names the table the trigger fired on, which
-- for the evidence table is the message it has always raised.
ALTER FUNCTION fora.prevent_evidence_mutation() RENAME TO refuse_append_once_mutation;

CREATE OR REPLACE FUNCTION fora.refuse_append_once_mutation() RETURNS TRIGGER AS $$
BEGIN
    RAISE EXCEPTION '%.% is append-once: % is prohibited', TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_transaction_events_no_row_mutation
    BEFORE UPDATE OR DELETE ON fora.transaction_events
    FOR EACH ROW EXECUTE FUNCTION fora.refuse_append_once_mutation();

CREATE TRIGGER trg_transaction_events_no_truncate
    BEFORE TRUNCATE ON fora.transaction_events
    FOR EACH STATEMENT EXECUTE FUNCTION fora.refuse_append_once_mutation();
