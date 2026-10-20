#!/usr/bin/env bash
# Prove what deploy/zitadel/bootstrap/instance.sh does in each of its cases,
# with curl stubbed (tests/lib/zitadel-curl-stub.sh): every request is recorded
# with its Host, bearer and organization header, a credential on curl's command
# line fails the run, and the stub answers like Zitadel v3.4.9 did in the
# proof. The test checks:
#   * a token that opens the instance: nothing changes, and the System API is
#     never called, not even when no System API token is given;
#   * a refused token, or a token for a differently named instance: failure,
#     still without the System API;
#   * no token and no System API token, or a token directory that cannot be
#     written: exit 2 before any request;
#   * the System API refusing the token: failure, nothing created, no file;
#   * no instance yet: one create with the agreed body, sent with the System
#     API token and no Host header; the owner's token is written 0600, then
#     used, with the instance's Host, to wait for the instance, through the
#     403 a new owner gets until its instance-wide role exists (a stored
#     token's 403 fails at once);
#   * the instance exists but its token is gone: recovery mints a new token
#     through the instance's own API when allowed, then deletes every earlier
#     token of the owner, and fails cleanly when not allowed;
#   * the instance's address and token file are required.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SCRIPT="${REPO_ROOT}/deploy/zitadel/bootstrap/instance.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "zitadel instance guard test FAILED: $1" >&2; exit 1; }

. "$(dirname "${BASH_SOURCE[0]}")/lib/guard.sh"

mkdir -p "${work}/bin"
ln -s "${REPO_ROOT}/deploy/terraform/scripts/tests/lib/zitadel-curl-stub.sh" "${work}/bin/curl"
# The answers, like Zitadel v3.4.9's in the proof:
#   GET  /management/v1/orgs/me     STUB_ORGS_ME (200, 401 or 404 once then 200)
#   GET  /admin/v1/instances/me     the instance named STUB_NAME; STUB_INSTANCES_ME
#                                   403once answers 403 the first time, 403 always
#   POST /system/v1/instances/_search  STUB_SEARCH: none, found or refused
#   POST /system/v1/instances/_create  a new id and the token "new-pat"
#   POST /admin/v1/orgs/_search     STUB_RECOVERY: allowed or denied
#   POST /management/v1/users/_search and …/pats  the machine user, "recovered-pat"
#   POST …/pats/_search             the user's tokens: the new one and "tok-old"
cat > "${work}/routes.sh" <<'EOF'
route() {
    case "${method} ${path}" in
        "GET /management/v1/orgs/me")
            case "${STUB_ORGS_ME:-200}" in
                404once)
                    if [ -f "${STUB_DIR}/seen-404" ]; then answer 200 '{"org":{"id":"org-1","name":"publishers"}}'
                    else touch "${STUB_DIR}/seen-404"; answer 404 '{"code":5,"message":"Errors.Org.NotFound"}'; fi ;;
                401) answer 401 '{"code":16,"message":"Errors.Token.Invalid (AUTH-7fs1e)"}' ;;
                *) answer 200 '{"org":{"id":"org-1","name":"publishers"}}' ;;
            esac ;;
        "GET /admin/v1/instances/me")
            case "${STUB_INSTANCES_ME:-200}" in
                403once) [ -f "${STUB_DIR}/seen-403" ] || { touch "${STUB_DIR}/seen-403"; answer 403 '{"code":7,"message":"No matching permissions found (AUTH-5mWD2)"}'; return; } ;;
                403) answer 403 '{"code":7,"message":"No matching permissions found (AUTH-5mWD2)"}'; return ;;
            esac
            answer 200 "{\"instance\":{\"id\":\"inst-1\",\"details\":{\"sequence\":\"1\"},\"state\":\"STATE_RUNNING\",\"name\":\"${STUB_NAME:-fora-publishers}\"}}" ;;
        "POST /system/v1/instances/_search")
            case "${STUB_SEARCH:-none}" in
                found) answer 200 '{"details":{"totalResult":"1"},"result":[{"id":"inst-1","details":{"sequence":"9"},"name":"fora-publishers"}]}' ;;
                refused) answer 401 '{"code":16,"message":"Errors.Token.Invalid (AUTH-7fs1e)"}' ;;
                *) answer 200 '{"details":{"totalResult":"0"}}' ;;
            esac ;;
        "POST /system/v1/instances/_create")
            answer 200 '{"instanceId":"inst-1","details":{"sequence":"483","resourceOwner":"org-1"},"pat":"new-pat"}' ;;
        "POST /admin/v1/orgs/_search")
            case "${STUB_RECOVERY:-denied}" in
                allowed) answer 200 '{"details":{"totalResult":"1"},"result":[{"id":"org-1","name":"publishers"}]}' ;;
                *) answer 403 '{"code":7,"message":"No matching permissions found (AUTH-5mWD2)"}' ;;
            esac ;;
        "POST /management/v1/users/_search")
            answer 200 '{"details":{"totalResult":"1"},"result":[{"id":"user-1","userName":"fora-bootstrap"}]}' ;;
        "POST /management/v1/users/user-1/pats")
            answer 200 '{"details":{"sequence":"10"},"tokenId":"tok-1","token":"recovered-pat"}' ;;
        "POST /management/v1/users/user-1/pats/_search")
            answer 200 '{"details":{"totalResult":"2"},"result":[{"id":"tok-old","details":{"sequence":"2"},"expirationDate":"2099-01-01T00:00:00Z"},{"id":"tok-1","details":{"sequence":"10"},"expirationDate":"2099-01-01T00:00:00Z"}]}' ;;
        "DELETE /management/v1/users/user-1/pats/tok-old") answer 200 '{"details":{"sequence":"11"}}' ;;
        *) answer 404 '{"code":5,"message":"stub: no such route"}' ;;
    esac
}
EOF

