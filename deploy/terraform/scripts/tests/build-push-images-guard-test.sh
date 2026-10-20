#!/usr/bin/env bash
# Prove build-push-images.sh keeps its contract with docker stubbed on PATH:
# with no service names the staging stack's four images are built and pushed
# (onboarding only when named); with names only those are, in the order given;
# a named service whose Dockerfile the tree does not carry is refused before
# any build; the console, also built only by name, comes from its
# own Dockerfile with the tag it is pushed under as its EDGE_RELEASE_REF build
# argument, no other image gets a build argument, and that Dockerfile sets
# CONSOLE_EDGE_RELEASE_REF from the argument in its final stage; an unknown
# name is refused before any build; and the run ends by printing one digest
# per pushed service, read back from the registry (buildx imagetools
# inspect), not from the local image.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REAL_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

# The script runs from a scratch copy of the repository, so which Dockerfiles
# exist is this test's to decide: the script builds only services whose source
# the tree carries. lib/ comes along because the script sources it, and
# scripts/ is a link because lib/staging-env.sh reads the shared Python helpers
# there. Every Dockerfile starts present; one case removes the onboarding
# service's.
root="${work}/root"
mkdir -p "${root}/deploy/terraform/scripts"
cp "${REAL_ROOT}/deploy/terraform/scripts/build-push-images.sh" "${root}/deploy/terraform/scripts/"
cp -R "${REAL_ROOT}/deploy/terraform/scripts/lib" "${root}/deploy/terraform/scripts/"
ln -s "${REAL_ROOT}/scripts" "${root}/scripts"
SCRIPT="${root}/deploy/terraform/scripts/build-push-images.sh"
# published-ref-allow: the scratch tree stands in for one that carries the onboarding service
onboarding_dockerfile=src/onboarding/Dockerfile
for f in src/exchange/Dockerfile src/broker/Dockerfile src/identity/Dockerfile "${onboarding_dockerfile}" \
    src/console/Dockerfile deploy/terraform/publisher/Dockerfile; do
    mkdir -p "$(dirname "${root}/${f}")"
    echo "FROM scratch" > "${root}/${f}"
done

fail() { echo "build-push-images guard test FAILED: $1" >&2; exit 1; }

mkdir -p "${work}/bin"
# Stub docker: records every call; `buildx imagetools inspect <ref>` answers a
# digest derived from the reference so each service gets a distinct value.
cat > "${work}/bin/docker" <<STUB
#!/usr/bin/env bash
echo "\$*" >> "${work}/calls"
if [ "\$1" = buildx ]; then
    printf 'sha256:%064d\n' "\$(printf '%s' "\$4" | cksum | cut -d' ' -f1)"
fi
STUB
chmod +x "${work}/bin/docker"

run_script() {
    rm -f "${work}/calls"
    out="$(PATH="${work}/bin:${PATH}" PREFIX=group/project REGISTRY=reg.example "${SCRIPT}" "$@" 2>&1)" && return 0 || return $?
}
built() { grep '^build ' "${work}/calls" | sed 's/.* -t reg.example\/group\/project\/\([a-z]*\):.*/\1/' | paste -sd' ' -; }

# Default: all four, in the fixed order.
run_script --tag=r1 || fail "default run exited $?: ${out}"
[ "$(built)" = "exchange broker identity publisher" ] || fail "default built '$(built)', expected all four"
[ "$(grep -c '^push ' "${work}/calls")" = 4 ] || fail "default pushed $(grep -c '^push ' "${work}/calls") images"

# Named services: exactly those, in the order given, and a digest line each.
# onboarding is nameable (a release publishes it) but never in the default set.
run_script --tag=r1 identity exchange onboarding || fail "named run exited $?: ${out}"
[ "$(built)" = "identity exchange onboarding" ] || fail "named run built '$(built)', expected 'identity exchange onboarding'"
grep -q "${onboarding_dockerfile}" "${work}/calls" || fail "onboarding was not built from its own Dockerfile: $(cat "${work}/calls")"
grep -qE '^  onboarding: sha256:[0-9]{64}$' <<<"${out}" || fail "no digest line for onboarding: ${out}"
grep -q 'buildx imagetools inspect reg.example/group/project/identity:r1 --format' "${work}/calls" \
    || fail "the digest is not read back from the registry: $(cat "${work}/calls")"
