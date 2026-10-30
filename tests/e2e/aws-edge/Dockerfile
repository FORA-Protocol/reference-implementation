# AWS CloudFront canned-policy signed-URL verifier stand-in.
# Node 22 has a built-in fetch + RSA-SHA1 verify via the crypto module, so we
# stay dependency-free. Built from the repo root (see docker-compose.e2e.yml
# build.context) so the shared discovery addresses and key-directory builders
# (src/edge/src/well-known.mjs) can be copied in — the shim imports them
# instead of hand-rolling the paths and the directory shape.

FROM node:22-slim
WORKDIR /app
COPY tests/e2e/aws-edge/package.json tests/e2e/aws-edge/server.mjs ./
COPY src/edge/src/well-known.mjs ./well-known.mjs
ENV NODE_ENV=production \
    PORT=8788
EXPOSE 8788
USER node
ENTRYPOINT ["node", "server.mjs"]
