-- ListUnblockedExchanges returns every exchange the operator has not BLOCKED,
-- INCLUDING the ones whose last probe did not leave them routable, and carries
-- probe_state on each row so a caller decides for itself what to do with them.
-- The health refresher's read. It must see DOWN and REFUSED rows or it could
-- never probe one back to life, so it does not pre-filter routable rows.
-- BLOCKED rows stay excluded: the operator withdrew trust from them, so the
-- broker sends them no traffic at all, a health probe included.
-- exchange_id closes the ordering. Priority and trust_level do not break every
-- tie, so two rows equal on both came back in whichever order the planner chose,
-- and a caller reading position-dependent answers off this list got a different
-- one on each call.
-- name: ListUnblockedExchanges :many
SELECT * FROM broker.exchanges
 WHERE trust_level != 'BLOCKED'
 ORDER BY priority DESC, trust_level, exchange_id;

-- name: UpsertExchange :one
INSERT INTO broker.exchanges (
    exchange_id, domain, endpoint, trust_level, supported_profiles, priority
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (exchange_id) DO UPDATE
    SET domain = EXCLUDED.domain,
        endpoint = EXCLUDED.endpoint,
        trust_level = EXCLUDED.trust_level,
        supported_profiles = EXCLUDED.supported_profiles,
        priority = EXCLUDED.priority,
        updated_at = NOW()
RETURNING *;
