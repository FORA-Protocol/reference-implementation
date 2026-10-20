#!/usr/bin/env sh
# Zitadel bootstrap — provisions one Zitadel instance for one side of the
# deployment. The deployment runs two instances of one Zitadel, each with its
# own address, accounts, browser session and look:
#
#   developers  the instance Zitadel creates on its first start, where MCP
#               developers sign up and sign in. Gets the Identity Service's
#               OIDC client, `fora-identity`.
#   publishers  the instance instance.sh creates through the System API, where
#               publishers sign in to the console. Gets the console's OIDC
#               client, `fora-console`, and a human admin, `zadmin`.
#
# Run once per instance, with ZITADEL_INSTANCE_ROLE naming the side and the
# Host header and token of that instance. The publishers side runs through
# publishers.sh, which makes sure the instance exists first. A re-run brings
# the instance back to what this script describes and changes nothing that
# already matches (see Idempotency).
#
# Provisions, in order:
#   1. project `fora`
#   2. an org login policy: MFA off, password reset shown, self-registration on,
#      external IdPs allowed; then the side's look and language, instance-wide
#      (branding.sh): English only, colors, logo, icon, font, the titles of the
#      sign-in and sign-up pages, and the four account emails
#   3. the side's confidential OIDC client. The MCP client gets none; it
#      self-registers over DCR. The developers side also removes a
#      `fora-console` client left from the time both clients shared this
#      instance, unless REMOVE_CONSOLE_CLIENT=0.
#   4. publishers: the human admin `zadmin` (IAM_OWNER and ORG_OWNER), so
#      operators can sign in to this instance's Zitadel console as they can to
#      the developer instance's
#   5. local users, so a stack works without Google, unless IMPORT_DEMO_USERS=0
#      (production: no shared-password accounts on the sign-in page).
#      developers: alice@acme.local, which the e2e suites register at
#      exchanges; bob@acme.local, which no suite ever registers — it stands for
#      a freshly signed-up agent; carol@acme.local, which only the balance
#      suite registers and spends as. publishers: alice@acme.local, the
#      console suites' publisher.
#   6. developers: Google as a federated IdP, AND the login-policy link that
#      renders it. The publishers side never gets this one: publishers' Google
#      sign-in is configured with credentials of its own.
#
# Each OIDC client gets Development mode only when one of its addresses is not
# https; a laptop and the e2e stack use plain http. A client whose addresses
# are all https gets it off.
#
# internal/testutil/zitadelbootstrap.go is a SECOND implementation of this, for
# the `zitadel` test tier. A change here does not reach it. That tier provisions
# a fresh container once, so it only ever creates: it never updates an existing
# app, knows no roles, has no console app, no Google federation and only the
# alice user, and its one app registers loopback http addresses with
# Development mode on. Its app name and login policy differ too.
#
# Inputs (env), besides lib.sh's ZITADEL_BASE_URL and WAIT_SECONDS:
#   ZITADEL_INSTANCE_ROLE  developers or publishers (required)
#   ZITADEL_INSTANCE_HOST  Host header — Zitadel routes instances by it: the
#                          developer instance's ExternalDomain, or the publisher
#                          instance's address (default localhost)
#   ZITADEL_ISSUER         The instance's externally reachable issuer, used for
#                          the printed exports (default http://localhost:58080)
#   ZITADEL_PAT_FILE       The instance's admin token (default /bootstrap/zadmin.pat,
#                          which FirstInstance writes for the developer instance)
#   ZITADEL_PAT            The token's value, overriding the file
#   ROTATE_CLIENT_SECRET   1 mints the side's client a new secret even when the
#                          stored one still works
#   developers:
#   IDENTITY_AUTH_ISSUER   Identity Service's own issuer; its /callback becomes the
#                          client's redirect URI (default http://localhost:8083)
#   EXTRA_REDIRECT_URIS    Additional space-separated redirect URIs (optional)
#   OUT_DIR                Where the client id/secret are written (default /bootstrap)
#   REMOVE_CONSOLE_CLIENT  0 keeps a `fora-console` client in this instance;
#                          anything else (default 1) removes it and its files in
#                          OUT_DIR
#   GOOGLE_CLIENT_ID       Google OAuth client id — omit to skip federation
#   GOOGLE_CLIENT_SECRET   Google OAuth client secret — omit to skip federation
#   publishers:
#   CONSOLE_PUBLIC_ORIGIN  The console's own origin; its /auth/callback becomes the
#                          client's redirect URI, and its /signed-out the address
#                          a sign-out returns to (default http://localhost:8085)
#   EXTRA_CONSOLE_REDIRECT_URIS  Additional redirect URIs for the console client
#   CONSOLE_OUT_DIR        Where the client id/secret are written (default
#                          /console-oidc): a directory of the console's own,
#                          because the developer instance's volume also holds
#                          its admin token
#   ZITADEL_ADMIN_PASSWORD The admin's password, set only when the admin is
#                          created (required for provisioning)
#   ZITADEL_ADMIN_PASSWORD_FILE  A file holding it instead; the variable wins
#                          when both are set, as ZITADEL_PAT does over its file
#   both:
#   ALICE_PASSWORD         Password of the local users (default Alice12345!)
#   IMPORT_DEMO_USERS      0 skips the local users; anything else (default 1)
#                          imports them
#
# Usage:
#   bootstrap.sh                  provision, then print the exports
#   bootstrap.sh --emit-exports   print an earlier run's exports and exit,
#                                 touching neither Zitadel nor the token.
#                                 This is what `make zitadel-creds` runs —
#                                 the wire format lives here only.
#
# Outputs:
#   developers: $OUT_DIR/identity_client_id, $OUT_DIR/identity_client_secret
#   publishers: $CONSOLE_OUT_DIR/console_client_id, $CONSOLE_OUT_DIR/console_client_secret
#   `export ...` lines on STDOUT (all logging goes to stderr, so `eval` is safe)
#
# Idempotency: a re-run against a provisioned instance changes nothing that
# already matches, and says so. Every create tolerates AlreadyExists, every
# update an unchanged answer. A client that already exists is brought to the
# settings this run computes: its redirect and post-logout addresses, and
# Development mode; the update also resets any other setting changed on the
# client by hand to this script's value or Zitadel's default. Zitadel never
# shows a secret twice, so the client's stored secret is kept when the files
# name this client and its token endpoint accepts them, and a new one is minted
# only when they do not, or when ROTATE_CLIENT_SECRET=1 asks for it. A service
# reads its secret at start, so after a new one it must be restarted. The
# admin's and the local users' passwords are set only when they are created.
# It is NOT safe against a partially completed run; wipe the volumes and start
# over.
#
# Exit codes: 0 ok, 1 provisioning failure, 2 bad input / unreachable Zitadel /
# the token opens the other side's instance.
set -eu

