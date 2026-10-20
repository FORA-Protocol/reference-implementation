-- A listing may exist before its terms are chosen, and may not be active
-- without them.
--
-- WHY terms BECOMES NULLABLE. A publisher can save a domain in the console
-- before finishing its setup and come back to it later. The licensing step is
-- one of the steps they can leave for later, so a listing saved from it has no
-- terms yet. NULL is that answer: "not chosen", which no terms document can
-- say, because every document permits or charges for something.
--
-- WHY THE CHECK. An ACTIVE listing is what catalog push accepts entries for
-- and what the automatic import prices pages with, so it must always carry its
-- terms. The service refuses to activate a listing without them; this says the
-- same thing in the schema, so a hand-written UPDATE cannot sell a site under
-- terms nobody agreed.
--
-- WHY jsonb_typeof AND NOT ONLY IS NOT NULL. A CHECK whose expression is NULL
-- passes. The shape check 000039 added reads NULL for a NULL column -- which is
-- why dropping NOT NULL is enough for a pending listing -- and it reads NULL
-- for a JSON null too, because 'null'::jsonb has no members to look up. A JSON
-- null is not an SQL NULL, so IS NOT NULL alone would let it through. Requiring
-- an object refuses both.
--
-- Every row that exists when this runs satisfies the check: registration has
-- always written an object, and 000039 gave the rows before it one.
ALTER TABLE fora.publisher_domain_listings
    ALTER COLUMN terms DROP NOT NULL,
    ADD CONSTRAINT publisher_domain_listings_active_has_terms
        CHECK (state <> 'ACTIVE' OR (terms IS NOT NULL AND jsonb_typeof(terms) = 'object'));
