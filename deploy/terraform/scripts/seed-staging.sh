#!/usr/bin/env bash
# One-time staging seed: registers the Exchange seat, the publisher and its
# site, the signing identities, and the Broker's exchange row, then ingests the
# three demo feeds (Stoa Press, Harmonia Records, FoleyWorks) as ONE submission
# through the production ingest binary (cmd/fora-ingest) over public HTTPS. The
# three feeds are demo brands, not publishers: every record is rewritten to the
# stack's publisher domain, so the Exchange sees one seat, one publisher and one
# site serving all of them.
#
# Four rows make the deployment sellable, and they are four different things:
#   * fora.tenants                  the SEAT — one Exchange identity that sells
#                                   here. Its domain is the EXCHANGE's own
#                                   hostname, and it holds the seller settings
#                                   and the URL-signing key refs.
#   * fora.publishers               the PAYEE. Its id is what the site's
#                                   manifest attests and what every catalog row
#                                   is settled to.
#   * fora.publisher_domains        the SITE, carrying its CDN's signing scheme.
#   * fora.publisher_domain_listings the site offered on this seat, ACTIVE, with
#                                   the content path scope and terms preset.
#
# The listing is seeded ACTIVE on purpose. Catalog push refuses an entry whose
# site has no active listing on the seat, and the registration flow that would
# activate one from a served manifest does not exist yet — so until it lands,
# seeding is the only way a listing becomes active.
#
# No admin RPC exists yet for seat/publisher/agent/exchange registration (the
# admin plane covers fee-rate + reporting-policy only), so registration is a
# DOCUMENTED one-time SQL step executed over SSH — printed in full before it
# runs, never hidden. When registration RPCs land, this script should switch
# to them.
#
# Prerequisites: stack applied (terraform outputs readable), keys generated
# (gen-keys.sh), images pushed, DNS live, Caddy certs issued.
#
# Env (all optional):
#   STACK_DIR        default deploy/terraform/stacks/staging-aws
#   TENANT_ID        the seat's id; default tenant-staging. One seat per
#                    deployment — adding a publisher does NOT need a new one.
#   PUBLISHER_DOMAIN site hostname to register and seed the catalog for.
#                    Default: the stack's own edge-fronted hostname
#                    (publisher_hostname output). Set it to onboard another
#                    publisher fronted by a separately applied stacks/edge (the
#                    deploy_edge = false mode), e.g.:
#                      PUBLISHER_DOMAIN=news.publisher.example \
#                      PUBLISHER_ID=news-publisher \
#                      deploy/terraform/scripts/seed-staging.sh
#   PUBLISHER_ID     the payee id. Default: the stack's resource_owner_id
#                    output, which is the id the stack's own publisher manifest
#                    attests. A site whose manifest attests a different id has
#                    every push refused, so set this together with
#                    PUBLISHER_DOMAIN when onboarding somebody else's site.
#   PUBLISHER_NAME   display name for the publisher row; default the payee id.
#   LISTING_TERMS    the licensing document the seat sells the site under, as
#                    JSON; defaults to free for every offered use
#   FEED             push exactly this one feed file instead of the three
#                    demo feeds merged (deploy/fixtures/demo/{philosophy,
#                    music,sfx}.jsonl).
#   AGENT_ID / CONTRIBUTOR_ID — identity ids come from lib/staging-env.sh, which
#   reads the smoke ids from the generated key files' kids (shared with
#   gen-keys.sh and smoke.sh so the ids can never drift apart). This script
#   additionally proves the ids equal the hostnames the stack serves the key
#   directories at, before seeding rows under them. BROKER_RELAY_KID is not read
#   here: the Broker's relay key needs no fora.agents row (see the identity
#   inserts below).
#
# Usage:
#   deploy/terraform/scripts/seed-staging.sh

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"
TENANT_ID="${TENANT_ID:-tenant-staging}"
# The licensing this seat sells the site under. Free for every use the
# vocabulary offers, which is what a seeded site says before any publisher has
# agreed anything narrower; the catalog entries carry their own terms, and those
# are what an offer is built from.
# Written out first rather than inline: a default inside ${VAR:-...} ends at the
# first closing brace, and this value is full of them.
DEFAULT_LISTING_TERMS='{"pricing_model":"free","rate":"","permitted_functions":["search","ai-input","ai-index","ai-train","crawl"],"attribution_required":false}'
LISTING_TERMS="${LISTING_TERMS:-${DEFAULT_LISTING_TERMS}}"
FEED="${FEED:-}"
DEMO_FEEDS=(
    "${REPO_ROOT}/deploy/fixtures/demo/philosophy.jsonl"
    "${REPO_ROOT}/deploy/fixtures/demo/music.jsonl"
    "${REPO_ROOT}/deploy/fixtures/demo/sfx.jsonl"
)