# The plumbing every script in this directory shares: logging, the API calls,
# reading the token and waiting for Zitadel.
. "$(dirname "$0")/lib.sh"

ROLE="${ZITADEL_INSTANCE_ROLE:-}"
HOST_HDR="${HOST_HDR:-localhost}"
ISSUER="${ZITADEL_ISSUER:-http://localhost:58080}"
PAT_FILE="${PAT_FILE:-/bootstrap/zadmin.pat}"
ROTATE_CLIENT_SECRET="${ROTATE_CLIENT_SECRET:-0}"
ALICE_PASSWORD="${ALICE_PASSWORD:-Alice12345!}"
IMPORT_DEMO_USERS="${IMPORT_DEMO_USERS:-1}"
SCOPES="openid profile email"
IDP_NAME="Google"
# developers
AUTH_ISSUER="${IDENTITY_AUTH_ISSUER:-http://localhost:8083}"
OUT_DIR="${OUT_DIR:-/bootstrap}"
REMOVE_CONSOLE_CLIENT="${REMOVE_CONSOLE_CLIENT:-1}"
APP_NAME="fora-identity"
# publishers. The console's client is separate from the Identity Service's
# because the two services authenticate different people for different things —
# agent developers and publishers — in instances of their own.
CONSOLE_APP_NAME="fora-console"
CONSOLE_ISSUER_URL="${CONSOLE_PUBLIC_ORIGIN:-http://localhost:8085}"
CONSOLE_OUT_DIR="${CONSOLE_OUT_DIR:-/console-oidc}"
ADMIN_USER="zadmin"

