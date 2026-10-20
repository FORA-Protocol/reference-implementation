# Multi-stage build for the Broker service.
#
# Build from repository root:
#   docker build -f src/broker/Dockerfile -t ghcr.io/fora-protocol/broker:dev .

FROM golang:1.26-alpine AS builder
WORKDIR /build

ARG GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
ARG GOSUMDB="sum.golang.org"
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

# There is no in-tree proto/ directory to copy. The protocol is consumed as the
# published Go module github.com/FORA-Protocol/protocol, pinned in the root
# go.mod, so `go mod download` below is what fetches it.
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

COPY internal/ ./internal/
COPY src/broker/ ./src/broker/

ARG GO_BUILD_P=3
ENV CGO_ENABLED=0 GOOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/broker ./src/broker/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/broker /broker

# image.source is what links the published package to its repository on GitHub,
# which is in turn what allows the package to be made public. Version and
# revision are deliberately absent: neither is knowable until a build runs, so
# the publishing workflow stamps those two as --label flags instead.
LABEL org.opencontainers.image.source="https://github.com/FORA-Protocol/reference-implementation" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="FORA Broker" \
      org.opencontainers.image.description="FORA Broker service: resource discovery and exchange routing"

USER nonroot:nonroot
EXPOSE 8082
ENTRYPOINT ["/broker"]