grep -qE '^  identity: sha256:[0-9]{64}$' <<<"${out}" || fail "no digest line for identity: ${out}"
grep -qE '^  exchange: sha256:[0-9]{64}$' <<<"${out}" || fail "no digest line for exchange: ${out}"
grep -q 'publisher' <<<"${out}" && fail "the named run mentions the publisher: ${out}"

# The console: by name only, from its own Dockerfile, and with the tag the run
# pushes as EDGE_RELEASE_REF, which the image keeps as the edge release the
# console names. TAG comes from the environment here, the way a release passes
# it. Only the console's build may carry a build argument.
TAG=v1.2.3 run_script exchange onboarding console || fail "console run exited $?: ${out}"
[ "$(built)" = "exchange onboarding console" ] || fail "console run built '$(built)', expected 'exchange onboarding console'"
console_build="$(grep '^build .* -t reg.example/group/project/console:v1.2.3 ' "${work}/calls")" \
    || fail "no build of console:v1.2.3: $(cat "${work}/calls")"
grep -qE -- '(^| )-f src/console/Dockerfile( |$)' <<<"${console_build}" \
    || fail "the console was not built from its own Dockerfile: ${console_build}"
grep -qE -- '(^| )--build-arg[ =]EDGE_RELEASE_REF=v1\.2\.3( |$)' <<<"${console_build}" \
    || fail "the console was not built with the pushed tag as EDGE_RELEASE_REF: ${console_build}"
[ "$(grep -c -- '--build-arg' "${work}/calls")" = 1 ] \
    || fail "an image other than the console was built with a build argument: $(cat "${work}/calls")"
grep -qE '^  console: sha256:[0-9]{64}$' <<<"${out}" || fail "no digest line for console: ${out}"
# The argument reaches the console only if its Dockerfile declares it in the
# final stage, with no default, and sets CONSOLE_EDGE_RELEASE_REF from it
# there. Declared in an earlier stage or not at all, the setting is empty in
# every image. With a default, an image built without the argument would name
# a version nobody released.
console_dockerfile="${REAL_ROOT}/src/console/Dockerfile"
awk '/^FROM /{arg=0; env=0}
     /^ARG EDGE_RELEASE_REF$/{arg=1}
     /^ENV CONSOLE_EDGE_RELEASE_REF=\$\{EDGE_RELEASE_REF\}$/{if (arg) env=1}
     END{exit !env}' "${console_dockerfile}" \
    || fail "src/console/Dockerfile does not declare ARG EDGE_RELEASE_REF, with no default, in its final stage and set CONSOLE_EDGE_RELEASE_REF from it"

# An unknown service is refused before any docker call.
if run_script --tag=r1 exchange origin; then fail "an unknown service name was accepted"; fi
grep -q "unknown service 'origin'" <<<"${out}" || fail "unknown service not named in the error: ${out}"
[ ! -f "${work}/calls" ] || fail "docker was called for a refused run: $(cat "${work}/calls")"

# A named service whose source the tree does not carry is refused before any
# docker call, rather than skipped: its caller would go on to use a tag nobody
# pushed. The default set does not name it, so the default run still builds.
rm "${root}/${onboarding_dockerfile}"
if run_script --tag=r1 exchange onboarding; then fail "a service with no Dockerfile in the tree was built"; fi
grep -q "onboarding: ${onboarding_dockerfile} is not in this tree" <<<"${out}" \
    || fail "the refusal does not name the missing Dockerfile: ${out}"
[ ! -f "${work}/calls" ] || fail "docker was called for a refused run: $(cat "${work}/calls")"
run_script --tag=r1 || fail "the default run needs no onboarding source, yet exited $?: ${out}"
[ "$(built)" = "exchange broker identity publisher" ] || fail "default without onboarding built '$(built)'"

echo "build-push-images guard test passed (default: the staging four; named services only, onboarding by name; the console by name, with its tag as EDGE_RELEASE_REF, which its Dockerfile keeps; unknown refused; a service whose Dockerfile the tree lacks refused; digests read back from the registry)"
