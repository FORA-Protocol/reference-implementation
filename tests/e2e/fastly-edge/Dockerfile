# Fastly Compute runner. Builds a WASM from the Hono app and serves it via
# `fastly compute serve` (Viceroy under the hood).
#
# Build from repo root:
#   docker build -f tests/e2e/fastly-edge/Dockerfile -t fora/fastly-edge:dev .

FROM node:22-bookworm-slim AS builder
WORKDIR /app

RUN apt-get update \
    && apt-get install -y --no-install-recommends curl ca-certificates tar \
    && rm -rf /var/lib/apt/lists/*

# Fastly CLI — pinned version, architecture-aware.
ARG FASTLY_CLI_VERSION=14.3.1
ARG TARGETARCH=amd64
RUN curl -fsSL "https://github.com/fastly/cli/releases/download/v${FASTLY_CLI_VERSION}/fastly_v${FASTLY_CLI_VERSION}_linux-${TARGETARCH}.tar.gz" \
    | tar -xz -C /usr/local/bin fastly \
    && chmod +x /usr/local/bin/fastly \
    && fastly version

# Project skeleton + the upstream edge source (mounted read-only via build
# context from repo root).
COPY tests/e2e/fastly-edge/package.json tests/e2e/fastly-edge/build.mjs tests/e2e/fastly-edge/fastly.toml ./
COPY tests/e2e/fastly-edge/src ./src
# Pull the shared edge package so npm can pack it as a local dependency.
COPY src/edge/package.json /app/edge-src/package.json
COPY src/edge/src /app/edge-src/src
# @fora-protocol/sdk is installed from the npm registry at an exact release
# version, and npm verifies its recorded sha512 integrity against the downloaded
# tarball. Written out once, with the reasoning, in
# src/edge/Dockerfile.miniflare — three copies of it is how the correction
# reached three sites of eight last time.

# Rewrite the @fora/edge reference to point at the copied package. Packing it
# under /app lets esbuild resolve hono/zod from /app/node_modules without
# installing edge-src's development dependencies.
RUN node -e "const fs=require('fs');const p=JSON.parse(fs.readFileSync('package.json'));p.dependencies['@fora/edge']='file:./edge-src';fs.writeFileSync('package.json',JSON.stringify(p,null,2));"

# Persist npm's download cache across builds — the slowest two stages of a cold
# stack build. It holds every registry tarball this step downloads, the SDK's
# among them. The mount lives with the builder, so nothing here reaches a
# layer.
#
# id, because BuildKit otherwise names a cache mount after its target and all
# three npm mounts in this stack would be one cache under one lock, serializing
# images compose builds concurrently. Per image, one writer each.
RUN --mount=type=cache,id=npm-fastly-edge,target=/root/.npm,sharing=locked \
    npm install esbuild@^0.24.2 --no-save --install-links

RUN node build.mjs

FROM node:22-bookworm-slim
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/fastly /usr/local/bin/fastly
COPY --from=builder /app/bin /app/bin
COPY tests/e2e/fastly-edge/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
EXPOSE 7676
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