# run_instance [VAR=value ...] — the real script against the stub, with the
# token directory under ${work}/pub. Returns the script's status.
run_instance() {
    rm -f "${work}/requests.json" "${work}/seen-404" "${work}/seen-403" "${work}/err.txt"
    touch "${work}/requests.json"
    env -u ZITADEL_SYSTEM_TOKEN_FILE -u ZITADEL_PAT PATH="${work}/bin:${PATH}" STUB_DIR="${work}" \
        STUB_ROUTES="${work}/routes.sh" ZITADEL_INSTANCE_HOST=login.console.example \
        ZITADEL_PAT_FILE="${work}/pub/bootstrap.pat" WAIT_SECONDS=3 "$@" \
        "${SCRIPT}" >/dev/null 2>"${work}/err.txt"
}
requests() { jq -sc "$1" "${work}/requests.json"; }
count() { jq -s "[.[] | select($1)] | length" "${work}/requests.json"; }
err() { cat "${work}/err.txt"; }
reset_dir() { rm -rf "${work}/pub"; mkdir -p "${work}/pub"; }
printf 'stub-system-token\n' > "${work}/system.token"

# A token that opens the instance: nothing to change, no System API, and no
# System API token needed.
reset_dir
printf 'existing-pat\n' > "${work}/pub/bootstrap.pat"
run_instance || fail "a working token failed the run: $(err)"
[ "$(count '.path | startswith("/system/")')" = 0 ] || fail "a working token still called the System API: $(requests '.')"
[ "$(requests '[.[].bearer] | unique')" = '["existing-pat"]' ] || fail "the token was not the bearer: $(requests '.')"
[ "$(requests '[.[].host] | unique')" = '["login.console.example"]' ] || fail "instance calls lacked the instance's Host: $(requests '.')"
grep -q 'already exists and its token works; nothing to change' "${work}/err.txt" || fail "the no-change run did not say so: $(err)"
[ "$(cat "${work}/pub/bootstrap.pat")" = existing-pat ] || fail "the token file changed"

