-- The transaction event journal, fora.transaction_events (migration 000044).
-- Rows are appended and never changed.

-- name: AppendTransactionEvent :execrows
-- Appends one event. A second event with the same (event_type, agent_id,
-- idempotency_key) is dropped, so a denial raised again for the same agent and
-- item key is recorded once. The affected-row count tells the caller which of
-- the two happened.
INSERT INTO fora.transaction_events (
    event_id, event_type, created_at, tenant_id, idempotency_key,
    offer_id, agent_id, resource_id, resource_owner_id, denial_reason
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (event_type, agent_id, idempotency_key) DO NOTHING;

-- name: CountPublisherDenialsByReason :many
-- How many of one publisher's items this seat refused in
-- [period_start, period_end), per denial reason. The publisher is the resource
-- owner recorded when each item was refused, not the catalog entry's current
-- owner. Reasons with no denial in the period are absent.
SELECT e.denial_reason, count(*) AS denials
  FROM fora.transaction_events e
 WHERE e.tenant_id = @tenant_id
   AND e.resource_owner_id = @publisher_id
   AND e.event_type = 'BILLING_AUTHORIZATION'
   AND e.created_at >= @period_start
   AND e.created_at < @period_end
 GROUP BY e.denial_reason;
