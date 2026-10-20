-- name: GetPublisher :one
-- Resolves one payee by its minted id. Deployment-wide by design: the id groups
-- an owner's sites across every seat that sells them, so there is no tenant_id
-- to filter on here.
SELECT * FROM fora.publishers WHERE publisher_id = $1;

-- name: CreatePublisher :one
-- Records a newly minted payee. Registration runs this and the caller's grant in
-- one transaction, so a publisher can never exist with nobody able to act for
-- it. No ON CONFLICT: the id is minted per registration, so a collision is a
-- broken minter and must surface rather than silently return someone else's row.
INSERT INTO fora.publishers (publisher_id, display_name)
VALUES ($1, $2)
RETURNING *;

-- name: LockPublisher :one
-- Takes the payee's row lock for the rest of the transaction, so registrations
-- for one payee that count what it already holds run one after another rather
-- than each counting before the other has written. FOR NO KEY UPDATE rather
-- than FOR UPDATE: it conflicts with itself, which is all the registrations
-- need, and not with the KEY SHARE lock a foreign-key check takes, so writes
-- that only reference the payee -- a site, a grant -- are not held up by it.
-- Deployment-wide for the reason GetPublisher gives.
SELECT publisher_id FROM fora.publishers WHERE publisher_id = $1 FOR NO KEY UPDATE;