# Two modes: provision (default), or re-print an earlier run's credentials.
MODE="provision"
case "${1:-}" in
    --emit-exports) MODE="emit" ;;
    "") ;;
    *)
        printf 'usage: %s [--emit-exports]\n' "$0" >&2
        exit 2
        ;;
esac

# The side's credentials: where its client's id and secret are written, and
# the prefix of its exports.
case "$ROLE" in
    developers) CREDS_DIR="$OUT_DIR"; CREDS=identity; EXPORT_PREFIX=IDENTITY ;;
    publishers) CREDS_DIR="$CONSOLE_OUT_DIR"; CREDS=console; EXPORT_PREFIX=CONSOLE ;;
    *)
        log "ZITADEL_INSTANCE_ROLE must be developers or publishers, not '${ROLE}'"
        exit 2
        ;;
esac
ID_FILE="$CREDS_DIR/${CREDS}_client_id"
SECRET_FILE="$CREDS_DIR/${CREDS}_client_secret"

# The admin's password, publishers only: checked before any request, so a run
# missing it stops before it changes anything. It goes into a JSON body
# verbatim, hence no quotes or backslashes.
ADMIN_PASSWORD=""
if [ "$ROLE" = publishers ] && [ "$MODE" = provision ]; then
    ADMIN_PASSWORD="${ZITADEL_ADMIN_PASSWORD:-}"
    if [ -z "$ADMIN_PASSWORD" ] && [ -n "${ZITADEL_ADMIN_PASSWORD_FILE:-}" ]; then
        [ -r "$ZITADEL_ADMIN_PASSWORD_FILE" ] || { log "cannot read $ZITADEL_ADMIN_PASSWORD_FILE"; exit 2; }
        ADMIN_PASSWORD="$(tr -d '\n' <"$ZITADEL_ADMIN_PASSWORD_FILE")"
    fi
    [ -n "$ADMIN_PASSWORD" ] || {
        log "the publishers role needs the admin's password: ZITADEL_ADMIN_PASSWORD_FILE or ZITADEL_ADMIN_PASSWORD"
        exit 2
    }
    case "$ADMIN_PASSWORD" in
        *[\"\\]*) log "the admin's password may not contain a quote or a backslash"; exit 2 ;;
    esac
fi

# The look and language of the role's sign-in pages and emails (apply_branding).
. "$(dirname "$0")/branding.sh"

# ── 0. the right instance ───────────────────────────────────────────────────

# check_instance — the token and Host header could open either instance, so
# make sure they open this role's before anything is written: a developer
# token with the publishers role would otherwise put the console's client back
# where it must not be.
check_instance() {
    read_instance
    ok || die "reading the instance at $HOST_HDR failed ($STATUS): $(body_head)"
    case "$ROLE" in
        publishers)
            [ "$INSTANCE_SEEN" = "$PUBLISHER_INSTANCE_NAME" ] || {
                log "the publishers role needs the instance '$PUBLISHER_INSTANCE_NAME', but $HOST_HDR is '${INSTANCE_SEEN}'"
                exit 2
            } ;;
        developers)
            [ "$INSTANCE_SEEN" != "$PUBLISHER_INSTANCE_NAME" ] || {
                log "the developers role was pointed at the publisher instance at $HOST_HDR"
                exit 2
            } ;;
    esac
    log "instance '$INSTANCE_SEEN' at $HOST_HDR: provisioning the $ROLE side"
}

# ── 1. project ──────────────────────────────────────────────────────────────

create_project() {
    api POST /projects '{"name":"fora","projectRoleAssertion":true,"projectRoleCheck":false}'
    PROJECT_ID="$(field id)"
    if [ -z "$PROJECT_ID" ]; then
        api POST /projects/_search \
            '{"queries":[{"nameQuery":{"name":"fora","method":"TEXT_QUERY_METHOD_EQUALS"}}]}'
        PROJECT_ID="$(field id)"
    fi
    [ -n "$PROJECT_ID" ] || die "could not resolve project id: $(body_head)"
    log "project fora id=$PROJECT_ID"
}

