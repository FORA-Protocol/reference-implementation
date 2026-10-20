-- name: HasPublisherGrant :one
-- Answers the question every console request asks before it touches anything:
-- may this person act for this payee on this seat? Tenant-scoped.
SELECT EXISTS (
    SELECT 1 FROM fora.publisher_grants
     WHERE tenant_id = $1 AND publisher_id = $2
       AND oidc_issuer = $3 AND oidc_subject = $4
);

-- name: ListPublisherGrantsForActor :many
-- Every payee one person may act for on one seat, for the console's publisher
-- picker. Tenant-scoped.
SELECT publisher_id FROM fora.publisher_grants
 WHERE tenant_id = $1 AND oidc_issuer = $2 AND oidc_subject = $3
 ORDER BY publisher_id;

-- name: CreatePublisherGrant :exec
-- Grants one person the right to act for one payee on one seat. Runs in the same
-- transaction as the publisher it grants, so a registration cannot commit a
-- payee nobody can reach. ON CONFLICT DO NOTHING makes re-granting an existing
-- pair a no-op rather than an error: the grant is a set membership, and adding a
-- member twice is the same set.
INSERT INTO fora.publisher_grants (tenant_id, publisher_id, oidc_issuer, oidc_subject)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, publisher_id, oidc_issuer, oidc_subject) DO NOTHING;
