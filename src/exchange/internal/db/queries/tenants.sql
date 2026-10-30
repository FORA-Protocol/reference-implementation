-- name: GetTenantByDomain :one
SELECT * FROM fora.tenants WHERE domain = $1;

-- name: GetTenantByID :one
SELECT * FROM fora.tenants WHERE tenant_id = $1;

-- name: InsertTenant :one
-- Creates one Exchange seat. `domain` is the hostname the seat answers on --
-- the value EXCHANGE_DOMAIN carries -- not a publisher's site. The signing
-- scheme and the CloudFront key-pair id are NOT here: those describe one site
-- and live on fora.publisher_domains. What the seat holds is the private-key
-- side, ed25519_key_ref and rsa_key_ref.
INSERT INTO fora.tenants (
    tenant_id, domain, ed25519_key_ref, reporting_policy, rsa_key_ref
) VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreateTenantIfAbsent :one
-- Creates a seat an operator's boot fixture declares (ADR-029), with both of its
-- policies, unless a seat already holds this tenant id or this domain. A stored
-- seat is left exactly as it is, whatever the fixture says now, so a boot after
-- the first changes nothing that a product surface or an operator set since. A
-- conflict on either key returns no row; the caller reads the seat back by its
-- id to tell a seat that is already stored from a domain another seat holds.
INSERT INTO fora.tenants (
    tenant_id, domain, ed25519_key_ref, rsa_key_ref,
    allow_broker_relay, activate_new_agents_by_default
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: SetTenantAllowBrokerRelay :execrows
-- Flips the broker-relay opt-in for a tenant. Admin / fixture path; the
-- column defaults to FALSE on insert so this is only needed when a tenant
-- explicitly opts into broker-on-behalf reporting.
-- Returns rows-affected so a call for a missing tenant is a detectable no-op
-- rather than a silent success.
UPDATE fora.tenants
   SET allow_broker_relay = $2
 WHERE tenant_id = $1;

-- name: SetTenantReportingPolicy :execrows
-- Replaces the reporting_policy JSONB for a tenant. The admin SetReportingPolicy
-- RPC write path (also used by fixtures to seed required_fields,
-- quantity_tolerance, or window_seconds without bypassing the repo layer).
-- Returns rows-affected so the caller can tell a real update from a no-op on a
-- missing tenant.
UPDATE fora.tenants
   SET reporting_policy = $2
 WHERE tenant_id = $1;

-- name: SetTenantActivateNewAgentsByDefault :execrows
-- Flips the per-tenant policy for whether a newly registered agent starts
-- active in the billing system-of-record. Admin / fixture path; the column
-- defaults to TRUE on insert, so this is only needed to opt a tenant out.
-- Mirrors SetTenantAllowBrokerRelay.
-- Returns rows-affected so a call for a missing tenant is a detectable no-op
-- rather than a silent success.
UPDATE fora.tenants
   SET activate_new_agents_by_default = $2
 WHERE tenant_id = $1;

-- name: SetTenantFeeRateBps :execrows
-- Replaces the tenant-level default commission rate (basis points) and the
-- operator note in one write. The admin SetTenantFeeRate RPC write path (also a
-- fixture mutator). Full replace: fee_rate_notes is set to $3, which is NULL
-- when the operator omits it. Returns rows-affected so a call for a missing
-- tenant is a detectable no-op rather than a silent success.
UPDATE fora.tenants
   SET fee_rate_bps = $2,
       fee_rate_notes = $3
 WHERE tenant_id = $1;
