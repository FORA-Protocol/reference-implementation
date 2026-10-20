#!/bin/sh
# Mints the publisher console's Ed25519 signing key for a local or end-to-end
# stack. It is NOT a deployment step: a real deployment generates the key once
# and keeps it in its secret store (see the console's deployment document).
#
# Idempotent: an existing key is left alone, so restarting the stack does not
# invalidate signatures the Exchange has already learned.
set -eu

KEY_DIR="${KEY_DIR:-/keys}"
KEY_FILE="$KEY_DIR/console-signing-key.pem"

mkdir -p "$KEY_DIR"
if [ -s "$KEY_FILE" ]; then
    echo "console signing key already present at $KEY_FILE"
    exit 0
fi

openssl genpkey -algorithm ed25519 -out "$KEY_FILE"
chmod 0444 "$KEY_FILE"
echo "wrote $KEY_FILE"
