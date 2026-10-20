#!/usr/bin/env bash
# Generate one stack's key material into deploy/terraform/stacks/<stack>/keys/
# (gitignored — no private key material is ever tracked in git).
#
# Usage:
#   deploy/terraform/scripts/gen-keys.sh <staging-aws|demo-aws|production-aws>
#
# Every stack gets the four service keys and the catalog-contributor identity
# the automatic importer signs with. The smoke AGENT is minted for staging-aws
# and demo-aws only; production-aws has no smoke check, so nothing there would
# ever read it. The publisher console's signing key, and the Zitadel System
# API key pair that creates the publishers' Zitadel instance, are minted for
# production-aws only; staging-aws and demo-aws do not run the console.
#
# Outputs:
#   ed25519-private.pem      Exchange offer/URL signing key (uploaded to VM)
#   rsa-private.pem          Exchange RSA key for CloudFront-scheme tenants (uploaded)
#   broker-relay-key.json    Broker relay signing keypair (uploaded)
#   broker-identity-seed     Broker IDENTITY key seed, b64url 32 bytes (LOCAL
#                            source of truth; the derived PEM below is what
#                            ships). A DIFFERENT key from the relay one above:
#                            the relay key signs Broker->Exchange calls (its
#                            pubkey is published in the Broker's own WBA
#                            directory, where the Exchange resolves it), while
#                            this one is the identity the Broker publishes in
#                            that same directory with a 90-day validity window.
#                            It must survive a restart, or the Broker breaks a
#                            promise it just published.
#   broker-identity-key.pem  The same identity key as a PEM (raw 64-byte
#                            payload, the shape the Broker's key-file loader
#                            accepts — NOT PKCS#8). Uploaded to the VM and
#                            read via BROKER_ED25519_KEY_FILE, so the key
#                            ships as a root-owned file like its peers rather
#                            than as an env value in the compose file.
#   console-signing-key.pem  Publisher console signing key, production-aws only
#                            (uploaded to the VM). The console signs its calls
#                            to the Exchange with it and publishes the public
#                            half in its own key directory. A PKCS#8 Ed25519
#                            PEM, as `openssl genpkey` writes it: the console
#                            loads no other key format, and compose-stack's
#                            console_signing_key_pem accepts only this shape.
#   zitadel-system-api-private.pem  Zitadel System API signing key, RSA-3072,
#                            production-aws only. LOCAL ONLY: Terraform never
#                            reads it, it is never uploaded, and nothing on the
#                            VM holds it. bootstrap-identity.sh signs a
#                            five-minute System API token with it when the
#                            publishers' Zitadel instance has to be created or
#                            its bootstrap token recovered.
#   zitadel-system-api-public.pem   Its public half, derived again on every run.
#                            The stack hands it to Zitadel, which checks every
#                            System API token against it.
#   agent-key.json           Smoke agent keypair (LOCAL ONLY — smoke.sh signs
#                            Broker calls with it; no service on the VM reads it)
#   contributor-key.json     Catalog-contributor keypair. Uploaded to the VM
#                            for the catalog importer service alone (the
#                            Exchange never mounts it); on the smoke stacks it
#                            also signs seed-staging.sh's fora-ingest push.
#   smoke-agent-wba.json         PUBLIC Web Bot Auth key directories for the
#   catalog-contributor-wba.json identities above, PRE-SIGNED: the JSON object
#                            {body, content_digest, signature_input,
#                            signature} compose-stack's static_wba_directories
#                            takes. body is the JWK Set (public keys only);
#                            the other three are the response-signature
#                            headers, made by the identity's own key for its
#                            hostname (the kid), because a verifier under the
#                            Web Bot Auth profile takes a key only from a
#                            directory response that key signed for the host
#                            it fetched from. The stack reads them at apply
#                            time and serves each at its identity's hostname
#                            (/.well-known/http-message-signatures-directory),
#                            which is how the Exchange and Broker verify those
#                            signatures. Re-derived and re-signed on every run
#                            from whatever keypair is current.
#
# There is no shared key registry file: services learn verification keys only
# via well-known discovery (each participant publishes its own keys).
#
# Idempotent: existing keypairs are reused (re-running never rotates keys under
# a live stack). Every run does rewrite the public directory documents:
# catalog-contributor-wba.json, and smoke-agent-wba.json on staging-aws and
# demo-aws, keep the same public key and get a new validity window, with
# not_before one hour before the run and not_after 90 days after it, and new
# response signatures valid for the same 90 days.
#
# The signing goes through src/exchange/cmd/fora-sign-directory, built here from this
# repository with go: it signs with the protocol SDK at the version go.mod
# pins, so this script carries no signing code of its own and no second pin.
#
# The identity ids ARE hostnames — <label>.<domain> — because each id is the
# host the services fetch the signer's key directory from. lib/staging-env.sh
# resolves them: from the existing key files' kids, or (first run only) from
# STAGING_DOMAIN — the domain the stack serves the identity directories
# directly under. For stacks/staging-aws, and for the production stack, that is
# the tfvars `domain`; for stacks/demo-aws, where every name nests under the
# publisher label, it is the publisher hostname (<publisher_subdomain>.<domain>):
#
#   STAGING_DOMAIN=staging-zone.example deploy/terraform/scripts/gen-keys.sh staging-aws
#   STAGING_DOMAIN=demo.publisher.example deploy/terraform/scripts/gen-keys.sh demo-aws
#   STAGING_DOMAIN=prod.example deploy/terraform/scripts/gen-keys.sh production-aws

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "${1:-}" in
    staging-aws|demo-aws) smoke_identities=1 console_keys=0 ;;
    production-aws) smoke_identities=0 console_keys=1 ;;
    *) echo "usage: $0 <staging-aws|demo-aws|production-aws>" >&2; exit 2 ;;
