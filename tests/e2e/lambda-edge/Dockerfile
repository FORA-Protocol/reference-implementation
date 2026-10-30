# Lambda@Edge runtime for the E2E stack: the REAL production artifact running
# inside the AWS Lambda runtime image.
#
# The package is produced by src/edge/scripts/build-lambda-edge.mjs — the
# same script deploy/terraform/scripts/build-lambda-edge.sh runs for a
# deployment — so what the tests drive is the pair of files that would be
# uploaded to Lambda: the release bundle lambda-edge.mjs, and the index.mjs
# beside it that assigns the per-deployment config and re-exports the handler.
# That is also the layout the console hands a publisher. The AWS base image
# ships the Lambda Runtime Interface Emulator, so the container answers real
# Lambda invocations at POST /2015-03-31/functions/function/invocations on
# port 8080.
#
# Two packages are built from two configs (see write-configs.mjs): the one in
# the task root publishes a Web Bot Auth key, the one in no-wba/ publishes
# none. Each directory holds its own index.mjs next to its own bundle, exactly
# as a deployment's zip does. One container serves one handler, so the compose
# stack runs two services off this one image and each covers one posture of
# the WBA directory route.
#
# Build context is the repo root (see docker-compose.e2e.yml) so both src/edge/
# and this directory are reachable.

# @fora-protocol/sdk is a git dependency pinned to one commit of the protocol
# repository. Written out once, with the reasoning, in
# src/edge/Dockerfile.miniflare — three copies of it is how the correction
# reached three sites of eight last time. The full node image rather than -slim
# so git is present for npm's clone fallback without an apt layer.
FROM node:22 AS builder
WORKDIR /app
COPY src/edge/package.json src/edge/package-lock.json ./
# Persist npm's download cache across builds — the slowest two stages of a cold
# stack build. It holds every tarball this step downloads, the SDK's codeload
# tarball among them. The mount lives with the builder, so nothing here reaches a
# layer.
#
# id, because BuildKit otherwise names a cache mount after its target and all
# three npm mounts in this stack would be one cache under one lock, serializing
# images compose builds concurrently. Per image, one writer each.
RUN --mount=type=cache,id=npm-lambda-edge,target=/root/.npm,sharing=locked \
    npm ci --include=dev
COPY src/edge/ ./
COPY tests/e2e/lambda-edge/write-configs.mjs /build/write-configs.mjs
RUN node /build/write-configs.mjs --out-dir /build/config --edge-dir /app
# The build script smoke-invokes each package it produces (the health route,
# and the discovery document and an unsigned human read handed back to
# CloudFront), which needs no network — so a broken config fails the image
# build here rather than the first test. The publisher domain comes from
# write-configs.mjs, the one place it is named.
RUN node scripts/build-lambda-edge.mjs --config /build/config/with-wba.json \
      --domain "$(cat /build/config/domain)" --out-dir dist/lambda-edge-with-wba \
    && node scripts/build-lambda-edge.mjs --config /build/config/no-wba.json \
      --domain "$(cat /build/config/domain)" --out-dir dist/lambda-edge-no-wba

FROM public.ecr.aws/lambda/nodejs:22
COPY --from=builder /app/dist/lambda-edge-with-wba/index.mjs /app/dist/lambda-edge-with-wba/lambda-edge.mjs ${LAMBDA_TASK_ROOT}/
COPY --from=builder /app/dist/lambda-edge-no-wba/index.mjs /app/dist/lambda-edge-no-wba/lambda-edge.mjs ${LAMBDA_TASK_ROOT}/no-wba/
# Kept out of the task root so the deployed surface stays exactly the two
# packages above.
COPY tests/e2e/lambda-edge/healthcheck.mjs /opt/healthcheck.mjs
CMD ["index.handler"]
