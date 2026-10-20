# Shared plumbing for the Zitadel setup scripts in this directory. Sourced, not
# run: POSIX sh, curl, grep and sed only, because the scripts run in the
# curlimages/curl image, which has no jq.
#
# Inputs every script here reads, each under one name (env):
#   ZITADEL_BASE_URL       Zitadel's API (default http://zitadel:8080, the
#                          address on a stack's network)
#   ZITADEL_INSTANCE_HOST  the instance's address, sent as the Host header:
#                          Zitadel picks the instance from it, not from the
#                          address dialled
#   ZITADEL_PAT_FILE       where the instance's token is kept; ZITADEL_PAT
#                          holds the token itself and overrides the file
#   WAIT_SECONDS           how long to wait for an instance to answer
#                          (default 180)
# A script sets its own default for the host and the token file, or refuses to
# run without them, right after sourcing this.
#
# Set while a script runs:
#   BASE, HOST_HDR, PAT_FILE  from the inputs above. An empty HOST_HDR sends no
#              Host header, for the System API, which names its instance in
#              the request
#   AUTH       the header file every request sends: use_token writes it
#   ORG_HDR    optional: an organization id sent as x-zitadel-orgid, for a
#              caller that has no organization of its own (a System API user)
#   LOG_PREFIX the tag on every log line (default "bootstrap")
#
# No secret reaches a command line, where any user of the host can read it in
# /proc: tokens and client credentials go to curl in a header file, and request
# bodies, which can carry a password, on curl's standard input. Both files are
# in WORK_DIR, a directory only this run can read, removed when the run ends.

# The publisher instance's name. instance.sh creates the instance under it, and
# bootstrap.sh's publishers role refuses any instance named otherwise (and its
# developers role refuses this one), so a token or Host aimed at the wrong
# instance stops before anything is written.
PUBLISHER_INSTANCE_NAME="fora-publishers"

BASE="${ZITADEL_BASE_URL:-http://zitadel:8080}"
HOST_HDR="${ZITADEL_INSTANCE_HOST:-}"
PAT_FILE="${ZITADEL_PAT_FILE:-}"
WAIT_SECONDS="${WAIT_SECONDS:-180}"

WORK_DIR="$(mktemp -d)"
RESP="$WORK_DIR/response"
AUTH="$WORK_DIR/bearer"
trap 'rm -rf "$WORK_DIR"' EXIT

log() { printf '%s\n' "${LOG_PREFIX:-bootstrap}: $*" >&2; }
die() { log "$*"; exit 1; }

# auth_file NAME SCHEME CREDENTIAL — writes "Authorization: SCHEME CREDENTIAL"
# to WORK_DIR/NAME, for a request to send with -H @file.
auth_file() {
    (umask 077 && printf 'Authorization: %s %s\n' "$2" "$3" >"$WORK_DIR/$1")
}

# use_token TOKEN — the bearer token every later request carries.
use_token() {
    auth_file bearer Bearer "$1"
    AUTH="$WORK_DIR/bearer"
}

# request FILE METHOD URL [CURL_ARGS...] — one request to Zitadel, by the rules
# every request here follows: the credentials in AUTH, the Host header unless
# HOST_HDR is empty, and x-zitadel-orgid when ORG_HDR is set. The answer's body
# lands in FILE and its status in $STATUS. Non-2xx is not an error; the caller
# decides. `|| true` keeps a cold start (curl exit 7, before Zitadel listens)
# from killing a readiness loop under `set -e`; curl still reports "000".
# POSIX sh has no local variables, so this one's carry a prefix no caller uses.
STATUS=""
request() {
    _rq_file="$1"; _rq_method="$2"; _rq_url="$3"
    shift 3
    if [ -n "${HOST_HDR:-}" ]; then set -- "$@" -H "Host: $HOST_HDR"; fi
    if [ -n "${ORG_HDR:-}" ]; then set -- "$@" -H "x-zitadel-orgid: $ORG_HDR"; fi
    STATUS="$(curl -s -o "$_rq_file" -w '%{http_code}' -X "$_rq_method" -H "@$AUTH" "$@" "$_rq_url" || true)"
}

# call METHOD PATH [BODY] — a JSON API call. PATH starts with the API prefix
# (/management/v1/…, /admin/v1/…); the answer is in $RESP. The body goes on
# standard input (a here-document, which keeps the call in this shell).
call() {
    if [ -n "${3:-}" ]; then
        request "$RESP" "$1" "$BASE$2" -H 'Content-Type: application/json' --data-binary @- <<BODY
$3
BODY
    else
        request "$RESP" "$1" "$BASE$2"
    fi
}

# api METHOD PATH [BODY] — a Management API call: the settings of the token's
# organization.
api() { call "$1" "/management/v1$2" "${3:-}"; }

