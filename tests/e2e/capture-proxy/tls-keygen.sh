#!/bin/sh
# Mints the e2e stack's TLS material at stack-up: a throwaway local CA and one
# server certificate, signed by it, for the capture proxy's TLS listener
# (capture-proxy in docker-compose.e2e.yml). Nothing here is committed and nothing here is a
# deployment step: a real deployment's certificates come from a public CA.
#
# Writes two volumes, so a consumer mounts only what it needs:
#
#   $CA_DIR/ca.pem          the CA certificate: the trust anchor every client
#                           in the stack is pointed at.
#   $CA_DIR/ca-bundle.pem   the system root bundle with the CA appended, for the
#                           SSL_CERT_FILE consumers. That setting replaces the
#                           default bundle file. Go and OpenSSL still read their
#                           certificate directory beside it, but httpx reads
#                           nothing else, so pointing it at ca.pem alone would
#                           leave the Python runner without a public root.
#   $PROXY_DIR/proxy.crt    the proxy's certificate, and
#   $PROXY_DIR/proxy.key    its private key. Only the capture proxy mounts this volume.
#
# The CA's private key is deleted once the proxy certificate is signed: nothing
# else is ever issued from it, so no file in either volume can mint a
# certificate the stack would trust.
#
# Re-mints on every run, like rsa-keygen: `make e2e-down` removes both volumes,
# and a stack-up that re-runs this one-shot also recreates the proxy that reads
# the result.
set -eu

CA_DIR="${CA_DIR:-/tls-ca}"
PROXY_DIR="${PROXY_DIR:-/tls-proxy}"
SYSTEM_BUNDLE="${SYSTEM_BUNDLE:-/etc/ssl/certs/ca-certificates.crt}"

# Every hostname the proxy answers https for: each name in the compose file's
# x-through-capture-proxy list, and the three wildcard zones the DNS sidecar
# points at the proxy. tests/e2e/harness/test_capture_proxy.py fetches over
# https, with verification on, from every name in that list, so a name added
# there and not here fails that test rather than a later one.
SAN="DNS:broker,DNS:exchange,DNS:exchange-b,DNS:exchange-c,DNS:exchange-d,DNS:console"
SAN="$SAN,DNS:misbehaving-exchange,DNS:unapproved-exchange,DNS:unlisted-exchange"
SAN="$SAN,DNS:foramcp.org,DNS:*.foramcp.org"
SAN="$SAN,DNS:agents.example,DNS:*.agents.example,DNS:sites.example,DNS:*.sites.example"
SAN="$SAN,DNS:demo.fora-protocol.org,DNS:music.demo.fora-protocol.org"
SAN="$SAN,DNS:sfx.demo.fora-protocol.org,DNS:archive.demo.fora-protocol.org"
# The static key-directory hosts the harness and the seed sign as.
SAN="$SAN,DNS:examplenews,DNS:catalog-contributor-e2e,DNS:lazy-agent-e2e,DNS:agent-e2e"
SAN="$SAN,DNS:agent-nobilling-e2e,DNS:agent-demo-eur,DNS:test-signer-e2e.v1"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

mkdir -p "$CA_DIR" "$PROXY_DIR"

# P-256 throughout: every client in the stack (Go, Node, Python, the Fastly
# runtime) accepts it. 30 days covers a stack left up for debugging.
openssl req -x509 -new -nodes -days 30 \
    -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
    -keyout "$work/ca.key" -out "$CA_DIR/ca.pem" \
    -subj "/CN=FORA e2e local CA" \
    -addext "basicConstraints=critical,CA:TRUE" \
    -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "subjectKeyIdentifier=hash"

openssl req -new -nodes \
    -newkey ec -pkeyopt ec_paramgen_curve:P-256 \
    -keyout "$PROXY_DIR/proxy.key" -out "$work/proxy.csr" \
    -subj "/CN=fora-e2e-tls-proxy"

# The extensions a strict verifier asks of a leaf: a SAN for every name, server
# auth only, and both key identifiers (Python 3.13's default context rejects a
# leaf without an authority key identifier).
cat >"$work/leaf.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=$SAN
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
EOF

openssl x509 -req -days 30 -in "$work/proxy.csr" \
    -CA "$CA_DIR/ca.pem" -CAkey "$work/ca.key" \
    -CAserial "$work/ca.srl" -CAcreateserial \
    -extfile "$work/leaf.ext" -out "$PROXY_DIR/proxy.crt"

cat "$SYSTEM_BUNDLE" "$CA_DIR/ca.pem" >"$CA_DIR/ca-bundle.pem"

# World-readable trust anchors: the Go services run as a non-root user. The
# proxy key stays readable by root only, which is the user the proxy runs as.
chmod 0444 "$CA_DIR/ca.pem" "$CA_DIR/ca-bundle.pem" "$PROXY_DIR/proxy.crt"
chmod 0400 "$PROXY_DIR/proxy.key"

echo "tls-keygen: wrote $CA_DIR/ca.pem, $CA_DIR/ca-bundle.pem and the proxy certificate"
