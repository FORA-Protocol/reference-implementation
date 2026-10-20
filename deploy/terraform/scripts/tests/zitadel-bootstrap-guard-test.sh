#!/usr/bin/env bash
# Prove what deploy/zitadel/bootstrap/bootstrap.sh sends for each side and
# where it writes the side's credentials. The REAL script runs against the
# shared curl stub (tests/lib/zitadel-curl-stub.sh), which records every
# request and fails the run on a credential or a JSON body on curl's command
# line; this test's routes answer Zitadel's APIs the way v3.4.9 does, with a
# distinct client id and secret per client. The test checks:
#   * the role is required, and a missing or unknown one, or a publishers run
#     without the admin's password, stops before any request;
#   * the instance check comes before any write: the publishers role refuses
#     an instance not named fora-publishers, the developers role refuses that
#     one, and neither writes anything;
#   * developers: only fora-identity, with Development mode from its own
#     addresses; a fora-console client and its files in OUT_DIR are removed,
#     unless REMOVE_CONSOLE_CLIENT=0 (any other value removes them); the three
#     local users; IDENTITY_ exports;
#   * publishers: only fora-console, with its sign-out address; the admin with
#     both roles; alice alone; never Google, even with credentials set; the
#     files in CONSOLE_OUT_DIR; CONSOLE_ exports with the instance's issuer;
#   * an existing client is updated with exactly a create's settings minus
#     name and version, and keeps its stored secret when the files name it and
#     the token endpoint accepts them; it gets a new one when the files are
#     missing or another client's, when the secret is refused, or when
#     ROTATE_CLIENT_SECRET=1; an answer without an OAuth error, or a refused
#     update, stops the run with the files untouched;
#   * an existing admin keeps its password: no update is ever sent for it; the
#     password variable wins over the password file;
#   * every request carries the instance's Host;
#   * publishers.sh checks the instance (instance.sh) before it provisions it,
#     and its --emit-exports sends no request;
#   * --emit-exports prints what the provisioning run printed;
#   * the look (branding.sh) against a stub that keeps what it is sent:
#     English only, each side's colors in both sets, the right mark on each
#     asset (including Instrument Sans for both audiences),
#     one activation, the three page titles, audience-specific email copy;
#     a second run writes none of it, and later runs
#     repair only the asset or message fields that drifted.
#
# The real-Zitadel counterparts are zitadel-bootstrap-reconcile-test.py (the
# developer instance) and zitadel-instances-test.py (the publisher instance).
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SCRIPT="${REPO_ROOT}/deploy/zitadel/bootstrap/bootstrap.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "zitadel bootstrap guard test FAILED: $1" >&2; exit 1; }

