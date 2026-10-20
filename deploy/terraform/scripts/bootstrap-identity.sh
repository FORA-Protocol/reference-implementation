#!/usr/bin/env bash
# Identity bootstrap: provisions the Zitadel instances the deployment signs
# people in with, and recreates the services that read their clients.
#
# The bundle runs one Zitadel with two instances, each with its own accounts,
# browser session and sign-in pages:
#
#   developers  the instance Zitadel creates on its first start, at the
#               zitadel_url output. It gets the Identity Service's OIDC client,
#               whose id and secret land in the shared bootstrap volume, where
#               the Identity Service reads them.
#   publishers  only on a stack with the console_login_url output: the
#               instance publishers sign in to the console through. It is
#               created through Zitadel's System API on the first run, and
#               gets the console's OIDC client and an admin, zadmin. The
#               client's id and secret land in /opt/fora/console-oidc, a host
#               directory only the console mounts (root, group 65532 — the
#               console's user — mode 0750, files 0640), because the shared
#               volume also holds the developer instance's admin token.
#
# Every run brings both instances to what deploy/zitadel/bootstrap describes
# and changes nothing that already matches: a client keeps its secret while
# its token endpoint accepts the stored one. The Identity Service and the
# console are recreated only when their client's files changed. ROTATE asks
# for new secrets on purpose.
#
# The publishers' instance keeps its bootstrap token in
# /opt/fora/zitadel-publishers (root, 0700), on the data volume. While the
# token is there nothing else is needed. Without it — the first run, or after
# the token was lost — the run needs the System API private key, which only
# the operators hold (gen-keys.sh writes it, LOCAL ONLY). The key signs a
# five-minute System API token on this machine. That token, and the
# publishers' admin password, reach the VM through SSH's standard input as
# root-only files, which are deleted when the run ends. Neither is ever on a
# command line: not here, not on the VM, and not inside the container, where
# curl gets credentials in a header file and request bodies on its standard
# input.
#
# The developer instance's `fora-console` client, left from before publishers
# had an instance of their own, is removed unless the console in the installed
# compose file still trusts the developer instance.
#
# Refused before any change on the VM (exit 2):
#   - a stack with console_url but no console_login_url (apply it first);
#   - an installed console that trusts another issuer than the stack's
#     publishers' instance. The publishers' run would replace the client files
#     that console signs in with, so roll the stack's release out first: on
#     the release that moves the console, the rollout stops at "console not
#     ready" until this script has run, and a second rollout of the same
#     release then records it;
#   - the publishers' instance has no token on the VM and the System API key
#     cannot be read;
#   - FORCE, which is retired: every run reconciles, and ROTATE names what to
#     rotate.
#
# Why this is an SSH step rather than a container in the bundle:
#
#   * What it runs is the directory deploy/zitadel/bootstrap: the provisioning
#     scripts and the logos and font they upload, about 270 KB and still about
#     160 KB gzipped. Cloud-init user data is capped at 16 KB gzipped for the
#     WHOLE bundle, and the bundle's other files already take most of that, so
#     shipping it in the VM's user data is not an option.
#   * Creating the publishers' instance takes the System API private key,
#     which never leaves the operators' machines.
#
# Prerequisites: stack applied, images pushed, DNS live, and Caddy holding a
# certificate for each Zitadel hostname (this script waits for that).
#
# Env (all optional):
#   STACK_DIR         default deploy/terraform/stacks/staging-aws
#   ROTATE            developers, publishers or all: new client secrets for
#                     that instance's client even though the stored ones work,
#                     and the services that read them recreated
#   ZITADEL_SYSTEM_API_KEY_FILE
#                     the System API private key, read only when the
#                     publishers' instance has no token on the VM (default
#                     ${STACK_DIR}/keys/zitadel-system-api-private.pem)
#   IMPORT_DEMO_USERS 0 skips the demo sign-in users the provisioning creates
#                     by default (alice, bob, carol for developers; alice for
#                     publishers); a production deployment runs with 0.
#                     Default 1, so demo and staging are unchanged.
#   ALICE_PASSWORD    password for the local sign-in users. Generated when
#                     unset, set only when a user is created, and printed only
#                     by the run that creates alice.
#   GOOGLE_CLIENT_ID / GOOGLE_CLIENT_SECRET
#                     enable Google as a federated identity provider for
#                     developers; omitted leaves local sign-in as the only option
#   WAIT_SECONDS      how long to wait for each Zitadel instance, inside the
#                     provisioning containers too, then for each service
#                     that is recreated (default 300 each)
#
# Exit: 0 done; 1 a failed step; 2 a refused input or precondition, before any
# change on the VM.
#
# Usage:
#   deploy/terraform/scripts/bootstrap-identity.sh

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"
. "$(dirname "${BASH_SOURCE[0]}")/lib/mcp-check.sh"

