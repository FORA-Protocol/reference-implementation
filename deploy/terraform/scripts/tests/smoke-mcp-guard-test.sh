#!/usr/bin/env bash
# Prove the MCP discovery leg of smoke.sh (lib/mcp-check.sh) does its one job:
# pass only when the bearer gate answers 401 with a resource_metadata URL and
# BOTH protected-resource documents — the one at the MCP origin's well-known
# path and the one the 401 points to — name the MCP origin as their resource.
# A guard that exists to fire has to be SEEN firing, so every refusal below
# is driven and watched. curl and terraform are stubbed on PATH with
# env-controlled answers; the real library functions run.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

mkdir -p "${work}/bin" "${work}/stack"
# curl: the last argument is the URL. The gate (…/mcp) answers with headers
# only, as `curl -D -` prints them; a protected-resource URL answers with the
# body and then the status, as `curl -w '\n%{http_code}'` prints them.
cat > "${work}/bin/curl" <<'STUB'
#!/usr/bin/env bash
url="${!#}"
case "${url}" in
    */mcp)
        if [ "${STUB_PROXY:-0}" = 1 ]; then printf 'HTTP/1.1 200 Connection established\r\n\r\n'; fi
        printf 'HTTP/2 %s \r\n' "${STUB_GATE_CODE:-401}"
        if [ -n "${STUB_CHALLENGE:-}" ]; then printf 'www-authenticate: %s\r\n' "${STUB_CHALLENGE}"; fi
        printf 'content-type: application/json\r\n\r\n' ;;
    "${STUB_DIRECT_URL}") printf '%s\n%s' "${STUB_DIRECT_BODY}" "${STUB_DIRECT_CODE:-200}" ;;
    "${STUB_LINKED_URL:-}") printf '%s\n%s' "${STUB_LINKED_BODY}" "${STUB_LINKED_CODE:-200}" ;;
    *) exit 7 ;;
esac
STUB
# terraform: `output -json` prints the env-controlled state, or fails like an
# unreadable backend; `output -raw identity_url` is the fallback read.
cat > "${work}/bin/terraform" <<'STUB'
#!/usr/bin/env bash
case "$* " in
    *"output -json "*) [ "${STUB_STATE_FAIL:-0}" = 1 ] && { echo "Error: Failed to load state" >&2; exit 1; }; printf '%s\n' "${STUB_STATE_JSON}" ;;
    *"output -raw identity_url "*) echo "https://identity.example" ;;
    *) exit 1 ;;
esac
STUB
chmod +x "${work}/bin/curl" "${work}/bin/terraform"

fail() { echo "smoke MCP guard test FAILED: $1" >&2; exit 1; }

# run_leg → the discovery leg as smoke.sh runs it, in a subshell with the
# stubs first on PATH: resolve the origin, hit the gate, check both documents.
# Each step exits explicitly on failure: the subshell runs inside `if` and
# `&&` tests below, where bash ignores set -e.
run_leg() {
    (
        export PATH="${work}/bin:${PATH}"
        export STACK_DIR="${work}/stack"
        . "${SCRIPTS_DIR}/lib/staging-env.sh"
        . "${SCRIPTS_DIR}/lib/mcp-check.sh"
        MCP_URL="$(mcp_origin)" || exit 1
        METADATA_URL="$(mcp_bearer_gate "${MCP_URL}")" || exit 1
        check_mcp_resource "direct" "${MCP_URL}/.well-known/oauth-protected-resource" "${MCP_URL}" || exit 1
        check_mcp_resource "linked" "${METADATA_URL}" "${MCP_URL}" || exit 1
        echo "origin=${MCP_URL} metadata=${METADATA_URL}"
    )
}

MCP="https://agents.example"
IDENTITY="https://identity.example"
state_with_mcp_url='{"mcp_url":{"value":"https://agents.example"},"identity_url":{"value":"https://identity.example"}}'
state_without_mcp_url='{"identity_url":{"value":"https://identity.example"}}'

# The baseline every case starts from: the 401 points at the MCP origin's
# own well-known path, and that document names the MCP origin.
baseline() {
    export STUB_STATE_JSON="${state_with_mcp_url}" STUB_STATE_FAIL=0
    export STUB_GATE_CODE=401
    export STUB_CHALLENGE="Bearer resource_metadata=\"${MCP}/.well-known/oauth-protected-resource\""
    export STUB_DIRECT_URL="${MCP}/.well-known/oauth-protected-resource"
    export STUB_DIRECT_BODY="{\"resource\":\"${MCP}\",\"authorization_servers\":[\"${IDENTITY}\"]}"
    export STUB_DIRECT_CODE=200
    export STUB_LINKED_URL="" STUB_LINKED_BODY="" STUB_LINKED_CODE=200
    export STUB_PROXY=0
}

# Matching resource on both paths → the leg passes and names both URLs.
baseline
out="$(run_leg)" || fail "leg rejected a matching resource"
[ "${out##*$'\n'}" = "origin=${MCP} metadata=${MCP}/.well-known/oauth-protected-resource" ] \
    || fail "leg passed but resolved the wrong URLs: ${out##*$'\n'}"

# Mismatch: the document advertises the Identity origin (the service's
# default audience) while clients connect to the MCP origin → MUST fail, and
# the message has to name both so the operator sees which side to change.
baseline
export STUB_DIRECT_BODY="{\"resource\":\"${IDENTITY}\"}"
if run_leg >/dev/null 2>"${work}/err"; then fail "leg passed an advertised resource that differs from the MCP origin"; fi
grep -q "advertises resource ${IDENTITY}, but MCP clients connect to ${MCP}" "${work}/err" \
    || fail "mismatch message does not name both origins: $(cat "${work}/err")"

