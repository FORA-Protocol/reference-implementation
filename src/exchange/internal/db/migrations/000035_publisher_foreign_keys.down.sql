-- Reverse of 000035_publisher_foreign_keys.up.sql.

ALTER TABLE fora.tenant_resource_owner_fee
    DROP CONSTRAINT IF EXISTS tenant_resource_owner_fee_publisher_fk;

ALTER TABLE fora.catalog
    DROP CONSTRAINT IF EXISTS catalog_resource_owner_fk;

ALTER TABLE fora.catalog
    ALTER COLUMN resource_owner_id SET DEFAULT '';
