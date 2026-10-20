#!/usr/bin/env bash
# Prove release-manifest.sh keeps its contract with docker stubbed on PATH:
# every field the header lists is present; each of the five digests is read
# back from the registry for <image-base>/<service>:<version> and the image
# reference is composed from it; the production service list names the four
# the stack runs; the artifact checksums are the files' real sha256; the SDK
# version is the go.mod pin; and a malformed digest from the registry fails
# the write, leaving no manifest behind.
#
# Run directly, or via test-terraform.sh (which wires it into the suite).
set -euo pipefail

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/release-manifest.sh"
command -v jq >/dev/null 2>&1 || { echo "missing: jq" >&2; exit 2; }
work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

fail() { echo "release-manifest guard test FAILED: $1" >&2; exit 1; }

# Stub docker: `buildx imagetools inspect <ref> --format ...` records the
# reference and answers a digest derived from it (distinct per service), or
# the staged garbage when asked to.
mkdir -p "${work}/bin"
cat > "${work}/bin/docker" <<STUB
#!/usr/bin/env bash
echo "\$*" >> "${work}/calls"
[ ! -f "${work}/garbage" ] || { echo "not-a-digest"; exit 0; }
printf 'sha256:%064d\n' "\$(printf '%s' "\$4" | cksum | cut -d' ' -f1)"
STUB
chmod +x "${work}/bin/docker"

printf 'worker bundle bytes\n' > "${work}/worker.mjs"
printf 'archive bytes\n' > "${work}/lambda-edge-inputs.tar.gz"
sha_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1; }

run() {
    rm -f "${work}/calls"
    out="$(PATH="${work}/bin:${PATH}" "${SCRIPT}" --version v1.2.3 --source-sha 0123abcd \
        --image-base reg.example/group/project --worker "${work}/worker.mjs" \
        --lambda-inputs "${work}/lambda-edge-inputs.tar.gz" --out "${work}/manifest.json" "$@" 2>&1)" && return 0 || return $?
}

run || fail "the write exited $?: ${out}"
m="${work}/manifest.json"
[ "$(jq -r .schema_version "${m}")" = 1 ] || fail "schema_version: $(cat "${m}")"
[ "$(jq -r .version "${m}")" = v1.2.3 ] || fail "version: $(cat "${m}")"
[ "$(jq -r .source_sha "${m}")" = 0123abcd ] || fail "source_sha: $(cat "${m}")"
[ "$(jq -r .protocol_sdk_module "${m}")" = github.com/FORA-Protocol/protocol ] || fail "protocol_sdk_module: $(cat "${m}")"
repo_root="$(cd "$(dirname "${SCRIPT}")/../../.." && pwd)"
pin="$(sed -n 's/^[[:space:]]*github\.com\/FORA-Protocol\/protocol[[:space:]]\{1,\}\(v[^[:space:]]*\).*/\1/p' "${repo_root}/go.mod" | head -n1)"
[ -n "${pin}" ] || fail "the test could not read the SDK pin from go.mod"
[ "$(jq -r .protocol_sdk_version "${m}")" = "${pin}" ] || fail "protocol_sdk_version '$(jq -r .protocol_sdk_version "${m}")' is not the go.mod pin ${pin}"
[ "$(jq -r '.production_services | join(" ")' "${m}")" = "exchange broker identity console" ] || fail "production_services: $(cat "${m}")"
[ "$(jq -r '.images | keys | join(" ")' "${m}")" = "broker console exchange identity onboarding" ] || fail "images keys: $(cat "${m}")"
for svc in exchange broker identity onboarding console; do
    ref="reg.example/group/project/${svc}:v1.2.3"
    grep -qF "buildx imagetools inspect ${ref} --format" "${work}/calls" || fail "${svc}: digest not read back from the registry for ${ref}: $(cat "${work}/calls")"
    expected="$(printf 'sha256:%064d' "$(printf '%s' "${ref}" | cksum | cut -d' ' -f1)")"
    [ "$(jq -r ".images.${svc}.digest" "${m}")" = "${expected}" ] || fail "${svc}: digest is not the registry's answer: $(jq -c ".images.${svc}" "${m}")"
    [ "$(jq -r ".images.${svc}.image" "${m}")" = "reg.example/group/project/${svc}@${expected}" ] || fail "${svc}: image reference not composed from the digest: $(jq -c ".images.${svc}" "${m}")"
done
[ "$(jq -r .cloudflare_worker.file "${m}")" = worker.mjs ] || fail "cloudflare_worker.file: $(cat "${m}")"
[ "$(jq -r .cloudflare_worker.sha256 "${m}")" = "$(sha_of "${work}/worker.mjs")" ] || fail "cloudflare_worker.sha256 is not the file's checksum"
[ "$(jq -r .lambda_edge_inputs.file "${m}")" = lambda-edge-inputs.tar.gz ] || fail "lambda_edge_inputs.file: $(cat "${m}")"
[ "$(jq -r .lambda_edge_inputs.sha256 "${m}")" = "$(sha_of "${work}/lambda-edge-inputs.tar.gz")" ] || fail "lambda_edge_inputs.sha256 is not the file's checksum"
jq -e '.published_at | test("^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")' "${m}" >/dev/null || fail "published_at: $(cat "${m}")"
grep -q 'onboarding: sha256:' <<<"${out}" || fail "the summary does not list the onboarding digest: ${out}"
grep -q 'console: sha256:' <<<"${out}" || fail "the summary does not list the console digest: ${out}"

# A malformed digest from the registry fails the write and leaves no manifest.
rm -f "${m}"; touch "${work}/garbage"
if run; then fail "a malformed digest was accepted: ${out}"; fi
grep -q 'malformed digest' <<<"${out}" || fail "the malformed digest was not named: ${out}"
[ ! -s "${m}" ] || fail "a manifest was written despite the malformed digest"
rm -f "${work}/garbage"

# A missing artifact is a usage failure before any registry call.
rm -f "${work}/calls"
if out="$(PATH="${work}/bin:${PATH}" "${SCRIPT}" --version v1.2.3 --source-sha 0123abcd --image-base reg.example/g/p \
        --worker "${work}/absent.mjs" --lambda-inputs "${work}/lambda-edge-inputs.tar.gz" --out "${m}" 2>&1)"; then
    fail "a missing worker bundle was accepted"
fi
[ ! -f "${work}/calls" ] || fail "the registry was asked although an artifact was missing"

echo "release-manifest guard test passed (every field; five digests read back and references composed; four production services; real checksums; go.mod pin; malformed digest and missing artifact refused)"
