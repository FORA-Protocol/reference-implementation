#!/usr/bin/env bash
# Prove redeploy-demo.sh runs the redeploy in order and stops at the first
# failure. The REAL script runs from a copy under a temporary repository root,
# beside stub versions of the scripts it calls; terraform, git, ssh,
# ssh-keygen and curl are stubbed on PATH. Every call is recorded, and the test checks:
#   * a clean run: the steps in order, one image tag for the build and for
#     Terraform, the VM replaced, the apply arguments passed through, and
#     STACK_DIR pointing at the demo stack in every step;
#   * the builds and the post-apply steps run from the exported commit, and
#     the bundle lands where the stack deploys it from: the default path, or
#     a lambda_zip_path set in tfvars;
#   * the Lambda config and the onboarding switch come from the current
#     configuration (terraform console, whose warnings precede the value on
#     stdout), not from the last apply's state; every console read runs with
#     deploy_edge off, so the edge module's bundle check cannot fire before
#     the bundle is built, and the apply does not;
#   * onboarding enabled: its image is built and handed to Terraform too,
#     unless the exported commit carries no source for it, in which case the
#     stack keeps the image its variables name;
#   * before the bootstrap, HTTPS is checked on every Caddy hostname the stack
#     outputs; a hostname without a trusted certificate stops the run with
#     that hostname and Caddy's error for it, or with the SSH error when the
#     log cannot be read;
#   * a failing step: nothing after it runs, and the script exits non-zero;
#   * a dirty tree, untracked files under deploy/, a missing PREFIX, or
#     -var / -var-file arguments: refused before anything runs.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "redeploy-demo guard test FAILED: $1" >&2; exit 1; }

scripts="${work}/deploy/terraform/scripts"
mkdir -p "${scripts}" "${work}/deploy/terraform/stacks/demo-aws" "${work}/bin"
cp -R "${REPO_ROOT}/deploy/terraform/scripts/redeploy-demo.sh" "${REPO_ROOT}/deploy/terraform/scripts/lib" "${scripts}/"
ln -s "${REPO_ROOT}/scripts" "${work}/scripts"
stack="$(cd "${work}/deploy/terraform/stacks/demo-aws" && pwd)"
calls="${work}/calls"
# The exported commit's src/: the redeploy rebuilds the onboarding image only
# when the commit carries that service's source, so one case removes it.
# published-ref-allow: a scratch stand-in for the onboarding service's source
onboarding_src="${work}/src/onboarding"
mkdir -p "${onboarding_src}"
echo "FROM scratch" > "${onboarding_src}/Dockerfile"

# Each called script records its name, whether it ran from the export or the
# working tree, STACK_DIR and its arguments, and fails when its name is
# FAIL_STEP. The bundle build writes a zip naming the tree it was built in.
for s in build-push-images.sh build-lambda-edge.sh bootstrap-identity.sh seed-staging.sh fund-staging-agent.sh smoke.sh; do
    cat > "${scripts}/${s}" <<STUB
#!/usr/bin/env bash
root="\$(cd "\$(dirname "\$0")/../../.." && pwd)"
[ "\${root}" = "${work}" ] && from=worktree || from=export
echo "${s} \${from} STACK_DIR=\${STACK_DIR} \$*" >> "${calls}"
if [ "${s}" = build-lambda-edge.sh ]; then
    mkdir -p "\${root}/src/edge/dist"
    echo "bundle from \${from}" > "\${root}/src/edge/dist/lambda-edge.zip"
fi
[ "\${FAIL_STEP:-}" != "${s}" ]
STUB
done
# terraform: console answers from the configuration (DESIRED); the state
# holds an older Lambda config and no onboarding_url, so reading either from
# state gives a different result. apply is recorded.
cat > "${work}/bin/terraform" <<STUB
#!/usr/bin/env bash
shift # -chdir=
case "\$1 \${3:-}" in
    console*)
        echo "terraform \$*" >> "${work}/console-calls"
        # Like the real console: a warning on stdout first.
        printf 'Warning: Due to the problems above, some expressions may produce unexpected results.\n\n\n'
        case "\$(cat)" in
            *lambda_edge_config*) echo "\"\$(printf '{"FORA_ENFORCE_BINDING":"false"}' | base64)\"" ;;
            *lambda_zip_path*) echo "\"\$(printf '%s' "\${ZIP_PATH:-./../../../../src/edge/dist/lambda-edge.zip}" | base64)\"" ;;
            local.onboarding_enabled) [ "\${ONBOARDING:-0}" = 1 ] && echo true || echo false ;;
            *) exit 1 ;;
        esac ;;
    "output lambda_edge_config") echo '{"FORA_ENFORCE_BINDING":"true"}' ;;
    "output vm_public_ip") echo 192.0.2.10 ;;
    "output caddy_hostnames") echo "exchange.demo.example login.demo.example" ;;
    apply*) echo "terraform \$*" >> "${calls}" ;;
    *) exit 1 ;;
