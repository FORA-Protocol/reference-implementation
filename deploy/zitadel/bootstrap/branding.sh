# The look and the language of one instance's sign-in pages and emails.
# Sourced by bootstrap.sh after the login policy, with ROLE set; everything here
# is instance-wide (the Admin API), so it covers every organization in the
# instance and every email Zitadel sends from it.
#
# Each value has one definition, in the case below:
#   developers  light pages and emails, dark FORA wordmark, Instrument Sans,
#               "FORA Developers"
#   publishers  the FORA website's palette with native dark controls,
#               the website wordmark as logo, the white FORA mark as icon,
#               Instrument Sans, "FORA Publishers"
#
# Steps, each logging "applied" or "unchanged". A run against an instance that
# already looks like this writes nothing:
#   1. English only: Zitadel would otherwise pick the language from the browser
#      (falling back to the first it knows, Bulgarian), and the titles and
#      email texts below exist in English.
#   2. the label policy's colors; Zitadel answers an unchanged update with an
#      error (INSTANCE-28fHe), which reads as unchanged.
#   3. logo, icon and font: each uploaded only when the instance's copy differs
#      from the file here (Zitadel stores every upload as a new object).
#   4. activation of the previewed policy, only when it differs from the active
#      one (an activation is always written).
#   5. the title of the sign-in page and of the two sign-up pages.
#   6. the text of the four emails a person can get — account
#      initialization (sent on sign-up), email verification, password reset,
#      password changed: FORA wording for each audience.
#
# No terms of service or privacy links: Zitadel's default privacy policy has
# none, so the sign-up page shows no acceptance checkbox. The "Powered by
# ZITADEL" mark stays as Zitadel shows it.

BRANDING_DIR="$(dirname "$0")/branding"

case "$ROLE" in
    developers)
        SIDE_NAME="FORA Developers"
        ACCOUNT="FORA developer"
        AUDIENCE="developer"
        EMAIL_DESTINATION="your FORA developer account"
        COLORS_LIGHT="#4f6df5 #fafafa #cd3d56 #12121a"
        COLORS_DARK="#2073c4 #111827 #ff3b5b #ffffff"
        THEME_MODE="THEME_MODE_LIGHT"
        LOGO_LIGHT="fora-wordmark-dark.png"
        LOGO_DARK="fora-wordmark.png"
        ICON_LIGHT="fora-mark-black.png"
        ICON_DARK="fora-mark-white.png"
        ;;
    publishers)
        SIDE_NAME="FORA Publishers"
        ACCOUNT="FORA publisher console"
        AUDIENCE="publisher"
        EMAIL_DESTINATION="the FORA publisher console"
        # FORA website colors. Caddy adds its gradient, secondary text and wordmark.
        # A dark palette alone leaves light-mode borders/buttons nearly black;
        # select Zitadel's native dark controls regardless of browser preference.
        # Emails use the light set: the console's panel background and text.
        COLORS_LIGHT="#4f6df5 #12121a #ff8a80 #e8e8f0"
        COLORS_DARK="#4f6df5 #0a0a0f #ff8a80 #e8e8f0"
        THEME_MODE="THEME_MODE_DARK"
        LOGO_LIGHT="fora-wordmark.png"
        LOGO_DARK="fora-wordmark.png"
        ICON_LIGHT="fora-mark-white.png"
        ICON_DARK="fora-mark-white.png"
        ;;
esac

# The instance's copy of an asset, for the comparison with the file here.
ASSET_TMP="$WORK_DIR/asset"

# ── 1. languages ────────────────────────────────────────────────────────────

apply_languages() {
    admin GET /restrictions
    ok || die "reading the instance's restrictions failed ($STATUS): $(body_head)"
    if grep -q '"allowedLanguages":\["en"\]' "$RESP"; then
        log "languages unchanged (English only)"
    else
        admin PUT /restrictions '{"allowedLanguages":{"list":["en"]}}'
        ok || die "restricting the instance to English failed ($STATUS): $(body_head)"
        log "languages applied (English only)"
    fi
    admin GET /languages/default
    if [ "$(field language)" = en ]; then
        log "default language unchanged (en)"
    else
        admin PUT /languages/default/en
        ok || die "setting the default language failed ($STATUS): $(body_head)"
        log "default language applied (en)"
    fi
}

