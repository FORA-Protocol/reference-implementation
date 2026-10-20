-- Reverse of 000042_listing_import_outcome.up.sql. sitemap_url and terms are
-- 000039's and stay.

ALTER TABLE fora.publisher_domain_listings
    DROP COLUMN IF EXISTS last_import_unsent,
    DROP COLUMN IF EXISTS last_import_unconfirmed,
    DROP COLUMN IF EXISTS last_import_refused,
    DROP COLUMN IF EXISTS last_import_accepted,
    DROP COLUMN IF EXISTS last_import_entries,
    DROP COLUMN IF EXISTS last_import_failure_reason,
    DROP COLUMN IF EXISTS last_import_outcome,
    DROP COLUMN IF EXISTS last_import_success_at,
    DROP COLUMN IF EXISTS last_import_attempt_at;

DROP TYPE IF EXISTS fora.import_outcome;