# ── 2. login policy ─────────────────────────────────────────────────────────

# The default policy forces MFA enrolment after the first password, breaking any
# scripted login. allowExternalIdp is what permits federated sign-in at all —
# without it no Google button renders, however the IdP itself is configured.
# allowRegister puts self-registration on the sign-in page, so a new developer
# or publisher can create an account without an operator pre-creating one.
#
# hidePasswordReset is false because the publisher console offers password
# sign-in and must offer recovery with it: a publisher who cannot reset a
# password has no way back in, and the provider is the only party that can
# reset one. Developers get the same policy, for the same reason.
LOGIN_POLICY='{
  "allowUsernamePassword": true,
  "allowRegister": true,
  "allowExternalIdp": true,
  "forceMfa": false,
  "forceMfaLocalOnly": false,
  "passwordlessType": "PASSWORDLESS_TYPE_NOT_ALLOWED",
  "hidePasswordReset": false,
  "ignoreUnknownUsernames": false,
  "passwordCheckLifetime": "864000s",
  "externalLoginCheckLifetime": "864000s",
  "mfaInitSkipLifetime": "0s",
  "secondFactorCheckLifetime": "64800s",
  "multiFactorCheckLifetime": "43200s",
  "allowDomainDiscovery": true,
  "disableLoginWithEmail": false,
  "disableLoginWithPhone": true
}'

set_login_policy() {
    api POST /policies/login "$LOGIN_POLICY"
    if ok; then
        log "org login policy created (MFA off, external IdP allowed, self-registration on)"
        return
    fi
    _create="$STATUS"
    # A custom policy already exists for this org → update it to the same shape.
    # An update that changes nothing comes back 400 NotChanged, which `benign`
    # accepts: the policy already reads the way this script wants it.
    api PUT /policies/login "$LOGIN_POLICY"
    if ok; then
        log "org login policy updated (MFA off, external IdP allowed, self-registration on)"
        return
    fi
    if benign; then
        log "org login policy unchanged (MFA off, external IdP allowed, self-registration on)"
        return
    fi
    die "login policy not applied (create $_create, update $STATUS): $(body_head)"
}

# ── 3. the OIDC client ──────────────────────────────────────────────────────

# dev_mode_for ADDRESSES — "false" when every address in the space-separated
# list is https, else "true". Development mode is what lets Zitadel accept a
# plain-http address: the loopback redirect a laptop uses, or a service name
# on the e2e stack's network. A deployment registers https addresses only, so
# its clients run with the mode off.
dev_mode_for() {
    for _address in $1; do
        case "$_address" in
            https://*) ;;
            *)
                printf true
                return
                ;;
        esac
    done
    printf false
}

# oidc_config_body REDIRECT_URIS POST_LOGOUT_URIS — the one definition of an
# OIDC client's settings here. A create sends it with the client's name and
# OIDC version added (create_body); the update of a client that already exists
# sends it as it is, because that call takes neither field. Both clients and
# both calls go through it, so a created client and an updated one cannot
# drift apart.
oidc_config_body() {
    cat <<JSON
{
  "redirectUris": $(json_array "$1"),
  "responseTypes": ["OIDC_RESPONSE_TYPE_CODE"],
  "grantTypes": ["OIDC_GRANT_TYPE_AUTHORIZATION_CODE", "OIDC_GRANT_TYPE_REFRESH_TOKEN"],
  "appType": "OIDC_APP_TYPE_WEB",
  "authMethodType": "OIDC_AUTH_METHOD_TYPE_BASIC",
  "postLogoutRedirectUris": $(json_array "$2"),
  "devMode": $(dev_mode_for "$1 $2"),
  "accessTokenType": "OIDC_TOKEN_TYPE_JWT",
  "accessTokenRoleAssertion": true,
  "idTokenRoleAssertion": true,
  "idTokenUserinfoAssertion": true,
  "clockSkew": "0s"
}
JSON
}

# create_body NAME CONFIG — CONFIG with the two fields only a create takes
# inserted after its opening brace.
create_body() {
    printf '{\n  "name": "%s",\n  "version": "OIDC_VERSION_1_0",%s' "$1" "${2#\{}"
}