BOOTSTRAP_SRC="${REPO_ROOT}/deploy/zitadel/bootstrap"
# On the VM the directory is replaced whole on every run, so a file deleted
# from the source does not linger. Earlier versions uploaded a single script
# to /opt/fora/zitadel-bootstrap.sh; the upload removes that leftover.
VM_BOOTSTRAP="/opt/fora/zitadel-bootstrap"
VM_BOOTSTRAP_OLD="/opt/fora/zitadel-bootstrap.sh"
BOOTSTRAP_VOLUME="${VM_COMPOSE_PROJECT}_zitadelbootstrap"
COMPOSE_NETWORK="${VM_COMPOSE_PROJECT}_default"
CURL_IMAGE="curlimages/curl:8.11.1"
# The console's own credentials directory on the VM, and the group its image
# runs as (distroless nonroot, uid and gid 65532).
VM_CONSOLE_OIDC="/opt/fora/console-oidc"
CONSOLE_GID=65532
# The publishers' instance's bootstrap token, and the two secrets a run may
# bring along, which are deleted again when it ends.
VM_PUBLISHERS="/opt/fora/zitadel-publishers"
VM_SECRETS="${VM_PUBLISHERS}/system.token ${VM_PUBLISHERS}/admin-password"
WAIT_SECONDS="${WAIT_SECONDS:-300}"
IMPORT_DEMO_USERS="${IMPORT_DEMO_USERS:-1}"
SYSTEM_KEY="${ZITADEL_SYSTEM_API_KEY_FILE:-${STACK_DIR}/keys/zitadel-system-api-private.pem}"

refuse() { echo "bootstrap-identity: $*" >&2; exit 2; }

[ -z "${FORCE:-}" ] || refuse "FORCE is retired: every run reconciles and keeps working secrets. To mint new ones, set ROTATE=developers, publishers or all."
case "${ROTATE:-}" in
    "" | developers | publishers | all) ;;
    *) refuse "ROTATE must be developers, publishers or all, not '${ROTATE}'" ;;
esac
for tool in terraform curl openssl jq git; do
    command -v "${tool}" >/dev/null 2>&1 || refuse "missing: ${tool}"
done
[ -f "${BOOTSTRAP_SRC}/publishers.sh" ] || refuse "missing: ${BOOTSTRAP_SRC}/publishers.sh"

# rotates SIDE — whether ROTATE asks for that instance's secrets.
rotates() { [ "${ROTATE:-}" = all ] || [ "${ROTATE:-}" = "$1" ]; }

# The image ships an entrypoint and a non-root default user. Both have to go:
# the entrypoint would swallow the command, and the bootstrap volume is owned
# by root, so a non-root process could not write the client credentials into
# it. Kept in one place so every docker run below agrees.
CURL_RUN=(--entrypoint sh --user 0)

# A stack without the console has no console outputs at all (tf_value).
IDENTITY_URL="$(tf_value identity_url)"
ZITADEL_URL="$(tf_value zitadel_url)"
CONSOLE_URL="$(tf_value console_url)"
CONSOLE_LOGIN_URL="$(tf_value console_login_url)"
if [ -z "${IDENTITY_URL}" ] || [ -z "${ZITADEL_URL}" ]; then
    refuse "the stack has no identity_url or zitadel_url output: apply it first"