# Document not served (404) → MUST fail.
baseline
export STUB_DIRECT_CODE=404
run_leg >/dev/null 2>&1 && fail "leg passed a 404 protected-resource document"

# Document is not JSON → MUST fail (jq cannot read a resource out of it).
baseline
export STUB_DIRECT_BODY='<html>not json</html>'
run_leg >/dev/null 2>&1 && fail "leg passed a document that is not JSON"

# Document is JSON without a resource field → MUST fail.
baseline
export STUB_DIRECT_BODY='{"authorization_servers":["https://identity.example"]}'
run_leg >/dev/null 2>&1 && fail "leg passed a document without a resource field"

# Through an HTTP CONNECT proxy curl prints the proxy's "200 Connection
# established" before the endpoint's 401 → still passes: the endpoint's own
# status is what counts.
baseline
export STUB_PROXY=1
run_leg >/dev/null 2>"${work}/err" || fail "leg rejected a 401 reached through a CONNECT proxy: $(cat "${work}/err")"
# ...and the proxy's 200 must not hide an open gate behind it.
baseline
export STUB_PROXY=1 STUB_GATE_CODE=200
run_leg >/dev/null 2>&1 && fail "leg passed an ungated endpoint behind a CONNECT proxy"

# Bearer gate open (200) → MUST fail: the tools would be reachable by anyone.
baseline
export STUB_GATE_CODE=200
run_leg >/dev/null 2>&1 && fail "leg passed an ungated MCP endpoint"

# 401 without WWW-Authenticate → MUST fail: a client cannot discover sign-in.
baseline
export STUB_CHALLENGE=""
run_leg >/dev/null 2>&1 && fail "leg passed a 401 without WWW-Authenticate"

# WWW-Authenticate without resource_metadata → MUST fail.
baseline
export STUB_CHALLENGE='Bearer realm="mcp"'
run_leg >/dev/null 2>&1 && fail "leg passed a challenge without resource_metadata"

# resource_metadata present but malformed (unquoted, then not an absolute
# URL) → MUST fail at the challenge, naming it: a fetch of the garbage would
# fail too, but with a message about an unreachable URL.
baseline
export STUB_CHALLENGE="Bearer resource_metadata=${MCP}/.well-known/oauth-protected-resource"
run_leg >/dev/null 2>"${work}/err" && fail "leg passed an unquoted resource_metadata value"
grep -q "no usable resource_metadata URL" "${work}/err" || fail "unquoted resource_metadata was not refused at the challenge: $(cat "${work}/err")"
baseline
export STUB_CHALLENGE='Bearer resource_metadata="/.well-known/oauth-protected-resource"'
run_leg >/dev/null 2>"${work}/err" && fail "leg passed a resource_metadata value that is not an absolute URL"
grep -q "no usable resource_metadata URL" "${work}/err" || fail "relative resource_metadata was not refused at the challenge: $(cat "${work}/err")"

# The 401 points at the IDENTITY host (the service derives that URL from its
# issuer) and the document there names the MCP origin → passes: this is the
# split-hostname deployment done right.
baseline
export STUB_CHALLENGE="Bearer resource_metadata=\"${IDENTITY}/.well-known/oauth-protected-resource\""
export STUB_LINKED_URL="${IDENTITY}/.well-known/oauth-protected-resource"
export STUB_LINKED_BODY="{\"resource\":\"${MCP}\"}"
out="$(run_leg)" || fail "leg rejected a 401 pointing at the Identity host whose document names the MCP origin"
[ "${out##*$'\n'}" = "origin=${MCP} metadata=${IDENTITY}/.well-known/oauth-protected-resource" ] \
    || fail "leg passed the Identity-hosted document but resolved the wrong URLs: ${out##*$'\n'}"

# Same, but the document the 401 points to differs from the direct one →
# MUST fail. With only the direct check, this would pass; it proves the
# linked document is checked on its own.
baseline
export STUB_CHALLENGE="Bearer resource_metadata=\"${IDENTITY}/.well-known/oauth-protected-resource\""
export STUB_LINKED_URL="${IDENTITY}/.well-known/oauth-protected-resource"
export STUB_LINKED_BODY="{\"resource\":\"${IDENTITY}\"}"
run_leg >/dev/null 2>&1 && fail "leg passed when only the direct document matched and the linked one did not"

# A stack applied before the mcp_url output existed → the Identity URL is the
# MCP origin, read through the raw output.
baseline
export STUB_STATE_JSON="${state_without_mcp_url}"
export STUB_CHALLENGE="Bearer resource_metadata=\"${IDENTITY}/.well-known/oauth-protected-resource\""
export STUB_DIRECT_URL="${IDENTITY}/.well-known/oauth-protected-resource"
export STUB_DIRECT_BODY="{\"resource\":\"${IDENTITY}\"}"
out="$(run_leg)" || fail "leg rejected a stack without the mcp_url output"
[ "${out##*$'\n'}" = "origin=${IDENTITY} metadata=${IDENTITY}/.well-known/oauth-protected-resource" ] \
    || fail "fallback resolved the wrong origin: ${out##*$'\n'}"

# A state Terraform cannot read → MUST abort, without the fallback: an
# unreadable state is not a stack without the output. The stubs are left as
# the fallback case set them, so a leg that did fall back would pass.
export STUB_STATE_FAIL=1
run_leg >/dev/null 2>&1 && fail "leg fell back to identity_url on a failed state read"

echo "smoke MCP guard test passed (match on both paths accepted, through a CONNECT proxy too; mismatch, bad document, open gate, bad challenge and unreadable state rejected; absent output falls back)"
