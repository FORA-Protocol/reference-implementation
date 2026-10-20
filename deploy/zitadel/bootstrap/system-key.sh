#!/usr/bin/env sh
# system-key.sh PRIVATE PUBLIC — the Zitadel System API key pair, the one way
# every stack makes it: deploy/terraform/scripts/gen-keys.sh for a deployment,
# local-system-key.sh for a laptop or end-to-end stack.
#
#   PRIVATE  an RSA-3072 key, made once and then kept: Zitadel verifies System
#            API tokens against its public half for as long as the stack lives.
#            It is written under umask 077 to a temporary name and renamed into
#            place, so an interrupted run never leaves a partial key that a
#            later run would keep.
#   PUBLIC   its public half, derived again on every run, so the file Zitadel
#            reads can never describe another key.
#
# Exit codes: 0 ok, 2 usage, anything else from openssl.
set -eu

[ "$#" = 2 ] || { echo "usage: system-key.sh PRIVATE PUBLIC" >&2; exit 2; }
private="$1"
public="$2"

if [ ! -s "$private" ]; then
    (umask 077 && openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out "$private.tmp" 2>/dev/null)
    mv "$private.tmp" "$private"
    echo "wrote $private"
fi
openssl pkey -in "$private" -pubout -out "$public.tmp"
chmod 0644 "$public.tmp"
mv "$public.tmp" "$public"
