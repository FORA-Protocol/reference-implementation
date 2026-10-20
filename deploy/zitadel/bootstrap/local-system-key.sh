#!/usr/bin/env sh
# Gives a laptop or end-to-end stack its System API key and a fresh token. It is
# NOT a deployment step: a deployment's key is generated once by
# deploy/terraform/scripts/gen-keys.sh and its private half stays with the
# operators; only a short-lived token minted from it reaches the stack.
#
# Writes into KEY_DIR (default /systemapi), through system-key.sh:
#   private.pem   the RSA key, made once and kept: Zitadel keeps verifying
#                 against the public half for as long as the stack lives
#   public.pem    its public half, which the stack's Zitadel reads through
#                 ZITADEL_SYSTEMAPIUSERS (Path), at its first System API call
#   token         a token from system-token.sh for AUDIENCE, minted on every
#                 run: it lives five minutes, so the stack runs this right
#                 before the step that uses it
#
# Inputs (env): AUDIENCE (required) — Zitadel's external URL, for example
# http://zitadel:8080 on the e2e stack; KEY_DIR.
set -eu

KEY_DIR="${KEY_DIR:-/systemapi}"
AUDIENCE="${AUDIENCE:?AUDIENCE is required, e.g. http://zitadel:8080}"

mkdir -p "$KEY_DIR"
sh "$(dirname "$0")/system-key.sh" "$KEY_DIR/private.pem" "$KEY_DIR/public.pem"

(umask 077 && sh "$(dirname "$0")/system-token.sh" "$KEY_DIR/private.pem" "$AUDIENCE" >"$KEY_DIR/token.new")
mv "$KEY_DIR/token.new" "$KEY_DIR/token"
echo "wrote a System API token for $AUDIENCE to $KEY_DIR/token"
