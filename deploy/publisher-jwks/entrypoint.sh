#!/bin/sh
# key-directories entrypoint — serve the discovery documents of every fixed e2e
# identity from one nginx, then exec nginx.
#
# Each identity's Ed25519 private key is mounted as one file, /keys/<host>.json,
# where <host> is the name the identity is served under (its network alias). For
# each key this script writes, under that host's own document root, TWO files:
#   * /.well-known/fora.json — the KEYLESS commercial overlay manifest:
#       {"ver": "1.0", "role": <ROLE_PUBLISHER or ROLE_AGENT>, "domain": <issuer>,
#        "catalog_contributors": [...], "exchanges": [...]}
#     (the last two only for a host PUBLISHER_HOSTS_JSON names);
#   * /.well-known/http-message-signatures-directory — the Web Bot Auth
#     directory (a JWK Set; the key carries NO kid, named by RFC 7638 thumbprint):
#       {"keys": [{"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA",
#                  "x": <base64url raw 32-byte pubkey>,
#                  "not_before": <RFC3339>, "not_after": <RFC3339>}]}
# and one nginx server block that serves them under that host name and no other.
# A request for any other name reaches the catch-all server in nginx.conf, which
# answers the health probe and 404 for everything else, so it never receives an
# identity's keys.
#
# Identity is anchored by WHERE a directory is fetched from, not by anything
# inside it: the directory carries no role and no domain. That makes the pairing
# of a key with its host the one thing that must not slip, so a key whose issuer
# is not the host it is mounted under stops the container at start, instead of
# serving one identity's key under another identity's name.
#
# not_before/not_after are wide-open (now-1h .. now+10y) so the fixture is never
# the reason a self-signup fails in the e2e stack.
#
# PUBLISHER_HOSTS_JSON maps each publisher host to its manifest fields:
#   {"<host>": {"exchanges": [...], "catalog_contributors": [...]}}
# Both members are optional. exchanges defaults to none; catalog_contributors
# defaults to the harness contributor, and an empty list leaves it out. A host
# the map does not name is an agent, whose manifest carries only its role and
# domain.

set -eu

KEYS_DIR=/keys
# The shared ed25519_keys.py (the one WBA entry shape + validity-window helper),
# copied in by the Dockerfile.
PY_LIB_DIR=/opt/fora/lib
WEB_ROOT=/var/www/fora
# One server block per host; nginx.conf includes /etc/nginx/hosts/*.conf.
HOSTS_CONF=/etc/nginx/hosts
# The response-signature headers of each host's directory, included only by that
# host's directory location. They stay outside the glob above: included at the
# http level, every host's signature would be sent with every response.
SIGNATURES=/etc/nginx/signatures

