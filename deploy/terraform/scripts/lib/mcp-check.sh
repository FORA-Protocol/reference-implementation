# The MCP endpoint's OAuth discovery, checked the way an MCP client checks it.
# Sourced by smoke.sh after lib/staging-env.sh (tf_out, tf_value).
#
# An MCP client that meets the bearer gate's 401 reads the resource_metadata
# URL from WWW-Authenticate, fetches the protected-resource document there,
# and compares the document's `resource` with the origin it connected to. On
# a mismatch the client refuses to start sign-in. The document advertises the
# service's token audience, which defaults to its issuer — so a deployment
# that serves /mcp under a hostname of its own advertises the wrong origin
# until IDENTITY_TOKEN_AUDIENCE names the MCP one. The two functions below
# make that mismatch a smoke failure instead of a client-side error.

# mcp_origin — the origin MCP clients connect to: the stack's mcp_url output,
# or identity_url on a stack applied before that output existed. Only an
# output that is absent falls back; a state Terraform cannot read fails the
# read (tf_value pipes through jq under pipefail), and the failure propagates.
mcp_origin() {
    local url
    url="$(tf_value mcp_url)" || return 1
    if [ -z "${url}" ]; then
        url="$(tf_out identity_url)" || return 1
    fi
    printf '%s\n' "${url}"
}

# mcp_bearer_gate ORIGIN — GET ORIGIN/mcp without a bearer: the answer must
# be 401 (the tools, which sign FORA calls with a developer's custodied key,
# are gated) with a WWW-Authenticate header naming a resource_metadata URL.
# Prints that URL. The service derives it from its issuer, so it may name the
# Identity hostname even when ORIGIN is the MCP one; the caller checks the
# document there all the same.
mcp_bearer_gate() {
    local origin="$1" headers code challenge url
    headers="$(curl -s -o /dev/null -D - "${origin}/mcp")" || {
        echo "mcp bearer gate: ${origin}/mcp unreachable" >&2
        return 1
    }
    # The LAST status line: through an HTTP CONNECT proxy curl prints the
    # proxy's "200 Connection established" before the endpoint's own answer.
    code="$(printf '%s\n' "${headers}" | tr -d '\r' | sed -n 's#^HTTP/[0-9.]* \([0-9][0-9][0-9]\).*#\1#p' | tail -n 1)"
    if [ "${code}" != "401" ]; then
        echo "mcp bearer gate: ${origin}/mcp returned ${code:-no status}, wanted 401" >&2
        return 1
    fi
    challenge="$(printf '%s\n' "${headers}" | tr -d '\r' | grep -i '^www-authenticate:' | tail -n 1 || true)"
    if [ -z "${challenge}" ]; then
        echo "mcp bearer gate: ${origin}/mcp answered 401 without a WWW-Authenticate header — a client cannot discover where to sign in" >&2
        return 1
    fi
    url="$(printf '%s' "${challenge}" | sed -n 's/.*resource_metadata="\([^"]*\)".*/\1/p')"
    case "${url}" in
        https://*|http://*) ;;
        *)
            echo "mcp bearer gate: WWW-Authenticate carries no usable resource_metadata URL: ${challenge#*: }" >&2
            return 1 ;;
    esac
    printf '%s\n' "${url}"
}

# check_mcp_resource LABEL URL ORIGIN — GET the protected-resource document at
# URL: it must answer 200 with a JSON `resource` equal to ORIGIN, exactly.
check_mcp_resource() {
    local label="$1" url="$2" origin="$3" answer code body got
    answer="$(curl -s -w '\n%{http_code}' "${url}")" || {
        echo "${label}: ${url} unreachable" >&2
        return 1
    }
    code="${answer##*$'\n'}"
    body="${answer%$'\n'*}"
    if [ "${code}" != "200" ]; then
        echo "${label}: ${url} returned ${code}, wanted 200" >&2
        return 1
    fi
    got="$(printf '%s' "${body}" | jq -er '.resource' 2>/dev/null)" || {
        echo "${label}: ${url} returned no JSON document with a resource field" >&2
        return 1
    }
    if [ "${got}" != "${origin}" ]; then
        echo "${label}: ${url} advertises resource ${got}, but MCP clients connect to ${origin} — a client refuses to sign in when these differ; set IDENTITY_TOKEN_AUDIENCE to the origin clients use" >&2
        return 1
    fi
    echo "${label}: ok (resource ${got})"
}
