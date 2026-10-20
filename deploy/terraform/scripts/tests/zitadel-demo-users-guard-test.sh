#!/usr/bin/env bash
# Prove the IMPORT_DEMO_USERS opt-out in deploy/zitadel/bootstrap/bootstrap.sh,
# on the developers side: by default all three demo users are imported (demo
# and staging unchanged), and IMPORT_DEMO_USERS=0 imports none while the rest
# of the provisioning still runs. (The publishers side, which imports alice
# alone, is covered by zitadel-bootstrap-guard-test.sh.) The REAL script runs
# end to end against the shared curl stub (tests/lib/zitadel-curl-stub.sh),
# answering every call with 200 and a body carrying the ids the script reads;
# the user import is the one call whose count changes between the runs.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
SCRIPT="${REPO_ROOT}/deploy/zitadel/bootstrap/bootstrap.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

mkdir -p "${work}/bin"
ln -s "${REPO_ROOT}/deploy/terraform/scripts/tests/lib/zitadel-curl-stub.sh" "${work}/bin/curl"
# Every call answers 200 with the ids the script reads.
cat > "${work}/routes.sh" <<'EOF'
route() { answer 200 '{"id":"p1","clientId":"c1","clientSecret":"s1"}'; }
EOF

fail() { echo "zitadel demo-users guard test FAILED: $1" >&2; exit 1; }

imports() { jq -s '[.[] | select(.path == "/management/v1/users/human/_import")] | length' "${work}/requests.json"; }

# run_bootstrap [VAR=value ...] → the number of user imports the run made
run_bootstrap() {
    rm -f "${work}/requests.json"
    touch "${work}/requests.json"
    env "$@" PATH="${work}/bin:${PATH}" STUB_DIR="${work}" STUB_ROUTES="${work}/routes.sh" ZITADEL_PAT=stub-pat \
        OUT_DIR="${work}/out" ZITADEL_INSTANCE_ROLE=developers \
        "${SCRIPT}" >/dev/null 2>"${work}/stderr.log" || fail "bootstrap exited $?: $(cat "${work}/stderr.log")"
    [ "$(jq -s '[.[] | select(.method == "POST" and .path == "/management/v1/projects/p1/apps/oidc")] | length' "${work}/requests.json")" = 1 ] \
        || fail "the OIDC app was not provisioned, so the run did not reach the import step"
    imports
}

# Default: all three users imported.
[ "$(run_bootstrap)" = 3 ] || fail "default run imported $(imports) users, expected 3"

# Opt-out: no user imported, and the run says so.
[ "$(run_bootstrap IMPORT_DEMO_USERS=0)" = 0 ] || fail "IMPORT_DEMO_USERS=0 still imported $(imports) users"
grep -q 'SKIP demo users' "${work}/stderr.log" || fail "the opt-out run did not log the skip"

# Anything other than 0 keeps the default.
[ "$(run_bootstrap IMPORT_DEMO_USERS=1)" = 3 ] || fail "IMPORT_DEMO_USERS=1 did not import all three users"

echo "zitadel demo-users guard test passed (default imports all three; IMPORT_DEMO_USERS=0 imports none)"
