CREATE INDEX publisher_grants_actor_idx
    ON fora.publisher_grants (tenant_id, oidc_issuer, oidc_subject);

DROP INDEX fora.publisher_grants_one_actor_idx;
