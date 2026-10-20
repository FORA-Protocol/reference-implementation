# Web Bot Auth conformance oracle sidecar for the e2e stack.
#
# Build context is this directory (see the wba-oracle-ts service in
# docker-compose.e2e.yml); nothing outside it goes into the image.
#
# Node 24 runs src/server.ts directly through its built-in type stripping, so
# there is no build step and no compiled copy to drift from the source that tsc
# checks. The base image is pinned by version and by digest: the oracle's
# answers must not change because a floating tag moved.
FROM node:24.21.0-bookworm-slim@sha256:0e0ff40c39bc087845bfb27465a0df4ea419520094bc35842ff83dd8cbe6f9b6

WORKDIR /app

# Runtime dependencies only, exactly as package-lock.json records them; npm ci
# checks each tarball against the lock's sha512 integrity.
COPY package.json package-lock.json ./
RUN --mount=type=cache,id=npm-wba-oracle-ts,target=/root/.npm,sharing=locked \
    npm ci --omit=dev --ignore-scripts

COPY src ./src

USER node
EXPOSE 8090
CMD ["node", "src/server.ts"]
