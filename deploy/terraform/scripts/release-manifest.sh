#!/usr/bin/env bash
# Write the release manifest: the one durable record of what a version is
# made of, and the only input the production deploy job reads. Written last
# in a publication, after every image and artifact exists, so a version with
# a manifest is a complete version and one without is not deployable.
#
# Every image digest is READ BACK from the registry at write time (buildx
# imagetools inspect on <image-base>/<service>:<version>), never taken from
# an earlier build step's output: the manifest records what the registry
# holds under that tag now, which is what a digest-pinned stack resolves.
#
# Fields (schema_version 1):
#   schema_version         1
#   version                the release tag, vMAJOR.MINOR.PATCH
#   source_sha             the commit the tag points at
#   protocol_sdk_module    github.com/FORA-Protocol/protocol
#   protocol_sdk_version   that module's version pinned in the root go.mod
#   image_base             <registry>/<path> every image name starts with
#   images.<service>       for exchange, broker, identity, onboarding, console:
#     .image                 <image_base>/<service>@<digest> — the reference a
#                            rollout --expect takes verbatim
#     .digest                sha256:<64 hex>
#   production_services    ["exchange","broker","identity","console"]: the
#                          four a production deployment runs. onboarding is
#                          published so that one version names one complete
#                          set of builds; no production deployment runs it.
#   cloudflare_worker      {file, sha256} of the generic worker bundle
#                          published next to this manifest
#   lambda_edge_inputs     {file, sha256} of the Lambda@Edge source archive
#                          published next to this manifest (source, lockfile
#                          and build script; the deployable zip is built at
#                          publisher deployment time from these)
#   published_at           UTC timestamp of this write
#
# Usage:
#   deploy/terraform/scripts/release-manifest.sh --version <tag> --source-sha <sha>
#       --image-base <registry/path> --worker <worker.mjs> --lambda-inputs <archive>
#       --out <manifest.json>

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"

usage() { echo "usage: $0 --version <tag> --source-sha <sha> --image-base <registry/path> --worker <file> --lambda-inputs <file> --out <file>" >&2; exit 2; }

version="" source_sha="" image_base="" worker="" lambda_inputs="" out=""
while [ $# -gt 0 ]; do
    case "$1" in
        --version) version="${2:-}"; shift ;;
        --source-sha) source_sha="${2:-}"; shift ;;
        --image-base) image_base="${2:-}"; shift ;;
        --worker) worker="${2:-}"; shift ;;
        --lambda-inputs) lambda_inputs="${2:-}"; shift ;;
        --out) out="${2:-}"; shift ;;
        *) usage ;;
    esac
    shift || usage
done
[ -n "${version}" ] && [ -n "${source_sha}" ] && [ -n "${image_base}" ] && [ -n "${worker}" ] && [ -n "${lambda_inputs}" ] && [ -n "${out}" ] || usage
[ -f "${worker}" ] || { echo "missing worker bundle: ${worker}" >&2; exit 2; }
[ -f "${lambda_inputs}" ] || { echo "missing Lambda@Edge inputs archive: ${lambda_inputs}" >&2; exit 2; }
command -v docker >/dev/null 2>&1 || { echo "missing: docker" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "missing: jq" >&2; exit 2; }

sha256_of() {
    if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

# The SDK pin, from the module line of the root go.mod — the same line the
# SDK pin guard checks, so the manifest records the version the build used.
sdk_version="$(sed -n 's/^[[:space:]]*github\.com\/FORA-Protocol\/protocol[[:space:]]\{1,\}\(v[^[:space:]]*\).*/\1/p' "${REPO_ROOT}/go.mod" | head -n1)"
[ -n "${sdk_version}" ] || { echo "go.mod does not pin github.com/FORA-Protocol/protocol" >&2; exit 1; }

digest_of() {
    local ref="${image_base}/$1:${version}" digest
    digest="$(docker buildx imagetools inspect "${ref}" --format '{{.Manifest.Digest}}')"
    case "${digest}" in
        sha256:[0-9a-f]*) [ "${#digest}" = 71 ] || { echo "registry answered a malformed digest for ${ref}: '${digest}'" >&2; exit 1; } ;;
        *) echo "registry answered a malformed digest for ${ref}: '${digest}'" >&2; exit 1 ;;
    esac
    echo "${digest}"
}

exchange="$(digest_of exchange)"
broker="$(digest_of broker)"
identity="$(digest_of identity)"
onboarding="$(digest_of onboarding)"
console="$(digest_of console)"

jq -n \
    --arg version "${version}" --arg source_sha "${source_sha}" --arg sdk "${sdk_version}" \
    --arg base "${image_base}" \
    --arg exchange "${exchange}" --arg broker "${broker}" --arg identity "${identity}" --arg onboarding "${onboarding}" \
    --arg console "${console}" \
    --arg worker_file "$(basename "${worker}")" --arg worker_sha "$(sha256_of "${worker}")" \
    --arg lambda_file "$(basename "${lambda_inputs}")" --arg lambda_sha "$(sha256_of "${lambda_inputs}")" \
    --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
    def image(s; d): {image: ($base + "/" + s + "@" + d), digest: d};
    {
      schema_version: 1,
      version: $version,
      source_sha: $source_sha,
      protocol_sdk_module: "github.com/FORA-Protocol/protocol",
      protocol_sdk_version: $sdk,
      image_base: $base,
      images: {
        exchange: image("exchange"; $exchange),
        broker: image("broker"; $broker),
        identity: image("identity"; $identity),
        onboarding: image("onboarding"; $onboarding),
        console: image("console"; $console)
      },
      production_services: ["exchange", "broker", "identity", "console"],
      cloudflare_worker: {file: $worker_file, sha256: $worker_sha},
      lambda_edge_inputs: {file: $lambda_file, sha256: $lambda_sha},
      published_at: $now
    }' > "${out}"

echo "wrote ${out}:"
jq -r '"  version \(.version) at \(.source_sha) (sdk \(.protocol_sdk_version))", (.images | to_entries[] | "  \(.key): \(.value.digest)")' "${out}"
