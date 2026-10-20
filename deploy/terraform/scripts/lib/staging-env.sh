# Sourced by every script in deploy/terraform/scripts/ — not runnable on its
# own. Defines the values that more than one script must agree on, exactly
# once:
#
#   REPO_ROOT         absolute path to the repository root, resolved from this
#                     file's location so the scripts work from any cwd.
#
#   BROKER_RELAY_KID  The Broker relay key id, overridable via env. Names a
#                     KEY (a versioned key id, JOSE "kid" — rotating the key
#                     changes it). Resolved by the Exchange from the Broker's
#                     own Web Bot Auth directory, so it needs no DNS hostname.
#                     Like the smoke ids, an existing key file is the source
#                     of truth; before it exists the default is
#                     broker.<stack>.v1 (broker.staging.v1, broker.demo.v1,
#                     broker.production.v1), so no stack is named after
#                     another.
#
#   AGENT_ID          The smoke agent and catalog contributor identities.
#   CONTRIBUTOR_ID    Each id IS the hostname of the identity's Web Bot Auth
#                     directory (the services verify a signature by fetching
#                     https://<id>/.well-known/http-message-signatures-directory,
#                     and the harness signer sets Signature-Agent to the kid in
#                     the key file). The kid stored in the generated key file
#                     is therefore the single source of truth: when the file
#                     exists the id is read FROM it, so key generation,
#                     seeding, and smoke can never disagree. Before the file
#                     exists (first gen-keys.sh run) the id is derived
#                     as <label>.<STAGING_DOMAIN>, where STAGING_DOMAIN must
#                     be the domain the stack serves the identity directories
#                     directly under — the tfvars `domain` for
#                     stacks/staging-aws, the publisher hostname
#                     (<publisher_subdomain>.<domain>) for stacks/demo-aws,
#                     whose names all nest under that label. The stack serves
#                     each directory at exactly that hostname, and
#                     require_ids_match_stack below catches a mismatch.
#                     Env overrides still win, for a deliberate one-off.
#
#   STACK_DIR         Stack directory, overridable via env (gen-keys.sh sets
#                     it from its argument before sourcing this file); tf_out()
#   tf_out            reads one raw terraform output from it. Shared by
#                     seed-staging.sh and smoke.sh.
#
#   KEYS_DIR          The stack's key directory — where gen-keys.sh
#                     WRITES and seed-staging.sh, fund-staging-agent.sh, and
#                     smoke.sh READ. Derived from STACK_DIR so an overridden
#                     stack keeps producer and consumers on the same
#                     directory; defined only here for the same reason as the
#                     identity ids above.
#
#   VM_COMPOSE_FILE   Where cloud-init puts the Compose bundle ON THE VM
#                     (modules/compose-stack/templates/cloud-init.yaml.tftpl).
#                     Scripts that run docker compose over SSH must use this
#                     value, so a path move touches the template and this one
#                     line, not every script. Named VM_* to avoid colliding
#                     with docker compose's own COMPOSE_FILE variable.
#
#   SSH_CMD           The ssh command every script uses to reach the VM, built
#   load_ssh_cmd      by load_ssh_cmd as an ARRAY. The destination comes from
#                     the stack's vm_public_ip output; the identity does NOT
#                     come from Terraform at all. Set FORA_SSH_IDENTITY_FILE
#                     to select a private key and the helper adds
#                     "-o IdentitiesOnly=yes -i <path>"; leave it unset and
#                     your ssh client picks the key the way it normally does.
#                     FORA_SSH_IDENTITY_FILE MUST RESOLVE TO AN ABSOLUTE PATH.
#                     "~" is NOT expanded inside a quoted assignment, so write
#                     "$HOME/.ssh/<key>" or the full path instead.
#
#                     IdentitiesOnly=yes is not optional here. Without it ssh
#                     offers every key in the agent before the one named by
#                     -i. Each operator key is installed with an OpenSSH from=
#                     restriction, so the server refuses the ones that are not
#                     yours, and after enough refusals ssh reports "Too many
#                     authentication failures" — which reads like a broken key
#                     rather than the wrong key being offered first.
#
#                     The identity is deliberately not a Terraform output.
#                     State is local and gets copied between machines, and
#                     terraform output replays a value the last apply stored,
#                     so a private-key path taken from it points at whoever
#                     applied last. This helper makes the scripts identity-
#                     neutral. It does NOT make them state-free: it still
#                     reads vm_public_ip, and the scripts read many other
#                     outputs, so someone without the state still cannot run
#                     them.
#
#   VM_COMPOSE_PROJECT  The bundle's `name:` — the prefix Docker puts on the
#                     network and volumes it creates (<project>_default,
#                     <project>_<volume>). A script that runs a one-off
#                     container alongside the stack has to name them, and the
#                     value is set in docker-compose.yml.tftpl; changing it
#                     there means changing it here.

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"

