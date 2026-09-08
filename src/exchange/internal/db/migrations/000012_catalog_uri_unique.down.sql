-- Reverse of 000012_catalog_uri_unique.up.sql: drop the URI ownership constraint.

ALTER TABLE fora.catalog
    DROP CONSTRAINT catalog_uri_key;
