#!/usr/bin/env bash
# Build the Lambda@Edge package Terraform deploys.
#
# Lambda@Edge has no environment variables, so the per-deployment config
# travels in the package: an index.mjs that assigns the values of a flat JSON
# file of env-shaped strings and re-exports the handler from the release
# bundle beside it (see src/edge/scripts/build-lambda-edge.mjs for the file's
# contract, and src/edge/scripts/lambda-entry.mjs for why that works). It is
# the layout the console hands a publisher. Run this before every
# `terraform apply` that should pick up worker code OR config changes — both
# live in the zip.
#
# Output: src/edge/dist/lambda-edge.zip — index.mjs and lambda-edge.mjs at the
# archive root, handler "index.handler". The build smoke-invokes the package
# locally before zipping, so a config the worker's schema rejects fails here,
# not at the edge.
#
# Usage:
#   deploy/terraform/scripts/build-lambda-edge.sh <deploy-config.json>

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/edge-build.sh"

CONFIG_ARG="${1:?usage: build-lambda-edge.sh <deploy-config.json>}"
# Absolute before any cd, so a path relative to the caller's cwd keeps working.
CONFIG="$(cd "$(dirname "${CONFIG_ARG}")" && pwd)/$(basename "${CONFIG_ARG}")"
[ -f "${CONFIG}" ] || { echo "missing config file: ${CONFIG_ARG}" >&2; exit 2; }
command -v zip >/dev/null 2>&1 || { echo "missing: zip" >&2; exit 2; }

# The previous zip goes before anything is built. Under set -e a failed step --
# the install, the bundle, the smoke invoke -- stops the script, and a zip left
# from an earlier run would then be what Terraform deploys.
rm -f "${EDGE_DIR}/dist/lambda-edge.zip"

edge_npm_ci

echo "==> bundling Lambda@Edge worker"
node scripts/build-lambda-edge.mjs --config "${CONFIG}"

# -X drops platform extra fields, -j stores both files at the archive root —
# the flat layout the "index.handler" handler string expects, with the bundle
# where index.mjs imports it from.
zip -X -q -j dist/lambda-edge.zip dist/lambda-edge/index.mjs dist/lambda-edge/lambda-edge.mjs

echo "built ${EDGE_DIR}/dist/lambda-edge.zip"