fi
# The origin agents point an MCP client at: the Identity URL unless the stack
# serves the MCP endpoint under a name of its own. Resolved after the check
# above, so its fallback read of identity_url cannot fail first.
MCP_URL="$(mcp_origin)"
if [ -n "${CONSOLE_URL}" ] && [ -z "${CONSOLE_LOGIN_URL}" ]; then
    refuse "the stack has a console but no console_login_url output: apply the stack's current configuration first"
fi
if [ "${ROTATE:-}" = publishers ] && [ -z "${CONSOLE_LOGIN_URL}" ]; then
    refuse "ROTATE=publishers, but this stack has no publishers' instance (no console_login_url output)"
fi
ZITADEL_HOST="${ZITADEL_URL#https://}"
CONSOLE_LOGIN_HOST="${CONSOLE_LOGIN_URL#https://}"

load_ssh_cmd

# What the VM runs now decides what this run may touch.
INSTALLED_CONSOLE_ISSUER="$(installed_console_issuer)" || exit 1
REMOVE_CONSOLE_CLIENT=1
if [ "${INSTALLED_CONSOLE_ISSUER}" = "${ZITADEL_URL}" ]; then
    REMOVE_CONSOLE_CLIENT=0
fi
HAVE_PUBLISHERS_TOKEN=0
if [ -n "${CONSOLE_LOGIN_URL}" ]; then
    if [ -n "${INSTALLED_CONSOLE_ISSUER}" ] && [ "${INSTALLED_CONSOLE_ISSUER}" != "${CONSOLE_LOGIN_URL}" ]; then
        refuse "the installed console trusts ${INSTALLED_CONSOLE_ISSUER}, not the publishers' instance at ${CONSOLE_LOGIN_URL}. Roll out the stack's release first (on the release that moves the console, the rollout stops at 'console not ready' until this script has run), then run this again."
    fi
    if "${SSH_CMD[@]}" "sudo test -f ${VM_PUBLISHERS}/bootstrap.pat"; then
        HAVE_PUBLISHERS_TOKEN=1
    elif [ ! -r "${SYSTEM_KEY}" ]; then
        refuse "the publishers' instance has no bootstrap token on the VM, so the System API is needed, but ${SYSTEM_KEY} cannot be read. Point ZITADEL_SYSTEM_API_KEY_FILE at the private key gen-keys.sh wrote."
    fi
fi

# Wait for a status code, not for connectivity: while Caddy is still issuing
# the certificate curl fails outright, and a bare `curl -f` loop would report
# that as an indistinguishable failure at the end.
wait_for_http() { # wait_for_http <label> <url> <accepted code>
    local label="$1" url="$2" want="$3" waited=0 code
    echo "== waiting for ${label} (${url}) =="
    while [ "${waited}" -lt "${WAIT_SECONDS}" ]; do
        code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "${url}" || true)"
        if [ "${code}" = "${want}" ]; then
            echo "   ${label}: ready (${code}) after ${waited}s"
            return 0
        fi
        sleep 10
        waited=$((waited + 10))
    done
    echo "${label} did not answer ${want} within ${WAIT_SECONDS}s (last: ${code:-no response})" >&2
    echo "check DNS, the Caddy certificate for ${label}, and 'docker compose logs' on the VM" >&2
    exit 1
}

# identity_files — a digest of the Identity Service's client files, read
# through a throwaway container: the volume belongs to Docker, and no service
# in the bundle has a shell to inspect it with. Empty when they are absent.
identity_files() {
    "${SSH_CMD[@]}" "sudo docker run --rm ${CURL_RUN[*]} -v ${BOOTSTRAP_VOLUME}:/bootstrap ${CURL_IMAGE} \
        -c 'cat /bootstrap/identity_client_id /bootstrap/identity_client_secret 2>/dev/null | sha256sum'"
}
# console_files — the same for the console's client files, on the host.
console_files() {
    "${SSH_CMD[@]}" "sudo sh -c 'cat ${VM_CONSOLE_OIDC}/console_client_id ${VM_CONSOLE_OIDC}/console_client_secret 2>/dev/null | sha256sum'"
}

