#!/usr/bin/env bash
# Prove gen-keys.sh minds its argument: production-aws gets the service keys
# (two Exchange PEMs, the Broker relay key, the Broker identity seed and PEM),
# the catalog-contributor identity the importer signs with and its public
# key directory, signed by that identity's key for its hostname, and the
# publisher console's signing key, but no smoke
# agent; staging-aws and demo-aws get the smoke agent too, and no console key;
# the console key is a PKCS#8 Ed25519 PEM of its own, mode 0600, in the exact
# shape compose-stack's console_signing_key_pem accepts; a first run with no
# STAGING_DOMAIN is refused on every stack, since the contributor id is a
# hostname under it; a rerun needs no STAGING_DOMAIN because the ids are read
# back from the key files, and it keeps the contributor keypair and the
# console key byte for byte; an unknown stack is refused before anything is
# written. The REAL script runs, with real openssl and Python, against a copy
# of deploy/terraform/scripts placed under a temporary repository root, so it
# resolves ../stacks/<stack> to temporary directories and never touches the
# tracked stacks. `terraform` is stubbed to fail, as it would with no state.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "gen-keys guard test FAILED: $1" >&2; exit 1; }
. "$(dirname "${BASH_SOURCE[0]}")/lib/guard.sh"

# The copied script finds lib/staging-env.sh beside it, staging-env.sh finds
# scripts/lib (the Python helpers) four levels up, and the System API key is
# made by deploy/zitadel/bootstrap/system-key.sh — the symlinks put the real
# ones there. The production stack is the one gen-keys.sh mints the console's
# keys for; like the other two it exists here only as a scratch directory.
# published-ref-allow: a scratch copy of the stack directory gen-keys.sh writes into
prod_stack="${work}/deploy/terraform/stacks/production-aws"
mkdir -p "${work}/deploy/terraform/scripts" "${prod_stack}" \
    "${work}/deploy/terraform/stacks/staging-aws" "${work}/deploy/terraform/stacks/demo-aws" "${work}/bin"
cp -R "${REPO_ROOT}/deploy/terraform/scripts/gen-keys.sh" "${REPO_ROOT}/deploy/terraform/scripts/lib" \
    "${work}/deploy/terraform/scripts/"
ln -s "${REPO_ROOT}/scripts" "${work}/scripts"
# gen-keys.sh builds its key-directory signer from src/exchange/cmd/fora-sign-directory
# in the repository root, so the module files, src/ and internal/ are linked in too.
ln -s "${REPO_ROOT}/go.mod" "${work}/go.mod"
ln -s "${REPO_ROOT}/go.sum" "${work}/go.sum"
ln -s "${REPO_ROOT}/src" "${work}/src"
ln -s "${REPO_ROOT}/internal" "${work}/internal"
mkdir -p "${work}/deploy/zitadel"
ln -s "${REPO_ROOT}/deploy/zitadel/bootstrap" "${work}/deploy/zitadel/bootstrap"
printf '#!/usr/bin/env bash\nexit 1\n' > "${work}/bin/terraform"
chmod +x "${work}/bin/terraform"
SCRIPT="${work}/deploy/terraform/scripts/gen-keys.sh"

run_script() {
    out="$(PATH="${work}/bin:${PATH}" "${SCRIPT}" "$@" 2>&1)" && return 0 || return $?
}

service_keys=(ed25519-private.pem rsa-private.pem broker-relay-key.json broker-identity-seed broker-identity-key.pem)
contributor_files=(contributor-key.json catalog-contributor-wba.json)
agent_files=(agent-key.json smoke-agent-wba.json)
smoke_files=("${contributor_files[@]}" "${agent_files[@]}")
console_files=(console-signing-key.pem zitadel-system-api-private.pem zitadel-system-api-public.pem)

# The interpreter gen-keys.sh itself uses, which can import cryptography.
. "${REPO_ROOT}/scripts/lib/select-python.sh"

