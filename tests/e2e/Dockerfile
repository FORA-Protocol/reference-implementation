# pytest harness image — runs inside the compose network so the test URLs
# resolve via docker DNS (edge:80, broker:8082, exchange:8081, the demo
# publisher domains, etc.).
#
# Build context is the project root (see docker-compose.e2e.yml `runner`
# service).

# ───── Stage 1: build the REAL production ingest binary ──────────────────
# The e2e seed ingests the demo feeds by shelling out to the production
# `cmd/fora-ingest` binary (ParseJSONL → mapRecord, canonicalizing terms
# through the SDK helper → signed PushResources RPC; the Exchange runs the
# SDK's ingest-tier term checks) — NOT a Python re-implementation of mapper.go.
# The runner image has no Go toolchain, so we build the static binary here and
# COPY it into the runner. Mirrors src/exchange/Dockerfile's builder stage.
FROM golang:1.26-alpine AS ingest-builder
WORKDIR /build

ARG GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
ARG GOSUMDB="sum.golang.org"
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

COPY go.mod go.sum ./
COPY scripts/go-mod-fetch.sh ./
# The same two cache mounts the three service images carry, for the same reason
# and with the same locking split: without them every rebuild recompiles the
# whole dependency graph, and on a cold build that costs minutes. This stage is
# inside the path they were added to shorten — `make test-e2e` builds the runner
# every time, and this stage copies internal/ and src/exchange/, so any change
# under either rebuilds it.
#
# sharing=locked on the DOWNLOAD only. That step writes the module cache and
# compose builds these images concurrently, so two writers would race. The build
# step below only reads it and takes the default shared mode: locking it there
# serializes every Go image in the stack against every other, which measured
# slower than having no cache at all.
#
# Module fetch with retry and proxy fallback. Single canonical definition
# copied from scripts/go-mod-fetch.sh; run with sh (POSIX sh only, no bashisms).
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked --mount=type=cache,target=/root/.cache/go-build \
    sh go-mod-fetch.sh
COPY internal/ ./internal/
COPY src/exchange/ ./src/exchange/
ARG GO_BUILD_P=3
ENV CGO_ENABLED=0 GOOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/fora-ingest ./src/exchange/cmd/fora-ingest

# ───── Stage 2: pytest harness ───────────────────────────────────────────
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim
WORKDIR /runner

# No git layer. This image used to install one, because fora-protocol and
# fora-protocol-sdk resolved from the protocol repository over git and uv had to
# clone them. Both are PyPI releases now, so nothing in this image resolves a git
# dependency. The harness suites that DO shell out to git — the structural-guard
# ones that build scratch repositories — all skip in this image, because the
# scripts they drive are not COPYed into it.

# The production ingest binary, copied from the Go builder stage. The seed
# (tests/e2e/harness/seed.py) shells out to this path once per demo feed.
COPY --from=ingest-builder /out/fora-ingest /usr/local/bin/fora-ingest

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

# Harness pyproject and its lock. There is no in-tree Python shim to mirror in:
# the MCP surface is the Go adapter, driven over the MCP protocol by
# test_mcp_endpoint.py. fora-protocol and fora-protocol-sdk install from PyPI at
# the version this file pins, so no sibling checkout is mirrored in either.
#
# The lock is copied because `==<version>` pins a name and a number, and the lock is
# the only file in this tree carrying the sha256 of the sdist and the wheel. A
# version specifier makes the resolution exact; it does not make the ARTIFACT
# exact, and an index that serves a package of that name and version serves
# different bytes. The host-side pin guard enforces that the lock resolves both
# distributions from https://pypi.org/simple and nowhere else, precisely for that
# reason -- and a build that never reads the lock sits outside that check,
# inheriting whatever index the build environment points uv at. This harness signs
# the RFC 9421 requests the Go services verify and the delivery proof the edge
# worker verifies, which is why its revision is tied to the other three.
#
# Before the release move the same image resolved both distributions from a
# [tool.uv.sources] entry at a full 40-hex commit, which is content-addressed: the
# pyproject alone pinned the bytes. Copying the lock is what restores that
# property now that the pyproject names a version instead.
COPY tests/e2e/pyproject.toml tests/e2e/uv.lock ./

# Install harness deps. The uv cache mount is the Python counterpart of the two
# Go ones above: without it every rebuild re-downloads every wheel, including the
# two protocol distributions. Its own id, so it is not shared with any other
# image's cache.
#
# `--locked` and not `--frozen`. Both install the locked resolution and verify each
# artifact against the lock's sha256, but only `--locked` also fails when the lock
# has gone stale against the pyproject; `--frozen` installs the old resolution and
# says nothing, which is how a re-pin would reach this image having changed the
# specifier and not the bytes. `--no-dev` keeps the image to the runtime
# dependencies: [tool.uv] default-groups names the dev group, which carries the
# linters this image never runs.
RUN --mount=type=cache,id=uv-e2e-runner,target=/root/.cache/uv \
    uv sync --locked --no-dev

# Test sources. The catalog feeds the seed ingests are part of this tree
# (harness/fixtures/catalog/), so they arrive with it — the seed resolves them
# from the harness package rather than from REPO_ROOT, which is why no separate
# COPY is needed and why the in-container layout does not have to mirror the
# host checkout.
COPY tests/e2e/harness /runner/harness
# The obligation registry the harness loads from the directory beside it (ADR-028 D1).
COPY tests/e2e/obligations /runner/obligations
# Cross-language golden vectors the harness reads by REPO_ROOT-relative path.
# The offer-acceptance vectors (testdata/acceptance-vectors.json) are the Go
# oracle that harness/test_acceptance.py pins the ported acceptance marshal
# against. REPO_ROOT-relative (REPO_ROOT=/runner) so the lookup
# resolves in-container as well as on the host checkout.
COPY testdata /runner/testdata

ENV PATH="/runner/.venv/bin:${PATH}"
ENTRYPOINT ["pytest", "-q", "harness"]