# regenerate_secret APP_ID — Zitadel v3 renamed this endpoint; keep the legacy
# path as a fallback for older instances left behind in a dev volume.
regenerate_secret() {
    api POST "/projects/$PROJECT_ID/apps/$1/oidc_config/_generate_client_secret" '{}'
    APP_CLIENT_SECRET="$(field clientSecret)"
    if [ -z "$APP_CLIENT_SECRET" ]; then
        api POST "/projects/$PROJECT_ID/apps/$1/oidc_config/_change_client_secret" '{}'
        APP_CLIENT_SECRET="$(field clientSecret)"
    fi
}

# token_endpoint_error CLIENT_ID SECRET — sets TOKEN_ERROR to the OAuth error
# the instance's token endpoint answers for a code it never issued, empty when
# the answer carries none ($STATUS holds its status). Measured on v3.4.9:
# client authentication comes first, so a wrong secret gets invalid_client,
# and a right one gets past it to the refusal of the code (invalid_request).
# The client's credentials go in a header file, as every token does here.
TOKEN_ERROR=""
token_endpoint_error() {
    auth_file client Basic "$(printf '%s:%s' "$1" "$2" | base64 | tr -d '\n')"
    _auth="$AUTH"
    AUTH="$WORK_DIR/client"
    request "$RESP" POST "$BASE/oauth/v2/token" -d grant_type=authorization_code \
        -d code=never-issued -d redirect_uri=https://unused.example/callback
    AUTH="$_auth"
    rm -f "$WORK_DIR/client"
    TOKEN_ERROR="$(json_string error)"
}

# secret_for NAME APP_ID ID_FILE SECRET_FILE — the client's secret for this run.
# Zitadel never shows a secret twice, so the one in SECRET_FILE is kept when
# ID_FILE names this client and the token endpoint accepts the pair; a new one
# is minted when either file is missing, the id is another client's, the
# endpoint refuses the secret, or ROTATE_CLIENT_SECRET=1. An answer without an
# OAuth error fails the run rather than rotating a secret it could not check.
secret_for() {
    _why=""
    if [ "$ROTATE_CLIENT_SECRET" = 1 ]; then
        _why="rotation requested"
    elif [ ! -s "$3" ] || [ ! -s "$4" ]; then
        _why="no stored secret"
    elif [ "$(tr -d '[:space:]' <"$3")" != "$APP_CLIENT_ID" ]; then
        _why="the stored client id is another client's"
    else
        _stored="$(tr -d '[:space:]' <"$4")"
        token_endpoint_error "$APP_CLIENT_ID" "$_stored"
        case "$TOKEN_ERROR" in
            invalid_client) _why="the stored secret is refused" ;;
            "") die "the token endpoint answered $STATUS without an OAuth error for $1: $(body_head)" ;;
            *)
                APP_CLIENT_SECRET="$_stored"
                log "app $1 kept its secret"
                return
                ;;
        esac
    fi
    regenerate_secret "$2"
    log "app $1 got a new secret ($_why)"
}

# find_app NAME — the id and client id of the project's app NAME, in APP_ID and
# APP_CLIENT_ID; both empty when there is none.
find_app() {
    api POST "/projects/$PROJECT_ID/apps/_search" \
        "{\"queries\":[{\"nameQuery\":{\"name\":\"$1\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}"
    ok || die "searching for app $1 failed ($STATUS): $(body_head)"
    APP_ID="$(field id)"
    APP_CLIENT_ID="$(field clientId)"
}