# ── 2. colors ───────────────────────────────────────────────────────────────

apply_colors() {
    # shellcheck disable=SC2086 # the lists are four words each, split on purpose
    set -- $COLORS_LIGHT $COLORS_DARK
    admin PUT /policies/label "$(cat <<JSON
{
  "primaryColor": "$1", "backgroundColor": "$2", "warnColor": "$3", "fontColor": "$4",
  "primaryColorDark": "$5", "backgroundColorDark": "$6", "warnColorDark": "$7", "fontColorDark": "$8",
  "hideLoginNameSuffix": false, "disableWatermark": false, "themeMode": "$THEME_MODE"
}
JSON
    )"
    if ok; then
        log "label colors applied"
    elif grep -q 'INSTANCE-28fHe' "$RESP"; then
        log "label colors unchanged"
    else
        die "updating the label policy failed ($STATUS): $(body_head)"
    fi
}

# ── 3. logo, icon and font ──────────────────────────────────────────────────

# fetch_asset PATH — the instance's previewed copy into $ASSET_TMP, its HTTP
# status in $STATUS.
fetch_asset() { request "$ASSET_TMP" GET "$BASE/assets/v1/instance/policy/label/$1/_preview"; }

# sync_asset PATH FILE TYPE — uploads FILE as the instance's asset at PATH
# unless the instance already holds exactly these bytes.
sync_asset() {
    _file="$BRANDING_DIR/$2"
    [ -r "$_file" ] || die "missing branding file $_file"
    fetch_asset "$1"
    if [ "$STATUS" = 200 ] &&
        [ "$(sha256sum <"$ASSET_TMP" | cut -d' ' -f1)" = "$(sha256sum <"$_file" | cut -d' ' -f1)" ]; then
        log "$1 unchanged ($2)"
        return
    fi
    # Zitadel reads the type from the bytes; the explicit type keeps curl from
    # guessing one from the name.
    request "$RESP" POST "$BASE/assets/v1/instance/policy/label/$1" -F "file=@$_file;type=$3"
    ok || die "uploading $2 as $1 failed ($STATUS): $(body_head)"
    log "$1 applied ($2)"
}

apply_assets() {
    sync_asset logo "$LOGO_LIGHT" image/png
    sync_asset logo/dark "$LOGO_DARK" image/png
    sync_asset icon "$ICON_LIGHT" image/png
    sync_asset icon/dark "$ICON_DARK" image/png
    sync_asset font instrument-sans.ttf font/ttf
}

# ── 4. activation ───────────────────────────────────────────────────────────

# policy_fields — the label policy in $RESP, one "name:value" line per field
# that decides the look. Every value is a flat string or boolean, and a field
# at its zero value is absent from the answer, which prints as empty.
POLICY_FIELDS="primaryColor backgroundColor warnColor fontColor primaryColorDark backgroundColorDark
warnColorDark fontColorDark hideLoginNameSuffix disableWatermark themeMode logoUrl iconUrl logoUrlDark
iconUrlDark fontUrl"
policy_fields() {
    for _f in $POLICY_FIELDS; do
        printf '%s:%s\n' "$_f" "$(grep -oE "\"$_f\":(\"[^\"]*\"|true|false)" "$RESP" | head -n 1 | cut -d: -f2-)"
    done
}

activate_if_needed() {
    admin GET /policies/label/_preview
    ok || die "reading the previewed label policy failed ($STATUS): $(body_head)"
    _preview="$(policy_fields)"
    admin GET /policies/label
    ok || die "reading the active label policy failed ($STATUS): $(body_head)"
    if [ "$(policy_fields)" = "$_preview" ]; then
        log "label policy unchanged (the active policy is the previewed one)"
        return
    fi
    admin POST /policies/label/_activate '{}'
    ok || die "activating the label policy failed ($STATUS): $(body_head)"
    log "label policy applied (activated)"
}