esac
# STACK_DIR is set from the argument BEFORE staging-env.sh is sourced, so
# KEYS_DIR — the directory this script WRITES and seed-staging.sh,
# fund-staging-agent.sh and smoke.sh READ — is derived from the same stack.
STACK_DIR="${here}/../stacks/$1"
. "${here}/lib/staging-env.sh"

# Fail fast with one clear line when a tool is absent: openssl generates the
# PEM signing keys, and go builds the key-directory signer. The Python
# interpreter needs no check here — staging-env.sh above already sourced
# select-python.sh, which picks uv or system python3.
command -v openssl >/dev/null 2>&1 || { echo "missing: openssl" >&2; exit 2; }
command -v go >/dev/null 2>&1 || { echo "missing: go (builds src/exchange/cmd/fora-sign-directory, which signs the key directories)" >&2; exit 2; }

# Empty ids mean the key files do not exist yet AND STAGING_DOMAIN is unset —
# there is nothing to mint hostname-shaped ids from (see staging-env.sh).
# Every stack needs the contributor id; only the smoke stacks need the agent.
if [ -z "${CONTRIBUTOR_ID}" ] || { [ "${smoke_identities}" = 1 ] && [ -z "${AGENT_ID}" ]; }; then
    echo "the identity key files do not exist yet, so their ids must be minted:" >&2
    echo "set STAGING_DOMAIN to the domain the stack serves the identity directories under and re-run, e.g." >&2
    echo "  STAGING_DOMAIN=staging-zone.example $0 staging-aws   # the tfvars 'domain'" >&2
    echo "  STAGING_DOMAIN=demo.publisher.example $0 demo-aws    # the publisher hostname" >&2
    echo "  STAGING_DOMAIN=prod.example $0 production-aws        # the tfvars 'domain'" >&2
    echo "(each id becomes <label>.<STAGING_DOMAIN> — the hostname the stack serves that identity's key directory at)" >&2
    exit 2
fi
[ "${smoke_identities}" = 1 ] || AGENT_ID=""

mkdir -p "${KEYS_DIR}"
chmod 0700 "${KEYS_DIR}"

# PEM signing keys (reused when present, like every keypair here).
if [ ! -f "${KEYS_DIR}/ed25519-private.pem" ]; then
    openssl genpkey -algorithm ED25519 -out "${KEYS_DIR}/ed25519-private.pem"
fi
if [ ! -f "${KEYS_DIR}/rsa-private.pem" ]; then
    openssl genrsa -out "${KEYS_DIR}/rsa-private.pem" 2048
fi

# The publisher console's signing key, reused when present like the keys
# above. genpkey writes the PKCS#8 form, the only one the console loads. The
# key goes to a temporary name first and is renamed into place complete, so an
# interrupted run cannot leave a partial file that the existence check would
# keep on every later run. The chmod sets 0600 whatever mode the openssl build
# creates the file with.
if [ "${console_keys}" = 1 ] && [ ! -f "${KEYS_DIR}/console-signing-key.pem" ]; then
    openssl genpkey -algorithm ED25519 -out "${KEYS_DIR}/console-signing-key.pem.tmp"
    chmod 0600 "${KEYS_DIR}/console-signing-key.pem.tmp"
    mv "${KEYS_DIR}/console-signing-key.pem.tmp" "${KEYS_DIR}/console-signing-key.pem"
fi