# expect_signed_directory <directory file> <key file> <host>: the file is the
# pre-signed key directory compose-stack takes, {body, content_digest,
# signature_input, signature}; the body lists exactly the key file's public
# key; content_digest is the SHA-256 of the body; and the one signature is by
# that key, tagged for directories, and verifies over the Web Bot Auth
# directory signature base for <host>. A directory signed for another host, by
# another key, or over other bytes fails here. The check rebuilds the base
# itself rather than calling the signer's SDK, so it does not grade the
# signer with its own code.
expect_signed_directory() {
    "${PYTHON[@]}" - "$@" <<'PY' || fail "$1 is not a key directory signed by $2 for $3"
import base64, hashlib, json, sys
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

path, key_path, host = sys.argv[1:4]
doc = json.load(open(path))
assert sorted(doc) == ["body", "content_digest", "signature", "signature_input"], sorted(doc)
x = json.load(open(key_path))["public_key"]
body = doc["body"]
assert [k["x"] for k in json.loads(body)["keys"]] == [x], body
digest = "sha-256=:" + base64.b64encode(hashlib.sha256(body.encode()).digest()).decode() + ":"
assert doc["content_digest"] == digest, doc["content_digest"]
label, inner = doc["signature_input"].split("=", 1)
thumb = base64.urlsafe_b64encode(
    hashlib.sha256(('{"crv":"Ed25519","kty":"OKP","x":"%s"}' % x).encode()).digest()
).rstrip(b"=").decode()
assert inner.startswith('("@authority";req "content-digest");'), inner
assert ';keyid="%s";' % thumb in inner and inner.endswith(';tag="http-message-signatures-directory"'), inner
sig_label, sig = doc["signature"].split("=", 1)
assert sig_label == label, doc["signature"]
base = '"@authority";req: %s\n"content-digest": %s\n"@signature-params": %s' % (host, digest, inner)
public = Ed25519PublicKey.from_public_bytes(base64.urlsafe_b64decode(x + "=" * (-len(x) % 4)))
public.verify(base64.b64decode(sig.strip(":")), base.encode())
PY
}

# expect_files <stack> <keys dir> <file>...: the stack's key directory holds
# exactly these files, none missing and none extra.
expect_files() {
    local stack="$1" dir="$2" got want
    shift 2
    got="$(ls -A "${dir}" | sort | paste -sd ' ' -)"
    want="$(printf '%s\n' "$@" | sort | paste -sd ' ' -)"
    [ "${got}" = "${want}" ] || fail "${stack} wrote [${got}], expected [${want}]"
}

# production-aws without STAGING_DOMAIN on a first run: refused, nothing
# written -- the contributor id is a hostname under that domain.
prod_keys="${prod_stack}/keys"
if run_script production-aws; then fail "production-aws minted a contributor id with no STAGING_DOMAIN"; fi
[ ! -e "${prod_keys}" ] || fail "the refused production-aws run still wrote ${prod_keys}"

# production-aws: the service keys, the contributor identity, the console's
# signing key and the Zitadel System API pair; no smoke agent.
STAGING_DOMAIN=prod.example run_script production-aws || fail "production-aws run exited $?: ${out}"
expect_files production-aws "${prod_keys}" "${service_keys[@]}" "${contributor_files[@]}" "${console_files[@]}"
grep -q '"kid": "broker.production.v1"' "${prod_keys}/broker-relay-key.json" || fail "the production relay kid is not broker.production.v1: $(cat "${prod_keys}/broker-relay-key.json")"
grep -q '"kid": "catalog-contributor.prod.example"' "${prod_keys}/contributor-key.json" || fail "the production contributor id is not <label>.<STAGING_DOMAIN>: $(cat "${prod_keys}/contributor-key.json")"
# The contributor's directory is signed by its own key for its own hostname.
expect_signed_directory "${prod_keys}/catalog-contributor-wba.json" "${prod_keys}/contributor-key.json" catalog-contributor.prod.example