# ── 5. page titles ──────────────────────────────────────────────────────────

# text_title SECTION — the title inside one section of a custom-text answer in
# $RESP. A section's values are flat strings, so its object holds no braces.
text_title() { sed -n "s/.*\"$1\":{\([^}]*\)}.*/\1/p" "$RESP" | string_of title; }

apply_titles() {
    admin GET /text/login/en
    ok || die "reading the sign-in texts failed ($STATUS): $(body_head)"
    if [ "$(text_title loginText)" = "$SIDE_NAME" ] &&
        [ "$(text_title registrationOptionText)" = "$SIDE_NAME" ] &&
        [ "$(text_title registrationUserText)" = "$SIDE_NAME" ]; then
        log "page titles unchanged ($SIDE_NAME)"
        return
    fi
    admin PUT /text/login/en "$(cat <<JSON
{
  "loginText": {"title": "$SIDE_NAME"},
  "registrationOptionText": {"title": "$SIDE_NAME"},
  "registrationUserText": {"title": "$SIDE_NAME"}
}
JSON
    )"
    ok || die "setting the page titles failed ($STATUS): $(body_head)"
    log "page titles applied ($SIDE_NAME)"
}

# ── 6. email texts ──────────────────────────────────────────────────────────

# apply_message TYPE — reconciles every text field; Zitadel supplies the action URL.
apply_message() {
    case "$1" in
        init)
            _subject="Welcome to $SIDE_NAME"
            _pre_header="Confirm your email and set up your $AUDIENCE account."
            _text="A $ACCOUNT account has been created for you. Use the button below to confirm your email address and finish setting up your account. Your sign-in username is {{.PreferredLoginName}}. Verification code: {{.Code}}. If you did not request this account, you can ignore this email."
            _button="Set up your account"
            ;;
        verifyemail)
            _subject="$SIDE_NAME: Verify your email"
            _pre_header="Confirm your email address for $EMAIL_DESTINATION."
            _text="Please confirm this email address for your $ACCOUNT account. Use the button below to verify your address. Verification code: {{.Code}}. If you did not request this, you can ignore this email."
            _button="Verify email address"
            ;;
        passwordreset)
            _subject="$SIDE_NAME: Reset your password"
            _pre_header="Choose a new password for your $AUDIENCE account."
            _text="We received a request to reset the password for your $ACCOUNT account. Use the button below to choose a new password. Reset code: {{.Code}}. If you did not request this, you can ignore this email. Your password will stay the same."
            _button="Reset your password"
            ;;
        password_change)
            _subject="$SIDE_NAME: Your password has changed"
            _pre_header="The password for your $AUDIENCE account was updated."
            _text="The password for your $ACCOUNT account has been changed. If you made this change, no action is needed. If you did not, go to the sign-in page and reset your password immediately."
            _button="Go to sign-in"
            ;;
    esac
    _message_fields="title subject preHeader greeting text buttonText footerText"
    _message="$(cat <<JSON
{"title":"$_subject","subject":"$_subject","preHeader":"$_pre_header","greeting":"Hello {{.DisplayName}},","text":"$_text","buttonText":"$_button","footerText":"$SIDE_NAME"}
JSON
    )"
    admin GET "/text/message/$1/en"
    ok || die "reading the $1 email's text failed ($STATUS): $(body_head)"
    _changed=false
    for _f in $_message_fields; do
        if [ "$(field "$_f")" != "$(printf '%s' "$_message" | string_of "$_f")" ]; then
            _changed=true
            break
        fi
    done
    if [ "$_changed" = false ]; then
        log "$1 email unchanged ($_subject)"
        return
    fi
    admin PUT "/text/message/$1/en" "$_message"
    ok || die "setting the $1 email's text failed ($STATUS): $(body_head)"
    log "$1 email applied ($_subject)"
}

apply_messages() {
    apply_message init
    apply_message verifyemail
    apply_message passwordreset
    apply_message password_change
}

apply_branding() {
    apply_languages
    apply_colors
    apply_assets
    activate_if_needed
    apply_titles
    apply_messages
}
