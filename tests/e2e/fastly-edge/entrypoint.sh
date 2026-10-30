#!/bin/sh
# Fastly Compute runtime entry: materializes a config_store in fastly.toml from
# runtime env vars, then starts `fastly compute serve` under Viceroy.
#
# Fastly's env() is limited to built-in vars; arbitrary demo config arrives via
# a ConfigStore named `fora_edge` (see src/index.ts).

set -eu

REQUIRED="EXCHANGE_URL EXCHANGE_WBA_URL ORIGIN_URL"
for name in $REQUIRED; do
  eval "v=\${$name:-}"
  if [ -z "$v" ]; then
    echo "fastly-edge: missing env $name" >&2
    exit 2
  fi
done

cat >/app/fastly.toml <<EOF
manifest_version = 3
name = "fora-fastly-edge"
description = "FORA edge verifier on Fastly Compute (WASM)"
authors = ["fora-demo"]
language = "javascript"
service_id = ""

[local_server]
  [local_server.backends]
    [local_server.backends.exchange]
    url = "${EXCHANGE_URL}"
    [local_server.backends.publisher]
    url = "${ORIGIN_URL}"

  [local_server.config_stores]
    [local_server.config_stores.fora_edge]
    format = "inline-toml"
      [local_server.config_stores.fora_edge.contents]
      EXCHANGE_URL = "${EXCHANGE_URL}"
      ORIGIN_URL = "${ORIGIN_URL}"
      EXCHANGE_WBA_URL = "${EXCHANGE_WBA_URL:-}"
      FORA_ENFORCE_BINDING = "${FORA_ENFORCE_BINDING:-}"
      WBA_KEYS_JSON = $(printf '%s' "${WBA_KEYS_JSON:-}" | node -e 'process.stdout.write(JSON.stringify(require("fs").readFileSync(0,"utf8")))')
      WBA_REVOCATION_URL = "${WBA_REVOCATION_URL:-}"
      WBA_SIGNED_DIRECTORY_JSON = $(printf '%s' "${WBA_SIGNED_DIRECTORY_JSON:-}" | node -e 'process.stdout.write(JSON.stringify(require("fs").readFileSync(0,"utf8")))')

[setup]
EOF

# The image's pinned Viceroy (see the Dockerfile), not one the CLI downloads.
exec fastly compute serve --skip-build --viceroy-path=/usr/local/bin/viceroy \
  --addr="${FASTLY_SERVE_ADDR:-0.0.0.0:7676}"