# The key-directory signer, built into a temporary directory removed on exit.
# It signs through the protocol SDK at the version go.mod pins.
signer_dir="$(mktemp -d)"
trap 'rm -rf "${signer_dir}"' EXIT
go -C "${REPO_ROOT}" build -o "${signer_dir}/fora-sign-directory" ./src/exchange/cmd/fora-sign-directory
# The Zitadel System API key pair, made by the script every stack uses for it:
# the private key once and kept, written the same way, and the public half
# derived from it on every run, so the file the stack reads can never describe
# another key.
if [ "${console_keys}" = 1 ]; then
    sh "${here}/../../zitadel/bootstrap/system-key.sh" "${KEYS_DIR}/zitadel-system-api-private.pem" \
        "${KEYS_DIR}/zitadel-system-api-public.pem"
fi

# The Python below stays inline in this shell script on purpose. It is mostly
# glue — write each keypair file if it does not exist yet — with one
# deliberate exception: the Broker identity PEM derivation at the bottom
# hand-rolls a non-standard PEM (raw
# 64-byte payload) because its ONLY consumer is the Broker's own loader and no
# library writes that shape. Everything else imports the shared helpers in
# scripts/lib. Moving this into a Python module of its own would mean one more
# package to maintain, for a utility an operator runs once per environment.
"${PYTHON[@]}" - "${REPO_ROOT}/scripts/lib" "${KEYS_DIR}" \
    "${BROKER_RELAY_KID}" "${AGENT_ID}" "${CONTRIBUTOR_ID}" "${signer_dir}/fora-sign-directory" <<'PY'
import base64
import json
import subprocess
import sys
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import Encoding, PublicFormat

sys.path.insert(0, sys.argv[1])
from ed25519_keys import (
    generate_seed_pub_b64,
    materialize_keypair,
    wba_directory_key,
    wba_validity_window,
)

keys_dir = Path(sys.argv[2])
broker_relay_kid, agent_id, contributor_id = sys.argv[3:6]

# (kid, filename). Same JSON shape as the e2e fixtures — the services,
# fora-ingest and the catalog importer read {kid, kty, crv, alg, private_key,
# public_key}. An empty agent id means the stack has no smoke check
# (production); the contributor is minted for every stack.
IDENTITIES = [(broker_relay_kid, "broker-relay-key.json"), (contributor_id, "contributor-key.json")]
if agent_id:
    IDENTITIES.append((agent_id, "agent-key.json"))


# Shared materializer: reuse-never-rotate, issuer stamped (and backfilled on
# reuse) so a staging key file mounted into a jwks host passes its issuer
# guard, 0600 — these are operator-held keys that never leave this machine.
pubkeys = {
    filename: materialize_keypair(keys_dir / filename, kid)
    for kid, filename in IDENTITIES
}

# Public Web Bot Auth directory documents for the operator-held identities:
# the contributor on every stack, the smoke agent where one exists. The stack
# serves each body byte for byte at its identity's hostname
# (/.well-known/http-message-signatures-directory), so the Exchange and Broker
# can verify those signatures from the public half alone. The relay key needs
# none: the Broker publishes it in its own directory. Directory keys carry NO
# kid — a verifier names them by RFC 7638 thumbprint. Re-derived
# unconditionally so the served document can never drift from the keypair it
# describes.
#
# The validity window is 90 days, matching what the Broker publishes for its
# own keys. These are real public hostnames whose private halves sit on an
# operator's machine with no revocation channel, so the window is the only
# thing that ever retires a leaked smoke key — a wide window would leave it
# verifying for years. Re-running this script before an apply is already the
# documented refresh path, so the bound costs nothing; a smoke run against a
# stack whose documents are older than 90 days fails signature verification,
# and the fix is exactly that documented path (re-run this script, re-apply).
_not_before, _not_after = wba_validity_window(lifetime_days=90)

# Each document is written pre-signed: the Web Bot Auth profile has a verifier
# take a key only from a directory response that key signed, for the host it
# fetched the directory from, and Caddy on the VM holds no key to sign with.
# The identity's id IS that host, so the signature is made for the kid, by the
# one key the directory lists, over the exact body Caddy will serve. The
# signatures last the same 90 days as the key window, so one refresh renews
# both. The result is the {body, content_digest, signature_input, signature}
# object compose-stack's static_wba_directories takes.
directory_signer = Path(sys.argv[6])
WBA_DOCUMENTS = [(contributor_id, "contributor-key.json", "catalog-contributor-wba.json")]
if agent_id:
    WBA_DOCUMENTS.append((agent_id, "agent-key.json", "smoke-agent-wba.json"))

