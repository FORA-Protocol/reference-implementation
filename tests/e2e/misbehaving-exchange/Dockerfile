# The e2e suite's Exchange impostor (see main.go). Test infrastructure: only
# docker-compose.e2e.yml builds it, and nothing publishes the image.
#
# Build context is the project root, because the impostor is a package of the
# root Go module and builds against its go.mod, its internal/ tree and the
# pinned protocol module, as the services do.
FROM golang:1.26-alpine AS builder
WORKDIR /build

ARG GOPROXY="https://proxy.golang.org|https://goproxy.io|direct"
ARG GOSUMDB="sum.golang.org"
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}

# The same two cache mounts the service images carry, with the same locking
# split: the download writes the module cache, so it is locked against the other
# images compose builds at once; the build only reads it.
#
# Module fetch with retry and proxy fallback. Single canonical definition
# copied from scripts/go-mod-fetch.sh; run with sh (POSIX sh only, no bashisms).
COPY go.mod go.sum ./
COPY scripts/go-mod-fetch.sh ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked --mount=type=cache,target=/root/.cache/go-build \
    sh go-mod-fetch.sh
COPY internal/ ./internal/
COPY tests/e2e/misbehaving-exchange/ ./tests/e2e/misbehaving-exchange/
ARG GO_BUILD_P=3
ENV CGO_ENABLED=0 GOOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -p ${GO_BUILD_P} -trimpath -ldflags="-s -w" \
      -o /out/misbehaving-exchange ./tests/e2e/misbehaving-exchange

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/misbehaving-exchange /misbehaving-exchange
USER nonroot:nonroot
EXPOSE 8081
ENTRYPOINT ["/misbehaving-exchange"]