# The console key must pass compose-stack's console_signing_key_pem
# validation. That validation accepts only the PKCS#8 Ed25519 PEM that
# `openssl genpkey` writes: the header, a 64-character body that starts with
# the fixed PKCS#8 Ed25519 prefix, and the footer. pem_body is copied from the
# validation's pattern, and the two greps fail this test when that pattern no
# longer contains pem_body or the label. A change to the shape in either
# gen-keys.sh or the module therefore fails this test. The label is
# interpolated, so this file never holds a PEM header and its footer as
# literal text: that pair is what the published-tree secret scan matches.
console_key="${prod_keys}/console-signing-key.pem"
pem_label="PRIVATE KEY"
pem_body='MC4CAQAwBQYDK2VwBCIEI[A-Za-z0-9+/]{43}'
module_tf="${REPO_ROOT}/deploy/terraform/modules/compose-stack/console.tf"
grep -qF "${pem_body}" "${module_tf}" && grep -qF "\"${pem_label}\"), var.console_signing_key_pem" "${module_tf}" \
    || fail "compose-stack's console_signing_key_pem validation no longer checks the shape this test checks; change the two together"
[ "$(wc -l <"${console_key}" | tr -d ' ')" = 3 ] || fail "the console key is not one three-line PEM block"
[ "$(sed -n 1p "${console_key}")" = "-----BEGIN ${pem_label}-----" ] || fail "the console key does not start with the PKCS#8 header"
[[ "$(sed -n 2p "${console_key}")" =~ ^${pem_body}$ ]] || fail "the console key's body is not a 64-character PKCS#8 Ed25519 body"
[ "$(sed -n 3p "${console_key}")" = "-----END ${pem_label}-----" ] || fail "the console key does not end with the PKCS#8 footer"
[ "$(mode "${console_key}")" = 600 ] || fail "the console key's mode is $(mode "${console_key}"), not 600"
if cmp -s "${console_key}" "${prod_keys}/ed25519-private.pem"; then fail "the console key is a copy of the Exchange's signing key"; fi

# The Zitadel System API pair: an RSA-3072 private key, root-only, whose public
# half is the other file, and which the module's public-key validation accepts.
system_key="${prod_keys}/zitadel-system-api-private.pem"
openssl pkey -in "${system_key}" -noout -text 2>/dev/null | grep -q '^Private-Key: (3072 bit' \
    || fail "the System API key is not RSA-3072: $(openssl pkey -in "${system_key}" -noout -text 2>&1 | head -n 1)"
[ "$(mode "${system_key}")" = 600 ] || fail "the System API key's mode is $(mode "${system_key}"), not 600"
[ "$(openssl pkey -in "${system_key}" -pubout)" = "$(cat "${prod_keys}/zitadel-system-api-public.pem")" ] \
    || fail "zitadel-system-api-public.pem is not the public half of zitadel-system-api-private.pem"
grep -qF '"^-----BEGIN PUBLIC KEY-----\\n[A-Za-z0-9+/=\\n]+-----END PUBLIC KEY-----\\n?$", var.zitadel_system_api_public_key' "${module_tf}" \
    || fail "compose-stack's zitadel_system_api_public_key validation no longer checks the shape this test checks; change the two together"
grep -Eq '^-----BEGIN PUBLIC KEY-----$' "${prod_keys}/zitadel-system-api-public.pem" \
    && ! grep -vEq '^(-----(BEGIN|END) PUBLIC KEY-----|[A-Za-z0-9+/=]+)$' "${prod_keys}/zitadel-system-api-public.pem" \
    || fail "zitadel-system-api-public.pem is not the PEM shape the module accepts"

