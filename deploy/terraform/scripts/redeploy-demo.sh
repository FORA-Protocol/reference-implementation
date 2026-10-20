#!/usr/bin/env bash
# Rebuild the AWS demo from the checked-out commit in one run. The steps run
# in this order, and the script stops at the first one that fails:
#
#   1. export the commit to a temporary directory
#   2. render the Lambda@Edge config from the current configuration
#   3. build and push the service images, tagged with the commit
#   4. build the Lambda@Edge bundle
#   5. terraform apply, with the VM replaced and the new image tag
#   6. forget the old VM's SSH host key and wait for the new VM's SSH
#   7. wait for a trusted certificate on every Caddy hostname
#   8. identity bootstrap, seed, test money, smoke check
#
# Steps 3, 4 and 8 run the export's copy of the scripts, so the images, the
# bundle and the seeded catalog come from the commit and nothing else: an
# untracked file in the working tree cannot reach them. Terraform runs in the
# working tree, where the stack's state, tfvars and keys are; for that reason
# the script refuses a working tree with uncommitted changes, or with
# untracked files under deploy/.
#
# Everything that lives on the VM's root disk is deleted: Postgres, Zitadel,
# TigerBeetle and the dev-mode Vault. Every developer sign-up and every agent
# key the Vault held is gone, and step 8 recreates only the demo's own data.
# DNS, the Elastic IP, CloudFront, the certificate, SES and the IAM users stay,
# so no hostname changes and SES does not have to verify the domain again.
# Caddy's certificates stay too, on their own data volume, so the new VM does
# not request them again from Let's Encrypt.
#
# This is for a demo that the deploy guide (deploy-demo-aws.md) has already
# brought up once: the keys, terraform.tfvars and secrets.auto.tfvars must be
# in place. Log in to the registry first (docker login), and have AWS
# credentials and, if needed, FORA_SSH_IDENTITY_FILE set.
#
# Env:
#   PREFIX     required: the path under the registry, as for
#              build-push-images.sh. The script passes REGISTRY, PREFIX and
#              TAG to Terraform too, so the new VM pulls exactly what was
#              pushed, whatever terraform.tfvars says.
#   REGISTRY   default registry.gitlab.com
#   TAG        image tag, default demo-<commit>
#   STACK_DIR  default the demo stack
#   GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET
#              passed to bootstrap-identity.sh; omit both for local sign-in
#              only
#   WAIT_SECONDS  how long to wait for the new VM's SSH (default 300)
#   TLS_WAIT_SECONDS
#              how long to wait for trusted HTTPS on every Caddy hostname
#              (default 300)
#
# Arguments are passed to terraform apply, e.g. -auto-approve. -var and
# -var-file are refused: the Lambda config is rendered before the apply
# without them, so a value set only there would not reach the bundle. Set
# such values in terraform.tfvars.
#
# Usage:
#   PREFIX=group/project deploy/terraform/scripts/redeploy-demo.sh [terraform apply args...]
set -euo pipefail

SCRIPTS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Absolute, whatever the caller exported: the seed runs from the export of the
# commit, where a relative stack path does not resolve to the keys.
export STACK_DIR="$(cd "${STACK_DIR:-${SCRIPTS}/../stacks/demo-aws}" && pwd)"
. "${SCRIPTS}/lib/staging-env.sh"

export REGISTRY="${REGISTRY:-registry.gitlab.com}"
WAIT_SECONDS="${WAIT_SECONDS:-300}"
TLS_WAIT_SECONDS="${TLS_WAIT_SECONDS:-300}"
VM_RESOURCE="module.vm.aws_instance.this"
# Where build-lambda-edge.sh writes the bundle, under the tree it runs in.
LAMBDA_ZIP="src/edge/dist/lambda-edge.zip"

[ -n "${PREFIX:-}" ] || { echo "PREFIX is required (path under the registry, e.g. PREFIX=group/project)" >&2; exit 2; }
export PREFIX
for arg in "$@"; do
    case "${arg}" in
        -var | -var[=-]* | --var | --var[=-]*)
            echo "${arg}: set Terraform variables in terraform.tfvars; the Lambda config is rendered without the script's arguments" >&2
            exit 2
            ;;
    esac
done

git -C "${REPO_ROOT}" diff --quiet HEAD \
    || { echo "the working tree has uncommitted changes; commit or stash them, the image tag must name what is deployed" >&2; exit 2; }
untracked="$(git -C "${REPO_ROOT}" ls-files --others --exclude-standard -- deploy)"
[ -z "${untracked}" ] \
    || { printf 'untracked files under deploy/, which Terraform may read:\n%s\n' "${untracked}" >&2; exit 2; }
TAG="${TAG:-demo-$(git -C "${REPO_ROOT}" rev-parse --short=12 HEAD)}"

echo "== 1. export the commit =="
src="$(mktemp -d)"
trap 'rm -rf "${src}"' EXIT
git -C "${REPO_ROOT}" archive HEAD | tar -x -C "${src}"
SRC_SCRIPTS="${src}/deploy/terraform/scripts"

