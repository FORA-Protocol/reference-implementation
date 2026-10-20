-- Reverse of 000036_tenant_site_signing_cutover.up.sql: put the two
-- site-shaped columns, and the constraint that spans them, back on the seat
-- row exactly as 000002 created them.
--
-- The restored columns come back EMPTY. The up migration moved no data, so this
-- direction has none to restore: every row returns on the 'ED25519' default
-- with a NULL key-pair id. A deployment rolling back past this point must
-- re-enter the scheme and the key-pair id for any tenant that signs with
-- CloudFront RSA, or the restored CHECK refuses the write that names the scheme.
ALTER TABLE fora.tenants
    ADD COLUMN signing_scheme         fora.signing_scheme NOT NULL DEFAULT 'ED25519',
    ADD COLUMN cloudfront_key_pair_id TEXT;

ALTER TABLE fora.tenants
    ADD CONSTRAINT tenants_rsa_fields_required
    CHECK (
        signing_scheme <> 'AWS_CLOUDFRONT_RSA'
        OR (rsa_key_ref IS NOT NULL AND cloudfront_key_pair_id IS NOT NULL)
    );