# Fail fast with one clear line when a tool is absent: terraform reads the
# stack outputs (tf_out), go runs the fora-ingest catalog push, python3
# converts the public keys into the hex form the SQL needs, uv runs the signed
# Register call that mints the smoke agent's billing account.
command -v terraform >/dev/null 2>&1 || { echo "missing: terraform" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "missing: go" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "missing: python3" >&2; exit 2; }
command -v uv >/dev/null 2>&1 || { echo "missing: uv" >&2; exit 2; }

# Builds SSH_CMD from the stack.s vm_public_ip output. The identity comes from
# FORA_SSH_IDENTITY_FILE, not from Terraform - see lib/staging-env.sh.
load_ssh_cmd

# The rows seeded below are keyed by the smoke ids, and the services verify
# those identities' signatures by fetching https://<id>/.well-known/... — so
# an id that is not the hostname the stack serves is a guaranteed 401 later.
# Refuse to seed it.
require_ids_match_stack
EXCHANGE_URL="$(tf_out exchange_url)"
EXCHANGE_DOMAIN="${EXCHANGE_URL#https://}"

# The seat's domain and the Exchange's EXCHANGE_DEFAULT_TENANT must be the same
# string, and the stack is the one place that derives it (its
# default_tenant_domain output). Reading it back here keeps the two tools from
# drifting instead of each deriving its own copy.
SEAT_DOMAIN="$(tf_out default_tenant_domain 2>/dev/null || true)"
if [ -z "${SEAT_DOMAIN}" ]; then
    echo "could not read default_tenant_domain from the stack — re-apply it" >&2
    exit 2
fi
# A tenant row is the Exchange seat, so the stack's two derivations of the
# Exchange's hostname have to agree. If they ever stop agreeing, the Exchange
# looks its seat up under one name while this script creates it under the
# other, and every agent Register is refused.
if [ "${SEAT_DOMAIN}" != "${EXCHANGE_DOMAIN}" ]; then
    echo "the stack's default_tenant_domain (${SEAT_DOMAIN}) is not its exchange_url host (${EXCHANGE_DOMAIN})." >&2
    echo "A tenant row is the Exchange seat, so the two must be the same string. Fix the stack, apply, then re-run." >&2
    exit 2
fi

# The SITE to register and push a catalog for. A publisher is not a tenant any
# more: it is registered as its own record and listed on the seat above, so
# onboarding one fronted by a separately applied edge stack means passing its
# hostname here, not inventing a second seat.
PUBLISHER="${PUBLISHER_DOMAIN:-$(tf_out publisher_hostname 2>/dev/null || true)}"
if [ -z "${PUBLISHER}" ]; then
    echo "the stack publishes no publisher_hostname (deploy_edge = false?) — pass PUBLISHER_DOMAIN=<site hostname>" >&2
    exit 2
fi

# The PAYEE. The site's manifest attests this id, and catalog push refuses an
# entry whose attested owner is not the registered one — so the default comes
# from the same stack output that renders the manifest.
PUBLISHER_ID="${PUBLISHER_ID:-$(tf_out resource_owner_id 2>/dev/null || true)}"
if [ -z "${PUBLISHER_ID}" ]; then
    echo "could not read resource_owner_id from the stack — re-apply it, or pass PUBLISHER_ID=<payee id>" >&2
    echo "It must equal the exchanges[].ext.resource_owner_id the site's /.well-known/fora.json attests." >&2
    exit 2
fi
PUBLISHER_NAME="${PUBLISHER_NAME:-${PUBLISHER_ID}}"

# Public keys as hex for bytea literals (fora.agents.public_key stores raw bytes).
pub_hex() {
    python3 - "$1" <<'PY'
import base64
import json
import sys

with open(sys.argv[1]) as f:
    pub = json.load(f)["public_key"]
print(base64.urlsafe_b64decode(pub + "=" * (-len(pub) % 4)).hex())
PY
}

CONTRIBUTOR_PUB_HEX="$(pub_hex "${KEYS_DIR}/contributor-key.json")"
AGENT_PUB_HEX="$(pub_hex "${KEYS_DIR}/agent-key.json")"

