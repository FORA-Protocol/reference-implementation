#!/usr/bin/env bash
# Generate the demo ExampleNews subscription publisher's Ed25519 keypair, whose
# public half the e2e docker stack's key-directories service serves in the
# examplenews key directory, signing that directory's response with it.
#
# No private-key material is tracked in git. This file is generated at
# bootstrap and mounted (read-only, at /keys/examplenews.json) into the
# key-directories container from deploy/publisher-keys by docker-compose.e2e.yml. `make e2e-keys` runs it
# before every `make e2e-up`.
#
# Idempotent: the file is replaced on each run, in one rename, and written 0600.
#
# Usage:
#   scripts/gen-examplenews-publisher-key.sh                       # default kid=examplenews.sub.2026q2
#   PUBLISHER_KID=examplenews.sub.2026q3 scripts/gen-examplenews-publisher-key.sh
#
# Outputs:
#   deploy/publisher-keys/examplenews-subscription-key.json  (gitignored — private key)

set -euo pipefail

PUBLISHER_KID="${PUBLISHER_KID:-examplenews.sub.2026q2}"
# The manifest domain the key-directories entrypoint publishes — the compose alias.
PUBLISHER_ISSUER="${PUBLISHER_ISSUER:-examplenews}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KEY_FILE="${REPO_ROOT}/deploy/publisher-keys/examplenews-subscription-key.json"

mkdir -p "$(dirname "$KEY_FILE")"

# Interpreter selection (PYTHON array) shared by every key-gen script.
. "$REPO_ROOT/scripts/lib/select-python.sh"

"${PYTHON[@]}" - "$REPO_ROOT/scripts/lib" "$PUBLISHER_KID" "$PUBLISHER_ISSUER" "$KEY_FILE" <<'PY'
import json
import sys

sys.path.insert(0, sys.argv[1])
from ed25519_keys import generate_seed_pub_b64, write_text_atomic

kid, issuer, key_path = sys.argv[2:5]

priv_seed_b64, _pub_b64 = generate_seed_pub_b64()

# Shape consumed by deploy/publisher-jwks/entrypoint.sh: the Ed25519 seed under
# "private". The entrypoint derives the pubkey and renders the verify-side JWK.
# Atomic: another stack of this checkout may be starting its key-directories,
# which reads this file, while this run replaces it.
write_text_atomic(key_path, json.dumps({
    "kid": kid,
    # The key-directories entrypoint requires `issuer` for the manifest domain
    # (it refuses to guess from the kid), and refuses a key whose issuer is not
    # the host it is mounted under: this publisher's domain is its compose
    # alias, the kid's brand prefix.
    "issuer": issuer,
    "alg": "EdDSA",
    "use": "verify",
    "private": priv_seed_b64,
}, indent=2, sort_keys=True) + "\n", 0o600)

print(f"wrote {key_path} (kid={kid})")
PY
