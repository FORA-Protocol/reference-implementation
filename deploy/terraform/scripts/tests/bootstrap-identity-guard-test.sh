#!/usr/bin/env bash
# Prove what bootstrap-identity.sh does to each Zitadel instance, what it hands
# the provisioning containers, how secrets reach the VM, and what it recreates.
# The REAL script runs with `terraform`, `ssh` and `curl` stubbed on PATH; the
# ssh stub plays the VM, keeping its files under a scratch directory, and
# records every remote command and every value sent on its standard input.
# The test checks:
#   * a stack without the console runs the developers' instance only, removes
#     its leftover console client, and recreates the Identity Service only when
#     its client's files changed; ROTATE=developers mints a new secret;
#   * a console stack also runs the publishers' instance through
#     publishers.sh, with one name per input and the wait forwarded: without a
#     token on the VM, a System API token signed with the operator's key, and
#     the admin password, arrive on stdin as root-only files that are deleted
#     afterwards, also when the container fails or the run stops before it
#     starts, and never appear in a command line; with a token, no key is
#     needed and none is read;
#   * only the tracked files of deploy/zitadel/bootstrap are uploaded: an
#     untracked file or a private key next to them stays on this machine;
#   * the console's directory is root-owned with group 65532, 0750 and 0640,
#     the console is recreated only when its files changed and the installed
#     bundle has a console, and ROTATE=publishers touches only that instance;
#   * refused before any change: FORCE, an unknown ROTATE, ROTATE=publishers
#     on a stack without that instance, console_url without console_login_url,
#     an installed console on another issuer, and a missing System API key when
#     one is needed;
#   * alice's password is printed only by the run that creates alice.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

SCRIPTS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "${SCRIPTS_DIR}/../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "bootstrap-identity guard test FAILED: $1" >&2; exit 1; }
. "$(dirname "${BASH_SOURCE[0]}")/lib/guard.sh"

mkdir -p "${work}/bin" "${work}/stack/keys"
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "${work}/stack/keys/zitadel-system-api-private.pem" 2>/dev/null
openssl pkey -in "${work}/stack/keys/zitadel-system-api-private.pem" -pubout -out "${work}/system-api-public.pem"
ADMIN_PASSWORD="Admin-Secret-Pass-1!"

# terraform -chdir=<stack> output -json | output -raw <name>. STUB_CONSOLE=1
# adds the console's three outputs; STUB_CONSOLE=url-only adds console_url
# alone, as a stack applied before the publishers' instance existed.
cat > "${work}/bin/terraform" <<EOF
#!/usr/bin/env bash
shift
echo "\$*" >> "\${STUB_DIR}/terraform.log"
case "\$*" in
    "output -raw vm_public_ip") echo 198.51.100.10 ;;
    "output -raw identity_url") echo https://id.example ;;
    "output -raw console_login_admin_password") printf '%s' '${ADMIN_PASSWORD}' ;;
    "output -json")
        console=""
        case "\${STUB_CONSOLE-}" in
            1) console=',"console_url":{"value":"https://console.example"},"console_login_url":{"value":"https://login.console.example"},"console_login_admin_password":{"sensitive":true,"value":"${ADMIN_PASSWORD}"}' ;;
            url-only) console=',"console_url":{"value":"https://console.example"}' ;;
        esac
        mcp=""
        if [ -n "\${STUB_MCP_URL-}" ]; then mcp=",\"mcp_url\":{\"value\":\"\${STUB_MCP_URL}\"}"; fi
        printf '{"identity_url":{"value":"https://id.example"},"zitadel_url":{"value":"https://login.example"}%s%s}\n' "\${mcp}" "\${console}" ;;
    *) exit 1 ;;
