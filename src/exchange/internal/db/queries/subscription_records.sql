-- name: InsertSubscriptionRecord :one
-- Creates the trust record of one subscription scope under one seat. Fixture and
-- test path; the UNIQUE (tenant_id, scope) refuses a second record for the scope.
INSERT INTO fora.subscription_records (record_id, tenant_id, scope, issuer_identity, issuer_keys)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UpsertSubscriptionRecord :one
-- Creates the seat's record for the scope, or replaces the issuer and the trusted
-- root keys of the one it already holds. The database owns the create-or-replace
-- decision through the (tenant_id, scope) constraint, so two concurrent writes
-- cannot both see "no record". A key missing from the new list is an explicit
-- withdrawal of trust: a chain rooted in it stops verifying at once. ended_at is
-- left as it is: replacing keys does not reopen an ended record.
INSERT INTO fora.subscription_records (record_id, tenant_id, scope, issuer_identity, issuer_keys)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, scope) DO UPDATE
   SET issuer_identity = EXCLUDED.issuer_identity,
       issuer_keys     = EXCLUDED.issuer_keys
RETURNING *;

-- name: GetSubscriptionRecordByTenantAndScope :one
-- The read the delegation verifier makes: the record that owns this scope in
-- this seat, ended or not. An ended record still answers with its keys.
SELECT * FROM fora.subscription_records
 WHERE tenant_id = $1 AND scope = $2;

-- name: EndSubscriptionRecord :execrows
-- Marks the record ended. The key list is left as it is on purpose. Keyed by
-- tenant as well as record id: a record id from another seat updates nothing.
UPDATE fora.subscription_records
   SET ended_at = NOW()
 WHERE tenant_id = $1 AND record_id = $2 AND ended_at IS NULL;
