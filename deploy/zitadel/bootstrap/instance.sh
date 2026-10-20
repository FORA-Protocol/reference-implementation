#!/usr/bin/env sh
# Makes sure the publisher instance exists: a second instance of the same
# Zitadel deployment, with its own address, accounts, browser session and look,
# where publishers sign in to the console. MCP developers stay in the instance
# Zitadel creates on its first start.
#
# The instance's bootstrap token (ZITADEL_PAT_FILE) decides what happens:
#   token present     check that it opens the instance, and stop: nothing to
#                     change. The System API is not called, so a routine run
#                     needs no System API token.
#   no token, and no  create the instance, named fora-publishers (lib.sh),
#   instance          through the System API with a machine owner, and write
#                     that owner's token. Zitadel returns the token only in
#                     the creation response.
#   no token, but the the token was lost. While the System API user holds the
#   instance exists   recovery membership for this instance (IAM_OWNER, scoped
#                     to its id), mint the machine owner a new token and delete
#                     every other token it has, so a copied one stops working;
#                     without the membership, stop and say what to do.
#
# publishers.sh runs this before bootstrap.sh; the guard and real-Zitadel tests
# run it on its own.
#
# Inputs (env), besides lib.sh's ZITADEL_BASE_URL and WAIT_SECONDS:
#   ZITADEL_INSTANCE_HOST      the instance's address, e.g. login.console.example;
#                              also the Host header its own APIs are called with
#                              (required)
#   ZITADEL_PAT_FILE           where the instance's bootstrap token is kept
#                              (required)
#   ZITADEL_SYSTEM_TOKEN_FILE  a System API token from system-token.sh; read only
#                              when ZITADEL_PAT_FILE does not exist
#   INSTANCE_ORG               the first organization, default publishers
#   MACHINE_USERNAME           the machine owner, default fora-bootstrap
#
# Exit codes: 0 ok, 1 failure, 2 bad input / unreachable Zitadel.
set -eu

LOG_PREFIX=instance
. "$(dirname "$0")/lib.sh"

INSTANCE_NAME="$PUBLISHER_INSTANCE_NAME"
INSTANCE_ORG="${INSTANCE_ORG:-publishers}"
MACHINE_USERNAME="${MACHINE_USERNAME:-fora-bootstrap}"
RECOVERY_HINT="see 'Recovering the publisher instance's token' in deploy/zitadel/RUNBOOK.md"

[ -n "$HOST_HDR" ] || { log "ZITADEL_INSTANCE_HOST is required"; exit 2; }
[ -n "$PAT_FILE" ] || { log "ZITADEL_PAT_FILE is required"; exit 2; }

# system METHOD PATH [BODY] — a System API call. It carries the System API
# token and no Host header: the System API takes its instance from the request.
system() {
    _host="$HOST_HDR"; _auth="$AUTH"
    HOST_HDR=""; AUTH="$SYSTEM_AUTH"
    call "$1" "/system/v1$2" "${3:-}"
    HOST_HDR="$_host"; AUTH="$_auth"
}

# require_name [new] — the token opened an instance; make sure it is this one
# (read_instance, in lib.sh, says what "new" changes).
require_name() {
    read_instance "${1:-}"
    [ "$INSTANCE_SEEN" = "$INSTANCE_NAME" ] ||
        die "the instance at $HOST_HDR is named '${INSTANCE_SEEN:-?}', not '$INSTANCE_NAME' ($STATUS: $(body_head))"
}

# write_pat TOKEN — next to the file first, then renamed over it, so a reader
# never sees half a token.
write_pat() {
    (umask 077 && printf '%s\n' "$1" >"$PAT_FILE.new") && mv "$PAT_FILE.new" "$PAT_FILE"
}

# ── a token exists: check it, and that is all ───────────────────────────────

# The file, not ZITADEL_PAT: the file is what records that the instance exists.
if [ -s "$PAT_FILE" ]; then
    use_token "$(tr -d '[:space:]' <"$PAT_FILE")"
    if ! wait_ready refusal; then
        log "the token in $PAT_FILE does not open the instance at $HOST_HDR ($STATUS)"
        log "$RECOVERY_HINT"
        exit 1
    fi
    require_name
    log "instance $INSTANCE_NAME at $HOST_HDR already exists and its token works; nothing to change"
    exit 0
fi

# ── no token: the System API decides between creating and recovering ───────

SYSTEM_TOKEN_FILE="${ZITADEL_SYSTEM_TOKEN_FILE:-}"
if [ -z "$SYSTEM_TOKEN_FILE" ] || [ ! -s "$SYSTEM_TOKEN_FILE" ]; then
    log "there is no token at $PAT_FILE, and no System API token to create or recover the instance with"
    log "(mint one with system-token.sh and pass its file as ZITADEL_SYSTEM_TOKEN_FILE)"
    exit 2