# render_host KEY HOST ROLE EXCHANGES_JSON CATALOG_CONTRIBUTORS_JSON writes HOST's
# fora.json and Web Bot Auth directory under $WEB_ROOT/HOST/.well-known, from the
# private key in KEY. The manifest's domain is the key's issuer: the override is
# cleared, so the check after rendering compares the key's own claim to the host.
render_host() {
    mkdir -p "$WEB_ROOT/$2/.well-known"
    EXCHANGES_JSON="$4" \
    CATALOG_CONTRIBUTORS_JSON="$5" \
    MANIFEST_DOMAIN_OVERRIDE="" \
    python3 - "$1" "$WEB_ROOT/$2/.well-known/fora.json" "" "$3" "$PY_LIB_DIR" <<'PY'
import base64
import json
import os
import sys
from pathlib import Path

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import (
    Encoding,
    PublicFormat,
)

key_path, manifest_path, kid_override, role, py_lib_dir = sys.argv[1:6]

# The WBA entry shape and validity window come from the same shared module
# every key-gen script uses (copied into the image by the Dockerfile), so the
# served directory cannot drift from the shape those scripts emit.
sys.path.insert(0, py_lib_dir)
from ed25519_keys import wba_directory_key, wba_validity_window

with Path(key_path).open() as fh:
    spec = json.load(fh)

# The resource-owner key fixture stores the Ed25519 seed (32 bytes) base64url-
# encoded under "private". Some fixtures use "seed"; the e2e harness contributor
# fixture (gen-e2e-keys.sh) and the agent
# fixtures (shared with the pytest signer) use "private_key". Accept any of the three.
priv_b64 = spec.get("private") or spec.get("seed") or spec.get("private_key")
if not priv_b64:
    sys.exit(f"publisher-jwks: no 'private'/'seed'/'private_key' in {key_path}")
raw = base64.urlsafe_b64decode(priv_b64 + "=" * (-len(priv_b64) % 4))
if len(raw) == 64:
    # Ed25519 stdlib-format key: seed (32) || pubkey (32). Use the seed.
    seed = raw[:32]
elif len(raw) == 32:
    seed = raw
else:
    sys.exit(f"publisher-jwks: key length {len(raw)} not 32/64 in {key_path}")
priv = Ed25519PrivateKey.from_private_bytes(seed)
pub_obj = priv.public_key()
# Unified FORA JWK `x`: base64url (no padding) of the raw 32-byte Ed25519 key.
pub_raw = pub_obj.public_bytes(Encoding.Raw, PublicFormat.Raw)
pub_x = base64.urlsafe_b64encode(pub_raw).rstrip(b"=").decode("ascii")

# Wide-open fixture window (now-1h .. now+10y): the shared helper applies the
# same one-hour clock-skew backdate every issuer in the repo uses.
valid_from, valid_until = wba_validity_window(lifetime_days=3650)

kid = kid_override or spec.get("kid") or "publisher-key"

# Optional exchanges[] (Phase-2 demo): the Broker's publisher-probe reads this
# to route a resolve to the owning Exchange. Each EXCHANGES_JSON entry carries
# {domain, endpoint, supported_profiles?}; per-entry supported_profiles are
# hoisted to a top-level supported_profiles set, because the protocol's
# AuthorizedExchange has no per-entry slot.
exchanges_env = os.environ.get("EXCHANGES_JSON", "").strip()
exchanges_in = json.loads(exchanges_env) if exchanges_env else []
# An entry's ext is carried through as given: it is where a publisher attests
# ext.resource_owner_id, the payee an Exchange reads before it accepts a
# catalog push for the site, so a manifest without it lists an Exchange that
# refuses every entry pushed for this host.
exchanges = [
    {
        "domain": e["domain"],
        "endpoint": e["endpoint"],
        "relationship": "PROVIDER_RELATIONSHIP_DIRECT",
        **({"ext": e["ext"]} if e.get("ext") else {}),
    }
    for e in exchanges_in
]
profiles = sorted({p for e in exchanges_in for p in e.get("supported_profiles", [])})

# The manifest domain should equal the host this document is served at, so
# the published document stays self-consistent (registration itself anchors
# identity to the host the key directory is fetched from and never reads the
# manifest's domain). Allow an explicit override; otherwise the key
# file's `issuer` is REQUIRED — guessing the domain from the kid up to its
# first dot would silently truncate a dotted identity (test-signer-e2e.v1
# would serve domain test-signer-e2e), so an absent issuer fails container
# start loudly instead.
domain = os.environ.get("MANIFEST_DOMAIN_OVERRIDE", "").strip() or spec.get(
    "issuer", ""
).strip()
if not domain:
    sys.exit(
        f"publisher-jwks: {key_path} carries no 'issuer' and "
        "MANIFEST_DOMAIN_OVERRIDE is unset — refusing to guess the manifest "
        "domain from the kid. Re-run the key-gen script (it stamps/backfills "
        "issuer) or set MANIFEST_DOMAIN_OVERRIDE."
    )

# catalog_contributors defaults to the demo harness contributor (the examplenews
# subscription publisher authorizes it); a self-hosting contributor host serves
# only its own key and passes CATALOG_CONTRIBUTORS_JSON='[]' to suppress it.
contributors_env = os.environ.get("CATALOG_CONTRIBUTORS_JSON", "").strip()
contributors = (
    json.loads(contributors_env)
    if contributors_env
    else [{"domain": "catalog-contributor-e2e", "relationship": "harness"}]
)

# Keyless commercial overlay — identity keys moved to the WBA directory below.
manifest = {
    "ver": "1.0",
    # role generalized (v1.1): ROLE_PUBLISHER for a host PUBLISHER_HOSTS_JSON
    # names, ROLE_AGENT for any other. domain keeps the env-override-aware
    # derivation.
    "role": role,
    "domain": domain,
}
# catalog_contributors / exchanges / supported_profiles are publisher-only fields
# (the self-signup Gate-1 list + the publisher-probe routing); an agent manifest
# omits them. Publisher fields stay env-driven (ours), so the multi-publisher
# demo can vary contributors/exchanges/profiles per host.
if role == "ROLE_PUBLISHER":
    if contributors:
        manifest["catalog_contributors"] = contributors
    if exchanges:
        manifest["exchanges"] = exchanges
    if profiles:
        manifest["supported_profiles"] = profiles

# Web Bot Auth directory — the signing key as a JWK Set, NO kid (named by
# thumbprint). kid is retained only for the log line below. The entry comes
# from the shared wba_directory_key helper, never hand-written here.
wba = {"keys": [wba_directory_key(pub_x, valid_from, valid_until)]}

Path(manifest_path).write_text(json.dumps(manifest, indent=2) + "\n")
wba_path = Path(manifest_path).parent / "http-message-signatures-directory"
wba_path.write_text(json.dumps(wba, indent=2) + "\n")
print(
    f"publisher-jwks: wrote {manifest_path} + {wba_path} "
    f"(kid={kid}, valid_from={valid_from}, valid_until={valid_until})"
)
PY
}