# provision_app NAME REDIRECT_URIS POST_LOGOUT_URIS — creates the client, or
# brings the one that already exists to these settings and keeps or replaces
# its secret (secret_for). Leaves the client id and secret in APP_CLIENT_ID
# and APP_CLIENT_SECRET.
#
# Creates first, unlike configure_google which searches first. Measured on v3.4.9:
# a duplicate POST apps/oidc returns 409, a duplicate POST idps/google returns 200
# and a second provider. The server guards app names, so create-first is correct
# here and cheaper; don't "fix" it into find-before-create for symmetry.
provision_app() {
    _name="$1"
    _config="$(oidc_config_body "$2" "$3")"
    api POST "/projects/$PROJECT_ID/apps/oidc" "$(create_body "$_name" "$_config")"
    APP_CLIENT_ID="$(field clientId)"
    APP_CLIENT_SECRET="$(field clientSecret)"
    if [ -n "$APP_CLIENT_ID" ]; then
        log "app $_name created"
    else
        # Distinguish "already there" from a real fault; reporting both as the
        # former hides a broken create until it surfaces far from here.
        benign || die "app $_name create failed ($STATUS): $(body_head)"
        find_app "$_name"
        [ -n "$APP_ID" ] || die "could not resolve app $_name: $(body_head)"
        _app_id="$APP_ID"
        # Without this update the client keeps the addresses it was created
        # with: a client first made with a laptop's or a placeholder origin
        # would refuse every sign-in from the real one.
        api PUT "/projects/$PROJECT_ID/apps/$_app_id/oidc_config" "$_config"
        if ok; then
            log "app $_name already existed; updated its settings"
        elif benign; then
            log "app $_name already existed with these settings"
        else
            die "app $_name update failed ($STATUS): $(body_head)"
        fi
        secret_for "$_name" "$_app_id" "$ID_FILE" "$SECRET_FILE"
    fi
    [ -n "$APP_CLIENT_ID" ] && [ -n "$APP_CLIENT_SECRET" ] ||
        die "could not resolve client id/secret for $_name: $(body_head)"
}

provision_identity_app() {
    provision_app "$APP_NAME" "$AUTH_ISSUER/callback ${EXTRA_REDIRECT_URIS:-}" ""
    CLIENT_ID="$APP_CLIENT_ID"
    CLIENT_SECRET="$APP_CLIENT_SECRET"
    log "app $APP_NAME client_id=$CLIENT_ID redirect=$AUTH_ISSUER/callback"
}

# The console ends the person's session at Zitadel when they sign out, and
# Zitadel sends the browser back only to an address registered for it.
provision_console_app() {
    provision_app "$CONSOLE_APP_NAME" \
        "$CONSOLE_ISSUER_URL/auth/callback ${EXTRA_CONSOLE_REDIRECT_URIS:-}" \
        "$CONSOLE_ISSUER_URL/signed-out"
    CLIENT_ID="$APP_CLIENT_ID"
    CLIENT_SECRET="$APP_CLIENT_SECRET"
    log "app $CONSOLE_APP_NAME client_id=$CLIENT_ID redirect=$CONSOLE_ISSUER_URL/auth/callback"
}

# remove_console_app — developers only. Before publishers had an instance of
# their own, the console's client lived in this one; it must not stay, or an
# account here could still sign in to the console. Its files in OUT_DIR, where
# stacks without a console directory kept them, go with it.
remove_console_app() {
    find_app "$CONSOLE_APP_NAME"
    if [ -n "$APP_ID" ]; then
        api DELETE "/projects/$PROJECT_ID/apps/$APP_ID"
        ok || die "removing app $CONSOLE_APP_NAME failed ($STATUS): $(body_head)"
        log "app $CONSOLE_APP_NAME removed: publishers sign in through their own instance"
    else
        log "no $CONSOLE_APP_NAME app in this instance"
    fi
    for _file in "$OUT_DIR/console_client_id" "$OUT_DIR/console_client_secret"; do
        if [ -e "$_file" ]; then
            rm -f "$_file"
            log "removed $_file"
        fi
    done
}

# ── 4. the publisher instance's admin, and 5. local users ───────────────────

# import_human LOGIN EMAIL FIRST LAST PASSWORD — creates a local account with a
# verified email, or leaves one that already exists as it is: the password is
# set only here, when the account is created. Leaves the account's id in
# USER_ID, searched for when the account already existed.
import_human() {
    api POST /users/human/_import "$(cat <<JSON
{
  "userName": "$1",
  "profile": {
    "firstName": "$3", "lastName": "$4",
    "displayName": "$3 $4", "preferredLanguage": "en"
  },
  "email": {"email": "$2", "isEmailVerified": true},
  "password": "$5",
  "passwordChangeRequired": false
}
JSON
    )"
    if ok; then
        USER_ID="$(field userId)"
        log "user $1 created"
    elif benign; then
        api POST /users/_search \
            "{\"queries\":[{\"userNameQuery\":{\"userName\":\"$1\",\"method\":\"TEXT_QUERY_METHOD_EQUALS\"}}]}"
        USER_ID="$(field id)"
        log "user $1 already exists; its password is left as it is"
    else
        die "$1 import failed ($STATUS): $(body_head)"
    fi
}