# secure_console_dir — root owns the console's directory and both files, and
# the console's group may read them; nobody else on the VM can. Run before the
# provisioning, so the files are never written into a directory others can
# enter, and after it, because the provisioning container creates them owned by
# root with mode 0644.
secure_console_dir() {
    "${SSH_CMD[@]}" "sudo mkdir -p ${VM_CONSOLE_OIDC} \
        && sudo chown -R root:${CONSOLE_GID} ${VM_CONSOLE_OIDC} \
        && sudo chmod 0750 ${VM_CONSOLE_OIDC} \
        && sudo find ${VM_CONSOLE_OIDC} -type f -exec chmod 0640 {} +"
}

# put_secret PATH — writes standard input to a root-only file on the VM. The
# value travels inside the SSH connection, never on a command line.
put_secret() {
    "${SSH_CMD[@]}" "sudo sh -c 'umask 077 && cat > $1'"
}
# The secrets go even when this script stops halfway; the publishers' run
# deletes them itself too, whatever its outcome.
SECRETS_PLACED=0
remove_secrets() {
    [ "${SECRETS_PLACED}" = 0 ] || "${SSH_CMD[@]}" "sudo rm -f ${VM_SECRETS}" || true
}

# Each provisioning log goes to the terminal as it happens and is kept for the
# summary; the exports on the container's stdout, which carry the secret, go
# nowhere.
RUN_LOG="$(mktemp)"
trap 'rm -f "${RUN_LOG}"; remove_secrets' EXIT
provisioning_log() { tee -a "${RUN_LOG}" >&2; }

# The digest of no file at all: what identity_files and console_files report
# when the provisioning wrote nothing.
NO_FILES="e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

# The OIDC discovery document rather than a Zitadel-specific health path: it
# is the thing the Identity Service itself will fetch, so a 200 here proves
# what the next steps actually depend on — Zitadel serving, DNS resolving, and
# Caddy holding a certificate for the hostname.
wait_for_http "zitadel" "${ZITADEL_URL}/.well-known/openid-configuration" 200

# Zitadel's password policy wants all four character classes; the suffix
# guarantees them whatever the random part happens to contain.
ALICE_PASSWORD="${ALICE_PASSWORD:-$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9')Aa1!}"

echo "== upload the provisioning directory =="
# The directory's tracked files only: in an operator's checkout it can hold
# other files, ignored ones included, and a System API private key left there
# must never reach the VM. A tar stream over the existing ssh connection
# rather than scp: SSH_CMD is a whole command line, and rewriting it into
# scp's argument shape is a parsing job with no upside. It is unpacked beside
# the target and moved into place, so a run that breaks off midway leaves the
# previous copy whole. root owns the copy and only root can enter it.
# COPYFILE_DISABLE keeps macOS tar from adding its ._ metadata files.
git -C "${BOOTSTRAP_SRC}" ls-files -z | COPYFILE_DISABLE=1 tar -C "${BOOTSTRAP_SRC}" --null -T - -cf - | "${SSH_CMD[@]}" \
    "sudo rm -rf ${VM_BOOTSTRAP}.new && sudo mkdir -m 0700 ${VM_BOOTSTRAP}.new \
        && sudo tar -C ${VM_BOOTSTRAP}.new --no-same-owner -xf - \
        && sudo chmod 0700 ${VM_BOOTSTRAP}.new \
        && sudo rm -rf ${VM_BOOTSTRAP} && sudo mv ${VM_BOOTSTRAP}.new ${VM_BOOTSTRAP} \
        && sudo rm -f ${VM_BOOTSTRAP_OLD}"

