#!/usr/bin/env bash
# Prove deploy/zitadel/bootstrap/system-token.sh mints the token Zitadel's
# System API accepts, and nothing else: an RS256 JWT whose signature verifies
# with the public half of the key, whose claims are exactly iss, sub, aud, iat
# and exp, dated a minute back and valid for five minutes, printed alone on
# stdout. Bad input exits 2 with nothing on stdout: a key that is not RSA, a
# key that cannot be read, an audience that is not an http(s) URL, a user or
# audience with a quote, backslash or space, and the wrong number of arguments.
# local-system-key.sh, which laptop and e2e stacks use, keeps its key across
# runs and mints a fresh, verifiable token each time; system-key.sh, which
# makes the key for it and for gen-keys.sh, replaces an empty key file and
# leaves no temporary file. (A real Zitadel accepting these tokens is
# zitadel-instances-test.py's job.)
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SCRIPT="${REPO_ROOT}/deploy/zitadel/bootstrap/system-token.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "zitadel system-token guard test FAILED: $1" >&2; exit 1; }
. "$(dirname "${BASH_SOURCE[0]}")/lib/guard.sh"

openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "${work}/key.pem" 2>/dev/null
openssl pkey -in "${work}/key.pem" -pubout -out "${work}/key.pub"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "${work}/other.pem" 2>/dev/null
openssl pkey -in "${work}/other.pem" -pubout -out "${work}/other.pub"
openssl genpkey -algorithm ed25519 -out "${work}/ed25519.pem"