# A rerun reads the ids back from the key files: no STAGING_DOMAIN needed.
# It reuses the keys it finds and never rotates them: the contributor keypair
# and the console key keep their exact bytes, and no file is added.
cp "${prod_keys}/contributor-key.json" "${work}/contributor-key.before"
cp "${console_key}" "${work}/console-signing-key.before"
cp "${system_key}" "${work}/system-api-key.before"
# A public half that no longer matches (another key's): the rerun replaces it.
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 2>/dev/null | openssl pkey -pubout \
    > "${prod_keys}/zitadel-system-api-public.pem"
run_script production-aws || fail "production-aws rerun exited $?: ${out}"
cmp -s "${system_key}" "${work}/system-api-key.before" || fail "the production-aws rerun rotated zitadel-system-api-private.pem"
[ "$(openssl pkey -in "${system_key}" -pubout)" = "$(cat "${prod_keys}/zitadel-system-api-public.pem")" ] \
    || fail "the production-aws rerun did not derive zitadel-system-api-public.pem again"
cmp -s "${prod_keys}/contributor-key.json" "${work}/contributor-key.before" || fail "the production-aws rerun rotated contributor-key.json"
cmp -s "${console_key}" "${work}/console-signing-key.before" || fail "the production-aws rerun rotated console-signing-key.pem"
expect_files production-aws "${prod_keys}" "${service_keys[@]}" "${contributor_files[@]}" "${console_files[@]}"

# staging-aws: the service keys plus the smoke identities and their documents,
# and no console key.
STAGING_DOMAIN=zone.example run_script staging-aws || fail "staging-aws run exited $?: ${out}"
stg_keys="${work}/deploy/terraform/stacks/staging-aws/keys"
expect_files staging-aws "${stg_keys}" "${service_keys[@]}" "${smoke_files[@]}"
grep -q '"kid": "smoke-agent.zone.example"' "${stg_keys}/agent-key.json" || fail "the smoke agent id is not <label>.<STAGING_DOMAIN>"
# Each identity's directory is signed by that identity's key for its hostname,
# never by the other one's.
expect_signed_directory "${stg_keys}/smoke-agent-wba.json" "${stg_keys}/agent-key.json" smoke-agent.zone.example
expect_signed_directory "${stg_keys}/catalog-contributor-wba.json" "${stg_keys}/contributor-key.json" catalog-contributor.zone.example
grep -q '"kid": "broker.staging.v1"' "${stg_keys}/broker-relay-key.json" || fail "the staging relay kid is not broker.staging.v1"

# demo-aws: the same files as staging-aws, also with no console key.
STAGING_DOMAIN=demo.publisher.example run_script demo-aws || fail "demo-aws run exited $?: ${out}"
expect_files demo-aws "${work}/deploy/terraform/stacks/demo-aws/keys" "${service_keys[@]}" "${smoke_files[@]}"

# staging-aws without STAGING_DOMAIN on a first run: refused, nothing written.
rm -rf "${stg_keys}"
if run_script staging-aws; then fail "staging-aws minted smoke ids with no STAGING_DOMAIN"; fi
[ ! -e "${stg_keys}" ] || fail "the refused staging-aws run still wrote ${stg_keys}"

# An unknown or missing stack name is a usage error.
if run_script edge; then fail "an unknown stack name was accepted"; fi
grep -q 'usage:' <<<"${out}" || fail "unknown stack did not print usage: ${out}"
if run_script; then fail "a missing stack name was accepted"; fi

echo "gen-keys guard test passed (production-aws: service keys, the contributor identity, the console key and the System API pair, no smoke agent, relay kid broker.production.v1; each identity's key directory signed by its own key for its own hostname; console key: PKCS#8 Ed25519 in the shape compose-stack accepts, mode 0600, not the Exchange's key; System API key: RSA-3072, mode 0600, public half derived again on every run; staging-aws and demo-aws: smoke agent too, no console keys; first run with no STAGING_DOMAIN refused, rerun needs none and keeps the contributor, console and System API keys; unknown stack refused)"
