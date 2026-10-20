# Multi-stage build for the Exchange service. The distroless runtime carries no
# shell, no package manager and no extra users. It is the largest of the three
# service images, because the TigerBeetle client below needs glibc.
#
# Build from repository root:
#   docker build -f src/exchange/Dockerfile -t ghcr.io/fora-protocol/exchange:dev .
#
# Two images come out of this file. The DEFAULT target, `production`, is the last
# stage, so a build that names no target gets it: its binary is built without any
# build tag. The `e2e` target is what the e2e stack builds
# (docker-compose.e2e.yml): its binary is built with the e2e tag and so carries
# the e2e-only control service (ADR-028 D6). The two share every stage up to the
# compile, and nothing in the production stage copies from the e2e builder.

FROM golang:1.26-bookworm AS source
WORKDIR /build

# TigerBeetle's Go client links a native library via CGO, so the shipped binary
# needs a C toolchain at build time and glibc at runtime. The builder is pinned
# to bookworm to match the distroless/base-debian12 runtime glibc; the Debian
# golang image ships no gcc, so install it.
RUN apt-get update && apt-get install -y --no-install-recommends gcc \
 && rm -rf /var/lib/apt/lists/*

ARG GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
ARG GOSUMDB="sum.golang.org"
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

# There is no in-tree proto/ directory to copy. The protocol is consumed as the
# published Go module github.com/FORA-Protocol/protocol, pinned in the root
# go.mod, so `go mod download` below is what fetches it.
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

# Source specific to the Exchange build target.
COPY internal/ ./internal/
COPY src/exchange/ ./src/exchange/

# CGO is required by the TigerBeetle client. CGO cannot cross-compile without a
# cross C toolchain, so this image builds for the native (amd64) arch only; the
# repo publishes no multi-arch image.
ARG GO_BUILD_P=3
ENV CGO_ENABLED=1 GOOS=linux

# The production binaries: no build tag. TARGETARCH is declared in each builder,
# because a build argument does not carry over from the stage it was declared in.
FROM source AS builder
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/exchange ./src/exchange/cmd/server \
 && go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/fora-import ./src/exchange/cmd/fora-import

# The e2e stack's binaries: the server built with the e2e tag, which compiles in
# the control service. Only the `e2e` target below copies from this stage.
FROM source AS builder-e2e
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -tags e2e -trimpath -ldflags="-s -w" \
      -o /out/exchange ./src/exchange/cmd/server \
 && go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/fora-import ./src/exchange/cmd/fora-import

FROM gcr.io/distroless/base-debian12:nonroot AS runtime

# image.source is what links the published package to its repository on GitHub,
# which is in turn what allows the package to be made public. Version and
# revision are deliberately absent: neither is knowable until a build runs, so
# the publishing workflow stamps those two as --label flags instead.
LABEL org.opencontainers.image.source="https://github.com/FORA-Protocol/reference-implementation" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="FORA Exchange" \
      org.opencontainers.image.description="FORA Exchange service: offers, transactions and signed content URLs"

USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/exchange"]

# The e2e stack's image. Not a production target: see the header.
FROM runtime AS e2e
COPY --from=builder-e2e /out/exchange /exchange
COPY --from=builder-e2e /out/fora-import /fora-import

# The production image, and the default target because it is the last stage.
FROM runtime AS production
COPY --from=builder /out/exchange /exchange
# The catalog importer ships in the same image as a second entrypoint: the
# compose bundle runs it as its own service (a separate key mount) on a timer,
# so a deployment pulls one image and the two binaries cannot drift apart.
COPY --from=builder /out/fora-import /fora-import