# import_admin — the human IAM_OWNER operators sign in to this instance's
# Zitadel console with, as the developer instance's zadmin from its first
# start.
import_admin() {
    import_human "$ADMIN_USER" "$ADMIN_USER@$HOST_HDR" Z Admin "$ADMIN_PASSWORD"
    [ -n "$USER_ID" ] || die "could not resolve the id of $ADMIN_USER: $(body_head)"
    admin POST /members "{\"userId\":\"$USER_ID\",\"roles\":[\"IAM_OWNER\"]}"
    benign || die "making $ADMIN_USER IAM_OWNER failed ($STATUS): $(body_head)"
    api POST /orgs/me/members "{\"userId\":\"$USER_ID\",\"roles\":[\"ORG_OWNER\"]}"
    benign || die "making $ADMIN_USER ORG_OWNER failed ($STATUS): $(body_head)"
    log "admin $ADMIN_USER holds IAM_OWNER and ORG_OWNER"
}

# import_user LOGIN FIRST LAST — a local user whose login is its email.
import_user() { import_human "$1" "$1" "$2" "$3" "$ALICE_PASSWORD"; }

# ── 6. Google federation (developers) ───────────────────────────────────────

google_body() {
    cat <<JSON
{
  "name": "$IDP_NAME",
  "clientId": "$GOOGLE_CLIENT_ID",
  "clientSecret": "$GOOGLE_CLIENT_SECRET",
  "scopes": ["openid", "profile", "email"],
  "providerOptions": {
    "isLinkingAllowed": true,
    "isCreationAllowed": true,
    "isAutoCreation": true,
    "isAutoUpdate": true,
    "autoLinking": "AUTO_LINKING_OPTION_EMAIL"
  }
}
JSON
}

# find_google — id of an existing google IdP, or empty. Searched BEFORE creating:
# Zitadel accepts a second provider under the same name, so a re-run would stack
# duplicate buttons on the login page. The match ignores case: earlier runs
# registered the provider under a lower-case name, and an exact match would miss
# those and create a second provider instead of renaming the one already there.
find_google() {
    api POST /idps/templates/_search \
        "{\"queries\":[{\"idpNameQuery\":{\"name\":\"$IDP_NAME\",\"method\":\"TEXT_QUERY_METHOD_EQUALS_IGNORE_CASE\"}}]}"
    field id
}

# link_to_login_policy IDP_ID — the step that actually puts the provider on the
# login page. Creating the IdP alone leaves it configured but invisible.
link_to_login_policy() {
    api POST /policies/login/idps "{\"idpId\":\"$1\",\"ownerType\":\"IDP_OWNER_TYPE_ORG\"}"
    if ok; then
        log "google IdP linked into the login policy"
    elif benign; then
        log "google IdP already linked into the login policy"
    else
        die "linking the google IdP failed ($STATUS): $(body_head)"
    fi
}

configure_google() {
    if [ -z "${GOOGLE_CLIENT_ID:-}" ] || [ -z "${GOOGLE_CLIENT_SECRET:-}" ]; then
        log "SKIP google federation — GOOGLE_CLIENT_ID/GOOGLE_CLIENT_SECRET unset."
        if [ "$IMPORT_DEMO_USERS" != "0" ]; then
            log "     Sign-in still works with alice@acme.local; set both variables"
        else
            log "     Sign-in is self-registration only; set both variables"
        fi
        log "     and re-run to add Google federation."
        return
    fi
    _idp_id="$(find_google)"
    if [ -n "$_idp_id" ]; then
        # UPDATE rather than skip. The credentials in the environment are the
        # operator's current intent, so a re-run after rotating the Google secret —
        # or after replacing placeholder credentials with real ones — must take
        # effect. Skipping would leave the old secret in place and fail only later,
        # at Google, with nothing here having reported a problem.
        api PUT "/idps/google/$_idp_id" "$(google_body)"
        benign || die "google IdP update failed ($STATUS): $(body_head)"
        log "google IdP updated id=$_idp_id"
    else
        api POST /idps/google "$(google_body)"
        _idp_id="$(field id)"
        [ -n "$_idp_id" ] || die "google IdP create returned no id: $(body_head)"
        log "google IdP created id=$_idp_id"
    fi
    link_to_login_policy "$_idp_id"
    log "reminder: Google's authorized redirect URI must be EXACTLY"
    log "     $ISSUER/ui/login/login/externalidp/callback"
}

