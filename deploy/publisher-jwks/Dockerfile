# publisher-jwks: test-only HTTP host that serves the catalog-
# contributor's /.well-known/fora.json (§6.6 schema) so the
# CatalogSignatureMiddleware self-signup flow can resolve the
# contributor's pubkey.
#
# Built for docker-compose.e2e.yml with the REPO ROOT as build context
# (context: ., dockerfile: deploy/publisher-jwks/Dockerfile) so the shared
# key helpers in scripts/lib travel into the image. The entrypoint reads the
# resource-owner's Ed25519 private key from /keys (mounted by compose)
# and renders the §6.6 manifest with the corresponding pubkey + a
# wide-open valid_from/valid_until window into the nginx doc root on
# every cold start.

# fora-sign-directory signs the rendered directory's response at start (see
# entrypoint.sh). It is a package of the root Go module, built against go.mod's
# protocol pin as the services are, so the image carries no second SDK pin.
FROM golang:1.26-alpine AS signer
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY internal/clock/ ./internal/clock/
COPY src/exchange/cmd/fora-sign-directory/ ./src/exchange/cmd/fora-sign-directory/
ENV CGO_ENABLED=0 GOOS=linux
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/fora-sign-directory ./src/exchange/cmd/fora-sign-directory

FROM nginx:1.27-alpine

RUN apk add --no-cache python3 py3-cryptography

COPY deploy/publisher-jwks/nginx.conf /etc/nginx/nginx.conf
COPY --from=signer /out/fora-sign-directory /usr/local/bin/fora-sign-directory
COPY deploy/publisher-jwks/entrypoint.sh /usr/local/bin/publisher-jwks-entrypoint.sh
# The one WBA entry shape + validity-window helper, shared with every key-gen
# script; the entrypoint imports it instead of carrying a shell-side copy.
COPY scripts/lib/ed25519_keys.py /opt/fora/lib/ed25519_keys.py
RUN chmod +x /usr/local/bin/publisher-jwks-entrypoint.sh

EXPOSE 80

ENTRYPOINT ["/usr/local/bin/publisher-jwks-entrypoint.sh"]
CMD ["nginx", "-g", "daemon off;"]