mkdir -p "${work}/bin"
ln -s "${REPO_ROOT}/deploy/terraform/scripts/tests/lib/zitadel-curl-stub.sh" "${work}/bin/curl"
# The answers, like Zitadel v3.4.9's, with a distinct client id and secret per
# client. Switches:
#   STUB_INSTANCE     the instance name GET /admin/v1/instances/me reports
#   STUB_APPS_EXIST   1: every client create answers 409 and the search finds it
#   STUB_CONSOLE_APP  present: a fora-console client exists in the instance
#   STUB_UPDATE       ok (default), unchanged or refused: the client update
#   STUB_TOKEN        invalid_request (default; a right secret), invalid_client,
#                     or html (a proxy page with no OAuth error)
#   STUB_USERS_EXIST  1: every user import and membership answers AlreadyExists
#   STUB_LABEL        refused: the label policy update answers 400 Invalid.Argument
#   STUB_MESSAGE      refused: a message text update answers 400 Invalid.Argument
# The look's endpoints keep state under ${STUB_DIR}/state, the way an instance
# does: what was PUT or uploaded is what the next GET returns, an unchanged
# label policy update is refused with INSTANCE-28fHe, and the previewed policy
# becomes the active one on activation.
cat > "${work}/routes.sh" <<'EOF'
exists() { answer 409 '{"code":6, "message":"Errors.AlreadyExists (STUB-6)", "details":[{"id":"STUB-6","message":"already exists"}]}'; }
state="${STUB_DIR}/state"
mkdir -p "${state}/assets"
# default_text TYPE — Zitadel v3.4.9's English subject (and title) of one email.
default_text() {
    case "$1" in
        init) echo "Initialize User" ;; verifyemail) echo "Verify email" ;;
        passwordreset) echo "Reset password" ;; password_change) echo "Password of user has changed" ;;
    esac
}
# preview — the previewed label policy: the colors last PUT and one URL per
# uploaded asset, named after the asset's bytes as Zitadel names a new object.
preview() {
    local colors="{}" urls="{}" name key file
    [ -f "${state}/label.json" ] && colors="$(jq -c 'del(.hideLoginNameSuffix, .disableWatermark)' "${state}/label.json")"
    for key in logoUrl:logo logoUrlDark:logo_dark iconUrl:icon iconUrlDark:icon_dark fontUrl:font; do
        file="${state}/assets/${key#*:}"
        [ -f "${file}" ] || continue
        name="$(sha256sum "${file}" | cut -c1-12)"
        urls="$(jq -c --arg k "${key%%:*}" --arg v "http://zitadel/assets/${key#*:}-${name}" '. + {($k): $v}' <<<"${urls}")"
    done
    jq -cn --argjson c "${colors}" --argjson u "${urls}" '{policy: ({details: {}} + $c + $u)}'
}
route() {
    name="$(printf '%s' "${data}" | jq -r 'try (.name // .queries[0].nameQuery.name // .userName // empty) catch empty' 2>/dev/null || true)"
    case "${path}" in
        /management/v1/projects/p1/apps/app-*) name="${path#/management/v1/projects/p1/apps/app-}"; name="${name%%/*}" ;;
    esac
    case "${method} ${path}" in
        "GET /management/v1/orgs/me") answer 200 '{"org":{"id":"org-1"}}' ;;
        "GET /admin/v1/instances/me")
            answer 200 "{\"instance\":{\"id\":\"inst-1\",\"details\":{\"sequence\":\"1\"},\"name\":\"${STUB_INSTANCE:-fora-poc}\"}}" ;;
        "POST /management/v1/projects/p1/apps/oidc")
            if [ "${STUB_APPS_EXIST:-0}" = 1 ]; then exists
            else answer 200 "{\"appId\":\"app-${name}\", \"clientId\":\"${name}-client\", \"clientSecret\":\"${name}-secret\"}"; fi ;;
        "POST /management/v1/projects/p1/apps/_search")
            if [ "${name}" = fora-identity ] || [ "${STUB_APPS_EXIST:-0}" = 1 ] || [ "${STUB_CONSOLE_APP:-}" = present ]; then
                answer 200 "{\"details\":{\"totalResult\":\"1\"}, \"result\":[{\"id\":\"app-${name}\", \"name\":\"${name}\", \"oidcConfig\":{\"clientId\":\"${name}-client\"}}]}"
            else
                answer 200 '{"details":{"totalResult":"0"}}'
            fi ;;
        "PUT /management/v1/projects/p1/apps/app-"*"/oidc_config")
            case "${STUB_UPDATE:-ok}" in
                ok) answer 200 '{"details":{"sequence":"9"}}' ;;
                unchanged) answer 400 '{"code":9, "message":"No changes (COMMAND-1m88i)", "details":[{"id":"COMMAND-1m88i", "message":"No changes"}]}' ;;
                refused) answer 400 '{"code":3, "message":"Errors.Invalid.Argument (STUB-1)"}' ;;
            esac ;;
        "POST /management/v1/projects/p1/apps/app-"*"/oidc_config/_generate_client_secret")
            answer 200 "{\"clientSecret\":\"${name}-regenerated\"}" ;;
        "DELETE /management/v1/projects/p1/apps/app-"*) answer 200 '{"details":{"sequence":"11"}}' ;;
        "POST /oauth/v2/token")
            case "${STUB_TOKEN:-invalid_request}" in
                invalid_client) answer 401 '{"error":"invalid_client","error_description":"client authentication failed"}' ;;
                html) answer 502 '<html><body>Bad Gateway</body></html>' ;;
                *) answer 400 '{"error":"invalid_request","error_description":"invalid code"}' ;;
            esac ;;
        "POST /management/v1/users/human/_import")
            if [ "${STUB_USERS_EXIST:-0}" = 1 ]; then exists; else answer 200 "{\"userId\":\"u-${name%%@*}\"}"; fi ;;
        "POST /management/v1/users/_search") answer 200 '{"details":{"totalResult":"1"},"result":[{"id":"u-zadmin","userName":"zadmin"}]}' ;;
        "POST /admin/v1/members" | "POST /management/v1/orgs/me/members")
            if [ "${STUB_USERS_EXIST:-0}" = 1 ]; then exists; else answer 200 '{"details":{"sequence":"12"}}'; fi ;;
        "POST /management/v1/idps/templates/_search") answer 200 '{"details":{"totalResult":"0"}}' ;;
        "POST /management/v1/idps/google") answer 200 '{"id":"idp-1"}' ;;
        "GET /admin/v1/restrictions")
            if [ -f "${state}/restrictions" ]; then answer 200 '{"details":{},"allowedLanguages":["en"]}'
            else answer 200 '{"details":{}}'; fi ;;
        "PUT /admin/v1/restrictions") touch "${state}/restrictions"; answer 200 '{"details":{}}' ;;
        "GET /admin/v1/languages/default") answer 200 "{\"language\":\"$(cat "${state}/language" 2>/dev/null || echo en)\"}" ;;
        "PUT /admin/v1/languages/default/"*) echo "${path##*/}" > "${state}/language"; answer 200 '{"details":{}}' ;;
        "PUT /admin/v1/policies/label")
            if [ "${STUB_LABEL:-}" = refused ]; then
                answer 400 '{"code":3, "message":"Errors.Invalid.Argument (STUB-1)"}'
            elif [ "$(jq -cS . <<<"${data}")" = "$(cat "${state}/label.json" 2>/dev/null)" ]; then
                answer 400 '{"code":9, "message":"Default Private Label Policy has not been changed (INSTANCE-28fHe)", "details":[{"id":"INSTANCE-28fHe"}]}'
            else jq -cS . <<<"${data}" > "${state}/label.json"; answer 200 '{"details":{}}'; fi ;;
        "GET /admin/v1/policies/label/_preview") answer 200 "$(preview)" ;;
        "GET /admin/v1/policies/label") answer 200 "$(cat "${state}/active.json" 2>/dev/null || echo '{"policy":{"details":{}}}')" ;;
        "POST /admin/v1/policies/label/_activate") preview > "${state}/active.json"; answer 200 '{"details":{}}' ;;
        "DELETE /admin/v1/policies/label/font") rm -f "${state}/assets/font"; answer 200 '{"details":{}}' ;;
        "GET /assets/v1/instance/policy/label/"*"/_preview")
            asset="${path#/assets/v1/instance/policy/label/}"; asset="${asset%/_preview}"
            if [ -f "${state}/assets/${asset//\//_}" ]; then answer_file 200 "${state}/assets/${asset//\//_}"
            else answer 404 '{"code":5,"message":"not found"}'; fi ;;
        "POST /assets/v1/instance/policy/label/"*)
            asset="${path#/assets/v1/instance/policy/label/}"
            cp "${form}" "${state}/assets/${asset//\//_}"; answer 200 '' ;;
        "GET /admin/v1/text/login/en")
            answer 200 "{\"customText\":$(cat "${state}/text-login.json" 2>/dev/null || echo '{"isDefault":true}')}" ;;
        "PUT /admin/v1/text/login/en") jq -c . <<<"${data}" > "${state}/text-login.json"; answer 200 '{"details":{}}' ;;
        "GET /admin/v1/text/default/message/"*"/en" | "GET /admin/v1/text/deafult/message/passwordreset/en")
            type="${path%/en}"; type="${type##*/}"
            case "${path}" in */default/message/passwordreset/*) answer 404 '{"code":5,"message":"Not Found"}'; return ;; esac
            answer 200 "{\"customText\":{\"details\":{},\"title\":\"$(default_text "${type}")\",\"subject\":\"$(default_text "${type}")\",\"isDefault\":true}}" ;;
        "GET /admin/v1/text/message/"*"/en")
            type="${path%/en}"; type="${type##*/}"
            answer 200 "{\"customText\":$(cat "${state}/text-${type}.json" 2>/dev/null || echo "{\"title\":\"$(default_text "${type}")\",\"subject\":\"$(default_text "${type}")\",\"isDefault\":true}")}" ;;
        "PUT /admin/v1/text/message/"*"/en")
            type="${path%/en}"; type="${type##*/}"
            if [ "${STUB_MESSAGE:-}" = refused ]; then
                answer 400 '{"code":3,"message":"Errors.Invalid.Argument (STUB-1)"}'; return
            fi
            jq -c . <<<"${data}" > "${state}/text-${type}.json"; answer 200 '{"details":{}}' ;;
        *) answer 200 '{"id":"p1"}' ;;
    esac
}
EOF