# admin METHOD PATH [BODY] — an Admin API call: settings of the whole instance.
admin() { call "$1" "/admin/v1$2" "${3:-}"; }

ok() { case "$STATUS" in 200 | 201) return 0 ;; *) return 1 ;; esac; }

# string_of NAME — the first string field NAME in the JSON on standard input.
string_of() { grep -o "\"$1\":\"[^\"]*\"" 2>/dev/null | head -n 1 | sed 's/^[^:]*:"//; s/"$//'; }

# json_string NAME — the first string field NAME in $RESP, whatever the status.
json_string() { string_of "$1" <"$RESP"; }

# field NAME — json_string, on a SUCCESS response only. The guard matters: a
# Zitadel error body carries its own `"id"` (the error code, e.g. "V3-DKcYh"),
# so reading a 409 yields a convincing non-id.
field() {
    ok || return 0
    json_string "$1"
}

# benign — succeeded, or was a no-op. Zitadel reports both "already exists" and
# "already has these values" as 4xx; for idempotent provisioning they are success.
# The second has no single spelling. Measured on v3.4.9, both answer 400: an
# unchanged login policy with "Errors.Org.LoginPolicy.NotChanged (Org-5M9vdd)",
# an unchanged OIDC app configuration with "No changes (COMMAND-1m88i)".
benign() {
    ok && return 0
    grep -qi 'alreadyexists\|already exists\|notchanged\|no changes' "$RESP"
}

# body_head — a bounded slice of the last response, for error messages.
body_head() { cut -c1-300 "$RESP" | tr -d '\n'; }

# json_array LIST — renders a space-separated list as a JSON string array.
json_array() {
    _out=""
    for _item in $1; do
        if [ -n "$_out" ]; then _out="$_out,"; fi
        _out="$_out\"$_item\""
    done
    printf '[%s]' "$_out"
}

# read_pat — the instance's token: ZITADEL_PAT, or the file at PAT_FILE once it
# is there (for the developer instance, FirstInstance writes it during
# Zitadel's first start).
read_pat() {
    if [ -n "${ZITADEL_PAT:-}" ]; then
        use_token "$ZITADEL_PAT"
        return
    fi
    _i=0
    while [ ! -s "$PAT_FILE" ]; do
        _i=$((_i + 1))
        if [ "$_i" -gt "$WAIT_SECONDS" ]; then
            log "timed out waiting for the instance's token at $PAT_FILE"
            log "(for the developer instance, FirstInstance writes it; check \`docker compose logs zitadel\`)"
            exit 2
        fi
        sleep 1
    done
    _token="$(tr -d '[:space:]' <"$PAT_FILE")"
    [ -n "$_token" ] || die "the token at $PAT_FILE is empty"
    use_token "$_token"
}

# wait_ready [refusal] — polls the instance's Management API with the token
# until it answers, for up to WAIT_SECONDS. A cold Zitadel answers 000, and a
# new instance 404 for about a second while Zitadel builds its read model.
# With "refusal", a 401 or 403 returns 1 at once: the token is not valid here.
wait_ready() {
    _i=0
    while :; do
        api GET /orgs/me
        if ok; then
            log "management API reachable at $BASE (Host: $HOST_HDR)"
            return 0
        fi
        if [ "${1:-}" = refusal ]; then
            case "$STATUS" in 401 | 403) return 1 ;; esac
        fi
        _i=$((_i + 1))
        if [ "$_i" -gt "$WAIT_SECONDS" ]; then
            log "the instance at $BASE (Host: $HOST_HDR) did not answer within ${WAIT_SECONDS}s (last status $STATUS)"
            if [ "$STATUS" = "000" ]; then
                log "(status 000 = nothing listening; check \`docker compose logs zitadel\`)"
            else
                log "(body: $(body_head))"
                log "(an 'Instance not found' means the Host header names no instance)"
            fi
            exit 2
        fi
        if [ "$_i" = "15" ]; then
            log "still waiting for Zitadel to come up (first boot takes ~30-45s)..."
        fi
        sleep 1
    done
}

# read_instance [new] — the name of the instance the token and Host open, in
# INSTANCE_SEEN (empty when it cannot be read; $STATUS says why). A token made
# a moment ago (new) may get 403 for a few seconds: Zitadel grants a new
# owner's instance-wide role (IAM_OWNER) later than the organization role
# wait_ready needs. Such a token is given WAIT_SECONDS; a stored token's 403 is
# its answer.
INSTANCE_SEEN=""
read_instance() {
    _i=0
    while :; do
        admin GET /instances/me
        if ok || [ "${1:-}" != new ] || [ "$STATUS" != 403 ] || [ "$_i" -ge "$WAIT_SECONDS" ]; then
            break
        fi
        _i=$((_i + 1))
        sleep 1
    done
    INSTANCE_SEEN="$(field name)"
}