# Interpreter selection (PYTHON array) shared by every key-gen script. Sourced
# here — not per consumer — because staging_id below runs AT SOURCE TIME, so
# every script that sources this file needs a working interpreter even on a
# machine that has uv but no system python3.
. "${REPO_ROOT}/scripts/lib/select-python.sh"

# A script that works on another stack by default names it in DEFAULT_STACK
# before sourcing this file (drop-issuer-publishers.sh, for example, names
# production-aws); STACK_DIR still overrides it.
STACK_DIR="${STACK_DIR:-${REPO_ROOT}/deploy/terraform/stacks/${DEFAULT_STACK:-staging-aws}}"
KEYS_DIR="${STACK_DIR}/keys"
tf_out() { terraform -chdir="${STACK_DIR}" output -raw "$1"; }

# tf_value NAME — one output's value, or nothing for an output that is null.
# Terraform leaves a null output out of the state, so a stack without the
# console has no console_url at all, and `output -raw` fails on a missing one;
# JSON reads both as empty, while a state Terraform cannot read still fails.
# The JSON is piped, never stored: it holds every sensitive output too.
tf_value() {
    terraform -chdir="${STACK_DIR}" output -json | jq -r --arg name "$1" '.[$name].value // empty'
}

# relay_kid: the kid inside the existing relay key file, else broker.<stack>.v1
# from the stack directory's name (staging-aws -> staging).
relay_kid() {
    if [ -f "${KEYS_DIR}/broker-relay-key.json" ]; then
        "${PYTHON[@]}" -c 'import json, sys; print(json.load(open(sys.argv[1]))["kid"])' "${KEYS_DIR}/broker-relay-key.json"
    else
        local stack
        stack="$(basename "${STACK_DIR}")"
        echo "broker.${stack%-aws}.v1"
    fi
}
BROKER_RELAY_KID="${BROKER_RELAY_KID:-$(relay_kid)}"

# Builds SSH_CMD as an array. Building the array directly, rather than word-
# splitting a Terraform output, also keeps an identity path containing a space
# in one element.
# load_ssh_cmd [batch] — SSH_CMD, the command that reaches the stack's VM.
# With "batch", ssh fails instead of asking anything (BatchMode): an operator
# script that runs unattended must never stop at a prompt.
load_ssh_cmd() {
    local host
    host="$(tf_out vm_public_ip)"

    SSH_CMD=(ssh)
    if [ "${1:-}" = batch ]; then
        SSH_CMD+=(-o BatchMode=yes)
    fi

    if [ -n "${FORA_SSH_IDENTITY_FILE:-}" ]; then
        SSH_CMD+=(
            -o IdentitiesOnly=yes
            -i "${FORA_SSH_IDENTITY_FILE}"
        )
    fi

    SSH_CMD+=("ubuntu@${host}")
}

# staging_id <key-file> <tf-output> <label>: the identity id for one smoke
# keypair. Three sources, in trust order:
#   1. The kid inside an existing key file — it is what the signer puts in
#      Signature-Agent, so once the file exists nothing else can be the id.
#   2. The applied stack's hostname output — when minting a NEW key against a
#      stack that already exists, the stack knows exactly which hostname it
#      serves the identity's directory at, so the id is read from it instead
#      of being re-derived from a label that may have drifted from the tfvars.
#   3. <label>.<STAGING_DOMAIN> — the TRUE FIRST RUN only (no key file, no
#      applied stack). This literal must equal the stack's
#      smoke_agent_subdomain / catalog_contributor_subdomain variable
#      DEFAULT: the keys must exist before the first apply (the stack reads
#      the public WBA documents at plan time), so a stack output cannot be
#      the only source, and this label is the seed both sides grow from.
#      require_ids_match_stack verifies the pair after every apply.
# Absent all three, print nothing — the consumer scripts guard and explain.
staging_id() {
    local key_file="$1" tf_output="$2" label="$3" from_stack
    if [ -f "${key_file}" ]; then
        "${PYTHON[@]}" -c 'import json, sys; print(json.load(open(sys.argv[1]))["kid"])' "${key_file}"
        return
    fi
    if from_stack="$(tf_out "${tf_output}" 2>/dev/null)" && [ -n "${from_stack}" ]; then
        echo "${from_stack}"
        return
    fi
    if [ -n "${STAGING_DOMAIN:-}" ]; then
        echo "${label}.${STAGING_DOMAIN}"
    fi
}