esac
EOF
# The VM. /bootstrap is the shared volume and /opt/fora the host directory,
# both under ${STUB_DIR}/vm. The provisioning containers write their client's
# id and secret, keeping a secret that exists unless ROTATE_CLIENT_SECRET=1;
# the publishers' one also writes the instance's token, and needs either that
# token or a System API token to start. STUB_INSTALLED_CONSOLE is the issuer of
# the console in the installed compose file (empty: none); the VM picks it out
# of the rendered file with the script's own python3 program, which the stub
# runs. The permission step runs against the scratch copy of /opt/fora, with
# chown recorded instead of run, because only root may give a file to root.
# STUB_SECURE_FAIL=1 fails that step, and STUB_PUT_PASSWORD_FAIL=1 the upload
# of the admin's password, to stop a run after its secrets are on the VM.
cat > "${work}/bin/ssh" <<'EOF'
#!/usr/bin/env bash
cmd="$(printf '%s' "${!#}" | tr -s ' ')"
printf '%s\n' "${cmd}" >> "${STUB_DIR}/ssh.log"
vm="${STUB_DIR}/vm"
pub="${vm}/opt/fora/zitadel-publishers"
digest() { cat "$@" 2>/dev/null | sha256sum; }
# client DIR NAME — the files a provisioning container writes for one client.
client() {
    mkdir -p "$1"
    echo "$2-client" > "$1/$2_client_id"
    if [ ! -f "$1/$2_client_secret" ] || [[ "${cmd}" == *"ROTATE_CLIENT_SECRET=1"* ]]; then
        echo "$2-secret-${RANDOM}${RANDOM}" > "$1/$2_client_secret"
    fi
    chmod 0644 "$1/$2_client_id" "$1/$2_client_secret"
}
case "${cmd}" in
    "set -o pipefail; sudo docker compose -f /opt/fora/docker-compose.yml config --format json | python3 -c "*)
        if [ -n "${STUB_INSTALLED_CONSOLE:-}" ]; then
            config="$(printf '{"services":{"console":{"environment":{"CONSOLE_OIDC_ISSUER":"%s"}}}}' "${STUB_INSTALLED_CONSOLE}")"
        else
            config='{"services":{"identity":{}}}'
        fi
        printf '%s' "${config}" | eval "${cmd#*| }" ;;
    "sudo test -f /opt/fora/zitadel-publishers/bootstrap.pat") [ -f "${pub}/bootstrap.pat" ] ;;
    "sudo rm -rf /opt/fora/zitadel-bootstrap.new "*)
        rm -rf "${STUB_DIR}/uploaded" && mkdir -p "${STUB_DIR}/uploaded" && tar -C "${STUB_DIR}/uploaded" -xf - ;;
    *"-v fora_zitadelbootstrap:/bootstrap curlimages/curl:8.11.1 -c 'cat /bootstrap/identity_client_id"*)
        digest "${vm}/bootstrap/identity_client_id" "${vm}/bootstrap/identity_client_secret" ;;
    "sudo sh -c 'cat /opt/fora/console-oidc/console_client_id"*)
        digest "${vm}/opt/fora/console-oidc/console_client_id" "${vm}/opt/fora/console-oidc/console_client_secret" ;;
    "sudo docker run "*" /zitadel/bootstrap.sh")
        client "${vm}/bootstrap" identity
        if [ "${STUB_ALICE:-0}" = 1 ] && [ ! -f "${vm}/alice" ]; then
            touch "${vm}/alice"
            echo "bootstrap: user alice@acme.local created" >&2
        fi
        echo "export IDENTITY_OIDC_CLIENT_SECRET=on-stdout-only" ;;
    "sudo mkdir -p /opt/fora/zitadel-publishers && sudo chown root:root /opt/fora/zitadel-publishers && sudo chmod 0700 /opt/fora/zitadel-publishers")
        mkdir -p "${pub}" ;;
    "sudo sh -c 'umask 077 && cat > /opt/fora/zitadel-publishers/"*"'")
        name="${cmd##*/}"; name="${name%\'}"
        if [ "${name}" = admin-password ] && [ "${STUB_PUT_PASSWORD_FAIL:-0}" = 1 ]; then cat >/dev/null; exit 1; fi
        tee "${pub}/${name}" >> "${STUB_DIR}/stdin.log"; echo >> "${STUB_DIR}/stdin.log"
        cp "${pub}/${name}" "${STUB_DIR}/received-${name}" ;;
    "sudo docker run "*" /zitadel/publishers.sh; status=\$?; sudo rm -f /opt/fora/zitadel-publishers/system.token /opt/fora/zitadel-publishers/admin-password; exit \${status}")
        status=0
        if [ ! -f "${pub}/bootstrap.pat" ] && [ ! -s "${pub}/system.token" ]; then
            echo "instance: no token and no System API token" >&2; status=1
        elif [ "${STUB_PUBLISHERS_STATUS:-0}" != 0 ]; then
            status="${STUB_PUBLISHERS_STATUS}"
        else
            echo instance-token > "${pub}/bootstrap.pat"
            [ "${STUB_NO_CONSOLE_FILES:-0}" = 1 ] || client "${vm}/opt/fora/console-oidc" console
        fi
        rm -f "${pub}/system.token" "${pub}/admin-password"
        exit "${status}" ;;
    "sudo mkdir -p /opt/fora/console-oidc "*)
        [ "${STUB_SECURE_FAIL:-0}" = 0 ] || exit 1
        sudo() {
            if [ "$1" = chown ]; then echo "$* " | sed "s#${vm}##g" >> "${STUB_DIR}/chown.log"; else "$@"; fi
        }
        eval "${cmd//\/opt\/fora/${vm}/opt/fora}" ;;
    "sudo rm -f /opt/fora/zitadel-publishers/system.token /opt/fora/zitadel-publishers/admin-password")
        rm -f "${pub}/system.token" "${pub}/admin-password" ;;
    "sudo docker compose -f /opt/fora/docker-compose.yml up -d --force-recreate "*) ;;
    *) echo "unexpected remote command: ${cmd}" >&2; exit 1 ;;
