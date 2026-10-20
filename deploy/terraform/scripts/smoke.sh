#!/usr/bin/env bash
# Staging smoke check: service health over public HTTPS, then the full
# discover -> execute -> signed-URL -> edge -> origin proof
# (tests/e2e/smoke_staging.py), including a no-proof-of-possession fetch
# that must be refused. The proof covers licensing, delivery, and edge
# enforcement; it does not assert billing behavior (the stack merely runs
# with the tigerbeetle adapter configured).
#
# The Identity Service is checked for liveness, for its MCP endpoint being
# gated, and for that endpoint advertising the origin clients connect to as
# its OAuth protected resource (lib/mcp-check.sh) — but no leg drives a tool
# call THROUGH that endpoint: doing so needs an OAuth bearer, which means
# completing developer sign-up. The end-to-end proof below signs Broker calls
# directly with the local agent key instead.
#
# Env (optional):
#   STACK_DIR         default deploy/terraform/stacks/staging-aws
#   AGENT_ID          default from lib/staging-env.sh, which reads it from the
#                     generated agent key file's kid (shared with key
#                     generation and seeding so the id can never drift apart)
#   PUBLISHER_DOMAIN  publisher hostname to smoke against. Default: the
#                     stack's own edge-fronted hostname (publisher_hostname
#                     output). Set it to smoke another publisher fronted by
#                     a separately applied stacks/edge (deploy_edge = false
#                     mode) — the hostname must be seeded first, see
#                     seed-staging.sh.
#   FORA_STAGING_ENFORCE_BINDING
#                     "true"/"false" override for the bare-fetch expectation.
#                     Default: read back from the stack's fora_enforce_binding
#                     output, so the expectation follows the deployment —
#                     "true" expects the bare fetch refused (403), "false"
#                     expects it delivered (200). Set it only when smoking a
#                     separately applied stacks/edge (deploy_edge = false),
#                     where the stack has no edge posture to read; unset
#                     there, the smoke assumes "true" (the module default).
#
# Usage:
#   deploy/terraform/scripts/smoke.sh

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"
. "$(dirname "${BASH_SOURCE[0]}")/lib/mcp-check.sh"

command -v terraform >/dev/null 2>&1 || { echo "missing: terraform" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "missing: curl" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "missing: jq" >&2; exit 2; }
command -v uv >/dev/null 2>&1 || { echo "missing: uv" >&2; exit 2; }

EXCHANGE_URL="$(tf_out exchange_url)"
BROKER_URL="$(tf_out broker_url)"
IDENTITY_URL="$(tf_out identity_url)"
# The origin agents point an MCP client at — the Identity URL unless the
# stack serves the MCP endpoint under a name of its own.
MCP_URL="$(mcp_origin)"
# The proof signs as the smoke agent, and the services verify that signature by
# fetching https://<AGENT_ID>/.well-known/... — catch an id/hostname mismatch
# here with an explanation instead of as an unexplained 401 below.
require_ids_match_stack
# With deploy_edge = false the publisher_hostname output is null and tf_out
# fails — in that mode the hostname to smoke MUST come from PUBLISHER_DOMAIN.
PUBLISHER="${PUBLISHER_DOMAIN:-$(tf_out publisher_hostname 2>/dev/null || true)}"
if [ -z "${PUBLISHER}" ]; then
    echo "the stack has no edge-fronted hostname (deploy_edge = false) — pass PUBLISHER_DOMAIN=<site hostname>" >&2
    exit 2
fi
# The bare-fetch expectation follows the deployment: read the deployed edge's
# posture back from the stack. With deploy_edge = false the output is null and
# tf_out fails — fall back to "true" (the module default posture), overridable
# via FORA_STAGING_ENFORCE_BINDING for a stacks/edge applied with "false".
ENFORCE_BINDING="$(tf_out fora_enforce_binding 2>/dev/null || echo "true")"
# The site root's bot expectation follows the edge's licensed scope. Only
# demo-aws renders a Lambda config; a stack without one gates the whole site,
# which an empty scope means. A PUBLISHER_DOMAIN smoke targets a separately
# applied edge, whose scope this stack does not know, so it keeps the empty
# scope too. The read and the extraction are separate steps, so a config that
# exists but cannot be parsed stops the smoke.
EDGE_CONFIG=""
if [ -z "${PUBLISHER_DOMAIN:-}" ]; then
    EDGE_CONFIG="$(tf_out lambda_edge_config 2>/dev/null || true)"