# ── developers ──────────────────────────────────────────────────────────────
# Provisioning runs INSIDE the compose network, dialling Zitadel directly at
# http://zitadel:8080. It sends the instance's public hostname as the Host
# header because Zitadel routes instances by that, not by the address dialled,
# so bootstrapping does not depend on the public certificate being valid, only
# on Zitadel being up.
DEVELOPERS_ENV=(
    -e "ZITADEL_INSTANCE_ROLE=developers"
    -e "REMOVE_CONSOLE_CLIENT=${REMOVE_CONSOLE_CLIENT}"
    -e "ZITADEL_BASE_URL=http://zitadel:8080"
    -e "ZITADEL_INSTANCE_HOST=${ZITADEL_HOST}"
    -e "ZITADEL_ISSUER=${ZITADEL_URL}"
    -e "WAIT_SECONDS=${WAIT_SECONDS}"
    -e "IDENTITY_AUTH_ISSUER=${IDENTITY_URL}"
    -e "OUT_DIR=/bootstrap"
    -e "ALICE_PASSWORD=${ALICE_PASSWORD}"
    -e "IMPORT_DEMO_USERS=${IMPORT_DEMO_USERS}"
)
if rotates developers; then
    DEVELOPERS_ENV+=(-e "ROTATE_CLIENT_SECRET=1")
fi
if [ -n "${GOOGLE_CLIENT_ID:-}" ] && [ -n "${GOOGLE_CLIENT_SECRET:-}" ]; then
    echo "   (Google federation enabled)"
    DEVELOPERS_ENV+=(
        -e "GOOGLE_CLIENT_ID=${GOOGLE_CLIENT_ID}"
        -e "GOOGLE_CLIENT_SECRET=${GOOGLE_CLIENT_SECRET}"
    )
fi
if [ "${REMOVE_CONSOLE_CLIENT}" = 0 ]; then
    echo "   (the installed console still trusts ${ZITADEL_URL}: its client stays in the developer instance)"
fi

echo "== provision the developer instance (${ZITADEL_URL}; redirect URI ${IDENTITY_URL}/callback) =="
identity_before="$(identity_files)"
# printf %q shell-quotes every element for the remote command line, so a
# password or secret cannot break out of it. stdout carries the exports, with
# the secret; only the log on stderr is shown.
"${SSH_CMD[@]}" \
    "sudo docker run --rm ${CURL_RUN[*]} --network ${COMPOSE_NETWORK} \
        -v ${BOOTSTRAP_VOLUME}:/bootstrap \
        -v ${VM_BOOTSTRAP}:/zitadel:ro \
        $(printf '%q ' "${DEVELOPERS_ENV[@]}") \
        ${CURL_IMAGE} /zitadel/bootstrap.sh" 2>&1 >/dev/null | provisioning_log
identity_after="$(identity_files)"
[ "${identity_after%% *}" != "${NO_FILES}" ] || {
    echo "provisioning finished but the Identity Service's client files are absent — it cannot start" >&2
    exit 1
}

if [ "${identity_after}" != "${identity_before}" ]; then
    echo "== recreate the Identity Service onto its new client =="
    # force-recreate rather than restart: the credentials are read once at
    # startup, and a recreate is the unambiguous way to get a process that has
    # read them.
    "${SSH_CMD[@]}" "sudo docker compose -f ${VM_COMPOSE_FILE} up -d --force-recreate identity" >/dev/null
else
    echo "   the Identity Service's client is unchanged: not recreated"
fi
wait_for_http "identity" "${IDENTITY_URL}/healthz" 200

