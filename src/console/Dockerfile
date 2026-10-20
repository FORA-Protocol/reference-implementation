# Multi-stage build for the publisher console: the web application a publisher
# signs in to, registers through, and downloads their setup files from.
# Distroless runtime, no CGO. It holds one table of its own -- who has signed
# in -- and one secret, the key it signs its calls to the Exchange with.
#
# Build from repository root:
#   docker build -f src/console/Dockerfile -t ghcr.io/fora-protocol/console:dev .

# The edge packages a publisher installs, built once here rather than once per
# publisher. Nothing about a publisher is in them: the Cloudflare worker reads
# its settings from the platform's bindings, and the console assigns the
# Lambda@Edge settings in a few lines it puts in front of that bundle when it
# assembles a publisher's zip. So this stage runs at release time and every
# publisher installs the same reviewed bytes.
FROM node:22-bookworm-slim AS edge
WORKDIR /edge
COPY src/edge/package.json src/edge/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY src/edge/ ./
RUN npm run build:release

FROM golang:1.26-bookworm AS builder
WORKDIR /build

ARG GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
ARG GOSUMDB="sum.golang.org"
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

# Root go module + deps.
COPY go.mod go.sum ./
COPY scripts/go-mod-fetch.sh ./
# Persist the Go module and build caches across builds. See the sibling
# Dockerfiles for why sharing=locked is on the download step alone.
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked --mount=type=cache,target=/root/.cache/go-build \
    sh go-mod-fetch.sh

# Source specific to the console build target. The templates and the vendored
# stylesheet are embedded into the binary, so nothing but the binary ships.
COPY internal/ ./internal/
COPY src/console/ ./src/console/
# The edge release's own statement of how a package is configured: the Workers
# runtime settings, the Worker name and the Lambda entry template. The console
# embeds it, so a publisher's download and the edge's tests read one source.
COPY src/edge/edgerelease/ ./src/edge/edgerelease/

ENV CGO_ENABLED=0 GOOS=linux
ARG GO_BUILD_P=3
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/console ./src/console/cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/console /console
# Read at request time rather than embedded, so `go build ./...` needs no Node
# and a deployment that ships no packages still runs -- it hands out the
# configuration file and the written procedure, which is where every publisher
# was before the packages existed.
COPY --from=edge /edge/dist/release/ /edge-packages/
ENV CONSOLE_EDGE_PACKAGE_DIR=/edge-packages
# The version of the edge packages the console tells every publisher to build
# from. A release build passes its version tag here, so the image names its own
# release. Without the argument the value is empty, which the console treats as
# unset; a deployment's own setting overrides either.
ARG EDGE_RELEASE_REF
ENV CONSOLE_EDGE_RELEASE_REF=${EDGE_RELEASE_REF}

# image.source is what links the published package to its repository on GitHub,
# which is in turn what allows the package to be made public. Version and
# revision are deliberately absent: neither is knowable until a build runs, so
# the publishing workflow stamps those two as --label flags instead.
LABEL org.opencontainers.image.source="https://github.com/FORA-Protocol/reference-implementation" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.title="FORA Publisher Console" \
      org.opencontainers.image.description="FORA publisher console: sign-in, publisher and site registration, and the generated setup files"

USER nonroot:nonroot
EXPOSE 8085
ENTRYPOINT ["/console"]
