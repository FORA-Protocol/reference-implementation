#!/usr/bin/env bash
# A stand-in for curl as deploy/zitadel/bootstrap/lib.sh runs it, for the guard
# tests of the scripts in that directory. A test puts it on PATH as `curl`.
#
# Each request is appended to ${STUB_DIR}/requests.json as one JSON line:
# method, path (without scheme and host), host (the Host header), bearer,
# user (the client id of a Basic header), org (x-zitadel-orgid), form (the file
# a -F upload names), status (what the stub answered) and body (parsed when it
# is JSON, the form fields joined with & otherwise).
#
# The answer comes from `route`, a function the file named by STUB_ROUTES
# defines. It sees the variables above (and out, the -o file) and answers with
# `answer STATUS BODY` or `answer_file STATUS FILE`.
#
# Understands what lib.sh passes: -s, -o FILE, -w FORMAT, -X METHOD,
# -H "Name: value", -H @FILE (headers, one per line), -d FIELD,
# --data-binary @- (the body on standard input), -F file=@PATH;type=TYPE and
# the URL. It refuses anything else, and refuses a credential or a JSON body on
# the command line, where every user of the host could read it: lib.sh sends
# those in a header file and on standard input.
set -euo pipefail

refuse() { echo "curl stub: $1" >&2; exit 2; }

method=GET url="" out="" data="" host="" bearer="" user="" org="" form=""
header() {
    case "$1" in
        "Host: "*) host="${1#Host: }" ;;
        "Authorization: Bearer "*) bearer="${1#Authorization: Bearer }" ;;
        "Authorization: Basic "*) user="$(printf '%s' "${1#Authorization: Basic }" | base64 -d)"; user="${user%%:*}" ;;
        "x-zitadel-orgid: "*) org="${1#x-zitadel-orgid: }" ;;
        "Content-Type: "*) ;;
        *) refuse "unexpected header '$1'" ;;
    esac
}
while [ $# -gt 0 ]; do
    case "$1" in
        -s) ;;
        -o) out="$2"; shift ;;
        -w) shift ;;
        -X) method="$2"; shift ;;
        -H)
            case "$2" in
                @*)
                    while IFS= read -r line || [ -n "${line}" ]; do header "${line}"; done < "${2#@}" ;;
                Authorization:*) refuse "a credential on the command line: -H '$2'" ;;
                *) header "$2" ;;
            esac
            shift ;;
        -d)
            case "$2" in "{"*) refuse "a JSON body on the command line: -d '$2'" ;; esac
            data="${data:+${data}&}$2"; shift ;;
        --data-binary)
            [ "$2" = @- ] || refuse "--data-binary takes @- only, not '$2'"
            data="$(cat)"; shift ;;
        -F) form="${2#file=@}"; form="${form%%;*}"; shift ;;
        http://* | https://*) url="$1" ;;
        *) refuse "unexpected argument '$1'" ;;
    esac
    shift
done
path="/${url#http*://*/}"

answered=""
record() {
    jq -cn --arg m "${method}" --arg p "${path}" --arg h "${host}" --arg b "${bearer}" --arg u "${user}" \
        --arg o "${org}" --arg f "${form}" --arg s "${answered}" --arg d "${data}" \
        '{method: $m, path: $p, host: $h, bearer: $b, user: $u, org: $o, form: $f, status: $s,
          body: ($d | if . == "" then null else (try fromjson catch .) end)}' >> "${STUB_DIR}/requests.json"
}
trap record EXIT
answer() { answered="$1"; printf '%s' "$2" > "${out}"; printf '%s' "$1"; }
answer_file() { answered="$1"; cp "$2" "${out}"; printf '%s' "$1"; }

# shellcheck source=/dev/null
. "${STUB_ROUTES:?STUB_ROUTES names the file that defines the route function}"
route
