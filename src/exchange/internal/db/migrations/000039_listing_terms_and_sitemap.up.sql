-- The licensing a publisher agreed, and where their resources are listed.
--
-- WHY terms_preset GOES. It named one of two arrangements -- free, or charge
-- per access -- and that is not what a publisher decides. They decide which
-- uses they permit, whether attribution is required, and at what price, which
-- is four answers rather than one. A name for a fixed combination of the four
-- cannot record a fifth combination, so a publisher who wanted search free and
-- AI training refused had no way to say it. The column carried no such row and
-- never could.
--
-- WHY JSONB AND NOT FOUR COLUMNS. The shape is the protocol's license terms,
-- which this deployment does not own: a use the protocol registers later is a
-- value in an existing list here, and would be a migration as columns. The
-- document is written and read through one Go type, and the CHECK below pins
-- the two members every reader dereferences -- enough that a hand-written row
-- cannot be silently unreadable, without restating the type in SQL.
--
-- WHY sitemap_url IS SEPARATE. It says where the resources are; the terms say
-- what they cost. Automatic catalogue import reads the first and prices with
-- the second, and a deployment that has one without the other can do neither.
--
-- Existing rows predate this and carry a preset name. There is no honest
-- translation -- "free" said nothing about which uses were permitted -- so they
-- are given the same free-for-every-offered-use terms the console's own default
-- produces, and an empty sitemap, which reads as "not recorded". Those rows
-- were written by an operator seeding a stack, not by a publisher agreeing
-- anything.
ALTER TABLE fora.publisher_domain_listings
    ADD COLUMN terms JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN sitemap_url TEXT NOT NULL DEFAULT '';

UPDATE fora.publisher_domain_listings
   SET terms = jsonb_build_object(
       'pricing_model', 'free',
       'rate', '',
       'permitted_functions', jsonb_build_array('search', 'ai-input', 'ai-index', 'ai-train', 'crawl'),
       'attribution_required', false)
 WHERE terms = '{}'::jsonb;

ALTER TABLE fora.publisher_domain_listings
    DROP COLUMN terms_preset,
    ALTER COLUMN terms DROP DEFAULT,
    ADD CONSTRAINT publisher_domain_listings_terms_shape
        CHECK (jsonb_typeof(terms -> 'pricing_model') = 'string'
           AND jsonb_typeof(terms -> 'permitted_functions') = 'array');