# A refused token, and a token for a differently named instance: failure,
# without the System API.
run_instance STUB_ORGS_ME=401 && fail "a refused token did not fail the run"
grep -q 'does not open the instance' "${work}/err.txt" || fail "the refused token was not reported: $(err)"
[ "$(count '.path | startswith("/system/")')" = 0 ] || fail "a refused token led to the System API"
run_instance STUB_NAME=fora-poc && fail "an instance with another name was accepted"
grep -q "named 'fora-poc', not 'fora-publishers'" "${work}/err.txt" || fail "the wrong instance was not named: $(err)"
# A stored token that may not read the instance fails at once: only a token
# made a moment ago is given time for its instance-wide role.
run_instance STUB_INSTANCES_ME=403 && fail "a stored token without the instance's role was accepted"
[ "$(count '.path == "/admin/v1/instances/me"')" = 1 ] || fail "a stored token's 403 was waited on: $(requests '.')"

# No token, no System API token: exit 2 before any request.
reset_dir
status=0; run_instance || status=$?
[ "${status}" = 2 ] || fail "no tokens at all exited ${status}, want 2: $(err)"
[ "$(count 'true')" = 0 ] || fail "a run with no tokens sent requests: $(requests '.')"

# A token directory that cannot be written: exit 2 before any request, so the
# owner's one-time token is never fetched into nowhere.
status=0; run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" ZITADEL_PAT_FILE="${work}/missing-dir/bootstrap.pat" || status=$?
[ "${status}" = 2 ] || fail "an unwritable token directory exited ${status}, want 2: $(err)"
[ "$(count 'true')" = 0 ] || fail "an unwritable token directory still sent requests: $(requests '.')"

# The System API refuses the token: failure, nothing created, no file.
reset_dir
status=0; run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" STUB_SEARCH=refused || status=$?
[ "${status}" = 1 ] || fail "a refused System API token exited ${status}, want 1: $(err)"
grep -q 'the System API refused the token' "${work}/err.txt" || fail "the refusal was not explained: $(err)"
[ "$(count '.path == "/system/v1/instances/_create"')" = 0 ] || fail "an instance was created after the refusal"
[ ! -e "${work}/pub/bootstrap.pat" ] || fail "a token file appeared after the refusal"

# No instance yet: one create, with the System API token and no Host; the
# owner's token is written 0600 and then waited on with the instance's Host,
# through the second of read-model lag a new instance shows.
reset_dir
run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" STUB_ORGS_ME=404once || fail "the create run failed: $(err)"
[ "$(requests '[.[] | select(.path | startswith("/system/")) | {bearer, host}] | unique')" = '[{"bearer":"stub-system-token","host":""}]' ] \
    || fail "System API calls must carry the System API token and no Host: $(requests '.')"
[ "$(requests '[.[] | select(.path == "/system/v1/instances/_create") | .body]')" = \
    '[{"instanceName":"fora-publishers","firstOrgName":"publishers","customDomain":"login.console.example","machine":{"userName":"fora-bootstrap","name":"FORA bootstrap service account","personalAccessToken":{"expirationDate":"2099-01-01T00:00:00Z"}},"defaultLanguage":"en"}]' ] \
    || fail "create body: $(requests '[.[] | select(.path == "/system/v1/instances/_create") | .body]')"
[ "$(cat "${work}/pub/bootstrap.pat")" = new-pat ] || fail "the owner's token was not written"
[ "$(mode "${work}/pub/bootstrap.pat")" = 600 ] || fail "token file mode $(mode "${work}/pub/bootstrap.pat")"
[ "$(count '.path == "/management/v1/orgs/me" and .bearer == "new-pat" and .host == "login.console.example"')" = 2 ] \
    || fail "the new token was not polled through the 404 with the instance's Host: $(requests '.')"
