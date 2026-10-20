-- Move the site-shaped signing settings off the seat row (per ADR-026 D5, as
-- amended 2026-09-18).
--
-- fora.tenants is the Exchange SEAT: one identity that sells on this
-- deployment, holding the seller settings and the URL-signing PRIVATE key
-- references (ed25519_key_ref, rsa_key_ref). Those stay. What leaves is the
-- pair that describes one publisher's site rather than the seat:
--
--   * signing_scheme         -- which edge verifies that site's delivery URLs
--   * cloudfront_key_pair_id -- the id CloudFront assigned in the publisher's
--                               own AWS account for that site
--
-- Both now live on fora.publisher_domains, written by 000034. URL minting reads
-- the scheme and the key-pair id from the site row and the private-key
-- reference from the seat row, so one seat can sell a CloudFront site and a
-- Cloudflare site at once -- the constraint that pinned a tenant to a domain.
--
-- fora.signing_scheme (the enum type) is NOT dropped: publisher_domains uses it.
--
-- No backfill accompanies this. A deployment that already holds tenant rows
-- holds them under the pre-cutover meaning of tenants.domain -- a publisher's
-- site, not the Exchange's hostname -- so these values cannot be moved onto a
-- publisher_domains row without first deciding which publisher each site
-- belongs to, and that decision is registration, not a migration step.

-- The CHECK spans signing_scheme and cloudfront_key_pair_id, so it goes with
-- them. The equivalent rule does not follow the columns to the site table: a
-- site legitimately exists with a NULL key-pair id between registration and the
-- publisher uploading the public key to their own AWS account, so
-- publisher_domains enforces only that a present id is non-empty.
ALTER TABLE fora.tenants DROP CONSTRAINT IF EXISTS tenants_rsa_fields_required;

ALTER TABLE fora.tenants
    DROP COLUMN cloudfront_key_pair_id,
    DROP COLUMN signing_scheme;