echo "== 2. render the Lambda@Edge config =="
tf_vars=(-var="image_registry=${REGISTRY}" -var="image_prefix=${PREFIX}" -var="image_tag=${TAG}")
# Evaluated from the configuration and tfvars as they are now, not read from
# the state of the last apply, which predates any change to them. The
# expression is the lambda_edge_config output's; base64 keeps the value free
# of the quoting terraform console adds to strings. The console may print
# warnings on stdout before the value, so only the last line is the value.
#
# The values read here do not depend on deploy_edge, so the console runs with
# it off. That leaves the edge module out of the evaluation, whose check that
# the bundle exists would otherwise report an error on a checkout where step 4
# has not built the bundle yet. The apply below keeps the configured value.
tf_eval() { echo "$1" | terraform -chdir="${STACK_DIR}" console -no-color "${tf_vars[@]}" -var=deploy_edge=false | tail -n 1; }
tf_eval 'base64encode(jsonencode(local.lambda_edge_config))' | tr -d '"' | base64 -d > "${STACK_DIR}/lambda-edge-config.json"

# The path the stack deploys the bundle from: the default, or lambda_zip_path
# from tfvars. A relative path is relative to the stack directory.
zip_path="$(tf_eval 'base64encode(local.lambda_zip_path)' | tr -d '"' | base64 -d)"
case "${zip_path}" in /*) ;; *) zip_path="${STACK_DIR}/${zip_path}" ;; esac

# When the onboarding service is on, its image is rebuilt from this commit
# like the others, if the commit carries the service's source. A tree without
# it cannot build the image, so the stack keeps the onboarding_image its
# variables name.
services=(exchange broker identity publisher)
if [ "$(tf_eval local.onboarding_enabled)" = true ]; then
    # published-ref-allow: whether the export carries the onboarding source decides the rebuild
    if [ -f "${src}/src/onboarding/Dockerfile" ]; then
        services+=(onboarding)
        tf_vars+=(-var="onboarding_image=${REGISTRY}/${PREFIX}/onboarding:${TAG}")
    else
        echo "onboarding is on, but this commit does not carry its source: keeping the onboarding_image the stack's variables name"
    fi
fi

echo "== 3. build and push ${services[*]} as ${TAG} =="
"${SRC_SCRIPTS}/build-push-images.sh" --tag="${TAG}" "${services[@]}"

echo "== 4. build the Lambda@Edge bundle =="
"${SRC_SCRIPTS}/build-lambda-edge.sh" "${STACK_DIR}/lambda-edge-config.json"
mkdir -p "$(dirname "${zip_path}")"
cp "${src}/${LAMBDA_ZIP}" "${zip_path}"

echo "== 5. terraform apply, replacing the VM =="
terraform -chdir="${STACK_DIR}" apply -replace="${VM_RESOURCE}" "${tf_vars[@]}" "$@"

echo "== 6. wait for the new VM's SSH =="
# The Elastic IP is the same, the host key is new: ssh would refuse the
# changed key, so drop the old entry and accept the new one on first contact.
host="$(tf_out vm_public_ip)"
ssh-keygen -R "${host}" >/dev/null 2>&1 || true # no entry to remove is fine
load_ssh_cmd
waited=0
until ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 "${SSH_CMD[@]:1}" true 2>/dev/null; do
    if [ "${waited}" -ge "${WAIT_SECONDS}" ]; then
        echo "no SSH to ${host} within ${WAIT_SECONDS}s" >&2
        exit 1
    fi
    sleep 10
    waited=$((waited + 10))
done

echo "== 7. wait for HTTPS on every Caddy hostname =="
# curl checks the certificate against the system trust store, as the agents'
# clients do, and any HTTP status passes: a 502 from a service that is still
# starting still means the certificate is in place. A missing certificate
# stops the run here, with the hostname and Caddy's own ACME error, instead of
# as a Zitadel timeout inside the bootstrap. For a Let's Encrypt rate limit
# that error carries the time after which a retry can work.
deadline=$((SECONDS + TLS_WAIT_SECONDS))
for name in $(tf_out caddy_hostnames); do
    until curl -sS -o /dev/null --max-time 10 "https://${name}/" 2>/dev/null; do
        if [ "${SECONDS}" -ge "${deadline}" ]; then
            echo "no trusted certificate for ${name} within ${TLS_WAIT_SECONDS}s" >&2
            # Read the log first and search it second, so a failed SSH read
            # is reported as such and not as a log without errors.
            if logs="$("${SSH_CMD[@]}" "sudo docker compose -f ${VM_COMPOSE_FILE} logs --no-color caddy" 2>&1)"; then
                errors="$(printf '%s\n' "${logs}" | grep -F "\"${name}\"" | grep -F '"level":"error"' | tail -n 3 || true)"
                echo "Caddy's last errors for ${name}:" >&2
                echo "${errors:-(none)}" >&2
            else
                echo "could not read Caddy's log: ${logs}" >&2
            fi
            exit 1
        fi
        sleep 10
    done
done

echo "== 8. bootstrap, seed, fund, smoke =="
"${SRC_SCRIPTS}/bootstrap-identity.sh"
"${SRC_SCRIPTS}/seed-staging.sh"
"${SRC_SCRIPTS}/fund-staging-agent.sh"
"${SRC_SCRIPTS}/smoke.sh"

echo "demo redeployed at ${TAG}"