grep -q 'created instance fora-publishers (inst-1) at login.console.example' "${work}/err.txt" || fail "the create was not reported: $(err)"
[ ! -e "${work}/pub/bootstrap.pat.new" ] || fail "the temporary token file was left behind"
# ...and through the 403 a new owner gets while Zitadel grants its
# instance-wide role, which comes after the organization role orgs/me needs.
reset_dir
run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" STUB_INSTANCES_ME=403once || fail "a new owner's first 403 failed the run: $(err)"
[ "$(count '.path == "/admin/v1/instances/me" and .bearer == "new-pat"')" = 2 ] || fail "the 403 was not waited through: $(requests '.')"
# A 403 that does not pass ends the run once WAIT_SECONDS have gone.
reset_dir
status=0; run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" STUB_INSTANCES_ME=403 || status=$?
[ "${status}" = 1 ] || fail "a lasting 403 exited ${status}, want 1: $(err)"
[ "$(count '.path == "/admin/v1/instances/me"')" = 4 ] || fail "a lasting 403 was not given exactly WAIT_SECONDS: $(requests '.')"

# The instance exists but its token is gone, and the System API user may not act
# inside it: failure, nothing created, no file.
reset_dir
status=0; run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" STUB_SEARCH=found || status=$?
[ "${status}" = 1 ] || fail "an unrecoverable lost token exited ${status}, want 1: $(err)"
grep -q 'cannot be recovered' "${work}/err.txt" || fail "the missing permission was not explained: $(err)"
[ "$(count '.path == "/system/v1/instances/_create"')" = 0 ] || fail "a second instance was created for the same domain"
[ ! -e "${work}/pub/bootstrap.pat" ] || fail "a token file appeared without a recovery"

# ...and with the recovery membership: a new token through the instance's own
# API, with the System API token, the instance's Host and its organization.
run_instance ZITADEL_SYSTEM_TOKEN_FILE="${work}/system.token" STUB_SEARCH=found STUB_RECOVERY=allowed || fail "the recovery failed: $(err)"
[ "$(cat "${work}/pub/bootstrap.pat")" = recovered-pat ] || fail "the recovered token was not written"
[ "$(requests '[.[] | select(.path == "/management/v1/users/user-1/pats") | {bearer, host, org}]')" = \
    '[{"bearer":"stub-system-token","host":"login.console.example","org":"org-1"}]' ] \
    || fail "the token was not minted inside the instance's organization: $(requests '.')"
[ "$(count '.path == "/system/v1/instances/_create"')" = 0 ] || fail "the recovery created an instance"
grep -q 'recovered a token for fora-bootstrap' "${work}/err.txt" || fail "the recovery was not reported: $(err)"
# Every earlier token of the user is deleted, and only after the new one has
# opened the instance: a token that was copied stops working.
[ "$(requests '[.[] | select(.method == "DELETE") | {path, bearer}]')" = \
    '[{"path":"/management/v1/users/user-1/pats/tok-old","bearer":"recovered-pat"}]' ] \
    || fail "the earlier token was not deleted with the new one: $(requests '.')"
[ "$(requests '[.[] | .method + " " + .path] | (index("DELETE /management/v1/users/user-1/pats/tok-old") > index("GET /admin/v1/instances/me"))')" = true ] \
    || fail "the earlier token was deleted before the new one was known to work: $(requests '[.[] | .method + " " + .path]')"
grep -q 'deleted the earlier token tok-old of fora-bootstrap' "${work}/err.txt" || fail "the deletion was not reported: $(err)"

# The instance's address and its token file are required.
status=0; run_instance ZITADEL_INSTANCE_HOST= || status=$?
[ "${status}" = 2 ] || fail "a run without ZITADEL_INSTANCE_HOST exited ${status}, want 2: $(err)"
grep -q 'ZITADEL_INSTANCE_HOST is required' "${work}/err.txt" || fail "the missing host was not named: $(err)"
status=0; run_instance ZITADEL_PAT_FILE= || status=$?
[ "${status}" = 2 ] || fail "a run without ZITADEL_PAT_FILE exited ${status}, want 2: $(err)"
[ "$(count 'true')" = 0 ] || fail "a run without its inputs sent requests: $(requests '.')"

echo "zitadel instance guard test passed (working token: no System API; refusals; create with the owner's token written 0600, its 403 waited through, a stored token's not; recovery allowed, deleting the earlier token, and refused; inputs required)"