for kid, key_filename, wba_filename in WBA_DOCUMENTS:
    document = {"keys": [wba_directory_key(pubkeys[key_filename], _not_before, _not_after)]}
    body = json.dumps(document, indent=2) + "\n"
    body_path = directory_signer.parent / f"{wba_filename}.body"
    body_path.write_text(body)
    signed = subprocess.run(
        [str(directory_signer), "-key", str(keys_dir / key_filename), "-authority", kid,
         "-body", str(body_path), "-days", "90"],
        capture_output=True, text=True,
    )
    if signed.returncode != 0:
        sys.exit(f"signing {wba_filename} for {kid} failed: {signed.stderr.strip()}")
    headers = dict(line.split(": ", 1) for line in signed.stdout.splitlines())
    wba_path = keys_dir / wba_filename
    wba_path.write_text(json.dumps({
        "body": body,
        "content_digest": headers["Content-Digest"],
        "signature_input": headers["Signature-Input"],
        "signature": headers["Signature"],
    }, indent=2) + "\n")
    wba_path.chmod(0o644)

# The Broker's IDENTITY key. It is outbound-only (it stamps the intermediary
# attestation) and the Broker publishes its own public half in its WBA
# directory.
#
# Two artifacts, one key. The seed is the compact source of truth; the PEM is
# derived from it deterministically and is what ships to the VM, so the key
# travels as a root-owned key FILE like its peers (BROKER_ED25519_KEY_FILE),
# never as an env value inside the world-readable compose file. The PEM is
# deliberately NOT the PKCS#8 shape `openssl genpkey` writes: the Broker's
# key-file loader wants the raw 64-byte ed25519 private key (seed || public
# key) as the block payload, and this script is the producer of that shape.
# Derive-if-missing means a deployment that already has a seed gets a PEM for
# the SAME key — no identity rotation.
seed_path = keys_dir / "broker-identity-seed"
pem_path = keys_dir / "broker-identity-key.pem"


def pem_payload(path):
    return base64.b64decode(
        "".join(
            line
            for line in path.read_text().splitlines()
            if line and not line.startswith("-----")
        )
    )


if not seed_path.exists():
    if pem_path.exists():
        # A lost seed is recoverable: the PEM payload is seed || public key,
        # so its first 32 bytes ARE the seed. Re-derive it and keep the
        # identity. Minting a fresh seed here instead would first write a
        # decoy that can never match the PEM, then push the operator toward
        # the identity rotation this design exists to prevent.
        identity_seed = (
            base64.urlsafe_b64encode(pem_payload(pem_path)[:32]).decode().rstrip("=")
        )
    else:
        identity_seed, _ = generate_seed_pub_b64()
    seed_path.write_text(identity_seed + "\n")
    seed_path.chmod(0o600)

seed_b64 = seed_path.read_text().strip()
seed_bytes = base64.urlsafe_b64decode(seed_b64 + "=" * (-len(seed_b64) % 4))
key = Ed25519PrivateKey.from_private_bytes(seed_bytes)
pub_bytes = key.public_key().public_bytes(Encoding.Raw, PublicFormat.Raw)

if pem_path.exists():
    # Both artifacts exist: prove they are still the SAME key. Without this, a
    # deleted-and-regenerated seed would silently diverge from the PEM that
    # keeps shipping, and a later PEM re-derivation would rotate the Broker's
    # published identity mid-window — the promise-break this whole design
    # exists to prevent.
    if pem_payload(pem_path)[:32] != seed_bytes:
        sys.exit(
            f"{pem_path.name} does not match {seed_path.name} — the two have "
            "diverged (was the seed deleted and regenerated?). Delete BOTH to "
            "mint a fresh identity, or restore the matching seed. Careful: a "
            "fresh identity breaks the validity window the Broker already "
            "published."
        )
else:
    payload = base64.b64encode(seed_bytes + pub_bytes).decode()
    body = "\n".join(payload[i : i + 64] for i in range(0, len(payload), 64))
    # Write-to-temp + atomic rename: an interrupted run must not leave a
    # truncated PEM behind, because the exists() guard above would then treat
    # the stump as final on every later run.
    tmp_path = pem_path.with_suffix(".pem.tmp")
    # The label is named once and interpolated into both halves so this file
    # never carries a BEGIN header and its matching END footer as literals.
    # That pair is what the secret scanner matches on, and it matches whatever
    # sits between the two. The bytes written are unchanged: the label the
    # Broker's loader and the broker_identity_key_pem validation both read is
    # the rendered one.
    label = "ED25519 PRIVATE KEY"
    tmp_path.write_text(f"-----BEGIN {label}-----\n{body}\n-----END {label}-----\n")
    tmp_path.chmod(0o600)
    tmp_path.replace(pem_path)

print(
    "gen-keys: "
    + ", ".join(kid for kid, _ in IDENTITIES)
    + f" -> {keys_dir}/ (private keypairs + broker identity seed/PEM;"
    + " public WBA directory documents for the contributor"
    + (" and the smoke agent)" if agent_id else ")")
)
PY

echo "done. keys are in ${KEYS_DIR} (gitignored)."