# ── 7. hand the client to its service ───────────────────────────────────────

# write_creds — the side's two files, written only when they would change, so
# a run that kept the secret leaves them as they were.
write_creds() {
    if [ "$(cat "$ID_FILE" 2>/dev/null || true)" = "$CLIENT_ID" ] &&
        [ "$(cat "$SECRET_FILE" 2>/dev/null || true)" = "$CLIENT_SECRET" ]; then
        log "$ID_FILE and $SECRET_FILE unchanged"
        return
    fi
    mkdir -p "$CREDS_DIR"
    printf '%s\n' "$CLIENT_ID" >"$ID_FILE"
    printf '%s\n' "$CLIENT_SECRET" >"$SECRET_FILE"
    log "wrote $ID_FILE and $SECRET_FILE"
}

# The ONE definition of the export format — a provisioning run and --emit-exports
# both come through here, so they cannot disagree. Reads the files, not the
# in-memory values, so both paths are identical. Only these lines go to stdout
# (logging is on stderr), which is what makes `eval "$(...)"` safe.
print_exports() {
    _id="$(cat "$ID_FILE" 2>/dev/null || true)"
    _secret="$(cat "$SECRET_FILE" 2>/dev/null || true)"
    if [ -z "$_id" ] || [ -z "$_secret" ]; then
        die "no credentials at $CREDS_DIR — run \`make zitadel-up\` first"
    fi
    printf 'export %s_OIDC_ISSUER=%s\n' "$EXPORT_PREFIX" "$ISSUER"
    printf 'export %s_OIDC_CLIENT_ID=%s\n' "$EXPORT_PREFIX" "$_id"
    printf 'export %s_OIDC_CLIENT_SECRET=%s\n' "$EXPORT_PREFIX" "$_secret"
    printf 'export %s_OIDC_SCOPES="%s"\n' "$EXPORT_PREFIX" "$SCOPES"
    if [ "$ROLE" = publishers ]; then
        printf 'export CONSOLE_OIDC_REDIRECT_URL=%s/auth/callback\n' "$CONSOLE_ISSUER_URL"
    fi
}

# --emit-exports re-prints an earlier run's credentials without touching Zitadel:
# no token, no readiness wait, no provisioning. Everything below is skipped.
if [ "$MODE" = "emit" ]; then
    print_exports
    exit 0
fi

read_pat
wait_ready
check_instance
create_project
set_login_policy
apply_branding
case "$ROLE" in
    developers)
        provision_identity_app
        if [ "$REMOVE_CONSOLE_CLIENT" != 0 ]; then
            remove_console_app
        else
            log "SKIP removing $CONSOLE_APP_NAME — REMOVE_CONSOLE_CLIENT=$REMOVE_CONSOLE_CLIENT"
        fi
        if [ "$IMPORT_DEMO_USERS" != "0" ]; then
            import_user alice@acme.local Alice Subscriber
            import_user bob@acme.local Bob Onlooker
            import_user carol@acme.local Carol Buyer
        else
            log "SKIP demo users — IMPORT_DEMO_USERS=0"
        fi
        configure_google
        ;;
    publishers)
        provision_console_app
        import_admin
        if [ "$IMPORT_DEMO_USERS" != "0" ]; then
            import_user alice@acme.local Alice Subscriber
        else
            log "SKIP demo users — IMPORT_DEMO_USERS=0"
        fi
        log "SKIP google federation — publishers' Google sign-in is configured separately"
        ;;
esac
write_creds
log "done"
print_exports
