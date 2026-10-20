-- The CDN a site sits behind, recorded on the site row.
--
-- WHY A COLUMN AND NOT A DERIVATION. Every supported integration verifies the
-- delivery URL inside an edge function and therefore signs with Ed25519, so
-- signing_scheme no longer tells the three apart: a Cloudflare site, a Fastly
-- site and a CloudFront site all carry 'ED25519'. The console still has to know
-- which one, because the configuration and the deployment instructions it
-- generates differ per integration and it regenerates them from the stored
-- registration rather than asking the publisher again.
--
-- WHY ON THE SITE. The amendment's rule: the site row holds physical facts
-- about the site, the listing row holds the commercial arrangement. Which CDN
-- serves a site is a fact about the site, true whichever seat sells it -- the
-- same reasoning that keeps signing_scheme and the CloudFront key-pair id here.
--
-- WHY A TEXT COLUMN WITH A CHECK RATHER THAN AN ENUM. The supported set is
-- expected to grow, and a CHECK is altered in one statement where an enum needs
-- a type change. The values are the same lowercase spellings the onboarding
-- detector produces, so nothing translates between detection and storage.
--
-- Existing rows predate self-service registration and were seeded by hand
-- against an edge the operator deployed, so backfilling them with a guess would
-- record a fact nobody established. They get the empty string, which the CHECK
-- admits and which the console reads as "not recorded" -- the operator sets it
-- when that site next re-registers.
ALTER TABLE fora.publisher_domains
    ADD COLUMN cdn TEXT NOT NULL DEFAULT '',
    ADD CONSTRAINT publisher_domains_cdn_supported
        CHECK (cdn IN ('', 'cloudflare', 'fastly', 'cloudfront'));