SQL_FILE="$(mktemp)"
TMP_FEED="$(mktemp)"
trap 'rm -f "${SQL_FILE}" "${TMP_FEED}"' EXIT

# The SQL references every operator-supplied value as a psql variable
# (:'name') — psql quotes those as SQL literals server-side, so no value is
# ever spliced into the SQL text and no quote in a value can change the
# statement. The heredoc is quoted on purpose: nothing interpolates here.
cat > "${SQL_FILE}" <<'EOSQL'
-- FORA staging one-time seed (idempotent; re-runs are no-ops).

-- The seat: one Exchange identity that sells on this deployment, keyed on the
-- EXCHANGE's own hostname. ed25519_key_ref matches the ref the Exchange
-- registers its loaded PEM under (FORA_DEMO_ED25519_KEY_REF default). The
-- signing scheme is not here — that is a fact about a site's CDN, and it sits
-- on the site row below.
INSERT INTO fora.tenants (
    tenant_id, domain, ed25519_key_ref, reporting_policy
) VALUES (:'tenant_id', :'seat_domain', 'exchange-primary', '{}')
ON CONFLICT (tenant_id) DO NOTHING;

-- Broker-relayed ExecuteTransaction is opt-in per seat.
UPDATE fora.tenants SET allow_broker_relay = TRUE WHERE tenant_id = :'tenant_id';

-- The payee. fora.catalog.resource_owner_id is a foreign key to this row, so it
-- must exist before the ingest below runs, and its id must equal the
-- exchanges[].ext.resource_owner_id the site's manifest attests.
INSERT INTO fora.publishers (publisher_id, display_name)
VALUES (:'publisher_id', :'publisher_name')
ON CONFLICT (publisher_id) DO NOTHING;

-- The site. Every site this script seeds is fronted by a Cloudflare edge worker,
-- which verifies Ed25519 signed URLs. An AWS-fronted site takes
-- AWS_CLOUDFRONT_RSA instead, plus the cloudfront_key_pair_id CloudFront
-- assigned in the publisher's own AWS account.
INSERT INTO fora.publisher_domains (domain, publisher_id, signing_scheme)
VALUES (:'publisher', :'publisher_id', 'ED25519')
ON CONFLICT (domain) DO NOTHING;

-- The site offered on this seat. ACTIVE from the start: catalog push refuses an
-- entry whose site has no active listing here, and nothing else can activate one
-- until the registration flow exists. An empty content_path_scope offers the
-- whole site.
INSERT INTO fora.publisher_domain_listings (
    tenant_id, domain, state, content_path_scope, terms
) VALUES (:'tenant_id', :'publisher', 'ACTIVE', '{}', :'listing_terms'::jsonb)
ON CONFLICT (tenant_id, domain) DO NOTHING;

-- Signing identities: the catalog contributor (ingest) and the smoke agent. Each
-- agent_id IS the hostname its Web Bot Auth directory is served at, because that
-- is what the services key an identity on: they derive the caller from the covered
-- Signature-Agent header and look the row up by that host.
--
-- There is deliberately NO row for the Broker's outbound-relay key. The Exchange
-- recognises a relay by comparing the caller's directory host to the host in
-- EXCHANGE_BROKER_WELLKNOWN_URL, and the transport has already proved the signing
-- key is published by that directory — a stronger statement than a row seeded
-- here. A row keyed on the relay KEY id (BROKER_RELAY_KID names a versioned JOSE
-- kid, "broker.<stack>.v1", not a hostname) could never have been found by that
-- lookup anyway, and the production seed has never carried one.
INSERT INTO fora.agents (agent_id, public_key, requester_type)
VALUES (:'contributor_id', decode(:'contributor_pub_hex', 'hex'), 'AGENT')
ON CONFLICT (agent_id) DO UPDATE SET public_key = EXCLUDED.public_key;

INSERT INTO fora.agents (agent_id, public_key, requester_type)
VALUES (:'agent_id', decode(:'agent_pub_hex', 'hex'), 'AGENT')
ON CONFLICT (agent_id) DO UPDATE SET public_key = EXCLUDED.public_key;

-- Exchange row the Broker routes resolves through.
INSERT INTO broker.exchanges (
    exchange_id, domain, endpoint, trust_level,
    supported_profiles, priority, probe_state
)
SELECT 'ex-staging', :'exchange_domain', :'exchange_url',
       'VERIFIED', '["fora-news-v1", "fora-comp-v1", "fora-music-v1"]'::jsonb, 10, 'LIVE'
WHERE NOT EXISTS (
    SELECT 1 FROM broker.exchanges WHERE domain = :'exchange_domain'
);
EOSQL

