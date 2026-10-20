#!/usr/bin/env sh
# Mints a token for Zitadel's System API: a JWT signed with RS256 by the private
# half of a System API user's key. Zitadel holds the public half in
# ZITADEL_SYSTEMAPIUSERS and checks, besides the signature:
#   iss and sub  the user's name in ZITADEL_SYSTEMAPIUSERS
#   aud          Zitadel's external URL, scheme and host, plus :port only when
#                the port is not the scheme's default: https://login.example on
#                a deployment, http://zitadel:8080 on the e2e stack
#   iat          not in the future and at most one hour old. The token is dated
#                a minute back, so a clock running a little ahead of Zitadel's
#                does not get it refused.
#   exp          not passed. The token lives five minutes: it is minted for one
#                run and is useless afterwards.
#
# The System API can create and delete instances, so the key needs the same care
# as Zitadel's master key: it stays with the operators, outside the repository
# and the deployment bundle. Run this where the key is; hand the run only the
# token.
#
# Usage: system-token.sh KEY_FILE AUDIENCE [USER]
#   USER defaults to fora-instance-admin, the name the stacks configure.
# Prints the token, and nothing else, on stdout.
# Exit codes: 0 ok, 2 bad input.
set -eu

usage() {
    printf 'usage: %s KEY_FILE AUDIENCE [USER]\n' "$0" >&2
    exit 2
}
[ "$#" -ge 2 ] && [ "$#" -le 3 ] || usage
key="$1"
aud="$2"
user="${3:-fora-instance-admin}"

case "$aud" in
    http://?* | https://?*) ;;
    *)
        printf 'system-token: the audience must be an http(s) URL, not %s\n' "$aud" >&2
        exit 2
        ;;
esac
# Both values go into the claims verbatim, so neither may carry a quote, a
# backslash or a space.
case "$aud$user" in
    *[\"\\\ ]*)
        printf 'system-token: quotes, backslashes and spaces are not allowed in the audience or user\n' >&2
        exit 2
        ;;
esac
[ -n "$user" ] || usage
[ -r "$key" ] || {
    printf 'system-token: cannot read the key file %s\n' "$key" >&2
    exit 2
}
# Zitadel verifies the signature with an RSA public key only. Every openssl and
# LibreSSL in use prints publicExponent for an RSA private key and nothing like
# it for any other kind.
openssl pkey -in "$key" -noout -text 2>/dev/null | grep -q 'publicExponent' || {
    printf 'system-token: %s is not an RSA private key\n' "$key" >&2
    exit 2
}

b64url() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }

now="$(date +%s)"
header="$(printf '{"alg":"RS256","typ":"JWT"}' | b64url)"
claims="$(printf '{"iss":"%s","sub":"%s","aud":"%s","iat":%d,"exp":%d}' \
    "$user" "$user" "$aud" "$((now - 60))" "$((now + 300))" | b64url)"
signature="$(printf '%s.%s' "$header" "$claims" | openssl dgst -sha256 -sign "$key" -binary | b64url)"
printf '%s.%s.%s\n' "$header" "$claims" "$signature"
