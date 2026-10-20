-- Turn the per-tenant welcome credit into a per-currency map.
--
-- The Exchange now bills on one TigerBeetle ledger per currency, so "the
-- welcome credit" is no longer one number: a deployment billing in EUR and USD
-- grants an amount in each, and a currency with no entry gets no grant. The
-- single NUMERIC column could only express one currency's amount, and which
-- currency it meant was implied by configuration rather than stored.
--
-- Shape: a JSONB object keyed by ISO 4217 alpha code, whose values are decimal
-- amounts as STRINGS — {"EUR": "50.00", "USD": "50.00"}. Strings, not JSON
-- numbers, because a JSON number is a float on the way in and out and would
-- round a money amount; the repo layer parses each string exactly and enforces
-- the same bounds the NUMERIC(20,8) column enforced (non-negative, at most 8
-- fraction digits, at most 12 integer digits). An empty object means no grant
-- in any currency, which is the strictly-prepaid behavior a tenant had with 0.
--
-- The CHECK is what SQL can enforce without a helper function: the value must
-- be an object, not an array, string or number. Key and amount validation stays
-- in the repo guard — a CHECK cannot contain the subquery that iterating
-- jsonb_object_keys would need, and the guard is where the currency set and the
-- decimal bounds are known anyway.
--
-- Existing rows: the reset that accompanies the multi-currency rollout deletes
-- agents, transactions, evidence and the ledger, but it KEEPS tenant rows. So
-- this migration has live rows to convert. The old value is deliberately NOT
-- carried over: EXCHANGE_DEFAULT_AGENT_CREDIT is the sole owner of this
-- setting and every boot replaces it, so a tenant is initialized to the empty
-- map here and the configured map is written by the first boot after the
-- migration. NOT NULL DEFAULT '{}'::jsonb is what performs that
-- initialization, in one instant ALTER with no table rewrite.
--
-- The column is renamed (singular to plural) on purpose: the type changed, and
-- a rename makes any query still expecting a number fail loudly instead of
-- reading a map through a NUMERIC decoder.

ALTER TABLE fora.tenants
    DROP COLUMN default_agent_credit;

ALTER TABLE fora.tenants
    ADD COLUMN default_agent_credits JSONB NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(default_agent_credits) = 'object');
