#!/usr/bin/env sh
# check-build-args.sh — Verify Dockerfile ARG defaults match canonical values.
#
# Run by `make quality` to catch drift between the canonical module-fetch
# policy (scripts/go-mod-fetch.sh) and the ARG defaults copied into Dockerfiles.
#
# Checks:
#   1. All ARG defaults match canonical values (GOPROXY, GOSUMDB, GO_BUILD_P)
#   2. ENV wiring exists for GOPROXY and GOSUMDB (multi-stage: at least one ENV)

set -e

cd "$(dirname "$0")/.."

# Canonical values from the single source of truth
GOPROXY_CANONICAL=$(scripts/go-mod-fetch.sh --print-goproxy)
GOSUMDB_CANONICAL=$(scripts/go-mod-fetch.sh --print-gosumdb)
GO_BUILD_P_CANONICAL=3

errors=0

# Check each Dockerfile
for dockerfile in src/*/Dockerfile tests/e2e/Dockerfile tests/e2e/*/Dockerfile; do
    if [ ! -f "$dockerfile" ]; then
        continue
    fi

    # Check ALL ARG GOPROXY lines (not just first)
    goproxy_args=$(grep -E '^ARG GOPROXY=' "$dockerfile" | sed 's/ARG GOPROXY=//' | tr -d '"' || true)
    if [ -n "$goproxy_args" ]; then
        echo "$goproxy_args" | while read -r val; do
            if [ "$val" != "$GOPROXY_CANONICAL" ]; then
                echo "ERROR: $dockerfile has ARG GOPROXY=\"$val\""
                echo "       canonical value: \"$GOPROXY_CANONICAL\""
                echo "       Run: scripts/go-mod-fetch.sh --print-goproxy"
                exit 1
            fi
        done || exit 1
    fi

    # Check ALL ARG GOSUMDB lines
    gosumdb_args=$(grep -E '^ARG GOSUMDB=' "$dockerfile" | sed 's/ARG GOSUMDB=//' | tr -d '"' || true)
    if [ -n "$gosumdb_args" ]; then
        echo "$gosumdb_args" | while read -r val; do
            if [ "$val" != "$GOSUMDB_CANONICAL" ]; then
                echo "ERROR: $dockerfile has ARG GOSUMDB=\"$val\""
                echo "       canonical value: \"$GOSUMDB_CANONICAL\""
                echo "       Run: scripts/go-mod-fetch.sh --print-gosumdb"
                exit 1
            fi
        done || exit 1
    fi

    # Check ALL ARG GO_BUILD_P lines
    go_build_p_args=$(grep -E '^ARG GO_BUILD_P=' "$dockerfile" | sed 's/ARG GO_BUILD_P=//' | tr -d '"' || true)
    if [ -n "$go_build_p_args" ]; then
        echo "$go_build_p_args" | while read -r val; do
            if [ "$val" != "$GO_BUILD_P_CANONICAL" ]; then
                echo "ERROR: $dockerfile has ARG GO_BUILD_P=\"$val\""
                echo "       canonical value: \"$GO_BUILD_P_CANONICAL\""
                exit 1
            fi
        done || exit 1
    fi

    # Check ENV wiring for GOPROXY/GOSUMDB exists (at least one ENV line references them)
    has_env_wiring=$(grep -E '^ENV (GOPROXY|GOSUMDB)=' "$dockerfile" | head -1 || true)
    if [ -z "$has_env_wiring" ] && [ -n "$goproxy_args" ]; then
        echo "ERROR: $dockerfile has ARG GOPROXY but no ENV GOPROXY= wiring"
        exit 1
    fi
done

echo "✓ All Dockerfile ARG defaults match canonical values"
