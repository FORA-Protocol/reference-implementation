-- Which catalog rows the automatic import may refresh, and what it does with
-- the pages a site already has.
--
-- fora.catalog.auto_imported records, per row, whether the last accepted push
-- that wrote the row carried the automatic import's label: the request header
-- X-Fora-Catalog-Source with the value auto-import. The Exchange sets it on
-- every accepted push -- true when the push carried the label, false
-- otherwise -- so the last push decides. The importer refreshes the rows it
-- wrote itself and leaves every other row to whoever pushed it. Every row that
-- exists when this migration runs reads false: a row whose source is unknown
-- is protected, and nothing here guesses which rows an earlier import wrote.
-- The label is not a trust boundary and is used for nothing else.
--
-- fora.publisher_domain_listings.import_replaces_manual_terms is the
-- publisher's answer, given at registration, to whether the automatic import
-- may take over the pages another source pushed for the site. When it may, it
-- replaces their terms and keeps their content id, title and metadata. It
-- belongs to the listing, like the sitemap and the licensing: what the import
-- does with a site's pages is part of the arrangement between the publisher
-- and the seat (see ADR-026, amendment of 2026-09-18). Listings registered
-- before this migration read false.
--
-- fora.publisher_domain_listings.last_import_skipped counts the entries the
-- last run built from the sitemap but did not send: pages another source
-- pushed, and pages the importer cannot push again under their row's identity.
-- With it six counts partition last_import_entries:
-- skipped + accepted + refused + unconfirmed + unsent = entries. A run is
-- complete when every entry it did not skip was accepted, so a run that
-- skipped every entry is complete.
ALTER TABLE fora.catalog
    ADD COLUMN auto_imported BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE fora.publisher_domain_listings
    ADD COLUMN import_replaces_manual_terms BOOLEAN NOT NULL DEFAULT false,
    ADD COLUMN last_import_skipped          INTEGER NOT NULL DEFAULT 0;
