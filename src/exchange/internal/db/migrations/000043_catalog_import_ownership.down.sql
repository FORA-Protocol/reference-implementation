-- Reverse of 000043_catalog_import_ownership.up.sql. The listing's other import
-- columns are 000039's and 000042's and stay.

ALTER TABLE fora.publisher_domain_listings
    DROP COLUMN IF EXISTS last_import_skipped,
    DROP COLUMN IF EXISTS import_replaces_manual_terms;

ALTER TABLE fora.catalog
    DROP COLUMN IF EXISTS auto_imported;
