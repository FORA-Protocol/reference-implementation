ALTER TABLE fora.publisher_domains
    DROP CONSTRAINT publisher_domains_cdn_supported,
    DROP COLUMN cdn;
