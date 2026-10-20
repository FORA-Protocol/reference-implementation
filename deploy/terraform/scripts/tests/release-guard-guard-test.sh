#!/usr/bin/env bash
# Prove release-guard.sh fails closed, with curl stubbed on PATH: 404 is the
# only answer that lets publication proceed (exit 0); 200 refuses (published),
# and so does the 302 an object-storage redirect answers for an existing file;
# 401, 403, 500, a timeout (000 and curl exit 28) and a non-numeric status all
# refuse; the request carries the job token header and a time limit; and with
# no CI_JOB_TOKEN the guard exits 2 without asking anything. Each refusal
# case is asserted on its own, so a guard rewritten to treat one of those
# errors as "absent" fails this test on that case.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/release-guard.sh"
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "release-guard guard test FAILED: $1" >&2; exit 1; }

# Stub curl: records its arguments, prints the staged status text (what
# -w '%{http_code}' would print) and exits with the staged code.
mkdir -p "${work}/bin"
cat > "${work}/bin/curl" <<STUB
#!/usr/bin/env bash
printf '%s\n' "\$*" > "${work}/curl-args"
printf '%s' "\$(cat "${work}/status")"
exit "\$(cat "${work}/exit")"
STUB
chmod +x "${work}/bin/curl"

url="https://gitlab.example/api/v4/projects/1/packages/generic/release/v1.2.3/release-manifest.json"
run() { # <status text> <curl exit> → exit status; stdout+stderr in $out
    printf '%s' "$1" > "${work}/status"
    echo "$2" > "${work}/exit"
    rm -f "${work}/curl-args"
    out="$(PATH="${work}/bin:${PATH}" CI_JOB_TOKEN=job-token-1 "${SCRIPT}" --url "${url}" 2>&1)" && return 0 || return $?
}

# Unpublished: exactly 404 → 0, and the request was made the right way.
run 404 0 || fail "a 404 was refused (rc=$?): ${out}"
grep -q 'unpublished' <<<"${out}" || fail "the 404 outcome was not explained: ${out}"
grep -qF -- "-H JOB-TOKEN: job-token-1" "${work}/curl-args" || fail "the request did not carry the job token header: $(cat "${work}/curl-args")"
grep -qF -- "--max-time" "${work}/curl-args" || fail "the request has no time limit: $(cat "${work}/curl-args")"
grep -qF -- "${url}" "${work}/curl-args" || fail "the request did not target the manifest url: $(cat "${work}/curl-args")"

# Published: 200 → refused, and the message says so.
if run 200 0; then fail "an existing manifest (200) was not refused"; fi
grep -q 'already exists' <<<"${out}" || fail "the 200 refusal does not name the cause: ${out}"
if run 302 0; then fail "an existing manifest behind a redirect (302) was not refused"; fi
grep -q 'already exists' <<<"${out}" || fail "the 302 refusal does not say the version is published: ${out}"
grep -qE -- '(^| )(-[a-zA-Z]*L|--location)' "${work}/curl-args" && fail "the guard follows redirects; it only needs the status"

# Every non-answer refuses, each asserted on its own.
for case in "401 0" "403 0" "500 0" "502 0" "000 28" "000 7" " 0" "<html>garbage 0"; do
    status="${case% *}"; code="${case##* }"
    if run "${status}" "${code}"; then
        fail "HTTP status '${status}' (curl exit ${code}) was treated as unpublished"
    fi
    grep -q 'refusing to publish' <<<"${out}" || fail "status '${status}' refused without the fail-closed message: ${out}"
done

# No token: usage failure before any request.
rm -f "${work}/curl-args"
if out="$(PATH="${work}/bin:${PATH}" CI_JOB_TOKEN= "${SCRIPT}" --url "${url}" 2>&1)"; then
    fail "ran without CI_JOB_TOKEN"
fi
[ ! -f "${work}/curl-args" ] || fail "curl was called without a token"
if out="$(PATH="${work}/bin:${PATH}" CI_JOB_TOKEN=t "${SCRIPT}" 2>&1)"; then
    fail "ran without --url"
fi

echo "release-guard guard test passed (404 proceeds; 200 and 302 refused as published; 401/403/5xx/timeout/garbage refused; token header and time limit sent; no token is a usage failure)"