# The two smoke identity ids (see staging_id above for the source order and
# why the first-run labels are literals here).
AGENT_ID="${AGENT_ID:-$(staging_id "${KEYS_DIR}/agent-key.json" smoke_agent_hostname "smoke-agent")}"
CONTRIBUTOR_ID="${CONTRIBUTOR_ID:-$(staging_id "${KEYS_DIR}/contributor-key.json" catalog_contributor_hostname "catalog-contributor")}"

# require_ids_match_stack: fail loudly when the key files' kids do not equal the
# hostnames the applied stack actually serves the directories at. Without this,
# a STAGING_DOMAIN typo at key-generation time (or a changed subdomain label in
# tfvars) surfaces only as an unexplained 401 at the signature gate. Called by
# seed-staging.sh and smoke.sh after the stack is applied.
require_ids_match_stack() {
    local agent_host contributor_host
    agent_host="$(tf_out smoke_agent_hostname 2>/dev/null || true)"
    contributor_host="$(tf_out catalog_contributor_hostname 2>/dev/null || true)"
    if [ -z "${agent_host}" ] || [ -z "${contributor_host}" ]; then
        echo "the stack exports no smoke identity hostname outputs — it was applied before the smoke key directories moved behind Caddy." >&2
        echo "Re-apply the stack (terraform -chdir=${STACK_DIR} apply), then re-run." >&2
        return 1
    fi
    if [ "${AGENT_ID}" != "${agent_host}" ] || [ "${CONTRIBUTOR_ID}" != "${contributor_host}" ]; then
        echo "smoke identity ids do not match the stack's directory hostnames:" >&2
        echo "  agent-key.json kid:       ${AGENT_ID:-<missing — run gen-keys.sh>}" >&2
        echo "  stack serves agent at:    ${agent_host}" >&2
        echo "  contributor-key.json kid: ${CONTRIBUTOR_ID:-<missing — run gen-keys.sh>}" >&2
        echo "  stack serves contrib at:  ${contributor_host}" >&2
        echo "The services fetch each signer's key from https://<kid>/.well-known/..., so a mismatch is a guaranteed 401." >&2
        echo "Regenerate the smoke keys with STAGING_DOMAIN equal to the stack tfvars 'domain' (delete the two key files first), or fix the stack's subdomain variables." >&2
        return 1
    fi
}

VM_COMPOSE_FILE="/opt/fora/docker-compose.yml"
VM_COMPOSE_PROJECT="fora"

# Execute operator SQL using the installed scoped RDS client, or the local
# database of self-hosted stacks. Preserve stdin and quote every psql argument.
remote_psql() {
    local database="${1:-}" service=postgres user=fora args
    case "${database}" in
        fora|sor|identity|console) ;;
        zitadel) service=zitadel-db; user=zitadel ;;
        *) echo "unsupported database: ${database}" >&2; return 2 ;;
    esac
    shift
    args=""
    [ "$#" -eq 0 ] || args="$(printf '%q ' "$@")"
    "${SSH_CMD[@]}" "if sudo test -f /opt/fora/database/connection.json; then
        sudo /usr/local/bin/fora-db $(printf '%q' "${database}") ${args}
    else
        sudo docker compose -f $(printf '%q' "${VM_COMPOSE_FILE}") exec -T ${service} psql -X -v ON_ERROR_STOP=1 -U ${user} -d $(printf '%q' "${database}") ${args}
    fi"
}

# installed_console_issuer: the OIDC issuer the console in the VM's installed
# Compose file trusts, or nothing when that file defines no console. A failed
# read fails the caller: what it may change depends on the answer. The value
# is picked out on the VM, so only it crosses SSH: the rendered file holds
# every secret of the deployment. The VM's python3 does the picking, because
# every Ubuntu host has it and not every host has jq.
installed_console_issuer() {
    local select='import json, sys; services = json.load(sys.stdin).get("services") or {}; issuer = ((services.get("console") or {}).get("environment") or {}).get("CONSOLE_OIDC_ISSUER"); issuer and print(issuer)'
    "${SSH_CMD[@]}" "set -o pipefail; sudo docker compose -f $(printf '%q' "${VM_COMPOSE_FILE}") config --format json | python3 -c $(printf '%q' "${select}")" || {
        echo "could not read ${VM_COMPOSE_FILE} on the VM" >&2
        return 1
    }
}
