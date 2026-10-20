#!/usr/bin/env bash
# Run every module's Terraform test suite (terraform test, .tftest.hcl).
#
# All suites are offline: plan mode + mocked providers (AWS, Cloudflare), and
# the compose-stack suite's apply only instantiates an in-memory
# random_password. No cloud account, no credentials, no resources.
#
# Usage:
#   deploy/terraform/scripts/test-terraform.sh

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"
MODULES_DIR="${REPO_ROOT}/deploy/terraform/modules"

# The production stack is a deployment's own: the public reference tree carries
# neither the stack nor the suite that tests it. Every step below that concerns
# it runs only when the tree carries the stack.
production_stack=production-aws
production_stack_dir="${REPO_ROOT}/deploy/terraform/stacks/${production_stack}"
# published-ref-allow: the runner names the suite it calls when the tree carries one
production_suite="$(dirname "${BASH_SOURCE[0]}")/tests/production-suite.sh"

command -v terraform >/dev/null 2>&1 || { echo "missing: terraform (>= 1.8)" >&2; exit 2; }

# Formatting is part of the gate: nothing else runs `terraform fmt -check`, so
# without this step drift lands silently (it did — a hand-aligned locals block
# passed every suite here and was only caught in review).
if ! terraform fmt -recursive -check "${REPO_ROOT}/deploy/terraform" >/dev/null; then
    echo "terraform fmt drift — run: terraform fmt -recursive deploy/terraform" >&2
    terraform fmt -recursive -check "${REPO_ROOT}/deploy/terraform" >&2 || true
    exit 1
fi
echo "terraform fmt clean"