# ── publishers ──────────────────────────────────────────────────────────────
if [ -n "${CONSOLE_LOGIN_URL}" ]; then
    "${SSH_CMD[@]}" "sudo mkdir -p ${VM_PUBLISHERS} && sudo chown root:root ${VM_PUBLISHERS} && sudo chmod 0700 ${VM_PUBLISHERS}"
    SECRETS_PLACED=1
    if [ "${HAVE_PUBLISHERS_TOKEN}" = 0 ]; then
        echo "== sign a System API token (the publishers' instance has no bootstrap token yet) =="
        sh "${BOOTSTRAP_SRC}/system-token.sh" "${SYSTEM_KEY}" "${ZITADEL_URL}" | put_secret "${VM_PUBLISHERS}/system.token"
    fi
    # The admin's password is used only when the bootstrap creates zadmin.
    tf_out console_login_admin_password | put_secret "${VM_PUBLISHERS}/admin-password"

    PUBLISHERS_ENV=(
        -e "ZITADEL_BASE_URL=http://zitadel:8080"
        -e "ZITADEL_INSTANCE_HOST=${CONSOLE_LOGIN_HOST}"
        -e "ZITADEL_PAT_FILE=/publishers/bootstrap.pat"
        -e "ZITADEL_SYSTEM_TOKEN_FILE=/publishers/system.token"
        -e "ZITADEL_ISSUER=${CONSOLE_LOGIN_URL}"
        -e "WAIT_SECONDS=${WAIT_SECONDS}"
        -e "CONSOLE_PUBLIC_ORIGIN=${CONSOLE_URL}"
        -e "CONSOLE_OUT_DIR=/console-oidc"
        -e "ZITADEL_ADMIN_PASSWORD_FILE=/publishers/admin-password"
        -e "ALICE_PASSWORD=${ALICE_PASSWORD}"
        -e "IMPORT_DEMO_USERS=${IMPORT_DEMO_USERS}"
    )
    if rotates publishers; then
        PUBLISHERS_ENV+=(-e "ROTATE_CLIENT_SECRET=1")
    fi

    echo "== provision the publishers' instance (${CONSOLE_LOGIN_URL}; redirect URI ${CONSOLE_URL}/auth/callback) =="
    secure_console_dir
    console_before="$(console_files)"
    # publishers.sh creates the instance, or checks that the stored token
    # still opens it, then provisions it. The two secrets are deleted whatever
    # the outcome, and the container's status is the command's.
    "${SSH_CMD[@]}" \
        "sudo docker run --rm ${CURL_RUN[*]} --network ${COMPOSE_NETWORK} \
            -v ${VM_PUBLISHERS}:/publishers -v ${VM_CONSOLE_OIDC}:/console-oidc \
            -v ${VM_BOOTSTRAP}:/zitadel:ro \
            $(printf '%q ' "${PUBLISHERS_ENV[@]}") \
            ${CURL_IMAGE} /zitadel/publishers.sh; \
        status=\$?; sudo rm -f ${VM_SECRETS}; exit \${status}" 2>&1 >/dev/null | provisioning_log
    SECRETS_PLACED=0
    secure_console_dir
    console_after="$(console_files)"
    [ "${console_after%% *}" != "${NO_FILES}" ] || {
        echo "provisioning finished but the console's client files are absent from ${VM_CONSOLE_OIDC} — the console cannot start" >&2
        exit 1
    }
    wait_for_http "the publishers' Zitadel instance" "${CONSOLE_LOGIN_URL}/.well-known/openid-configuration" 200

    if [ -z "${INSTALLED_CONSOLE_ISSUER}" ]; then
        echo "   the installed bundle has no console yet: the rollout that adds it starts it on these files"
    elif [ "${console_after}" != "${console_before}" ]; then
        echo "== recreate the console onto its new client =="
        "${SSH_CMD[@]}" "sudo docker compose -f ${VM_COMPOSE_FILE} up -d --force-recreate console" >/dev/null
        wait_for_http "console" "${CONSOLE_URL}/healthz" 200
    else
        echo "   the console's client is unchanged: not recreated"
    fi
fi

echo
echo "identity bootstrap complete."
echo "  MCP endpoint:      ${MCP_URL}/mcp"
echo "  developer sign-up: ${IDENTITY_URL}"
echo "  developers' Zitadel console: ${ZITADEL_URL} (user zadmin — 'terraform -chdir=${STACK_DIR} output -raw zitadel_admin_password')"
if [ -n "${CONSOLE_LOGIN_URL}" ]; then
    echo "  publisher console: ${CONSOLE_URL}"
    echo "  publishers' Zitadel console: ${CONSOLE_LOGIN_URL} (user zadmin — 'terraform -chdir=${STACK_DIR} output -raw console_login_admin_password')"
fi
if [ "${IMPORT_DEMO_USERS}" = "0" ]; then
    echo "  local sign-in users: none imported (IMPORT_DEMO_USERS=0); people self-register"
elif grep -q 'user alice@acme.local created' "${RUN_LOG}"; then
    echo "  local sign-in user: alice@acme.local / ${ALICE_PASSWORD}"
    echo
    echo "The sign-in password above was set because this run created the user;"
    echo "record it now, or reset it later from the Zitadel console."
else
    echo "  local sign-in users: already present; their passwords were not changed"
fi
