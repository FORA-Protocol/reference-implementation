#!/usr/bin/env bash
# Build + push the service images (exchange, broker, identity, onboarding,
# console, publisher) and print each one's digest — the value a digest-pinned
# stack pins.
#
# Registry, path prefix, and tag are first-class knobs so the same script
# serves GitLab Container Registry (default), GHCR, Docker Hub, or ECR:
#
#   REGISTRY  registry host                 (default: registry.gitlab.com)
#   PREFIX    path under the registry        (required, e.g. group/project)
#   TAG       image tag                      (default: latest)
#
# Resulting names: <REGISTRY>/<PREFIX>/<service>:<TAG> — exactly what
# stacks/staging-aws composes from image_registry/image_prefix/image_tag.
# Service names given as arguments limit the run to those images; no names
# means the staging stack's four (exchange broker identity publisher). The
# onboarding preview service and the publisher console are built only when
# named. A named service whose Dockerfile is not in this tree is refused
# before anything is built: a tree can carry less than every service, and a run
# that skipped one would leave its caller holding a tag nobody pushed. The
# console's build passes TAG as the EDGE_RELEASE_REF build argument, and the
# image keeps that value as CONSOLE_EDGE_RELEASE_REF: the version of the edge
# packages the console tells every publisher to build from. A release passes
# its version tag as TAG.
#
# Log in to the registry first (docker login <REGISTRY>); this script does not
# handle credentials.
#
# Usage:
#   PREFIX=group/project deploy/terraform/scripts/build-push-images.sh [--tag=<tag>] [<service>...]

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"
REGISTRY="${REGISTRY:-registry.gitlab.com}"
TAG="${TAG:-latest}"

# The services this script knows. A service's Dockerfile is
# src/<service>/Dockerfile, except the demo publisher origin's, which belongs
# to this package. Repo root is the build context for all of them on purpose:
# src/identity/Dockerfile copies the root go.mod/go.sum and the shared
# internal/ tree, not just src/identity.
KNOWN_SERVICES=(exchange broker identity onboarding console publisher)
dockerfile_for() {
    case "$1" in
        publisher) echo deploy/terraform/publisher/Dockerfile ;;
        *) echo "src/$1/Dockerfile" ;;
    esac
}
known_service() {
    local known
    for known in "${KNOWN_SERVICES[@]}"; do
        [ "${known}" != "$1" ] || return 0
    done
    return 1
}

services=()
for arg in "$@"; do
    case "${arg}" in
        --tag=*) TAG="${arg#--tag=}" ;;
        *)
            known_service "${arg}" \
                || { echo "unknown service '${arg}' (one of: ${KNOWN_SERVICES[*]})" >&2; exit 2; }
            services+=("${arg}")
            ;;
    esac
done
[ "${#services[@]}" -gt 0 ] || services=(exchange broker identity publisher)

# Refused before any docker call: see the header.
for service in "${services[@]}"; do
    [ -f "${REPO_ROOT}/$(dockerfile_for "${service}")" ] || {
        echo "${service}: $(dockerfile_for "${service}") is not in this tree, so its image cannot be built here" >&2
        exit 2
    }
done

if [ -z "${PREFIX:-}" ]; then
    echo "PREFIX is required (path under the registry, e.g. PREFIX=group/project)" >&2
    exit 2
fi

command -v docker >/dev/null 2>&1 || { echo "missing: docker" >&2; exit 2; }

digests=()
push_image() {
    local service="$1" full digest
    local build_args=()
    full="${REGISTRY}/${PREFIX}/${service}:${TAG}"
    # Only the console takes a build argument: the tag it is pushed under,
    # which it names as the edge release (see the header).
    [ "${service}" != console ] || build_args=(--build-arg "EDGE_RELEASE_REF=${TAG}")
    echo "==> build ${full}"
    docker build --platform=linux/amd64 -f "$(dockerfile_for "${service}")" ${build_args[@]+"${build_args[@]}"} -t "${full}" .
    echo "==> push ${full}"
    docker push "${full}"
    # The digest of what the registry now holds under that tag — read back
    # from the registry, not from the local image, so it is the value a
    # digest-pinned stack will resolve.
    digest="$(docker buildx imagetools inspect "${full}" --format '{{.Manifest.Digest}}')"
    digests+=("${service}=${digest}")
}

cd "${REPO_ROOT}"
for service in "${services[@]}"; do push_image "${service}"; done

echo "done: ${REGISTRY}/${PREFIX}/{$(IFS=,; echo "${services[*]}")}:${TAG}"
echo "digests (the *_image_digest values for a digest-pinned stack):"
for entry in "${digests[@]}"; do echo "  ${entry%%=*}: ${entry#*=}"; done
