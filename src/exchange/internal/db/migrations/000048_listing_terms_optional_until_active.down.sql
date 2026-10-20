-- Reverses 000048. A listing saved without terms cannot exist under the NOT
-- NULL column this restores, so it is deleted rather than given terms: the
-- code before 000048 activates a pending listing whenever its manifest passes,
-- so terms written here would be sold although the publisher never chose them.
--
-- Only the listing goes. The site row stays, so the hostname stays with the
-- publisher who saved it, and they can list it again from the console.
DELETE FROM fora.publisher_domain_listings WHERE terms IS NULL;

ALTER TABLE fora.publisher_domain_listings
    DROP CONSTRAINT publisher_domain_listings_active_has_terms,
    ALTER COLUMN terms SET NOT NULL;
