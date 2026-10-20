# Multi-stage build for the Identity Service (the WBA registry + MCP adapter).
# Distroless runtime, no CGO — unlike the Exchange this binary links no native
# library.
#
# Build from repository root:
#   docker build -f src/identity/Dockerfile -t ghcr.io/fora-protocol/identity:dev .
#
# Two images come out of this file. The DEFAULT target, `production`, is the last
# stage, so a build that names no target gets it: it carries the server alone.
# The `e2e` target is what the e2e stack builds (docker-compose.e2e.yml): it adds
# the operator's break-glass CLI, identity-operator, which the e2e suite runs
# inside the container to revoke an agent's key the way an operator would. The
# production image does not ship the CLI; an operator runs it from a build of
# their own, with access to Vault and Postgres.

FROM golang:1.26-bookworm AS source
WORKDIR /build

ARG GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
ARG GOSUMDB="sum.golang.org"
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

# Root go module + deps.
COPY go.mod go.sum ./
COPY scripts/go-mod-fetch.sh ./
# Persist the Go module and build caches across builds.
#
# Without these every rebuild recompiles the whole dependency graph, because
# nothing in a container layer carries a compiler cache: on a cold stack build
# the go steps alone cost minutes. A cache mount lives with the builder rather
# than in the image, so it survives --no-cache and never lands in a layer.
#
# sharing=locked on the DOWNLOAD only. That step writes the module cache, and
# compose builds these services concurrently, so two writers would race. The
# build step below only reads it and takes the default shared mode: locking it
# there serializes every Go image in the stack against every other, which
# measured slower than having no cache at all.
#
# Module fetch with retry and proxy fallback. Single canonical definition
# copied from scripts/go-mod-fetch.sh; run with sh (POSIX sh only, no bashisms).
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked --mount=type=cache,target=/root/.cache/go-build \
    sh go-mod-fetch.sh

# Source specific to the Identity build target.
COPY internal/ ./internal/
COPY src/identity/ ./src/identity/

ARG GO_BUILD_P=3
ENV CGO_ENABLED=0 GOOS=linux

# The server. TARGETARCH is declared in each builder, because a build argument
# does not carry over from the stage it was declared in.
FROM source AS builder
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/identity ./src/identity/cmd/server

# The operator CLI. Only the `e2e` target below copies from this stage.
FROM source AS builder-operator
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/identity-operator ./src/identity/cmd/operator

FROM gcr.io/distroless/static-debian12:nonroot AS runtime

# image.source is what links the published package to its repository on GitHub,
# which is in turn what allows the package to be made public. Version and
# revision are deliberately absent: neither is knowable until a build runs, so
# the publishing workflow stamps those two as --label flags instead.
LABEL org.opencontainers.image.source="https://github.com/FORA-Protocol/reference-implementation" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="FORA Identity" \
      org.opencontainers.image.description="FORA Identity service: agent key registry and MCP adapter"

USER nonroot:nonroot
EXPOSE 8083
ENTRYPOINT ["/identity"]

# The e2e stack's image. Not a production target: see the header.
FROM runtime AS e2e
COPY --from=builder /out/identity /identity
COPY --from=builder-operator /out/identity-operator /identity-operator

# The production image, and the default target because it is the last stage.
FROM runtime AS production
COPY --from=builder /out/identity /identity
