-- Close the payee namespace: resource_owner_id becomes a real reference to a
-- registered publisher (per ADR-026 D1 and D2).
--
-- fora.catalog.resource_owner_id was added NOT NULL DEFAULT '' so the ALTER
-- would be instant on a table that might already hold rows. The default is
-- exactly what let a row exist with no payee, and a foreign key cannot coexist
-- with it -- '' would have to name a publisher. It goes with the constraint.
--
-- No backfill accompanies this. No deployment holds catalog rows, so there is
-- nothing to reconcile and no migration step that could reconcile it: the
-- publisher ids a backfill would need do not exist until someone registers
-- them. An operator restoring a database that predates this migration must
-- register each distinct resource_owner_id as a publisher first, or the
-- constraint refuses to validate and names the offending rows.
ALTER TABLE fora.catalog
    ALTER COLUMN resource_owner_id DROP DEFAULT;

ALTER TABLE fora.catalog
    ADD CONSTRAINT catalog_resource_owner_fk
    FOREIGN KEY (resource_owner_id) REFERENCES fora.publishers (publisher_id) ON DELETE RESTRICT;

-- The per-(tenant, resource owner) commission override keys on the same payee.
-- It was created without a foreign key because the payee had no table to point
-- at; it has one now. RESTRICT on the catalog above and CASCADE here is not an
-- inconsistency: a catalog row is licensed inventory that a delete would strand
-- mid-transaction, while an override is a commercial setting that means nothing
-- once the owner it overrides is gone.
ALTER TABLE fora.tenant_resource_owner_fee
    ADD CONSTRAINT tenant_resource_owner_fee_publisher_fk
    FOREIGN KEY (resource_owner_id) REFERENCES fora.publishers (publisher_id) ON DELETE CASCADE;
