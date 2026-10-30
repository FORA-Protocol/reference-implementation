#!/usr/bin/env bash
# Generate the Ed25519 identities the docker-compose E2E stack needs. The
# private halves are written under tests/e2e/harness/fixtures/ (gitignored — no
# private key material is tracked in git) where the runner image and the
# key-directories service (deploy/publisher-jwks image) read them.
#
# There is NO shared key registry file: every identity's public key is served
# ONLY under that identity's own host name in docker-compose.e2e.yml (an alias
# of the key-directories service, equal to the identity's kid), and the
# Exchange and Broker learn keys exclusively by fetching those directories —
# the same path production uses.
#
# Identities (kid → fixture file):
#   catalog-contributor-e2e          catalog_contributor_key.json
#   agent-nobilling-e2e              agent_nobilling_e2e_key.json
#   broker.broker-local.v1           broker_relay_key.json   (mounted into broker)
#   test-signer-e2e.v1               test_signer_key.json
#   demo.fora-protocol.org          selfpub_philosophy_key.json  (self-publish, kid=domain)
#   music.demo.fora-protocol.org     selfpub_music_key.json       (self-publish, kid=domain)
#   sfx.demo.fora-protocol.org       selfpub_sfx_key.json         (self-publish, kid=domain)
#   archive.demo.fora-protocol.org   selfpub_archive_key.json     (self-publish, kid=domain; no edge)
#
# NOTE: the kid values above (agent-nobilling-e2e, broker.broker-local.v1, etc.) are
# E2E/demo FIXTURES, not real public DNS domains. In the demo they act as the
# agent identity (the Signature-Agent value) and are reachable only on the
# local docker-compose network, via FORA_WELLKNOWN_{SCHEME,PORT}. Do not
# read them as real registered domains. (The self-publish entries where
# kid=domain are the exception — those are domain-shaped on purpose.)
#
# broker.broker-local.v1 is the Broker relay key. The Broker mounts the private
# fixture and publishes the pubkey in its OWN WBA directory at boot; the
# Exchange resolves it from there (EXCHANGE_BROKER_WELLKNOWN_URL).
#
# Well-known key distribution for publishers: after the WBA split each domain's
# edge SERVES its self key in its Web Bot Auth directory
# (/.well-known/http-message-signatures-directory) keys[] — with NO kid (the key
# is named by its RFC 7638 thumbprint) — so the Exchange learns it via the cached
# well-known fetch (Gate-1 self-signup, matched by caller_id == publisher domain)
# — never a DB pre-seed. This script emits a per-edge env_file under
# deploy/edge-keys/<service>.env carrying WBA_KEYS_JSON=<canonical fora.v1
# JsonWebKey[] for that domain's self key>; docker-compose.e2e.yml injects it via
# `env_file:`. The key distribution is thus derived from the same generated
# private material the signer uses — the served JWK and the signing key can never
# drift.
#
# Idempotent: a kid's keypair is generated ONLY when its private fixture is
# absent (fresh checkout, or after `e2e-down -v` clears them). An existing
# fixture is reused — so a repeat run (e.g. a second `e2e-up` against an
# already-booted stack) does NOT rotate keys, which would mismatch the
# containers' mounted keys.
#
# Private fixtures are written 0644 (world-readable). The broker and the
# key-directories service mount these keys read-only into distroless / nonroot containers; a 0600
# file owned by the generating user (root in CI) is unreadable across that UID
# boundary, so the container exits(1) at boot with "permission denied". 0644 is
# how a read-only key is normally provisioned into a container. This does NOT
# weaken the no-private-key-in-git invariant, which is enforced by .gitignore
# (tests/e2e/**/*key*.json), not by the on-disk mode. The mode is (re)applied
# on the reuse path too, to correct any pre-existing 0600 fixture.
#
# Outputs:
#   tests/e2e/harness/fixtures/<identity>_key.json       (gitignored — private keys)
#   deploy/edge-keys/<service>.env                       (gitignored — derived pubkeys)
#
# `make e2e-keys` runs it before every `make e2e-up`.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FIXTURES_DIR="${REPO_ROOT}/tests/e2e/harness/fixtures"
EDGE_KEYS_DIR="${REPO_ROOT}/deploy/edge-keys"

mkdir -p "$FIXTURES_DIR" "$EDGE_KEYS_DIR"

# Interpreter selection (PYTHON array) shared by every key-gen script.
. "$REPO_ROOT/scripts/lib/select-python.sh"

"${PYTHON[@]}" - "$REPO_ROOT/scripts/lib" "$FIXTURES_DIR" "$EDGE_KEYS_DIR" <<'PY'
import json
import sys
from pathlib import Path

sys.path.insert(0, sys.argv[1])
from ed25519_keys import (
    materialize_keypair,
    wba_directory_key,
    wba_validity_window,
    write_text_atomic,
)

fixtures_dir, edge_keys_dir = sys.argv[2:4]