# b64url_decode — base64url to bytes, padding restored.
b64url_decode() {
    local s
    s="$(tr '_-' '/+' <<<"$1")"
    case $((${#s} % 4)) in 2) s="${s}==" ;; 3) s="${s}=" ;; esac
    openssl base64 -d -A <<<"${s}"
}

# A token for the e2e stack's audience, default user.
before="$(date +%s)"
token="$("${SCRIPT}" "${work}/key.pem" http://zitadel:8080)"
after="$(date +%s)"
[ "$(grep -c . <<<"${token}")" = 1 ] || fail "stdout is not exactly one line: ${token}"
IFS=. read -r header claims signature extra <<<"${token}"
[ -n "${signature}" ] && [ -z "${extra:-}" ] || fail "not a three-part JWT: ${token}"
[ "$(b64url_decode "${header}")" = '{"alg":"RS256","typ":"JWT"}' ] || fail "header: $(b64url_decode "${header}")"
decoded="$(b64url_decode "${claims}")"
[ "$(jq -c 'keys' <<<"${decoded}")" = '["aud","exp","iat","iss","sub"]' ] || fail "claims carry other fields: ${decoded}"
[ "$(jq -r '.iss, .sub, .aud' <<<"${decoded}" | paste -sd' ' -)" = "fora-instance-admin fora-instance-admin http://zitadel:8080" ] \
    || fail "iss/sub/aud: ${decoded}"
iat="$(jq -r .iat <<<"${decoded}")"; exp="$(jq -r .exp <<<"${decoded}")"
[ "${iat}" -ge $((before - 60)) ] && [ "${iat}" -le $((after - 60)) ] || fail "iat ${iat} is not a minute before the run (${before}..${after})"
[ $((exp - iat)) = 360 ] || fail "exp is not five minutes after the run: iat ${iat}, exp ${exp}"
printf '%s.%s' "${header}" "${claims}" > "${work}/signed"
b64url_decode "${signature}" > "${work}/signature"
openssl dgst -sha256 -verify "${work}/key.pub" -signature "${work}/signature" "${work}/signed" >/dev/null \
    || fail "the signature does not verify with the key's public half"
if openssl dgst -sha256 -verify "${work}/other.pub" -signature "${work}/signature" "${work}/signed" >/dev/null 2>&1; then
    fail "the signature verifies with an unrelated key"
fi

# The user is an argument; the audience is copied verbatim.
token="$("${SCRIPT}" "${work}/key.pem" https://login.example ops-recovery)"
IFS=. read -r _ claims _ <<<"${token}"
[ "$(b64url_decode "${claims}" | jq -r '.iss, .sub, .aud' | paste -sd' ' -)" = "ops-recovery ops-recovery https://login.example" ] \
    || fail "a named user: $(b64url_decode "${claims}")"

# refused ARGS... — exit 2 and an empty stdout.
refused() {
    local status=0 out
    out="$("${SCRIPT}" "$@" 2>/dev/null)" || status=$?
    [ "${status}" = 2 ] || fail "'$*' exited ${status}, want 2"
    [ -z "${out}" ] || fail "'$*' printed something on stdout: ${out}"
}
refused "${work}/ed25519.pem" https://login.example
refused "${work}/missing.pem" https://login.example
refused "${work}/key.pem" login.example
refused "${work}/key.pem" ftp://login.example
refused "${work}/key.pem" 'https://login.example/"x'
refused "${work}/key.pem" https://login.example 'a"b'
refused "${work}/key.pem" https://login.example 'a\b'
refused "${work}/key.pem" https://login.example 'a b'
refused "${work}/key.pem"
refused "${work}/key.pem" https://login.example user extra

# local-system-key.sh, the laptop and e2e stacks' keeper: the key is made once
# and kept across runs, the public half matches it, and every run mints a fresh
# token for the given audience that verifies with that public half.
LOCAL="${REPO_ROOT}/deploy/zitadel/bootstrap/local-system-key.sh"
KEY_DIR="${work}/systemapi" AUDIENCE=http://zitadel:8080 "${LOCAL}" >/dev/null
first_key="$(cat "${work}/systemapi/private.pem")"; first_token="$(cat "${work}/systemapi/token")"
sleep 1
KEY_DIR="${work}/systemapi" AUDIENCE=http://zitadel:8080 "${LOCAL}" >/dev/null
[ "$(cat "${work}/systemapi/private.pem")" = "${first_key}" ] || fail "a second run replaced the System API key"
[ "$(cat "${work}/systemapi/token")" != "${first_token}" ] || fail "a second run did not mint a fresh token"
[ "$(openssl pkey -in "${work}/systemapi/private.pem" -pubout)" = "$(cat "${work}/systemapi/public.pem")" ] \
    || fail "public.pem is not the key's public half"
IFS=. read -r header claims signature <<<"$(cat "${work}/systemapi/token")"
printf '%s.%s' "${header}" "${claims}" > "${work}/signed"
b64url_decode "${signature}" > "${work}/signature"
openssl dgst -sha256 -verify "${work}/systemapi/public.pem" -signature "${work}/signature" "${work}/signed" >/dev/null \
    || fail "the local token does not verify with the local public key"
[ "$(b64url_decode "${claims}" | jq -r .aud)" = http://zitadel:8080 ] || fail "the local token's audience: $(b64url_decode "${claims}")"
[ "$(mode "${work}/systemapi/private.pem")" = 600 ] && [ "$(mode "${work}/systemapi/token")" = 600 ] \
    || fail "the private key and token must be 0600: $(mode "${work}/systemapi/private.pem") $(mode "${work}/systemapi/token")"
if (KEY_DIR="${work}/systemapi" "${LOCAL}" >/dev/null 2>&1); then fail "a run without AUDIENCE succeeded"; fi

# system-key.sh, which both key keepers use: a key file that an interrupted
# run left empty is replaced, the temporary files are gone afterwards, and the
# public half is written again to match the key.
KEYGEN="${REPO_ROOT}/deploy/zitadel/bootstrap/system-key.sh"
mkdir -p "${work}/keys"
: > "${work}/keys/private.pem"
printf 'stale public half\n' > "${work}/keys/public.pem"
sh "${KEYGEN}" "${work}/keys/private.pem" "${work}/keys/public.pem" >/dev/null
openssl pkey -in "${work}/keys/private.pem" -noout 2>/dev/null || fail "an empty key file was kept"
[ "$(openssl pkey -in "${work}/keys/private.pem" -text -noout | head -n 1)" = "Private-Key: (3072 bit, 2 primes)" ] \
    || fail "system-key.sh did not make an RSA-3072 key: $(openssl pkey -in "${work}/keys/private.pem" -text -noout | head -n 1)"
[ "$(openssl pkey -in "${work}/keys/private.pem" -pubout)" = "$(cat "${work}/keys/public.pem")" ] \
    || fail "the public half was not derived again"
[ "$(mode "${work}/keys/private.pem")" = 600 ] && [ "$(mode "${work}/keys/public.pem")" = 644 ] \
    || fail "key modes: $(mode "${work}/keys/private.pem") $(mode "${work}/keys/public.pem")"
[ -z "$(find "${work}/keys" -name '*.tmp')" ] || fail "temporary key files were left behind: $(ls "${work}/keys")"
status=0; sh "${KEYGEN}" "${work}/keys/private.pem" >/dev/null 2>&1 || status=$?
[ "${status}" = 2 ] || fail "system-key.sh with one argument exited ${status}, want 2"

echo "zitadel system-token guard test passed (RS256 signed by the key; exact claims; iat a minute back, five-minute life; bad input exits 2; local key kept, fresh token; system-key.sh replaces an empty key)"
