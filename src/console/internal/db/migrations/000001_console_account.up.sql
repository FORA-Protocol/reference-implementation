-- The console's only table: who has signed in.
--
-- WHAT IS NOT HERE, and deliberately. Publishers, sites, listings and grants all
-- live in the Exchange, which is the party that decides what a person may do.
-- The console asserts WHO is acting and asks; it stores no answer it could later
-- disagree with the Exchange about. There is therefore no publisher_id column
-- here, and no cached list of registrations: the console reads those back from
-- the Exchange on every page that needs them.
--
-- WHY (oidc_issuer, oidc_subject) IS THE KEY. It is the person's durable
-- identity at the login provider. An email address is reassignable, and two
-- accounts that happen to share one are not the same person, so matching on the
-- address would silently merge them. The Exchange keys its grants on the same
-- pair, which is what lets a grant written for one sign-in be found by the next.
CREATE SCHEMA IF NOT EXISTS console;

CREATE TABLE console.accounts (
    oidc_issuer    TEXT NOT NULL,
    oidc_subject   TEXT NOT NULL,
    -- The address and name the provider last told us. They are display values
    -- and nothing keys on them, so they are simply overwritten on each sign-in
    -- rather than versioned: the provider is authoritative about both.
    email          TEXT NOT NULL,
    -- Whether the provider says it verified the address. Publisher access is
    -- refused while this is false, and the Exchange refuses the same request
    -- independently -- one gate for the page, one for the write.
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    display_name   TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (oidc_issuer, oidc_subject)
);