PSQL_VARS=(
    -v "tenant_id=${TENANT_ID}"
    -v "seat_domain=${SEAT_DOMAIN}"
    -v "publisher=${PUBLISHER}"
    -v "publisher_id=${PUBLISHER_ID}"
    -v "publisher_name=${PUBLISHER_NAME}"
    -v "listing_terms=${LISTING_TERMS}"
    -v "contributor_id=${CONTRIBUTOR_ID}"
    -v "agent_id=${AGENT_ID}"
    -v "contributor_pub_hex=${CONTRIBUTOR_PUB_HEX}"
    -v "agent_pub_hex=${AGENT_PUB_HEX}"
    -v "exchange_domain=${EXCHANGE_DOMAIN}"
    -v "exchange_url=${EXCHANGE_URL}"
)

echo "== one-time registration SQL (via ${SSH_CMD[*]}) =="
cat "${SQL_FILE}"
echo "-- with:"
for entry in "${PSQL_VARS[@]}"; do
    [ "${entry}" = "-v" ] || echo "--   ${entry}"
done
echo "=========================================================="

# remote_psql shell-quotes every argument; psql still quotes SQL values.
remote_psql fora -v ON_ERROR_STOP=1 "${PSQL_VARS[@]}" < "${SQL_FILE}"

# No restart is needed: the Exchange grants the EXCHANGE_DEFAULT_AGENT_CREDIT it
# booted with from the first Register after the seat exists.

echo "== ingest demo feeds as ${CONTRIBUTOR_ID} (hosts rewritten to ${PUBLISHER}) =="
# The music and sfx feeds key their records to music.demo.<domain> and
# sfx.demo.<domain>, hosts this stack neither serves nor registers a site for,
# so the prefix goes too: every host becomes the one publisher domain (the
# landing page links all three brands under it). The delimiter is '#' because
# '|' is the alternation inside the group. The demo stack applies the same
# rewrite to the feed the Broker's catalog search indexes
# (modules/query-index-feed), so a change here has to be made there
# too.
if [ -n "${FEED}" ]; then
    FEEDS=("${FEED}")
else
    FEEDS=("${DEMO_FEEDS[@]}")
fi
cat "${FEEDS[@]}" \
    | sed -E "s#(music\.|sfx\.)?demo\.fora-protocol\.org#${PUBLISHER}#g" \
    > "${TMP_FEED}"

# One submission on purpose: the Exchange stores or refuses a submission
# whole, so merging the feeds makes the catalog all-or-nothing across all
# three brands — one feed per push could leave the first brand seeded and
# the second refused. The exit status is the whole verdict: the binary
# exits non-zero on any refusal, so there is no second signal to read off
# its report.
cd "${REPO_ROOT}"
if ! go run ./src/exchange/cmd/fora-ingest \
    --exchange-url "${EXCHANGE_URL}" \
    --tenant "${TENANT_ID}" \
    --key "${KEYS_DIR}/contributor-key.json" \
    "${TMP_FEED}"; then
    echo "ingest failed — seed is NOT complete" >&2
    exit 1
fi

# The SQL above gives the smoke agent an identity, not a billing account. The
# billing_ref every ledger account id is derived from is minted only by the
# Register RPC, which also creates the agent's TigerBeetle account. Skip this
# and the paid leg is denied at authorize with "billing ref required", before a
# balance is ever consulted — so funding would credit an account the Exchange
# never touches. Idempotent: an already-registered agent gets its stored ref
# back and nothing changes.
echo "== register the smoke agent's billing account as ${AGENT_ID} =="
BILLING_REF="$(
    FORA_STAGING_EXCHANGE_URL="${EXCHANGE_URL}" \
    FORA_STAGING_AGENT_KEY="${KEYS_DIR}/agent-key.json" \
        uv run --project "${REPO_ROOT}/tests/e2e" \
        python "${REPO_ROOT}/tests/e2e/register_staging_agent.py"
)"
if [ -z "${BILLING_REF}" ]; then
    echo "register returned no billing_ref — seed is NOT complete" >&2
    exit 1
fi

echo "seed complete: seat ${TENANT_ID} (${SEAT_DOMAIN}) selling ${PUBLISHER} for ${PUBLISHER_ID}, + demo feeds on ${EXCHANGE_URL}"
echo "  smoke agent billing_ref: ${BILLING_REF}"
echo "next: agent-balances.sh — verify the configured welcome credit; fund manually if the balance is zero"