fi
LICENSED_PATHS_JSON=""
if [ -n "${EDGE_CONFIG}" ]; then
    LICENSED_PATHS_JSON="$(printf '%s' "${EDGE_CONFIG}" \
        | "${PYTHON[@]}" -c 'import json, sys; print(json.load(sys.stdin).get("LICENSED_PATHS_JSON", ""))')"
fi

# Every leg captures the status and fails loudly on mismatch. A bare
# `curl -fsS url && echo ok` would NOT stop the script: under `set -e` a
# failure on the left side of && is exempt, so a downed service would be
# silently skipped and the smoke would keep going.
check_http() { # check_http <label> <url> <accepted codes...>
    local label="$1" url="$2" code want
    shift 2
    code="$(curl -s -o /dev/null -w '%{http_code}' "${url}")"
    for want in "$@"; do
        if [ "${code}" = "${want}" ]; then
            echo "${label}: ok (${code})"
            return 0
        fi
    done
    echo "${label}: unhealthy — ${url} returned ${code}, wanted $*" >&2
    exit 1
}

echo "== healthz =="
check_http "exchange" "${EXCHANGE_URL}/healthz" 200
check_http "broker" "${BROKER_URL}/healthz" 200
# A 503 here is the Identity Service reporting its database is unreachable; a
# connection failure usually means it never started, which on a fresh stack
# means bootstrap-identity.sh has not run yet.
check_http "identity" "${IDENTITY_URL}/healthz" 200

echo "== identity MCP endpoint =="
# 401 is the PASSING result: the endpoint is mounted and its bearer gate is
# active. Asserting it (rather than accepting any answer) makes this a
# negative-path check — a 200 would mean the MCP tools, which sign FORA calls
# with a developer's custodied key, are reachable by anyone. The 401 also has
# to say where the protected-resource document is.
METADATA_URL="$(mcp_bearer_gate "${MCP_URL}")"
echo "mcp bearer gate: ok (401, resource_metadata ${METADATA_URL})"
# Served outside the gate on purpose: it is how an MCP client that met that
# 401 discovers which authorization server to sign in against — and the
# document must name the origin the client connected to, or the client stops
# there. Checked at the well-known path of that origin and at the URL the
# 401 pointed to, since the service derives the latter from its issuer and the
# two can differ when the MCP endpoint has a hostname of its own.
check_mcp_resource "mcp discovery" "${MCP_URL}/.well-known/oauth-protected-resource" "${MCP_URL}"
check_mcp_resource "mcp discovery via the 401" "${METADATA_URL}" "${MCP_URL}"

echo "== edge manifest =="
check_http "edge fora.json" "https://${PUBLISHER}/.well-known/fora.json" 200

echo "== end-to-end proof =="
FORA_STAGING_EXCHANGE_URL="${EXCHANGE_URL}" \
FORA_STAGING_BROKER_URL="${BROKER_URL}" \
FORA_STAGING_PUBLISHER="${PUBLISHER}" \
FORA_STAGING_LICENSED_PATHS_JSON="${LICENSED_PATHS_JSON}" \
FORA_STAGING_AGENT_ID="${AGENT_ID}" \
FORA_STAGING_AGENT_KEY="${KEYS_DIR}/agent-key.json" \
FORA_STAGING_ENFORCE_BINDING="${FORA_STAGING_ENFORCE_BINDING:-${ENFORCE_BINDING}}" \
    uv run --project "${REPO_ROOT}/tests/e2e" python "${REPO_ROOT}/tests/e2e/smoke_staging.py"