esac
EOF
# curl -s -o /dev/null -w '%{http_code}' --max-time 10 <url>: every wait
# succeeds at once; the URL is recorded.
cat > "${work}/bin/curl" <<'EOF'
#!/usr/bin/env bash
echo "${!#}" >> "${STUB_DIR}/curl.log"
printf 200
EOF
chmod +x "${work}/bin"/*

# step LINE — a short name for one recorded remote command.
step() {
    case "$1" in
        *"config --format json | python3 -c "*) echo installed ;;
        "sudo test -f /opt/fora/zitadel-publishers/bootstrap.pat") echo token? ;;
        "sudo rm -rf /opt/fora/zitadel-bootstrap.new "*) echo upload ;;
        *"-c 'cat /bootstrap/identity_client_id"*) echo identity-files ;;
        "sudo sh -c 'cat /opt/fora/console-oidc/"*) echo console-files ;;
        "sudo docker run "*" /zitadel/bootstrap.sh") echo developers ;;
        "sudo mkdir -p /opt/fora/zitadel-publishers "*) echo publishers-dir ;;
        *"cat > /opt/fora/zitadel-publishers/system.token'") echo put-token ;;
        *"cat > /opt/fora/zitadel-publishers/admin-password'") echo put-password ;;
        "sudo docker run "*" /zitadel/publishers.sh; "*) echo publishers ;;
        "sudo mkdir -p /opt/fora/console-oidc "*) echo secure ;;
        "sudo rm -f /opt/fora/zitadel-publishers/system.token "*) echo remove-secrets ;;
        *"--force-recreate identity") echo recreate-identity ;;
        *"--force-recreate console") echo recreate-console ;;
        *) echo "?" ;;
    esac
}
steps() { while IFS= read -r line; do step "${line}"; done < "${work}/ssh.log" | paste -sd' ' -; }
developers_cmd() { grep ' /zitadel/bootstrap.sh$' "${work}/ssh.log" || true; }
publishers_cmd() { grep ' /zitadel/publishers.sh; ' "${work}/ssh.log" || true; }
upload_cmd() { grep '^sudo rm -rf /opt/fora/zitadel-bootstrap.new ' "${work}/ssh.log" || true; }
waits() { paste -sd' ' - < "${work}/curl.log"; }

# run_script [VAR=value ...] — the real script against the stubbed VM, whose
# files persist between runs. Output to out.txt; returns the script's status.
run_script() {
    rm -rf "${work}/ssh.log" "${work}/curl.log" "${work}/chown.log" "${work}/uploaded" "${work}/stdin.log" \
        "${work}/terraform.log" "${work}"/received-*
    touch "${work}/ssh.log" "${work}/curl.log" "${work}/chown.log" "${work}/stdin.log" "${work}/terraform.log"
    env -u FORCE -u ROTATE -u STUB_CONSOLE -u STUB_MCP_URL -u FORA_SSH_IDENTITY_FILE -u GOOGLE_CLIENT_ID -u GOOGLE_CLIENT_SECRET \
        -u ZITADEL_SYSTEM_API_KEY_FILE -u IMPORT_DEMO_USERS -u ALICE_PASSWORD \
        PATH="${work}/bin:${PATH}" STACK_DIR="${work}/stack" STUB_DIR="${work}" "$@" \
        "${SCRIPTS_DIR}/bootstrap-identity.sh" >"${work}/out.txt" 2>&1
}
must_run() { run_script "$@" || fail "run ($*) exited $?: $(cat "${work}/out.txt")"; }
# refused STATUS ALLOWED_STEPS VAR=value... — the run exits STATUS having sent
# exactly ALLOWED_STEPS to the VM, which are reads.
refused() {
    local status="$1" allowed="$2" got=0
    shift 2
    run_script "$@" || got=$?
    [ "${got}" = "${status}" ] || fail "run ($*) exited ${got}, want ${status}: $(cat "${work}/out.txt")"
    [ "$(steps)" = "${allowed}" ] || fail "run ($*) sent [$(steps)] to the VM, want [${allowed}]"
}
reset_vm() { rm -rf "${work}/vm"; mkdir -p "${work}/vm"; }
console=(STUB_CONSOLE=1 STUB_INSTALLED_CONSOLE=https://login.console.example)

# ── a stack without the console ─────────────────────────────────────────────
# A fresh VM: the developers' instance only, its leftover console client
# removed, and the real provisioning directory uploaded whole into a fresh copy
# root alone may enter, with the old single-file upload removed, and mounted
# read-only where the container runs it.
reset_vm
must_run
[ "$(steps)" = "installed upload identity-files developers identity-files recreate-identity" ] \
    || fail "identity-only run: $(steps)"
# The summary names the MCP endpoint agents point a client at: the Identity
# URL on a stack without the mcp_url output (applied before it existed), the
# MCP origin on one that serves the endpoint under a name of its own — the
# Identity URL's /mcp would fail the client's resource check there.
grep -q "MCP endpoint:      https://id.example/mcp" "${work}/out.txt" || fail "the summary does not name the Identity URL's /mcp without an mcp_url output: $(grep 'MCP endpoint' "${work}/out.txt")"
must_run STUB_MCP_URL=https://agents.example
grep -q "MCP endpoint:      https://agents.example/mcp" "${work}/out.txt" || fail "the summary does not name the split MCP origin: $(grep 'MCP endpoint' "${work}/out.txt")"
grep -q "redirect URI https://id.example/callback" "${work}/out.txt" || fail "the split run moved the redirect URI off the Identity URL: $(grep 'redirect URI' "${work}/out.txt")"
reset_vm
must_run
# (The read of the installed console's issuer names the console; it changes nothing.)
if grep -v 'config --format json | python3 -c ' "${work}/ssh.log" | grep -q -e console -e zitadel-publishers; then
    fail "an identity-only run touched the publishers' side: $(cat "${work}/ssh.log")"
fi
# uploaded_matches ROOT — the upload holds exactly the tracked files of ROOT's
# deploy/zitadel/bootstrap, byte for byte.
uploaded_matches() {
    local src="$1/deploy/zitadel/bootstrap" file
    [ "$(cd "${work}/uploaded" && find . -type f | sed 's|^\./||' | sort)" = "$(git -C "${src}" ls-files | sort)" ] \
        || fail "the upload is not the tracked files of ${src}: $(cd "${work}/uploaded" && find . -type f | sort | paste -sd' ' -)"
    while IFS= read -r file; do
        cmp -s "${work}/uploaded/${file}" "${src}/${file}" || fail "uploaded ${file} differs from the source"
    done < <(git -C "${src}" ls-files)
}
uploaded_matches "${REPO_ROOT}"
for part in "sudo chmod 0700 /opt/fora/zitadel-bootstrap.new " \
    "sudo mv /opt/fora/zitadel-bootstrap.new /opt/fora/zitadel-bootstrap " "sudo rm -f /opt/fora/zitadel-bootstrap.sh"; do
    [[ "$(upload_cmd)" == *"${part}"* ]] || fail "the upload lacks '${part}': $(upload_cmd)"
done
for part in " -v /opt/fora/zitadel-bootstrap:/zitadel:ro " " -v fora_zitadelbootstrap:/bootstrap " \
    " -e ZITADEL_INSTANCE_ROLE=developers " " -e REMOVE_CONSOLE_CLIENT=1 " " -e ZITADEL_INSTANCE_HOST=login.example " \
    " -e IDENTITY_AUTH_ISSUER=https://id.example " " -e OUT_DIR=/bootstrap " " -e WAIT_SECONDS=300 "; do
    [[ "$(developers_cmd)" == *"${part}"* ]] || fail "the developers' container lacks '${part}': $(developers_cmd)"
done
[[ "$(developers_cmd)" != *ROTATE_CLIENT_SECRET* ]] || fail "a plain run asked for rotation: $(developers_cmd)"
[ "$(waits)" = "https://login.example/.well-known/openid-configuration https://id.example/healthz" ] \
    || fail "identity-only waits: $(waits)"
if grep -q on-stdout-only "${work}/out.txt"; then fail "the container's exports, which carry the secret, were printed"; fi
if grep -q console_login_admin_password "${work}/terraform.log"; then fail "an identity-only run read the publishers' admin password"; fi

# ...again: the stored secret is kept, so nothing is recreated.
must_run
[ "$(steps)" = "installed upload identity-files developers identity-files" ] || fail "identity-only re-run: $(steps)"
grep -q "the Identity Service's client is unchanged: not recreated" "${work}/out.txt" || fail "the re-run did not say why: $(cat "${work}/out.txt")"

# ...with ROTATE=developers: a new secret, and the Identity Service recreated.
must_run ROTATE=developers
[[ "$(developers_cmd)" == *" -e ROTATE_CLIENT_SECRET=1 "* ]] || fail "ROTATE=developers did not reach the container: $(developers_cmd)"
[ "$(steps)" = "installed upload identity-files developers identity-files recreate-identity" ] || fail "rotated run: $(steps)"

# ...where the installed bundle still runs a console on the developer instance:
# its client stays.
must_run STUB_INSTALLED_CONSOLE=https://login.example
[[ "$(developers_cmd)" == *" -e REMOVE_CONSOLE_CLIENT=0 "* ]] || fail "a live console's client could be removed: $(developers_cmd)"
grep -q "the installed console still trusts https://login.example" "${work}/out.txt" || fail "keeping the client was not explained"

# ── a stack with the console ────────────────────────────────────────────────
# A fresh VM, the installed console already on the publishers' instance: both
# instances. The System API token is signed here with the operator's key and,
# with the admin password, sent on stdin; both are gone afterwards.
reset_vm
must_run "${console[@]}"
[ "$(steps)" = "installed token? upload identity-files developers identity-files recreate-identity publishers-dir put-token put-password secure console-files publishers secure console-files recreate-console" ] \
    || fail "console run: $(steps)"
cmd="$(publishers_cmd)"
for part in " -v /opt/fora/zitadel-publishers:/publishers " " -v /opt/fora/console-oidc:/console-oidc " \
    " -v /opt/fora/zitadel-bootstrap:/zitadel:ro " " -e ZITADEL_SYSTEM_TOKEN_FILE=/publishers/system.token " \
    " -e ZITADEL_INSTANCE_HOST=login.console.example " " -e ZITADEL_PAT_FILE=/publishers/bootstrap.pat " \
    " -e ZITADEL_ISSUER=https://login.console.example " " -e WAIT_SECONDS=300 " \
    " -e CONSOLE_PUBLIC_ORIGIN=https://console.example " " -e CONSOLE_OUT_DIR=/console-oidc " \
    " -e ZITADEL_ADMIN_PASSWORD_FILE=/publishers/admin-password "; do
    [[ "${cmd}" == *"${part}"* ]] || fail "the publishers' container lacks '${part}': ${cmd}"
done
# One name for each input: the instance step and the bootstrap read the same
# variables, and publishers.sh sets the role.
for part in INSTANCE_DOMAIN= " -e PAT_FILE=" ZITADEL_INSTANCE_ROLE=; do
    [[ "${cmd}" != *"${part}"* ]] || fail "the publishers' container got '${part}': ${cmd}"
done
[[ "${cmd}" != *fora_zitadelbootstrap* ]] || fail "the publishers' container got the developer instance's volume: ${cmd}"
token="$(cat "${work}/received-system.token")"
IFS=. read -r header claims signature <<<"${token}"
b64url_decode() { local s; s="$(tr '_-' '/+' <<<"$1")"; case $((${#s} % 4)) in 2) s="${s}==" ;; 3) s="${s}=" ;; esac; openssl base64 -d -A <<<"${s}"; }
printf '%s.%s' "${header}" "${claims}" > "${work}/signed"
b64url_decode "${signature}" > "${work}/signature"
openssl dgst -sha256 -verify "${work}/system-api-public.pem" -signature "${work}/signature" "${work}/signed" >/dev/null \
    || fail "the System API token was not signed with the operator's key"
[ "$(b64url_decode "${claims}" | jq -r .aud)" = https://login.example ] || fail "the System API token's audience: $(b64url_decode "${claims}")"
[ "$(cat "${work}/received-admin-password")" = "${ADMIN_PASSWORD}" ] || fail "the admin password sent: $(cat "${work}/received-admin-password")"
# No secret on any command line, and none left on the VM.
key_body="$(grep -v -- '-----' "${work}/stack/keys/zitadel-system-api-private.pem" | head -n 1)"
for secret in "${token}" "${ADMIN_PASSWORD}" "${key_body}"; do
    if grep -qF -- "${secret}" "${work}/ssh.log"; then fail "a secret appeared in a remote command line"; fi
done
[ ! -e "${work}/vm/opt/fora/zitadel-publishers/system.token" ] && [ ! -e "${work}/vm/opt/fora/zitadel-publishers/admin-password" ] \
    || fail "a secret was left on the VM"
dir="${work}/vm/opt/fora/console-oidc"
[ "$(mode "${dir}")" = 750 ] || fail "console directory mode $(mode "${dir}")"
for f in console_client_id console_client_secret; do
    [ "$(mode "${dir}/${f}")" = 640 ] || fail "${f} mode $(mode "${dir}/${f}")"
done
[ "$(sort -u "${work}/chown.log")" = "chown -R root:65532 /opt/fora/console-oidc " ] || fail "ownership of the console directory: $(cat "${work}/chown.log")"
[ "$(grep -c . "${work}/chown.log")" = 2 ] || fail "the directory was not secured both before and after the write"
[ "$(waits)" = "https://login.example/.well-known/openid-configuration https://id.example/healthz https://login.console.example/.well-known/openid-configuration https://console.example/healthz" ] \
    || fail "console run waits: $(waits)"
grep -q "publishers' Zitadel console: https://login.console.example" "${work}/out.txt" || fail "the summary does not name the publishers' instance"

# ...again: the instance's token is on the VM, so no key is read and no System
# API token is made; nothing changed, so nothing is recreated.
must_run "${console[@]}" ZITADEL_SYSTEM_API_KEY_FILE="${work}/absent.pem"
[ "$(steps)" = "installed token? upload identity-files developers identity-files publishers-dir put-password secure console-files publishers secure console-files" ] \
    || fail "console re-run: $(steps)"
grep -q "the console's client is unchanged: not recreated" "${work}/out.txt" || fail "the console re-run did not say why"

# ...with ROTATE=publishers: only the publishers' client, only the console.
must_run "${console[@]}" ROTATE=publishers
[[ "$(publishers_cmd)" == *" -e ROTATE_CLIENT_SECRET=1 "* ]] || fail "ROTATE=publishers did not reach its container"
[[ "$(developers_cmd)" != *ROTATE_CLIENT_SECRET* ]] || fail "ROTATE=publishers rotated the developers' client"
[ "$(steps)" = "installed token? upload identity-files developers identity-files publishers-dir put-password secure console-files publishers secure console-files recreate-console" ] \
    || fail "publishers rotation: $(steps)"

# A bundle that has no console yet: provisioned, but nothing to recreate.
must_run STUB_CONSOLE=1 STUB_INSTALLED_CONSOLE= ROTATE=all
[ "$(steps)" = "installed token? upload identity-files developers identity-files recreate-identity publishers-dir put-password secure console-files publishers secure console-files" ] \
    || fail "console stack before its rollout: $(steps)"
grep -q "the rollout that adds it starts it on these files" "${work}/out.txt" || fail "the missing console was not explained"

# A failing publishers' run: no restart, and the secrets are gone anyway.
reset_vm
if run_script "${console[@]}" STUB_PUBLISHERS_STATUS=1; then fail "a failed publishers' run succeeded"; fi
[ ! -e "${work}/vm/opt/fora/zitadel-publishers/system.token" ] && [ ! -e "${work}/vm/opt/fora/zitadel-publishers/admin-password" ] \
    || fail "a failed run left a secret on the VM"
if grep -q -- '--force-recreate console' "${work}/ssh.log"; then fail "the console was recreated after a failed run"; fi

# A run that stops after placing the secrets and before the publishers'
# container runs: the script's exit trap is all that deletes them, so the run
# must send that deletion and leave neither file behind. Once when securing
# the console's directory fails, with both secrets placed; once when the
# admin's password cannot be placed, with the System API token already there.
for failure in STUB_SECURE_FAIL=1 STUB_PUT_PASSWORD_FAIL=1; do
    reset_vm
    if run_script "${console[@]}" "${failure}"; then fail "a run with ${failure} succeeded"; fi
    [[ " $(steps) " != *" publishers "* ]] || fail "the publishers' container ran despite ${failure}: $(steps)"
    [[ "$(steps)" == *" remove-secrets" ]] || fail "the run with ${failure} did not delete the secrets on its way out: $(steps)"
    [ ! -e "${work}/vm/opt/fora/zitadel-publishers/system.token" ] && [ ! -e "${work}/vm/opt/fora/zitadel-publishers/admin-password" ] \
        || fail "the run with ${failure} left a secret on the VM"
done

# A run that leaves no console files stops before recreating the console.
reset_vm
if run_script "${console[@]}" STUB_NO_CONSOLE_FILES=1; then fail "missing console files did not fail the run"; fi
grep -q "the console's client files are absent" "${work}/out.txt" || fail "the missing files were not reported: $(cat "${work}/out.txt")"
if grep -q -- '--force-recreate console' "${work}/ssh.log"; then fail "the console was recreated without its files"; fi

# ── refusals, before any change on the VM ──────────────────────────────────
reset_vm
refused 2 "" FORCE=1
grep -q 'FORCE is retired' "${work}/out.txt" || fail "FORCE was not explained"
refused 2 "" ROTATE=everything
refused 2 "" ROTATE=publishers
refused 2 "" STUB_CONSOLE=url-only
refused 2 "installed" STUB_CONSOLE=1 STUB_INSTALLED_CONSOLE=https://login.example
grep -q "Roll out the stack's release first" "${work}/out.txt" || fail "the console on the developer instance was not explained"
refused 2 "installed token?" "${console[@]}" ZITADEL_SYSTEM_API_KEY_FILE="${work}/absent.pem"
grep -q "cannot be read" "${work}/out.txt" || fail "the missing key was not explained"

# ── the upload: tracked files only ──────────────────────────────────────────
# A copy of the scripts in a git checkout of its own, so the test can leave an
# untracked file and a private key in its bootstrap directory without touching
# the real one: neither reaches the VM.
copy="${work}/repo"
mkdir -p "${copy}/deploy/terraform" "${copy}/deploy/zitadel" "${copy}/scripts"
cp -R "${REPO_ROOT}/deploy/terraform/scripts" "${copy}/deploy/terraform/"
cp -R "${REPO_ROOT}/deploy/zitadel/bootstrap" "${copy}/deploy/zitadel/"
cp -R "${REPO_ROOT}/scripts/lib" "${copy}/scripts/"
git -C "${copy}" init -q
git -C "${copy}" add -A
git -C "${copy}" -c user.name=guard -c user.email=guard@example.invalid commit -qm copy
printf 'not for the VM\n' > "${copy}/deploy/zitadel/bootstrap/notes.txt"
cp "${work}/stack/keys/zitadel-system-api-private.pem" "${copy}/deploy/zitadel/bootstrap/system-api-private.pem"
reset_vm
SCRIPTS_DIR="${copy}/deploy/terraform/scripts" must_run
uploaded_matches "${copy}"
[ ! -e "${work}/uploaded/system-api-private.pem" ] && [ ! -e "${work}/uploaded/notes.txt" ] \
    || fail "an untracked file reached the VM: $(ls "${work}/uploaded")"

# ── alice ───────────────────────────────────────────────────────────────────
reset_vm
must_run STUB_ALICE=1 ALICE_PASSWORD=Alice-Pass-1!
grep -q 'local sign-in user: alice@acme.local / Alice-Pass-1!' "${work}/out.txt" || fail "alice's password was not printed by the run that created alice"
must_run STUB_ALICE=1 ALICE_PASSWORD=Alice-Pass-1!
if grep -q 'Alice-Pass-1!' "${work}/out.txt"; then fail "a run that did not create alice printed a password"; fi
grep -q 'already present; their passwords were not changed' "${work}/out.txt" || fail "the re-run did not say why"
must_run IMPORT_DEMO_USERS=0
grep -q 'none imported (IMPORT_DEMO_USERS=0)' "${work}/out.txt" || fail "IMPORT_DEMO_USERS=0 was not reported"

echo "bootstrap-identity guard test passed (developers only without the console; both instances with it; System API token and admin password on stdin, never on a command line, deleted afterwards, also by the exit trap; no key read while the instance's token exists; tracked files only uploaded; console directory and permissions; recreate only on change; ROTATE per instance; refusals before any change; alice's password printed once)"