esac
STUB
cat > "${work}/bin/git" <<STUB
#!/usr/bin/env bash
case "\$3" in
    diff) [ "\${DIRTY:-0}" = 0 ] ;;
    ls-files) [ "\${UNTRACKED:-0}" = 0 ] || echo deploy/terraform/stacks/demo-aws/override.tf ;;
    rev-parse) echo 0123456789ab ;;
    archive) tar -c -C "${work}" deploy scripts src ;;
esac
STUB
# ssh prints a Caddy log with one error per hostname, or fails when SSH_FAIL
# is set. curl fails for the hostname named in NO_TLS.
cat > "${work}/bin/ssh" <<STUB
#!/usr/bin/env bash
echo "ssh \$*" >> "${calls}"
case "\$*" in
    *"logs --no-color caddy"*)
        [ "\${SSH_FAIL:-0}" = 0 ] || { echo "connection refused"; exit 255; }
        echo 'caddy-1  | {"level":"error","identifier":"login.demo.example","error":"HTTP 429 rateLimited, retry after 2026-01-01 00:00:00 UTC"}'
        echo 'caddy-1  | {"level":"error","identifier":"exchange.demo.example","error":"other host"}' ;;
esac
STUB
cat > "${work}/bin/curl" <<STUB
#!/usr/bin/env bash
host="\${!#}"; host="\${host#https://}"; host="\${host%/}"
echo "curl \${host}" >> "${calls}"
[ "\${host}" != "\${NO_TLS:-}" ]
STUB
printf '#!/usr/bin/env bash\necho "ssh-keygen $*" >> "%s"\n' "${calls}" > "${work}/bin/ssh-keygen"
chmod +x "${scripts}"/*.sh "${work}/bin"/*

run_script() {
    rm -f "${calls}" "${work}/console-calls" "${work}/src/edge/dist/lambda-edge.zip"
    touch "${calls}"
    out="$(env -u STACK_DIR -u TAG PATH="${work}/bin:${PATH}" PREFIX="${PREFIX-group/project}" REGISTRY=reg.example \
        "${scripts}/redeploy-demo.sh" "$@" 2>&1)" && return 0 || return $?
}
steps() { cut -d' ' -f1 "${calls}" | paste -sd' ' -; }

tag=demo-0123456789ab
img="reg.example/group/project"

# A clean run: every step, in order.
run_script -auto-approve || fail "clean run exited $?: ${out}"
[ "$(steps)" = "build-push-images.sh build-lambda-edge.sh terraform ssh-keygen ssh curl curl bootstrap-identity.sh seed-staging.sh fund-staging-agent.sh smoke.sh" ] \
    || fail "clean run steps: $(steps)"
grep -qx "build-push-images.sh export STACK_DIR=${stack} --tag=${tag} exchange broker identity publisher" "${calls}" \
    || fail "images not built from the export with the commit tag: $(grep build-push "${calls}")"
grep -qx "build-lambda-edge.sh export STACK_DIR=${stack} ${stack}/lambda-edge-config.json" "${calls}" \
    || fail "the edge bundle was not built from the export and the rendered config"
[ "$(cat "${stack}/lambda-edge-config.json")" = '{"FORA_ENFORCE_BINDING":"false"}' ] \
    || fail "the Lambda config was not rendered from the current configuration: $(cat "${stack}/lambda-edge-config.json")"
[ "$(cat "${work}/src/edge/dist/lambda-edge.zip" 2>/dev/null)" = "bundle from export" ] \
    || fail "the bundle built from the export is not where the stack deploys it from"
grep -qx "terraform apply -replace=module.vm.aws_instance.this -var=image_registry=reg.example -var=image_prefix=group/project -var=image_tag=${tag} -auto-approve" "${calls}" \
    || fail "apply arguments: $(grep '^terraform' "${calls}")"
[ -s "${work}/console-calls" ] || fail "no terraform console read was recorded"
if grep -v -e '-var=deploy_edge=false' "${work}/console-calls"; then
    fail "a console read ran with the edge module in the evaluation"
fi
grep -qx "ssh-keygen -R 192.0.2.10" "${calls}" || fail "the old host key was not removed"
for h in exchange.demo.example login.demo.example; do
    grep -qx "curl ${h}" "${calls}" || fail "HTTPS on ${h} was not checked"
done
grep -q "StrictHostKeyChecking=accept-new .*ubuntu@192.0.2.10 true" "${calls}" || fail "SSH wait: $(grep '^ssh ' "${calls}")"
for s in bootstrap-identity.sh seed-staging.sh fund-staging-agent.sh smoke.sh; do
    grep -qx "${s} export STACK_DIR=${stack} " "${calls}" || fail "${s} did not run from the export against the demo stack"
done

# Onboarding on in the configuration: built and passed to Terraform with the
# same tag.
ONBOARDING=1 run_script || fail "onboarding run exited $?: ${out}"
grep -q " publisher onboarding$" "${calls}" || fail "onboarding image not built"
grep -q -- "-var=onboarding_image=${img}/onboarding:${tag}" "${calls}" || fail "onboarding image not passed to terraform"

# Onboarding on, but the commit carries no source for it: there is nothing to
# rebuild, so the stack keeps the image its variables name and the run goes on.
rm -rf "${onboarding_src}"
ONBOARDING=1 run_script || fail "onboarding run without its source exited $?: ${out}"
grep -q " publisher onboarding$" "${calls}" && fail "onboarding was built from a commit without its source"
grep -q -- "-var=onboarding_image=" "${calls}" && fail "an onboarding image was passed to terraform with no build behind it"
grep -q "does not carry its source" <<<"${out}" || fail "the skipped rebuild is not reported: ${out}"

# A lambda_zip_path set in tfvars: the bundle is copied there.
ZIP_PATH="${work}/custom/edge.zip" run_script || fail "custom zip path run exited $?: ${out}"
[ "$(cat "${work}/custom/edge.zip" 2>/dev/null)" = "bundle from export" ] \
    || fail "the bundle was not copied to the lambda_zip_path from tfvars"

# A hostname without a trusted certificate stops the run before the bootstrap,
# and the output names the hostname and Caddy's error for it only.
if NO_TLS=login.demo.example TLS_WAIT_SECONDS=0 run_script; then fail "a missing certificate did not fail the run"; fi
if grep -q bootstrap-identity.sh "${calls}"; then fail "the bootstrap ran without a certificate"; fi
[[ "${out}" == *"no trusted certificate for login.demo.example"* ]] || fail "the hostname was not reported: ${out}"
[[ "${out}" == *"retry after 2026-01-01"* ]] || fail "Caddy's error was not reported: ${out}"
[[ "${out}" != *"other host"* ]] || fail "another hostname's error was reported: ${out}"
# A failed SSH read is reported as such, not as a log without errors.
if SSH_FAIL=1 NO_TLS=login.demo.example TLS_WAIT_SECONDS=0 run_script; then fail "a missing certificate did not fail the run"; fi
[[ "${out}" == *"could not read Caddy's log: connection refused"* ]] || fail "the failed SSH read was not reported: ${out}"

# A failing step stops the run: nothing after the seed.
if FAIL_STEP=seed-staging.sh run_script; then fail "a failed seed did not fail the run"; fi
[ "$(steps)" = "build-push-images.sh build-lambda-edge.sh terraform ssh-keygen ssh curl curl bootstrap-identity.sh seed-staging.sh" ] \
    || fail "steps after a failed seed: $(steps)"
if FAIL_STEP=build-push-images.sh run_script; then fail "a failed image build did not fail the run"; fi
[ "$(steps)" = "build-push-images.sh" ] || fail "steps after a failed build: $(steps)"

# Refused before anything runs.
if DIRTY=1 run_script; then fail "a dirty tree was accepted"; fi
[ ! -s "${calls}" ] || fail "a dirty tree still ran: $(steps)"
if UNTRACKED=1 run_script; then fail "untracked files under deploy/ were accepted"; fi
[ ! -s "${calls}" ] || fail "untracked files under deploy/ still ran: $(steps)"
if PREFIX= run_script; then fail "a missing PREFIX was accepted"; fi
[ ! -s "${calls}" ] || fail "a missing PREFIX still ran: $(steps)"
for arg in -var=fora_enforce_binding=true -var-file=extra.tfvars --var=x=1; do
    if run_script -auto-approve "${arg}"; then fail "${arg} was accepted"; fi
    [ ! -s "${calls}" ] || fail "${arg} still ran: $(steps)"
done

echo "redeploy-demo guard test passed"
