-- Reverse of 000034_publisher_registration.up.sql.

DROP TABLE IF EXISTS fora.publisher_grants;
DROP TABLE IF EXISTS fora.publisher_domain_listings;
DROP TABLE IF EXISTS fora.publisher_domains;
DROP TABLE IF EXISTS fora.publishers;

DROP TYPE IF EXISTS fora.listing_state;