fi
auth_file system Bearer "$(tr -d '[:space:]' <"$SYSTEM_TOKEN_FILE")"
SYSTEM_AUTH="$WORK_DIR/system"

# Fail before the instance exists rather than after: Zitadel hands out the
# owner's token only once, so a token that cannot be written is a token lost.
_probe="$PAT_FILE.probe.$$"
if ! (umask 077 && : >"$_probe") 2>/dev/null; then
    log "cannot write to the directory of $PAT_FILE"
    exit 2
fi
rm -f "$_probe"

system POST /instances/_search "{\"queries\":[{\"domainQuery\":{\"domains\":[\"$HOST_HDR\"]}}]}"
case "$STATUS" in
    200) ;;
    401 | 403)
        log "the System API refused the token ($STATUS). Check that ZITADEL_SYSTEMAPIUSERS"
        log "holds the public half of the key the token was signed with, that the token's"
        log "audience is Zitadel's external URL, and that this machine's clock is right."
        exit 1
        ;;
    *) die "searching the instances failed ($STATUS): $(body_head)" ;;
esac
INSTANCE_ID="$(field id)"

if [ -z "$INSTANCE_ID" ]; then
    system POST /instances/_create "$(cat <<JSON
{
  "instanceName": "$INSTANCE_NAME",
  "firstOrgName": "$INSTANCE_ORG",
  "customDomain": "$HOST_HDR",
  "machine": {
    "userName": "$MACHINE_USERNAME",
    "name": "FORA bootstrap service account",
    "personalAccessToken": {"expirationDate": "2099-01-01T00:00:00Z"}
  },
  "defaultLanguage": "en"
}
JSON
    )"
    ok || die "creating the instance failed ($STATUS): $(body_head)"
    INSTANCE_ID="$(field instanceId)"
    _token="$(field pat)"
    [ -n "$_token" ] || die "the instance was created without a token in the answer: $(body_head)"
    write_pat "$_token"
    use_token "$_token"
    wait_ready refusal || die "the new instance refuses its own token ($STATUS): $(body_head)"
    require_name new
    log "created instance $INSTANCE_NAME ($INSTANCE_ID) at $HOST_HDR; its token is in $PAT_FILE"
    exit 0
fi

# ── the instance exists but its token is gone: recover ─────────────────────

log "instance $INSTANCE_ID answers at $HOST_HDR, but there is no token at $PAT_FILE"
AUTH="$SYSTEM_AUTH"
admin POST /orgs/_search \
    "{\"queries\":[{\"nameQuery\":{\"name\":\"$INSTANCE_ORG\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}"
case "$STATUS" in
    200) ;;
    401 | 403)
        log "the System API user may not act inside the instance ($STATUS), so its token cannot be recovered"
        log "$RECOVERY_HINT"
        exit 1
        ;;
    *) die "finding organization $INSTANCE_ORG failed ($STATUS): $(body_head)" ;;
esac
ORG_HDR="$(field id)"
[ -n "$ORG_HDR" ] || die "organization $INSTANCE_ORG not found in instance $INSTANCE_ID"
api POST /users/_search \
    "{\"queries\":[{\"userNameQuery\":{\"userName\":\"$MACHINE_USERNAME\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}"
_user="$(field id)"
[ -n "$_user" ] || die "user $MACHINE_USERNAME not found in instance $INSTANCE_ID ($STATUS): $(body_head)"
api POST "/users/$_user/pats" '{"expirationDate":"2099-01-01T00:00:00Z"}'
_token="$(field token)"
_token_id="$(field tokenId)"
[ -n "$_token" ] && [ -n "$_token_id" ] || die "minting a token for $MACHINE_USERNAME failed ($STATUS): $(body_head)"
write_pat "$_token"
ORG_HDR=""
use_token "$_token"
wait_ready refusal || die "the recovered token does not open the instance ($STATUS): $(body_head)"
require_name new

# The token was lost, or may have been copied: either way no earlier token of
# this user may keep working. It is deleted only now that the new one is known
# to open the instance, so the instance never has no working token.
api POST "/users/$_user/pats/_search" '{}'
ok || die "listing the tokens of $MACHINE_USERNAME failed ($STATUS): $(body_head)"
for _old in $(grep -o '"id":"[^"]*"' "$RESP" | sed 's/^"id":"//; s/"$//'); do
    [ "$_old" != "$_token_id" ] || continue
    api DELETE "/users/$_user/pats/$_old"
    ok || die "deleting the earlier token $_old of $MACHINE_USERNAME failed ($STATUS): $(body_head)"
    log "deleted the earlier token $_old of $MACHINE_USERNAME"
done
log "recovered a token for $MACHINE_USERNAME in instance $INSTANCE_NAME ($INSTANCE_ID); it is in $PAT_FILE"