# (kid, private-fixture filename). One Ed25519 keypair per identity. Every
# identity's pubkey is served solely under its own host name, or, for the
# broker relay key, by the Broker's own WBA directory.
#
# Adding an identity takes FIVE edits:
#   1. this list (mints the key fixture);
#   2. the key-directories service in docker-compose.e2e.yml: a read-only
#      mount of the fixture at /keys/<kid>.json and an alias equal to the kid;
#   3. the kid in STATIC_KEY_HOSTS in tests/e2e/harness/constants.py, the
#      names the in-network directory test checks;
#   4. the kid in the x-through-capture-proxy list in docker-compose.e2e.yml,
#      which sends every service's requests for the host through the capture
#      proxy, the stack's one https endpoint;
#   5. the kid in the certificate SAN list in
#      tests/e2e/capture-proxy/tls-keygen.sh.
# The guard suite tests/e2e/harness/test_guards_identity_lists.py connects
# edits 1-3: it fails when a minted identity is not mounted from its own
# fixture under its own name, when a mount and an alias do not pair up, or
# when the directory test does not check every served name. In the stack,
# tests/e2e/harness/test_capture_proxy.py fetches every name in the
# x-through-capture-proxy list over https with verification on, so a name
# left out of the certificate fails there. Nothing checks edit 4 statically.
IDENTITIES = [
    ("catalog-contributor-e2e", "catalog_contributor_key.json"),
    ("agent-nobilling-e2e", "agent_nobilling_e2e_key.json"),
    ("broker.broker-local.v1", "broker_relay_key.json"),
    ("test-signer-e2e.v1", "test_signer_key.json"),
    # The archive publisher's self-publish key (kid == its domain). Listed here
    # rather than in SELF_PUBLISH_IDENTITIES because that site has no edge to
    # hand an env file to: its directory is served by the key-directories
    # service, which mounts this fixture like every other identity's.
    ("archive.demo.fora-protocol.org", "selfpub_archive_key.json"),
]

# Self-publish identities: kid == publisher domain. Each domain's edge
# SERVES this key in its Web Bot Auth directory (see the emission below) so the
# Exchange learns it via the cached well-known fetch (Gate-1 self-signup); the
# fora.json overlay carries no keys at all. The demo
# ingest signs that publisher's feed with caller_id == kid == domain (Gate-2
# caller==domain). The pubkey is emitted to a per-edge env_file below.
#
# (kid==domain, private-fixture filename, edge env_file basename)
# The philosophy publisher is the BARE APEX demo domain (demo.fora-protocol.org)
# BY DESIGN, while music/sfx are subdomained (music./sfx.demo...). Its fixture
# selfpub_philosophy_key.json carries no 'philosophy' token in its kid because
# kid == domain == the apex — grep-by-domain on the apex, not on 'philosophy'.
SELF_PUBLISH_IDENTITIES = [
    ("demo.fora-protocol.org", "selfpub_philosophy_key.json", "edge"),
    ("music.demo.fora-protocol.org", "selfpub_music_key.json", "fastly-edge"),
    ("sfx.demo.fora-protocol.org", "selfpub_sfx_key.json", "aws-edge"),
]

fixtures = Path(fixtures_dir)


def materialize(kid: str, filename: str) -> str:
    """Ensure a fixture via the shared materializer; return its pubkey b64url.

    0644 (world-readable): the broker / nginx hosts mount these keys into
    distroless / nonroot containers; a 0600 file owned by the generating user
    (root in CI dind) is unreadable across the UID boundary. Applied on the
    reuse path too, to correct any pre-existing 0600 fixture.
    """
    return materialize_keypair(fixtures / filename, kid, mode=0o644)


for kid, filename in IDENTITIES:
    materialize(kid, filename)

# ── Self-publish keys (caller_id == domain) + per-edge WBA_KEYS_JSON ──
# Each edge serves exactly ONE active key (its own self key); selectValidKey in
# the Exchange picks ActiveKeys[0] in doc order, so the served list carries only
# the catalog-signing key. The validity window is wide-open (now-1h .. now+10y)
# so the fixture is never the reason a self-signup fails in the e2e stack.
_not_before, _not_after = wba_validity_window(lifetime_days=3650)

for domain, filename, edge_service in SELF_PUBLISH_IDENTITIES:
    pub_b64 = materialize(domain, filename)
    # WBA directory keys[] item: a pure JWK with NO kid (named by thumbprint),
    # matching the canonical fora-wba-directory.json schema the edge serves.
    wba_keys = [wba_directory_key(pub_b64, _not_before, _not_after)]
    env_path = Path(edge_keys_dir) / f"{edge_service}.env"
    # Atomic: another stack of this checkout may be reading it at compose up.
    write_text_atomic(
        env_path,
        "WBA_KEYS_JSON=" + json.dumps(wba_keys, separators=(",", ":")) + "\n",
        0o644,
    )

print(
    "gen-e2e-keys: wrote "
    + ", ".join(kid for kid, _ in IDENTITIES)
    + f" → {fixtures_dir}/ (private fixtures); "
    + "self-publish keys "
    + ", ".join(d for d, _, _ in SELF_PUBLISH_IDENTITIES)
    + f" → {edge_keys_dir}/*.env (WBA_KEYS_JSON)"
)
PY
