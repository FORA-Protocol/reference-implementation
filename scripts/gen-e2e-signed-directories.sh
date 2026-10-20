#!/usr/bin/env bash
# gen-e2e-signed-directories.sh
#
# Pre-sign each e2e publisher edge's Web Bot Auth key directory and add it to
# that edge's env_file as WBA_SIGNED_DIRECTORY_JSON.
#
# The Web Bot Auth profile requires a key directory response signed by every key
# it lists, for the host it is fetched from, and an edge holds no private key.
# scripts/gen-e2e-keys.sh mints each publisher's self-publish key and writes the
# public half to deploy/edge-keys/<service>.env as WBA_KEYS_JSON. This script
# signs the directory listing that key with its private fixture, through the
# edge's own signing tool (src/edge/scripts/sign-wba-directory.mjs, the SDK's
# signDirectoryResponse), for the publisher's host, and appends the result to the
# same env file. The edge serves it verbatim.
#
# The host is the one the Exchange fetches the directory from: each edge answers
# on port 80 under its publisher domain alias, so the authority is the bare
# domain. Re-run after gen-e2e-keys.sh; a re-run replaces the previous value.
#
# Needs node and src/edge/node_modules (`npm ci` in src/edge); it says so and
# stops when either is absent.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURES_DIR="${REPO_ROOT}/tests/e2e/harness/fixtures"
EDGE_KEYS_DIR="${REPO_ROOT}/deploy/edge-keys"
SIGNER="${REPO_ROOT}/src/edge/scripts/sign-wba-directory.mjs"

command -v node >/dev/null 2>&1 || { echo "gen-e2e-signed-directories: missing node" >&2; exit 2; }
if [ ! -d "${REPO_ROOT}/src/edge/node_modules" ]; then
    echo "gen-e2e-signed-directories: src/edge has no node_modules — run 'npm ci' in src/edge" >&2
    exit 2
fi

# (publisher domain == authority, private fixture, edge env_file basename): the
# self-publish identities scripts/gen-e2e-keys.sh mints, in the same order.
PUBLISHERS=(
    "demo.fora-protocol.org selfpub_philosophy_key.json edge"
    "music.demo.fora-protocol.org selfpub_music_key.json fastly-edge"
    "sfx.demo.fora-protocol.org selfpub_sfx_key.json aws-edge"
)

for entry in "${PUBLISHERS[@]}"; do
    read -r domain fixture service <<< "${entry}"
    env_file="${EDGE_KEYS_DIR}/${service}.env"
    [ -f "${env_file}" ] || { echo "gen-e2e-signed-directories: ${env_file} is missing — run scripts/gen-e2e-keys.sh first" >&2; exit 2; }
    [ -f "${FIXTURES_DIR}/${fixture}" ] || { echo "gen-e2e-signed-directories: ${FIXTURES_DIR}/${fixture} is missing — run scripts/gen-e2e-keys.sh first" >&2; exit 2; }
    keys="$(sed -n 's/^WBA_KEYS_JSON=//p' "${env_file}")"
    [ -n "${keys}" ] || { echo "gen-e2e-signed-directories: ${env_file} carries no WBA_KEYS_JSON" >&2; exit 2; }
    signed="$(node "${SIGNER}" --authority "${domain}" --keys "${keys}" \
        --signing-key "${FIXTURES_DIR}/${fixture}" --valid-days 3650)"
    kept="$(grep -v '^WBA_SIGNED_DIRECTORY_JSON=' "${env_file}" || true)"
    printf '%s\nWBA_SIGNED_DIRECTORY_JSON=%s\n' "${kept}" "${signed}" > "${env_file}"
    chmod 0644 "${env_file}"
done

echo "gen-e2e-signed-directories: signed the key directories → ${EDGE_KEYS_DIR}/*.env (WBA_SIGNED_DIRECTORY_JSON)"