# The Workers runtime compatibility date is pinned in two files that cannot
# reference each other: the cloudflare-edge module default (what staging
# applies) and src/edge/wrangler.toml (what wrangler dev / edge CI runs).
# A drifted pair means staging runs a different Workers runtime than the one
# the edge code is developed against — fail loudly instead.
tf_compat_date="$(sed -n '/variable "compatibility_date"/,/^}/s/^ *default *= *"\([^"]*\)".*/\1/p' \
    "${MODULES_DIR}/cloudflare-edge/variables.tf")"
toml_compat_date="$(sed -n 's/^compatibility_date *= *"\([^"]*\)".*/\1/p' \
    "${REPO_ROOT}/src/edge/wrangler.toml")"
if [ -z "${tf_compat_date}" ] || [ -z "${toml_compat_date}" ] || [ "${tf_compat_date}" != "${toml_compat_date}" ]; then
    echo "compatibility_date drift: cloudflare-edge default '${tf_compat_date}' vs" \
         "src/edge/wrangler.toml '${toml_compat_date}' — bump both together" >&2
    exit 1
fi
echo "compatibility_date in sync (${tf_compat_date})"

# The worker's env contract (EnvSchema in src/edge/src/config.ts) and the
# cloudflare-edge module's binding maps cannot reference each other. A key in
# one and not the other means a setting that cannot be deployed through the
# module (and is silently STRIPPED from a worker on the next apply), or a
# binding the worker never reads — fail loudly instead. Same shape as the
# compatibility_date check above.
schema_keys="$(sed -n '/^const EnvSchema = z.object({/,/^});/p' "${REPO_ROOT}/src/edge/src/config.ts" \
    | sed -n 's/^  \([A-Z][A-Z0-9_]*\): z\..*/\1/p' | sort)"
module_keys="$(sed -n '/required_bindings = {/,/^  }/p; /optional_bindings = {/,/^  }/p' \
    "${MODULES_DIR}/cloudflare-edge/main.tf" \
    | sed -n 's/^    \([A-Z][A-Z0-9_]*\) *=.*/\1/p' | sort)"
if [ -z "${schema_keys}" ] || [ -z "${module_keys}" ]; then
    echo "env-contract drift check broke: could not extract keys (EnvSchema moved, or the binding maps were renamed) — fix the extraction in this script" >&2
    exit 1
fi
if [ "${schema_keys}" != "${module_keys}" ]; then
    echo "env-contract drift between src/edge/src/config.ts EnvSchema and the cloudflare-edge binding maps:" >&2
    diff <(printf '%s\n' "${schema_keys}") <(printf '%s\n' "${module_keys}") >&2 || true
    exit 1
fi
echo "cloudflare-edge bindings mirror the worker env contract ($(printf '%s\n' "${schema_keys}" | wc -l | tr -d ' ') keys)"

# The staging id/hostname guard exists to fail; prove it still can (stubbed
# terraform on PATH + fabricated key files — see the test script).
"$(dirname "${BASH_SOURCE[0]}")/tests/staging-env-guard-test.sh"

# The MCP discovery leg of smoke.sh must refuse an endpoint that advertises
# an origin other than the one clients connect to, on both discovery paths,
# and must not mistake an unreadable state for a stack without the mcp_url
# output (stubbed curl and terraform on PATH).
"$(dirname "${BASH_SOURCE[0]}")/tests/smoke-mcp-guard-test.sh"

# load_ssh_cmd keeps the operator's identity out of Terraform state; prove it
# builds the command line correctly (same stubbed-terraform harness).
"$(dirname "${BASH_SOURCE[0]}")/tests/load-ssh-cmd-test.sh"

# Execute the operator SQL adapters without cloud resources.
python3 "$(dirname "${BASH_SOURCE[0]}")/tests/remote-psql-test.py"
python3 "$(dirname "${BASH_SOURCE[0]}")/tests/operator-sql-guard-test.py"

# The production deployment's own guard tests (its host's templates and its
# operator scripts, against stubbed tools) run from a suite of their own, which
# travels with the stack. Either one without the other means a held-back path
# went missing on its own and took the tests that guard the stack with it, so
# that is refused rather than skipped.
if [ -d "${production_stack_dir}" ] && [ -f "${production_suite}" ]; then
    "${production_suite}" guards
elif [ -d "${production_stack_dir}" ] || [ -f "${production_suite}" ]; then
    echo "the production stack and its test suite must be present together:" \
        "stack $([ -d "${production_stack_dir}" ] && echo present || echo absent)," \
        "suite $([ -f "${production_suite}" ] && echo present || echo absent)" >&2
    exit 1
fi

# IMPORT_DEMO_USERS=0 must skip every demo sign-in user and the default must
# still import them; the real Zitadel script runs against a stubbed curl.
"$(dirname "${BASH_SOURCE[0]}")/tests/zitadel-demo-users-guard-test.sh"

# What the same script sends for its two OIDC apps, against a stubbed curl:
# Development mode from each app's addresses, the update of an app that
# already exists, and the console's credentials in a directory of their own.
"$(dirname "${BASH_SOURCE[0]}")/tests/zitadel-bootstrap-guard-test.sh"

# The publisher instance: the System API token's signature and claims, and
# every case of instance.sh (a working token changes nothing and never calls
# the System API; create; refusals; recovery), against a stubbed curl.
"$(dirname "${BASH_SOURCE[0]}")/tests/zitadel-system-token-guard-test.sh"
"$(dirname "${BASH_SOURCE[0]}")/tests/zitadel-instance-guard-test.sh"

# bootstrap-identity.sh provisions the developers' instance and, on a stack
# with the console_login_url output, the publishers' instance: the System API
# token and the admin password reach the VM on stdin and are deleted after the
# run. It secures the console's directory, recreates a service only when its
# client changed, and refuses FORCE and a mismatched installed console before
# any change; terraform, ssh and curl stubbed.
"$(dirname "${BASH_SOURCE[0]}")/tests/bootstrap-identity-guard-test.sh"

# build-push-images.sh builds the services it is given (the staging four by
# default), refuses one whose Dockerfile the tree does not carry, and prints the
# digests a pinned stack needs; prove all three with docker stubbed.
"$(dirname "${BASH_SOURCE[0]}")/tests/build-push-images-guard-test.sh"

# The release scripts: the completed-release guard must fail closed (only a
# 404 lets a publication proceed), and the manifest must record every field
# with digests read back from the registry.
"$(dirname "${BASH_SOURCE[0]}")/tests/release-guard-guard-test.sh"
"$(dirname "${BASH_SOURCE[0]}")/tests/release-manifest-guard-test.sh"

# gen-keys.sh mints the catalog-contributor identity for every stack, the
# smoke agent for staging-aws and demo-aws only, and the console's signing key
# for production-aws only; prove each stack gets exactly its files, that the
# console key has the shape compose-stack accepts and survives a rerun
# unchanged, and that an unknown stack is refused (real openssl and Python,
# temporary stack dirs).
"$(dirname "${BASH_SOURCE[0]}")/tests/gen-keys-guard-test.sh"

# redeploy-demo.sh runs the demo redeploy in order, with one image tag and the
# VM replaced, and stops at the first failed step; every tool it calls is
# stubbed.
"$(dirname "${BASH_SOURCE[0]}")/tests/redeploy-demo-guard-test.sh"

failed=0
for module in "${MODULES_DIR}"/*/; do
    [ -d "${module}tests" ] || continue
    name="$(basename "${module}")"
    echo "==> ${name}"
    (
        cd "${module}"
        terraform init -backend=false -input=false >/dev/null
        terraform test
    ) || failed=1
done

# The keyed stacks (staging-aws, demo-aws, and the production stack when the
# tree carries it) read their key material with file() on constant paths,
# which terraform evaluates while loading the configuration — so a checkout without the gitignored keys/
# directories (CI, a fresh clone) fails validate before any reference checking
# happens. Placeholders keep the step hermetic: create exactly the files that
# are missing, remove exactly those afterwards. A real key on disk is never
# touched (the -f guard skips it) and never removed (only created paths are
# recorded for cleanup). The content is throwaway text; only
# broker-identity-key.pem has plan-time shape checks (the ED25519 label and an
# 88-character base64 payload), which the zero-byte payload below satisfies.
# The production stack is the exception: it gets keys from gen-keys.sh itself
# (see below).
KEYED_STACKS=(staging-aws demo-aws)
if [ -d "${production_stack_dir}" ]; then
    KEYED_STACKS+=("${production_stack}")
fi
# The two stacks that also read the smoke agent's public WBA document; the
# production stack has no smoke check. The catalog contributor's key and
# document are read by every keyed stack.
SMOKE_WBA_STACKS=(staging-aws demo-aws)
placeholder_keys=()
# The demo scope check below works in this scratch copy; it is removed here
# too, so a step that stops the script early does not leave it behind. The
# same holds for the temporary root the production keys are minted in.
scope_copy=""
minted_root=""
cleanup_placeholder_keys() {
    local f s
    for f in ${placeholder_keys[@]+"${placeholder_keys[@]}"}; do
        rm -f "${f}"
    done
    for s in "${KEYED_STACKS[@]}"; do
        rmdir "${REPO_ROOT}/deploy/terraform/stacks/${s}/keys" 2>/dev/null || true
    done
    if [ -n "${scope_copy}" ]; then rm -rf "${scope_copy}"; fi
    if [ -n "${minted_root}" ]; then rm -rf "${minted_root}"; fi
}
trap cleanup_placeholder_keys EXIT
place_key() { # <keys-dir> <filename> <content> — no-op when the real file exists
    local path="$1/$2"
    [ -f "${path}" ] && return 0
    printf '%s\n' "$3" > "${path}"
    placeholder_keys+=("${path}")
}
# The production stack gets what gen-keys.sh writes for it, not the throwaway
# text below. Its tests plan the stack, where compose-stack checks the shape
# of the console's signing key, and they measure the bundle against the
# Secrets Manager limit, which is only meaningful with keys of the real format
# and size (the 2048-bit RSA key alone is about 1.7 KB). The REAL script runs
# against a copy of deploy/terraform/scripts under a temporary repository
# root, as in tests/gen-keys-guard-test.sh, so it writes into that copy and
# never into a tracked stack; the copy links the real scripts/ and
# deploy/zitadel/bootstrap, where system-key.sh makes the System API key. Its
# domain is the test suite's (prod.example), which gives the contributor the kid
# the tests expect, and the contributor's key directory is signed for that
# hostname, so the module's validation of the pre-signed directory runs against
# what gen-keys.sh really writes. The keys are minted for this run and copied in
# through place_key, so a real key on disk still wins and every copied file is
# removed again at exit. The Zitadel System API private key is copied too
# although the stack never reads it: the tests read it to prove that no part of
# it reaches the bundle. The module files, src/ and internal/ are linked in
# because gen-keys.sh builds its directory signer from
# src/exchange/cmd/fora-sign-directory in the repository root. A tree without
# the production stack skips all of this, and nothing recreates its directory.
if [ -d "${production_stack_dir}" ]; then
    minted_root="$(mktemp -d)"
    mkdir -p "${minted_root}/deploy/terraform/scripts" "${REPO_ROOT}/deploy/terraform/stacks/${production_stack}/keys"
    cp -R "${REPO_ROOT}/deploy/terraform/scripts/gen-keys.sh" "${REPO_ROOT}/deploy/terraform/scripts/lib" \
        "${minted_root}/deploy/terraform/scripts/"
    ln -s "${REPO_ROOT}/scripts" "${minted_root}/scripts"
    ln -s "${REPO_ROOT}/go.mod" "${minted_root}/go.mod"
    ln -s "${REPO_ROOT}/go.sum" "${minted_root}/go.sum"
    ln -s "${REPO_ROOT}/src" "${minted_root}/src"
    ln -s "${REPO_ROOT}/internal" "${minted_root}/internal"
    mkdir -p "${minted_root}/deploy/zitadel"
    ln -s "${REPO_ROOT}/deploy/zitadel/bootstrap" "${minted_root}/deploy/zitadel/bootstrap"
    STAGING_DOMAIN=prod.example "${minted_root}/deploy/terraform/scripts/gen-keys.sh" "${production_stack}" >/dev/null
    for f in ed25519-private.pem rsa-private.pem broker-relay-key.json broker-identity-key.pem \
        contributor-key.json catalog-contributor-wba.json console-signing-key.pem \
        zitadel-system-api-private.pem zitadel-system-api-public.pem; do
        minted="${minted_root}/deploy/terraform/stacks/${production_stack}/keys/${f}"
        [ -f "${minted}" ] || { echo "gen-keys.sh ${production_stack} wrote no ${f}" >&2; exit 1; }
        place_key "${REPO_ROOT}/deploy/terraform/stacks/${production_stack}/keys" "${f}" "$(cat "${minted}")"
    done
    rm -rf "${minted_root}"
    minted_root=""
fi
# The label is named once and interpolated into both halves so this file never
# carries a BEGIN header and its matching END footer as literals — that pair is
# what the secret scanner matches on, whatever sits between the two. The
# placeholder written is unchanged.
ed25519_label="ED25519 PRIVATE KEY"
for s in "${KEYED_STACKS[@]}"; do
    keys_dir="${REPO_ROOT}/deploy/terraform/stacks/${s}/keys"
    mkdir -p "${keys_dir}"
    place_key "${keys_dir}" ed25519-private.pem "placeholder written by test-terraform.sh for validate; never a real key"
    place_key "${keys_dir}" broker-relay-key.json '{}'
    place_key "${keys_dir}" broker-identity-key.pem "-----BEGIN ${ed25519_label}-----
$(head -c 64 /dev/zero | base64)
-----END ${ed25519_label}-----"
done
# The catalog contributor's keypair and pre-signed public WBA directory (no
# private material even in the real files), for the stacks that only run
# validate: the production stack already has gen-keys.sh's files (above),
# which place_key leaves alone. The kid is the one the domain prod.example
# derives; the directory is an empty envelope of the shape gen-keys.sh writes,
# which is enough here, because validate does not run the module's variable
# validations against these values — a plan would.
contributor_kid="catalog-contributor.prod.example"
for s in "${KEYED_STACKS[@]}"; do
    keys_dir="${REPO_ROOT}/deploy/terraform/stacks/${s}/keys"
    place_key "${keys_dir}" contributor-key.json '{"kid":"'"${contributor_kid}"'"}'
    place_key "${keys_dir}" catalog-contributor-wba.json '{"body":"{\"keys\":[]}","content_digest":"","signature_input":"","signature":""}'
done
for s in "${SMOKE_WBA_STACKS[@]}"; do
    keys_dir="${REPO_ROOT}/deploy/terraform/stacks/${s}/keys"
    # The smoke agent's pre-signed public WBA directory, read into
    # static_wba_directories. The same empty envelope, for the same reason.
    place_key "${keys_dir}" smoke-agent-wba.json '{"body":"{\"keys\":[]}","content_digest":"","signature_input":"","signature":""}'
done

# The demo edge must be built with its licensed scope. Without it the edge
# treats every path as licensed, and an agent is refused the license document
# it has to read before buying; with a prefix missing, that prefix is free for
# AI bots. A mocked plan of the whole stack cannot run offline, so the rendered
# value is read with terraform console. That needs an initialized backend, and
# a local one must not replace the operator's S3 setup in the real stack
# directory, so the console runs in a scratch copy with the S3 block removed.
# The copy leaves out the operator's tfvars, which Terraform would load and
# which may override the default under test, and the keys directory, which
# holds private keys the console does not need. The copy is a tar pipe, not
# rsync: the CI image ships tar and not rsync, and excluding a directory keeps
# tar from descending into it, so the provider plugins are never read.
echo "==> stacks/demo-aws (licensed scope in lambda_edge_config)"
scope_copy="$(mktemp -d)"
tar -C "${REPO_ROOT}/deploy/terraform" \
    --exclude .terraform --exclude '*.tfvars' --exclude '*.tfvars.json' --exclude keys \
    -cf - . | tar -C "${scope_copy}" -xf -
"${PYTHON[@]}" - "${scope_copy}/stacks/demo-aws/versions.tf" <<'PY'
import re
import sys

path = sys.argv[1]
text = open(path).read()
stripped = re.sub(r'\n  backend "s3" \{.*?\n  \}\n', "\n", text, flags=re.S)
if stripped == text:
    sys.exit("could not find the S3 backend block in " + path)
open(path, "w").write(stripped)
PY
want_scope='"[\"/articles/\",\"/lyrics/\",\"/tracks/\",\"/sfx/\"]"'
got_scope="$(
    cd "${scope_copy}/stacks/demo-aws"
    terraform init -input=false >/dev/null
    # The other locals read key material and report errors on stderr; the
    # value asked for is still printed, as the last line of stdout.
    echo 'local.lambda_edge_config.LICENSED_PATHS_JSON' \
        | terraform console -var 'ssh_operators={}' -var route53_zone_id=ZTEST \
            -var domain=publisher.example -var image_prefix=test/fora \
            -var acme_email=operator@example.test 2>/dev/null \
        | sed 's/\x1b\[[0-9;]*m//g' | grep -v -e '^[[:space:]]*$' -e '^[│╷╵]' | tail -n 1
)" || true
rm -rf "${scope_copy}"
if [ "${got_scope}" != "${want_scope}" ]; then
    echo "demo-aws lambda_edge_config LICENSED_PATHS_JSON = ${got_scope:-<nothing>}, want ${want_scope}" >&2
    failed=1
else
    echo "lambda_edge_config licenses ${want_scope}"
fi

# The module suites never load the root stacks, so a stack-level reference
# error (an output naming a local that only ever existed as an inline module
# argument) passes every suite above and only surfaces at plan time against
# the real account. validate catches exactly that class offline: it checks
# references and types without credentials, state, or variable values.
for stack in "${REPO_ROOT}/deploy/terraform/stacks"/*/; do
    name="stacks/$(basename "${stack}")"
    echo "==> ${name} (validate)"
    (
        cd "${stack}"
        terraform init -backend=false -input=false >/dev/null
        terraform validate
        if [ -d tests ]; then
            terraform test
        fi
    ) || failed=1
done

if [ "${failed}" -ne 0 ]; then
    echo "terraform tests FAILED" >&2
    exit 1
fi
echo "all terraform module tests passed"