# sign_host KEY HOST signs HOST's directory response. The Web Bot Auth profile
# requires a directory's response to be signed by every key it lists, over
# "@authority";req and content-digest, so each host's response is signed for that
# host's own name. nginx runs no code per request, so the response is signed once,
# here: fora-sign-directory (built into the image from
# src/exchange/cmd/fora-sign-directory) signs through the protocol SDK and prints
# the three headers nginx then sends with the file.
sign_host() {
    headers="$SIGNATURES/$2.conf"
    fora-sign-directory -key "$1" -authority "$2" \
        -body "$WEB_ROOT/$2/.well-known/http-message-signatures-directory" \
      | while IFS= read -r line; do
            name="${line%%: *}"
            value="${line#*: }"
            printf "add_header %s '%s' always;\n" "$name" "$value"
        done >"$headers"
    if [ "$(wc -l <"$headers")" -ne 3 ]; then
        echo "key-directories: signing the directory for $2 failed" >&2
        exit 1
    fi
}

# write_server HOST writes the server block that serves HOST's two documents. An
# add_header is inherited from an outer level only when a level sets none of its
# own, so each location states every header it sends.
write_server() {
    cat >"$HOSTS_CONF/$1.conf" <<EOF
server {
    listen 80;
    server_name $1;
    root $WEB_ROOT/$1;

    location = /.well-known/fora.json {
        default_type application/json;
        add_header Cache-Control "no-store" always;
        try_files \$uri =404;
    }

    location = /.well-known/http-message-signatures-directory {
        default_type application/http-message-signatures-directory+json;
        add_header Cache-Control "no-store" always;
        include $SIGNATURES/$1.conf;
        try_files \$uri =404;
    }

    location / { return 404; }
}
EOF
}

# A restart renders exactly what is mounted now, never what an earlier start left.
rm -rf "$WEB_ROOT" "$HOSTS_CONF" "$SIGNATURES"
mkdir -p "$WEB_ROOT" "$HOSTS_CONF" "$SIGNATURES"

hosts=""
for key in "$KEYS_DIR"/*.json; do
    [ -e "$key" ] || continue
    host="$(basename "$key" .json)"
    # The name becomes a path and an nginx server_name, so it must be a host name.
    case "$host" in
        "" | .* | -* | *[!a-z0-9.-]*)
            echo "key-directories: refusing $key: '$host' is not a host name" >&2
            exit 1
            ;;
    esac
    if [ ! -s "$key" ]; then
        echo "key-directories: signing key not readable at $key" >&2
        exit 1
    fi
    # The host's role and publisher fields: three lines, the role first.
    settings="$(python3 -c '
import json, os, sys
entry = json.loads(os.environ.get("PUBLISHER_HOSTS_JSON") or "{}").get(sys.argv[1])
if entry is None:
    print("ROLE_AGENT\n\n")
else:
    print("ROLE_PUBLISHER")
    print(json.dumps(entry["exchanges"]) if "exchanges" in entry else "")
    print(json.dumps(entry["catalog_contributors"]) if "catalog_contributors" in entry else "")
' "$host")"
    role="$(printf '%s\n' "$settings" | sed -n 1p)"
    exchanges="$(printf '%s\n' "$settings" | sed -n 2p)"
    contributors="$(printf '%s\n' "$settings" | sed -n 3p)"

    render_host "$key" "$host" "$role" "$exchanges" "$contributors"
    domain="$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["domain"])' \
        "$WEB_ROOT/$host/.well-known/fora.json")"
    if [ "$domain" != "$host" ]; then
        echo "key-directories: $key is $domain's key, not $host's; serving it as $host would" \
            "publish one identity's key under another identity's name" >&2
        exit 1
    fi
    sign_host "$key" "$host"
    write_server "$host"
    hosts="$hosts $host"
done

if [ -z "$hosts" ]; then
    echo "key-directories: no key is mounted under $KEYS_DIR" >&2
    exit 1
fi
# A publisher host the map names but no key serves would be missing from the stack
# without a word, so it stops the start too. $hosts is unquoted on purpose: one
# argument per host.
python3 -c '
import json, os, sys
named = set(json.loads(os.environ.get("PUBLISHER_HOSTS_JSON") or "{}"))
missing = sorted(named - set(sys.argv[1:]))
if missing:
    sys.exit(f"key-directories: PUBLISHER_HOSTS_JSON names {missing}, which no mounted key serves")
' $hosts

nginx -t -q
echo "key-directories: serving$hosts; exec-ing $*"
exec "$@"
