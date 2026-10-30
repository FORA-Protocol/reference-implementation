-- Subscription records: the trust anchor of a subscription-gated licensing term.
--
-- A licensing term whose scopes carry one concrete 'subscription:' scope is
-- gated. The Exchange presents it at discovery, and executes a purchase of it,
-- only when the delegation chain in Requester.delegation verifies under one of
-- the Ed25519 root keys stored on the record that owns that scope, the chain is
-- bound to the key the request proved, and the verified scopes cover the term.
-- The record is the ONLY place a trusted root key comes from: a key the
-- publisher never put on its record grants nothing, and removing a key from the
-- list withdraws the trust it carried.
--
-- One row per (tenant, scope): a scope names exactly one subscription of one
-- seat, so a second record for the same scope in the same seat is a mistake,
-- not a second issuer. The same scope string in another seat is a different
-- record and its keys never cross over. Tenant-scoped like every other table of
-- this kind: every read AND every write filters by tenant_id, so a record id
-- from another seat names nothing.
--
-- Records are ended, never deleted, and ending does not touch the key list: a
-- purchase already made under the record stays attributable to the keys that
-- authorised it. Key replacement is an edit of issuer_keys.
--
-- issuer_keys holds raw 32-byte Ed25519 public keys. record_id is minted by the
-- repository (no column default: the one that was here could never fire).
-- Administration of these rows (an admin RPC, a console page) is not part of
-- this schema; the boot fixture and the repository writes are the only
-- producers today.

CREATE TABLE fora.subscription_records (
    record_id       TEXT        PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    scope           TEXT        NOT NULL,
    issuer_identity TEXT        NOT NULL,
    issuer_keys     BYTEA[]     NOT NULL,
    ended_at        TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT subscription_records_tenant_fk
        FOREIGN KEY (tenant_id) REFERENCES fora.tenants (tenant_id) ON DELETE CASCADE,
    CONSTRAINT subscription_records_tenant_scope_key UNIQUE (tenant_id, scope)
);
