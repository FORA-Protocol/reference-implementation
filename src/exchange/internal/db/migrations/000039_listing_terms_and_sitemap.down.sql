ALTER TABLE fora.publisher_domain_listings
    DROP CONSTRAINT publisher_domain_listings_terms_shape,
    ADD COLUMN terms_preset TEXT NOT NULL DEFAULT 'free';

ALTER TABLE fora.publisher_domain_listings
    ALTER COLUMN terms_preset DROP DEFAULT,
    DROP COLUMN sitemap_url,
    DROP COLUMN terms;