# run_bootstrap [VAR=value ...] — the real script with a fresh request log;
# OUT_DIR and CONSOLE_OUT_DIR persist between runs (reset_dirs clears them).
# stdout to out.txt, stderr to err.txt. Returns its status.
run_bootstrap() {
    rm -f "${work}/requests.json" "${work}/out.txt" "${work}/err.txt"
    touch "${work}/requests.json"
    env -u CONSOLE_OUT_DIR -u EXTRA_REDIRECT_URIS -u EXTRA_CONSOLE_REDIRECT_URIS -u ZITADEL_INSTANCE_ROLE \
        -u ROTATE_CLIENT_SECRET -u ZITADEL_ADMIN_PASSWORD -u ZITADEL_ADMIN_PASSWORD_FILE -u GOOGLE_CLIENT_ID \
        -u GOOGLE_CLIENT_SECRET -u REMOVE_CONSOLE_CLIENT \
        PATH="${work}/bin:${PATH}" STUB_DIR="${work}" STUB_ROUTES="${work}/routes.sh" ZITADEL_PAT=stub-pat OUT_DIR="${work}/out" \
        CONSOLE_OUT_DIR="${work}/console" ZITADEL_ISSUER=http://zitadel:8080 IMPORT_DEMO_USERS=0 "$@" \
        "${SCRIPT}" >"${work}/out.txt" 2>"${work}/err.txt"
}
must_run() { run_bootstrap "$@" || fail "bootstrap exited $? ($*): $(cat "${work}/err.txt")"; }
# must_exit CODE [VAR=value ...] — the run fails with exactly CODE.
must_exit() {
    local want="$1" status=0
    shift
    run_bootstrap "$@" || status=$?
    [ "${status}" = "${want}" ] || fail "($*) exited ${status}, want ${want}: $(cat "${work}/err.txt")"
}
reset_dirs() { rm -rf "${work}/out" "${work}/console"; }
reset_state() { rm -rf "${work}/state"; }
# Keep drifted fixtures compact like Zitadel's responses, so only the changed field differs.
put_state() { # FILE JQ_ARGS...
    local file="$1"
    shift
    jq -cS "$@" "${file}" > "${work}/next-state.json"
    mv -f "${work}/next-state.json" "${file}"
}
# branding_writes — every accepted request that changes the instance's look or
# language. An update Zitadel refuses as unchanged changes nothing.
branding_writes() {
    requests 'map(select(.method != "GET" and (.status | startswith("2")) and (.path | test("^/admin/v1/(restrictions|languages|policies/label|text/)|^/assets/"))) | .method + " " + .path)'
}
# requests FILTER — a jq filter over the array of recorded requests.
requests() { jq -cs "$1" "${work}/requests.json"; }
writes() { requests 'map(select(.method != "GET" and .path != "/oauth/v2/token" and (.path | test("_search$") | not)))'; }
create_body() { requests "map(select(.method == \"POST\" and .path == \"/management/v1/projects/p1/apps/oidc\" and .body.name == \"$1\"))[0].body"; }
update_body() { requests "map(select(.method == \"PUT\" and .path == \"/management/v1/projects/p1/apps/app-$1/oidc_config\"))[0].body"; }
calls_to() { requests "map(select(.path | test(\"$1\"))) | length"; }
expect_file() {
    [ -f "$1" ] || fail "$1 was not written"
    [ "$(cat "$1")" = "$2" ] || fail "$1 holds '$(cat "$1")', expected '$2'"
}
logged() { grep -q -- "$1" "${work}/err.txt" || fail "the run did not log '$1': $(cat "${work}/err.txt")"; }
# The settings both clients share, as the create sends them; only the name,
# the addresses and Development mode differ.
app_body() { # NAME REDIRECTS_JSON POST_LOGOUT_JSON DEV_MODE
    jq -cn --arg name "$1" --argjson redirects "$2" --argjson logout "$3" --argjson dev "$4" '{
        name: $name, version: "OIDC_VERSION_1_0", redirectUris: $redirects,
        responseTypes: ["OIDC_RESPONSE_TYPE_CODE"],
        grantTypes: ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"],
        appType: "OIDC_APP_TYPE_WEB", authMethodType: "OIDC_AUTH_METHOD_TYPE_BASIC",
        postLogoutRedirectUris: $logout, devMode: $dev, accessTokenType: "OIDC_TOKEN_TYPE_JWT",
        accessTokenRoleAssertion: true, idTokenRoleAssertion: true,
        idTokenUserinfoAssertion: true, clockSkew: "0s"}'
}
dev=(ZITADEL_INSTANCE_ROLE=developers IDENTITY_AUTH_ISSUER=https://id.example)
pub=(ZITADEL_INSTANCE_ROLE=publishers STUB_INSTANCE=fora-publishers CONSOLE_PUBLIC_ORIGIN=https://console.example
    ZITADEL_ADMIN_PASSWORD=Admin-Passw0rd! ZITADEL_ISSUER=https://login.console.example)

# ── the role, the admin's password and the instance come first ────────────────

must_exit 2
[ "$(requests 'length')" = 0 ] || fail "a run without a role sent requests: $(requests '.')"
must_exit 2 ZITADEL_INSTANCE_ROLE=both
[ "$(requests 'length')" = 0 ] || fail "a run with an unknown role sent requests"
must_exit 2 ZITADEL_INSTANCE_ROLE=publishers STUB_INSTANCE=fora-publishers CONSOLE_PUBLIC_ORIGIN=https://console.example
logged "needs the admin's password"
[ "$(requests 'length')" = 0 ] || fail "a publishers run without the admin's password sent requests"
must_exit 2 "${pub[@]}" ZITADEL_ADMIN_PASSWORD='bad"password'
[ "$(requests 'length')" = 0 ] || fail "a password with a quote reached Zitadel"
must_exit 2 "${pub[@]}" STUB_INSTANCE=fora-poc
logged "needs the instance 'fora-publishers'"
[ "$(writes)" = "[]" ] || fail "the publishers role wrote to another instance: $(writes)"
must_exit 2 "${dev[@]}" STUB_INSTANCE=fora-publishers
logged "pointed at the publisher instance"
[ "$(writes)" = "[]" ] || fail "the developers role wrote to the publisher instance: $(writes)"

# ── developers ────────────────────────────────────────────────────────────────

# The e2e inputs: plain-http service name, so Development mode on. Only the
# Identity Service's client; no console client in the instance, nothing to
# remove; the IDENTITY_ exports only.
reset_dirs
must_run ZITADEL_INSTANCE_ROLE=developers IDENTITY_AUTH_ISSUER=http://identity
[ "$(create_body fora-identity)" = "$(app_body fora-identity '["http://identity/callback"]' '[]' true)" ] \
    || fail "e2e identity create body: $(create_body fora-identity)"
[ "$(create_body fora-console)" = null ] || fail "the developers role created the console's client"
[ "$(calls_to '^/management/v1/projects/p1/apps/app-')" = 0 ] || fail "a fresh developers run touched an existing client"
logged "no fora-console app in this instance"
expect_file "${work}/out/identity_client_id" fora-identity-client
expect_file "${work}/out/identity_client_secret" fora-identity-secret
[ ! -e "${work}/console" ] || fail "the developers role wrote into the console's directory"
[ "$(cat "${work}/out.txt")" = "export IDENTITY_OIDC_ISSUER=http://zitadel:8080
export IDENTITY_OIDC_CLIENT_ID=fora-identity-client
export IDENTITY_OIDC_CLIENT_SECRET=fora-identity-secret
export IDENTITY_OIDC_SCOPES=\"openid profile email\"" ] || fail "developers exports: $(cat "${work}/out.txt")"
emitted="$(env PATH="${work}/bin:${PATH}" ZITADEL_INSTANCE_ROLE=developers OUT_DIR="${work}/out" \
    ZITADEL_ISSUER=http://zitadel:8080 "${SCRIPT}" --emit-exports)"
[ "${emitted}" = "$(cat "${work}/out.txt")" ] || fail "--emit-exports printed: ${emitted}"
# The instance check is the first thing after the readiness wait.
[ "$(requests '[.[] | .method + " " + .path][0:2]')" = '["GET /management/v1/orgs/me","GET /admin/v1/instances/me"]' ] \
    || fail "the instance was not checked before anything else: $(requests '[.[] | .method + " " + .path]')"

# https only: Development mode off; one plain-http extra redirect turns it on.
must_run "${dev[@]}"
[ "$(create_body fora-identity | jq '.devMode')" = false ] || fail "https identity client: $(create_body fora-identity)"
must_run "${dev[@]}" EXTRA_REDIRECT_URIS=http://localhost:8083/callback
[ "$(create_body fora-identity | jq -c '[.redirectUris, .devMode]')" = '[["https://id.example/callback","http://localhost:8083/callback"],true]' ] \
    || fail "identity with a plain-http extra redirect: $(create_body fora-identity)"

# A console client left in the instance, with its files in OUT_DIR, from the
# time both clients shared it: removed, unless REMOVE_CONSOLE_CLIENT=0.
printf 'old-console\n' > "${work}/out/console_client_id"; printf 'old-secret\n' > "${work}/out/console_client_secret"
must_run "${dev[@]}" STUB_CONSOLE_APP=present REMOVE_CONSOLE_CLIENT=0
[ "$(calls_to '^/management/v1/projects/p1/apps/app-fora-console')" = 0 ] || fail "REMOVE_CONSOLE_CLIENT=0 still removed the client"
[ -e "${work}/out/console_client_id" ] || fail "REMOVE_CONSOLE_CLIENT=0 still removed the files"
logged "SKIP removing fora-console"
must_run "${dev[@]}" STUB_CONSOLE_APP=present
[ "$(requests 'map(select(.method == "DELETE" and (.path | startswith("/management/")))) | map(.path)')" = '["/management/v1/projects/p1/apps/app-fora-console"]' ] \
    || fail "the console's client was not removed: $(requests 'map(select(.method == "DELETE"))')"
# Only 0 keeps it, as only 0 turns IMPORT_DEMO_USERS off.
must_run "${dev[@]}" STUB_CONSOLE_APP=present REMOVE_CONSOLE_CLIENT=yes
[ "$(calls_to '^/management/v1/projects/p1/apps/app-fora-console$')" = 1 ] || fail "REMOVE_CONSOLE_CLIENT=yes kept the console's client"
[ ! -e "${work}/out/console_client_id" ] && [ ! -e "${work}/out/console_client_secret" ] || fail "the console's old files stayed in OUT_DIR"
logged "app fora-console removed"

# The local users: all three on the developers side.
must_run "${dev[@]}" IMPORT_DEMO_USERS=1
[ "$(requests '[.[] | select(.path == "/management/v1/users/human/_import") | .body.userName]')" = \
    '["alice@acme.local","bob@acme.local","carol@acme.local"]' ] || fail "developers users: $(requests '[.[] | select(.path | test("_import")) | .body.userName]')"

# ── publishers ────────────────────────────────────────────────────────────────

# The console's client only, with its sign-out address; the admin with both
# roles; alice alone; no Google, credentials or not; the CONSOLE_ exports.
reset_dirs
must_run "${pub[@]}" IMPORT_DEMO_USERS=1 GOOGLE_CLIENT_ID=g GOOGLE_CLIENT_SECRET=s
[ "$(create_body fora-console)" = "$(app_body fora-console '["https://console.example/auth/callback"]' '["https://console.example/signed-out"]' false)" ] \
    || fail "publishers console create body: $(create_body fora-console)"
[ "$(create_body fora-identity)" = null ] || fail "the publishers role created the Identity Service's client"
[ "$(calls_to '/idps')" = 0 ] || fail "the publishers role configured Google: $(requests 'map(select(.path | test("/idps")))')"
[ "$(requests '[.[] | select(.path == "/management/v1/users/human/_import") | .body | {userName, email: .email.email, password, verified: .email.isEmailVerified}]')" = \
    '[{"userName":"zadmin","email":"zadmin@localhost","password":"Admin-Passw0rd!","verified":true},{"userName":"alice@acme.local","email":"alice@acme.local","password":"Alice12345!","verified":true}]' ] \
    || fail "publishers users: $(requests '[.[] | select(.path | test("_import")) | .body]')"
[ "$(requests '[.[] | select(.path | test("members$")) | {path, body}]')" = \
    '[{"path":"/admin/v1/members","body":{"userId":"u-zadmin","roles":["IAM_OWNER"]}},{"path":"/management/v1/orgs/me/members","body":{"userId":"u-zadmin","roles":["ORG_OWNER"]}}]' ] \
    || fail "admin memberships: $(requests '[.[] | select(.path | test("members$"))]')"
expect_file "${work}/console/console_client_id" fora-console-client
expect_file "${work}/console/console_client_secret" fora-console-secret
[ ! -e "${work}/out" ] || fail "the publishers role wrote into OUT_DIR"
[ "$(cat "${work}/out.txt")" = "export CONSOLE_OIDC_ISSUER=https://login.console.example
export CONSOLE_OIDC_CLIENT_ID=fora-console-client
export CONSOLE_OIDC_CLIENT_SECRET=fora-console-secret
export CONSOLE_OIDC_SCOPES=\"openid profile email\"
export CONSOLE_OIDC_REDIRECT_URL=https://console.example/auth/callback" ] || fail "publishers exports: $(cat "${work}/out.txt")"
emitted="$(env PATH="${work}/bin:${PATH}" ZITADEL_INSTANCE_ROLE=publishers CONSOLE_OUT_DIR="${work}/console" \
    CONSOLE_PUBLIC_ORIGIN=https://console.example ZITADEL_ISSUER=https://login.console.example "${SCRIPT}" --emit-exports)"
[ "${emitted}" = "$(cat "${work}/out.txt")" ] || fail "publishers --emit-exports printed: ${emitted}"
logged "SKIP google federation — publishers' Google sign-in is configured separately"

# The admin already exists: it is looked up, its memberships are confirmed,
# and no password is sent anywhere but the (refused) import.
must_run "${pub[@]}" STUB_USERS_EXIST=1
logged "user zadmin already exists; its password is left as it is"
[ "$(requests '[.[] | select(.body != null) | select(.body | tostring | test("Admin-Passw0rd!")) | .path]')" = '["/management/v1/users/human/_import"]' ] \
    || fail "the admin's password went somewhere other than the import: $(requests '.')"

# A password file instead of the variable; when both are set, the variable
# wins, as ZITADEL_PAT does over its file.
printf 'From-A-File-1!\n' > "${work}/admin.password"
must_run ZITADEL_INSTANCE_ROLE=publishers STUB_INSTANCE=fora-publishers CONSOLE_PUBLIC_ORIGIN=https://console.example \
    ZITADEL_ADMIN_PASSWORD_FILE="${work}/admin.password"
[ "$(requests '[.[] | select(.path == "/management/v1/users/human/_import") | .body.password]')" = '["From-A-File-1!"]' ] \
    || fail "the password file was not used: $(requests '[.[] | select(.path | test("_import")) | .body.password]')"
must_run "${pub[@]}" ZITADEL_ADMIN_PASSWORD_FILE="${work}/admin.password"
[ "$(requests '[.[] | select(.path == "/management/v1/users/human/_import") | .body.password]')" = '["Admin-Passw0rd!"]' ] \
    || fail "the password file won over the variable: $(requests '[.[] | select(.path | test("_import")) | .body.password]')"

# Every request carries the instance's Host, the token endpoint's included.
must_run "${pub[@]}" ZITADEL_INSTANCE_HOST=login.console.example STUB_APPS_EXIST=1
[ "$(requests '[.[].host] | unique')" = '["login.console.example"]' ] || fail "a request missed the instance's Host: $(requests '[.[] | {path, host}]')"
[ "$(requests '[.[] | select(.path == "/management/v1/users/human/_import") | .body.email.email]')" = '["zadmin@login.console.example"]' ] \
    || fail "the admin's email does not name the instance: $(requests '[.[] | select(.path | test("_import")) | .body.email]')"

# publishers.sh: the instance step, then the bootstrap; --emit-exports runs
# neither the instance step nor any request.
PUBLISHERS="${REPO_ROOT}/deploy/zitadel/bootstrap/publishers.sh"
mkdir -p "${work}/pub-token"; printf 'stub-pat\n' > "${work}/pub-token/bootstrap.pat"
rm -f "${work}/requests.json"; touch "${work}/requests.json"
env -u ZITADEL_PAT -u ZITADEL_INSTANCE_ROLE PATH="${work}/bin:${PATH}" STUB_DIR="${work}" STUB_ROUTES="${work}/routes.sh" \
    STUB_INSTANCE=fora-publishers ZITADEL_INSTANCE_HOST=login.console.example ZITADEL_PAT_FILE="${work}/pub-token/bootstrap.pat" \
    CONSOLE_OUT_DIR="${work}/console" CONSOLE_PUBLIC_ORIGIN=https://console.example ZITADEL_ADMIN_PASSWORD=Admin-Passw0rd! \
    ZITADEL_ISSUER=https://login.console.example IMPORT_DEMO_USERS=0 \
    "${PUBLISHERS}" >"${work}/out.txt" 2>"${work}/err.txt" || fail "publishers.sh failed: $(cat "${work}/err.txt")"
logged "instance fora-publishers at login.console.example already exists and its token works; nothing to change"
logged "provisioning the publishers side"
[ "$(requests '[.[] | .method + " " + .path][0:4]')" = '["GET /management/v1/orgs/me","GET /admin/v1/instances/me","GET /management/v1/orgs/me","GET /admin/v1/instances/me"]' ] \
    || fail "publishers.sh did not check the instance before provisioning it: $(requests '[.[] | .method + " " + .path]')"
grep -q '^export CONSOLE_OIDC_ISSUER=https://login.console.example$' "${work}/out.txt" || fail "publishers.sh printed no exports: $(cat "${work}/out.txt")"
rm -f "${work}/requests.json"; touch "${work}/requests.json"
emitted="$(env -u ZITADEL_INSTANCE_ROLE PATH="${work}/bin:${PATH}" CONSOLE_OUT_DIR="${work}/console" \
    CONSOLE_PUBLIC_ORIGIN=https://console.example ZITADEL_ISSUER=https://login.console.example "${PUBLISHERS}" --emit-exports)"
[ "${emitted}" = "$(cat "${work}/out.txt")" ] || fail "publishers.sh --emit-exports printed: ${emitted}"
[ "$(requests 'length')" = 0 ] || fail "publishers.sh --emit-exports sent requests: $(requests '.')"

# ── an existing client: update, then keep or replace the secret ───────────────

# The client exists and the files name it with a secret the token endpoint
# accepts: the update carries exactly a create's settings minus name and
# version, the secret is kept, and the files are left as they were.
must_run "${pub[@]}" STUB_APPS_EXIST=1
got="$(requests '[.[] | select(.path | test("/apps/|/oauth/")) | .method + " " + .path]')"
want='["POST /management/v1/projects/p1/apps/oidc","POST /management/v1/projects/p1/apps/_search","PUT /management/v1/projects/p1/apps/app-fora-console/oidc_config","POST /oauth/v2/token"]'
[ "${got}" = "${want}" ] || fail "an existing client with a working secret: ${got}"
[ "$(update_body fora-console)" = "$(app_body fora-console '["https://console.example/auth/callback"]' '["https://console.example/signed-out"]' false | jq -c 'del(.name, .version)')" ] \
    || fail "update body differs from the create's settings: $(update_body fora-console)"
[ "$(requests '[.[] | select(.path == "/oauth/v2/token") | .user]')" = '["fora-console-client"]' ] || fail "the token endpoint was not asked with the stored client id"
logged "app fora-console kept its secret"
logged "unchanged"
expect_file "${work}/console/console_client_secret" fora-console-secret

# "No changes" from Zitadel is success; the secret is still kept.
must_run "${pub[@]}" STUB_APPS_EXIST=1 STUB_UPDATE=unchanged
logged "app fora-console already existed with these settings"
[ "$(calls_to '_generate_client_secret')" = 0 ] || fail "an unchanged client got a new secret"

# A new secret when the stored one is refused, when the stored id is another
# client's, when the files are gone, and when rotation is asked for.
must_run "${pub[@]}" STUB_APPS_EXIST=1 STUB_TOKEN=invalid_client
logged "app fora-console got a new secret (the stored secret is refused)"
expect_file "${work}/console/console_client_secret" fora-console-regenerated
printf 'another-client\n' > "${work}/console/console_client_id"
must_run "${pub[@]}" STUB_APPS_EXIST=1
logged "got a new secret (the stored client id is another client's)"
expect_file "${work}/console/console_client_id" fora-console-client
reset_dirs
must_run "${pub[@]}" STUB_APPS_EXIST=1
logged "got a new secret (no stored secret)"
must_run "${pub[@]}" STUB_APPS_EXIST=1 ROTATE_CLIENT_SECRET=1
logged "got a new secret (rotation requested)"
[ "$(calls_to '/oauth/v2/token')" = 0 ] || fail "rotation still asked the token endpoint"
expect_file "${work}/console/console_client_secret" fora-console-regenerated

# An answer without an OAuth error, and a refused update, stop the run with the
# files untouched and no secret replaced.
printf 'fora-console-client\n' > "${work}/console/console_client_id"; printf 'kept-secret\n' > "${work}/console/console_client_secret"
must_exit 1 "${pub[@]}" STUB_APPS_EXIST=1 STUB_TOKEN=html
logged "answered 502 without an OAuth error"
expect_file "${work}/console/console_client_secret" kept-secret
[ "$(calls_to '_generate_client_secret')" = 0 ] || fail "an unreadable token answer still replaced the secret"
must_exit 1 "${pub[@]}" STUB_APPS_EXIST=1 STUB_UPDATE=refused
logged "app fora-console update failed (400)"
expect_file "${work}/console/console_client_secret" kept-secret
[ "$(calls_to '_generate_client_secret')" = 0 ] || fail "a refused update still replaced the secret"

# The developers side follows the same rules: an existing client with a
# working secret keeps it.
reset_dirs
mkdir -p "${work}/out"; printf 'fora-identity-client\n' > "${work}/out/identity_client_id"; printf 'id-secret\n' > "${work}/out/identity_client_secret"
must_run "${dev[@]}" STUB_APPS_EXIST=1
logged "app fora-identity kept its secret"
expect_file "${work}/out/identity_client_secret" id-secret

# ── the look and language ─────────────────────────────────────────────────────

BRANDING="${REPO_ROOT}/deploy/zitadel/bootstrap/branding"
same_bytes() { cmp -s "${work}/state/assets/$1" "${BRANDING}/$2" || fail "asset $1 is not $2"; }
# look_writes — the requests a fresh instance needs, in order.
look_writes() {
    printf '["PUT /admin/v1/restrictions","PUT /admin/v1/policies/label","POST /assets/v1/instance/policy/label/logo","POST /assets/v1/instance/policy/label/logo/dark","POST /assets/v1/instance/policy/label/icon","POST /assets/v1/instance/policy/label/icon/dark","POST /assets/v1/instance/policy/label/font","POST /admin/v1/policies/label/_activate","PUT /admin/v1/text/login/en","PUT /admin/v1/text/message/init/en","PUT /admin/v1/text/message/verifyemail/en","PUT /admin/v1/text/message/passwordreset/en","PUT /admin/v1/text/message/password_change/en"]'
}
body_of() { requests "map(select(.method == \"$1\" and .path == \"$2\"))[0].body"; }
expect_texts() { # SIDE
    [ "$(body_of PUT /admin/v1/text/login/en)" = "{\"loginText\":{\"title\":\"$1\"},\"registrationOptionText\":{\"title\":\"$1\"},\"registrationUserText\":{\"title\":\"$1\"}}" ] \
        || fail "page titles: $(body_of PUT /admin/v1/text/login/en)"
    local context="FORA developer"
    [ "$1" = "FORA Publishers" ] && context="FORA publisher console"
    for kind in init verifyemail passwordreset password_change; do
        body_of PUT "/admin/v1/text/message/${kind}/en" | jq -e '
            [.title, .subject, .preHeader, .greeting, .text, .buttonText, .footerText] |
            all(.[]; type == "string" and length > 0 and (ascii_downcase | contains("zitadel") | not))
        ' >/dev/null || fail "${kind} has missing or unbranded message fields"
        body_of PUT "/admin/v1/text/message/${kind}/en" | jq -e --arg context "${context}" '
            .greeting == "Hello {{.DisplayName}}," and (.text | contains($context))
        ' >/dev/null || fail "${kind} is missing its recipient or audience context"
        if [ "${kind}" != password_change ]; then
            body_of PUT "/admin/v1/text/message/${kind}/en" | jq -e '.text | contains("{{.Code}}")' >/dev/null \
                || fail "${kind} lost its code"
        fi
    done
    body_of PUT /admin/v1/text/message/init/en | jq -e --arg side "$1" '
        .subject == ("Welcome to " + $side) and .buttonText == "Set up your account" and
        (.text | contains("{{.PreferredLoginName}}") and contains("confirm your email address") and
         contains("ignore this email"))
    ' >/dev/null || fail "welcome email lost its account setup instructions"
    body_of PUT /admin/v1/text/message/password_change/en | jq -e '
        .text | contains("If you did not") and contains("reset your password immediately")
    ' >/dev/null || fail "password-change email lost its security instructions"
}

# A fresh developer instance: light pages and emails, compact dark wordmark,
# Instrument Sans, audience-specific copy and one policy activation.
reset_state
must_run "${dev[@]}"
[ "$(branding_writes)" = "$(look_writes)" ] || fail "developers look on a fresh instance: $(branding_writes)"
[ "$(body_of PUT /admin/v1/policies/label)" = '{"primaryColor":"#4f6df5","backgroundColor":"#fafafa","warnColor":"#cd3d56","fontColor":"#12121a","primaryColorDark":"#2073c4","backgroundColorDark":"#111827","warnColorDark":"#ff3b5b","fontColorDark":"#ffffff","hideLoginNameSuffix":false,"disableWatermark":false,"themeMode":"THEME_MODE_LIGHT"}' ] \
    || fail "developers colors: $(body_of PUT /admin/v1/policies/label)"
same_bytes logo fora-wordmark-dark.png; same_bytes icon fora-mark-black.png
same_bytes logo_dark fora-wordmark.png; same_bytes icon_dark fora-mark-white.png
same_bytes font instrument-sans.ttf
expect_texts "FORA Developers"
logged "languages applied (English only)"; logged "label policy applied (activated)"

# The same run again writes nothing, and says so for every item.
must_run "${dev[@]}"
[ "$(branding_writes)" = "[]" ] || fail "a second developers run changed the look: $(branding_writes)"
for line in "languages unchanged" "default language unchanged" "label colors unchanged" "logo unchanged" "logo/dark unchanged" \
    "icon unchanged" "icon/dark unchanged" "font unchanged" "label policy unchanged" "page titles unchanged (FORA Developers)" \
    "init email unchanged" "verifyemail email unchanged" "passwordreset email unchanged" "password_change email unchanged"; do
    logged "${line}"
done

# A deleted developer font is restored and the change activated.
rm -f "${work}/state/assets/font"
STUB_DIR="${work}" STUB_ROUTES="${work}/routes.sh" "${work}/bin/curl" -s -o /dev/null -w '%{http_code}' -X POST \
    http://zitadel:8080/admin/v1/policies/label/_activate >/dev/null
must_run "${dev[@]}"
[ "$(branding_writes)" = '["POST /assets/v1/instance/policy/label/font","POST /admin/v1/policies/label/_activate"]' ] \
    || fail "a deleted font on the developer instance: $(branding_writes)"

# Existing developer defaults are replaced even when the title already matches.
put_state "${work}/state/text-init.json" '.text = "Old developer message"'
must_run "${dev[@]}"
[ "$(branding_writes)" = '["PUT /admin/v1/text/message/init/en"]' ] \
    || fail "developer email body drift: $(branding_writes)"

# A fresh publisher instance: console panel colors in emails, native dark controls,
# the wordmark logo, white mark icon, Instrument Sans and FORA publisher email copy.
reset_state
must_run "${pub[@]}"
[ "$(branding_writes)" = "$(look_writes)" ] || fail "publishers look on a fresh instance: $(branding_writes)"
[ "$(body_of PUT /admin/v1/policies/label)" = '{"primaryColor":"#4f6df5","backgroundColor":"#12121a","warnColor":"#ff8a80","fontColor":"#e8e8f0","primaryColorDark":"#4f6df5","backgroundColorDark":"#0a0a0f","warnColorDark":"#ff8a80","fontColorDark":"#e8e8f0","hideLoginNameSuffix":false,"disableWatermark":false,"themeMode":"THEME_MODE_DARK"}' ] \
    || fail "publishers colors: $(body_of PUT /admin/v1/policies/label)"
for asset in logo logo_dark; do same_bytes "${asset}" fora-wordmark.png; done
for asset in icon icon_dark; do same_bytes "${asset}" fora-mark-white.png; done
same_bytes font instrument-sans.ttf
expect_texts "FORA Publishers"

# Again: nothing written. Then one asset drifts and the drift goes live: only
# that asset is uploaded, and the change activated.
must_run "${pub[@]}"
[ "$(branding_writes)" = "[]" ] || fail "a second publishers run changed the look: $(branding_writes)"

# Matching subjects and titles must not hide drift in any of the other fields.
cp -f "${work}/state/text-init.json" "${work}/expected-init.json"
for field in title subject preHeader greeting text buttonText footerText; do
    put_state "${work}/state/text-init.json" --arg field "${field}" '.[$field] = "Old message text"'
    must_run "${pub[@]}"
    [ "$(branding_writes)" = '["PUT /admin/v1/text/message/init/en"]' ] \
        || fail "${field} drift did not repair just the welcome email: $(branding_writes)"
    cmp -s "${work}/expected-init.json" "${work}/state/text-init.json" || fail "${field} was not restored"
done

# Upgrade the previous publisher palette and square logos through the same path.
put_state "${work}/state/label.json" '.backgroundColor = "#0a0a0f"'
for asset in logo logo_dark; do cp -f "${BRANDING}/fora-mark-white.png" "${work}/state/assets/${asset}"; done
STUB_DIR="${work}" STUB_ROUTES="${work}/routes.sh" "${work}/bin/curl" -s -o /dev/null -w '%{http_code}' -X POST \
    http://zitadel:8080/admin/v1/policies/label/_activate >/dev/null
must_run "${pub[@]}"
[ "$(branding_writes)" = '["PUT /admin/v1/policies/label","POST /assets/v1/instance/policy/label/logo","POST /assets/v1/instance/policy/label/logo/dark","POST /admin/v1/policies/label/_activate"]' ] \
    || fail "the previous publisher branding was not upgraded: $(branding_writes)"
jq -e '.policy.backgroundColor == "#12121a"' "${work}/state/active.json" >/dev/null \
    || fail "the publisher email background was not restored and activated"
for asset in logo logo_dark; do same_bytes "${asset}" fora-wordmark.png; done

cp "${BRANDING}/fora-mark-black.png" "${work}/state/assets/icon_dark"
STUB_DIR="${work}" STUB_ROUTES="${work}/routes.sh" "${work}/bin/curl" -s -o /dev/null -w '%{http_code}' -X POST \
    http://zitadel:8080/admin/v1/policies/label/_activate >/dev/null
must_run "${pub[@]}"
[ "$(branding_writes)" = '["POST /assets/v1/instance/policy/label/icon/dark","POST /admin/v1/policies/label/_activate"]' ] \
    || fail "a drifted asset: $(branding_writes)"
same_bytes icon_dark fora-mark-white.png

# A label policy update Zitadel refuses for any other reason stops the run
# before the titles and emails.
reset_state
must_exit 1 "${pub[@]}" STUB_LABEL=refused
logged "updating the label policy failed (400)"
[ "$(requests 'map(select(.path | test("/text/"))) | length')" = 0 ] || fail "the run went on after a refused label policy"

reset_state
must_exit 1 "${pub[@]}" STUB_MESSAGE=refused
logged "setting the init email's text failed (400)"
[ "$(calls_to '/text/message/verifyemail/en')" = 0 ] || fail "the run went on after a refused email update"
echo "zitadel bootstrap guard test passed (role, password and instance checked first; developers and publishers clients, users and exports; console client removal; secret kept, replaced or refused; admin password set once, the variable over the file; the instance's Host on every request; publishers.sh's order; each side's look applied once, re-applied only on drift)"
