-- The publisher registration model: the payee, the site, the seat-to-site
-- relationship, and who may act for a payee on a seat (per ADR-026 D1, as
-- amended 2026-09-18).
--
-- Four records, two scopes:
--   * fora.publishers                — the payee. Deployment-wide.
--   * fora.publisher_domains         — one site. Deployment-wide.
--   * fora.publisher_domain_listings — one site offered on one seat. Tenant-scoped.
--   * fora.publisher_grants          — who may act for one payee on one seat. Tenant-scoped.
--
-- fora.tenants is untouched here. It keeps signing_scheme and
-- cloudfront_key_pair_id, and its domain keeps its current meaning, until the
-- cutover that moves the running code onto these tables.

-- Every foreign key below is NAMED. A repository maps a violation to the domain
-- error the caller should see, and these tables have two foreign keys each: a
-- listing can fail because the seat is not there or because the site is not, and
-- reporting one as the other would send the caller looking in the wrong place.
-- Postgres reports the constraint name, so naming them is what lets the two be
-- told apart; an auto-generated name would work until someone renamed a column.

CREATE TYPE fora.listing_state AS ENUM (
    'PENDING_MANIFEST',
    'ACTIVE'
);

-- The payee. publisher_id is the same value fora.catalog.resource_owner_id
-- carries and the same value a site's manifest attests in
-- exchanges[].ext.resource_owner_id: one id, one word, the protocol's own name
-- for it. It is minted by the Exchange at registration and is opaque to
-- everyone else, so no format is constrained here.
--
-- DELIBERATELY NOT TENANT-SCOPED, and this is the point of the record rather
-- than a missed filter: the id is what groups one owner's sites across every
-- seat that sells them, so a tenant_id column would defeat the grouping it
-- exists for. The tenant-scoped state sits on the listing and grant tables
-- below. Same shape, and the same reasoning, as fora.agents.
CREATE TABLE fora.publishers (
    publisher_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One site, belonging to exactly one publisher. `domain` is the PUBLISHER's
-- own hostname -- the site that serves /.well-known/fora.json. It is not
-- fora.tenants.domain, which names the Exchange seat and is a separate
-- namespace; a value from one is never a value from the other.
--
-- DELIBERATELY NOT TENANT-SCOPED, for the reason the amendment states: a site
-- sits behind one CDN and carries one CloudFront key-pair id no matter which
-- seat sells it. Those are physical facts about the site. What the site offers
-- and on what terms is the commercial arrangement, and that is on the listing.
CREATE TABLE fora.publisher_domains (
    domain                 TEXT PRIMARY KEY,
    publisher_id           TEXT NOT NULL
        CONSTRAINT publisher_domains_publisher_fk
        REFERENCES fora.publishers (publisher_id) ON DELETE RESTRICT,
    signing_scheme         fora.signing_scheme NOT NULL DEFAULT 'ED25519',
    cloudfront_key_pair_id TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- An AWS-fronted site needs the key-pair id CloudFront assigned in the
    -- publisher's own AWS account before a URL can be signed for it. That id
    -- arrives AFTER registration -- the publisher uploads the public key, then
    -- enters what CloudFront gave back -- so this table deliberately does NOT
    -- carry the "RSA implies both fields present" constraint fora.tenants
    -- carries: the row legitimately exists with a NULL id while the publisher
    -- finishes that step. What this does enforce is that a present id is a
    -- real one, because an empty string would pass a NOT NULL check and then
    -- produce a signed URL nobody can verify.
    CONSTRAINT publisher_domains_key_pair_id_nonempty
        CHECK (cloudfront_key_pair_id IS NULL OR cloudfront_key_pair_id <> '')
);
CREATE INDEX publisher_domains_publisher_idx ON fora.publisher_domains (publisher_id);

-- One site offered on one seat: the relationship, and every piece of state
-- that belongs to it rather than to either side. Tenant-scoped, with tenant_id
-- leading the primary key so every read filters by seat.
--
-- The state starts PENDING_MANIFEST and becomes ACTIVE once the site's served
-- manifest names this seat with the registered publisher id. The proof is
-- continuous, not a one-time ceremony: verified_at records when it last
-- passed, last_checked_at when it was last looked at whatever the outcome.
--
-- content_path_scope and terms_preset live here rather than on the site row.
-- What a publisher offers, and on what terms, is agreed between one publisher
-- and one seat, so a site listed on two seats can offer /articles/ through one
-- and /archive/ through the other. An empty content_path_scope offers the
-- whole site.
CREATE TABLE fora.publisher_domain_listings (
    tenant_id          TEXT NOT NULL
        CONSTRAINT publisher_domain_listings_tenant_fk
        REFERENCES fora.tenants (tenant_id) ON DELETE CASCADE,
    domain             TEXT NOT NULL
        CONSTRAINT publisher_domain_listings_domain_fk
        REFERENCES fora.publisher_domains (domain) ON DELETE CASCADE,
    state              fora.listing_state NOT NULL DEFAULT 'PENDING_MANIFEST',
    content_path_scope TEXT[] NOT NULL DEFAULT '{}',
    terms_preset       TEXT NOT NULL,
    verified_at        TIMESTAMPTZ,
    last_checked_at    TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, domain),
    -- A NULL element is not a path, and it would reach the artifact builders as
    -- one. array_position is immutable, so the array can be checked in SQL
    -- instead of only in the repository.
    CONSTRAINT publisher_domain_listings_path_scope_no_nulls
        CHECK (array_position(content_path_scope, NULL::text) IS NULL)
);
CREATE INDEX publisher_domain_listings_domain_idx
    ON fora.publisher_domain_listings (domain);

-- Who may act for one publisher on one seat. The actor is the OIDC
-- (issuer, subject) pair the console asserts on every call; the console says
-- who the person is, the Exchange decides what they may do, and this table is
-- where it decides from.
--
-- Tenant-scoped: listing one publisher on two seats creates two grant sets, and
-- nothing is shared through the publisher record.
CREATE TABLE fora.publisher_grants (
    tenant_id    TEXT NOT NULL
        CONSTRAINT publisher_grants_tenant_fk
        REFERENCES fora.tenants (tenant_id) ON DELETE CASCADE,
    publisher_id TEXT NOT NULL
        CONSTRAINT publisher_grants_publisher_fk
        REFERENCES fora.publishers (publisher_id) ON DELETE CASCADE,
    oidc_issuer  TEXT NOT NULL,
    oidc_subject TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, publisher_id, oidc_issuer, oidc_subject)
);
-- Resolving "which publishers may this person act for on this seat" is the read
-- every console request makes before it touches anything else; the primary key
-- leads with publisher_id and cannot serve it.
CREATE INDEX publisher_grants_actor_idx
    ON fora.publisher_grants (tenant_id, oidc_issuer, oidc_subject);
