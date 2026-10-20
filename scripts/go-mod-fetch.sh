#!/usr/bin/env sh
# go-mod-fetch.sh — Go module download helper with proxy fallback and retry.
#
# Single canonical definition for GOPROXY and GOSUMDB.
# Mitigates transient proxy.golang.org HTTP/2 stream errors and network flakes.
# Cryptographic integrity is guaranteed by GOSUMDB verifying checksums against sum.golang.org.
#
# Invocation modes:
#   --print-goproxy: Print the GOPROXY value for Makefile consumption.
#   --print-gosumdb: Print the GOSUMDB value for Makefile consumption.
#   (no args):       Download modules with retry (called by Makefile, Dockerfiles, CI).
#
# IMPORTANT: Clear environment variables that could disable checksum verification.
# The publish gate and CI builds must never honor caller settings for these.
# Docker builds pass only the pinned values via build args, which take precedence
# over the script's defaults (GOPROXY stays overridable for mirrored/offline builds).

set -e

# Security: unset any caller environment that could disable checksum verification
unset GOPRIVATE GONOSUMDB GONOPROXY GOFLAGS

# Default values (Docker ARGs override these when passed as build args)
GOPROXY_DEFAULT="https://proxy.golang.org|https://goproxy.io|direct"
GOSUMDB_DEFAULT="sum.golang.org"

case "${1:-}" in
  --print-goproxy)
    printf "%s" "${GOPROXY_DEFAULT}"
    exit 0
    ;;
  --print-gosumdb)
    printf "%s" "${GOSUMDB_DEFAULT}"
    exit 0
    ;;
esac

# GOPROXY stays overridable for mirrored/offline builds.
# GOSUMDB is ALWAYS the checksum database — no path needs a different sumdb.
export GOPROXY="${GOPROXY:-${GOPROXY_DEFAULT}}"
export GOSUMDB="${GOSUMDB_DEFAULT}"

for i in 1 2 3; do
  GODEBUG=http2client=0 go mod download "$@" && break || sleep 2
done

# Trailing un-retried verification ensures persistent failures propagate exit code.
GODEBUG=http2client=0 go mod download "$@"
