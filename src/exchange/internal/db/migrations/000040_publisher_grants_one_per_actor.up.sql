-- One publisher per person per seat, held by the database.
--
-- Registration mints one publisher for each console account on a seat and
-- answers a repeat with the publisher the account already holds. Before this
-- index that answer depended on a read made before the write: two submits of
-- the registration form that were in flight together both read "no grant yet",
-- both minted a publisher, and both committed, leaving the person with two
-- payees. With this index the second grant insert waits for the first
-- transaction and then fails with a unique violation. The registration service
-- rolls its own transaction back and returns the publisher the first one
-- committed, so both submits get the same answer.
--
-- The index replaces publisher_grants_actor_idx rather than adding a second
-- index on the same columns: it serves the same read, "which publisher may this
-- person act for on this seat", which every console request makes first.
--
-- If a deployment already holds two grants for one person on one seat, this
-- migration fails on the CREATE and leaves the schema untouched. That is on
-- purpose: which of the two publishers the person keeps is an operator
-- decision, and a migration that picked one would delete a payee silently.
CREATE UNIQUE INDEX publisher_grants_one_actor_idx
    ON fora.publisher_grants (tenant_id, oidc_issuer, oidc_subject);

DROP INDEX fora.publisher_grants_actor_idx;
