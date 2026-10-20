-- What the automatic catalog import did to a listing on its last run.
--
-- The import reads its two settings from columns registration writes
-- (000039): sitemap_url, where the listing's pages are listed, and terms, the
-- licensing the publisher chose, which the importer prices in the
-- deployment's denomination on every run. This migration adds only what the
-- importer writes back. Both belong to the listing, not to the site: what a
-- publisher offers through one seat is the commercial arrangement, and the
-- site row holds only physical facts (see ADR-026, amendment of 2026-09-18).
--
-- One row per listing is the whole record: the importer has no run history
-- table.
--
--   complete     every entry built from the sitemap was accepted by the Exchange
--                (a valid empty sitemap is complete with zero entries)
--   failed       nothing was accepted: the licensing could not be turned into
--                license terms, the sitemap could not be walked, an entry
--                failed validation, or the Exchange refused the first
--                submission
--   partial      at least one submission was accepted and a later one was
--                refused or never sent
--   unconfirmed  at least one submission got no verdict from the Exchange, so
--                its entries may or may not be stored
--
-- last_import_success_at moves only on a complete run. The five counts are
-- taken from the push report's explicit per-submission results: refused counts
-- only submissions the Exchange answered with a verdict, unconfirmed those
-- without one, unsent the entries that never left the importer. Rejection is
-- never derived as submitted minus accepted.
CREATE TYPE fora.import_outcome AS ENUM (
    'complete',
    'failed',
    'partial',
    'unconfirmed'
);

ALTER TABLE fora.publisher_domain_listings
    ADD COLUMN last_import_attempt_at     TIMESTAMPTZ,
    ADD COLUMN last_import_success_at     TIMESTAMPTZ,
    ADD COLUMN last_import_outcome        fora.import_outcome,
    ADD COLUMN last_import_failure_reason TEXT,
    ADD COLUMN last_import_entries        INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_import_accepted       INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_import_refused        INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_import_unconfirmed    INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN last_import_unsent         INTEGER NOT NULL DEFAULT 0;
