#!/usr/bin/env bash
# check-sdk-pin-consistency.sh
#
# Structural guard: the FORA protocol/SDK git pins must not split-brain.
# The SDK is ONE repository (github.com/FORA-Protocol/protocol) consumed by
# git rev from several manifests; a partial bump silently runs different SDK
# revisions in different components, which is exactly the disease this guard
# pins shut. It asserts REV-AGREEMENT, never a specific SHA literal, so it
# stays valid across future re-pins.
#
# What rev-agreement alone does NOT catch, and check 6 below does: a lockfile
# whose rev string was edited by hand rather than regenerated. Every rev then
# agrees while the lock still records the PREVIOUS revision's metadata — its
# integrity hash, and, with teeth, its dependency list. `npm ci` builds the tree
# from that list, so a dependency the new revision added is never installed and
# the first import of it fails at runtime. This shipped once: the SDK gained a
# dependency on ajv, both package-locks kept a dependency list naming only
# canonicalize, and every check here passed.
#
# Invariants enforced:
#   1+2. tests/e2e/pyproject.toml pins TWO git deps from the protocol repo
#      (fora-protocol @ gen/python, fora-protocol-sdk @ sdk/python) — their
#      revs must be byte-identical. fora_sdk.acceptance imports wire.models, so
#      a split between the two means the harness signs one payload shape while
#      the services verify another. (This pair used to live in the Python MCP
#      shim; the shim is gone — the MCP surface is the Go adapter — so the
#      harness is now the only Python consumer.)
#   3. Each uv.lock resolves every FORA-Protocol/protocol.git source at the
#      rev its own pyproject pins (lock refreshed after every re-pin).
#   4. Each package.json @fora-protocol/sdk-l1 git pin equals every rev its
#      package-lock.json resolves for the protocol repo (lock refreshed
#      after every re-pin; `npm ci` installs the LOCK's rev, so a drifted
#      lock ships a different SDK than the manifest claims).
#   5. Cross-language manifest uniformity: the Python rev and both TS revs
#      must all be equal. SDK re-pins are uniform across languages (commit
#      pattern "chore(deps): re-pin the SDK to <sha> across Go/Python/TS");
#      per-language drift reintroduces split-brain SDK behavior. (go.mod is
#      excluded: its pseudo-version encodes only a 12-char prefix and may
#      legitimately become a tagged release; Go currency is checked by the
#      re-pin sweep, not this guard.)
#   6. Each package-lock.json's recorded metadata for the SDK — its version,
#      its four dependency maps, engines, os, cpu, license and funding —
#      matches what the pinned revision actually declares. npm copies every one
#      of those fields verbatim out of the packed package.json, so the
#      INSTALLED copy under node_modules is an independent witness: a rev
#      substituted into the lock by hand leaves the two disagreeing on whichever
#      fields that revision changed, and needs no network to catch. The entry's
#      remaining keys describe the install rather than the package and have
#      nothing on the installed side to compare against; a key belonging to
#      neither list fails the check rather than being skipped. Read only after node_modules/.package-lock.json is
#      shown to record the same revision the top-level lock names. Without that
#      step the check compares a stale tree against the un-edited metadata it
#      was built from, and passes.
#      A directory with no node_modules, or one built from another revision, is
#      reported as a failure naming what was not checked: a check that did not
#      run is not a check that succeeded.
#   7. Every corpus copy under testdata/ is byte-identical to the copy the
#      pinned Go module ships. Those files are the oracles the replay suites
#      read — the module-side guards under internal/guards/ and the Exchange's
#      own corpus replay through PushResources — so a re-pin that changes a
#      rule, a message or an alias without refreshing a copy makes those suites
#      assert the previous revision's answers and pass. The copy exists because testdata cannot
#      cross a module boundary and a module-cache read breaks in vendored or
#      air-gapped CI; that argument is about the TESTS, which must run
#      anywhere. This is a re-pin gate, so it may read the module directory —
#      and when it cannot, it fails naming what went unchecked rather than
#      passing.
#   8. Both package-lock.json files record the SAME integrity hash for the SDK
#      entry. They pin one revision (invariant 5), and one revision is one
#      tarball, so the hashes can only differ when one lock was regenerated and
#      the other had its rev substituted by hand: npm never checks integrity
#      for a git dependency, so nothing else would notice, and the lock goes on
#      stating a hash the pinned revision does not have.
#      This is a consistency check between the two locks and nothing more. It
#      cannot tell whether the hash they agree on belongs to the pinned
#      revision, and NOTHING here can: npm neither computes nor verifies an
#      integrity for a git dependency at install time, so no file on this
#      machine is an independent witness to that value. A re-pin that
#      substitutes the revision into both locks and leaves the previous
#      revision's hash in both passes this check. What catches that is
#      invariant 6, which compares the locks' recorded metadata against the
#      revision that is actually installed.
#
# The numbered sections below run in this order. Section 1+2 covers invariants
# 1 and 2 in one pass, which is why there is no section headed 2.
#
# Exits 0 when all pins agree, 1 otherwise.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
failures=0

fail() {
    echo "FAIL  $1"
    failures=$((failures + 1))
}

pass() {
    echo "PASS  $1"
}

# Extract the rev of a named git dep from a pyproject.toml [tool.uv.sources]
# line: `name = { git = ".../FORA-Protocol/protocol.git", ..., rev = "<sha>" }`
pyproject_rev() {
    local file="$1" dep="$2"
    grep -E "^${dep} = \{.*FORA-Protocol/protocol" "${file}" \
        | grep -oE 'rev = "[0-9a-f]{40}"' \
        | grep -oE '[0-9a-f]{40}' \
        | head -1
}

# All distinct 40-hex revs a lockfile records for the protocol repo
# (uv.lock: `rev=<sha>` query params + `#<sha>` fragments;
#  package-lock.json: `#<sha>` in both the dependency spec and `resolved`).
lock_revs() {
    local file="$1"
    grep -oE 'FORA-Protocol/protocol\.git[^"]*' "${file}" \
        | grep -oE '[0-9a-f]{40}' \
        | sort -u
}

# The @fora-protocol/sdk-l1 pin rev in a package.json.
package_json_rev() {
    local file="$1"
    grep -E '"@fora-protocol/sdk-l1"' "${file}" \
        | grep -oE '#[0-9a-f]{40}' \
        | grep -oE '[0-9a-f]{40}' \
        | head -1
}

require_nonempty() {
    local value="$1" what="$2"
    if [ -z "${value}" ]; then
        fail "${what}: pin not found (guard needs updating if the pin moved)"
        return 1
    fi
}

# ----- 1+2. Python manifest rev uniformity -----
# `|| true`: a missing pin must reach require_nonempty's FAIL, not trip
# set -e/pipefail inside the extraction pipeline and abort silently.
e2e_proto_rev="$(pyproject_rev "${repo_root}/tests/e2e/pyproject.toml" 'fora-protocol' || true)"
e2e_sdk_rev="$(pyproject_rev "${repo_root}/tests/e2e/pyproject.toml" 'fora-protocol-sdk' || true)"

require_nonempty "${e2e_proto_rev}" "tests/e2e/pyproject.toml fora-protocol" || true
require_nonempty "${e2e_sdk_rev}" "tests/e2e/pyproject.toml fora-protocol-sdk" || true

if [ -n "${e2e_proto_rev}" ] && [ -n "${e2e_sdk_rev}" ]; then
    if [ "${e2e_proto_rev}" = "${e2e_sdk_rev}" ]; then
        pass "tests/e2e/pyproject.toml: fora-protocol rev == fora-protocol-sdk rev (${e2e_sdk_rev})"
    else
        fail "tests/e2e/pyproject.toml: fora-protocol rev ${e2e_proto_rev} != fora-protocol-sdk rev ${e2e_sdk_rev}"
    fi
fi

# ----- 3. uv.lock agrees with its pyproject -----
check_uv_lock() {
    local lock="$1" expected="$2" label="$3"
    [ -n "${expected}" ] || return 0
    local revs stale
    revs="$(lock_revs "${repo_root}/${lock}" || true)"
    if [ -z "${revs}" ]; then
        fail "${lock}: no FORA-Protocol/protocol entries found (guard needs updating if the dep moved)"
        return 0
    fi
    stale="$(echo "${revs}" | grep -v "^${expected}$" || true)"
    if [ -n "${stale}" ]; then
        fail "${lock}: resolves rev(s) $(echo "${stale}" | tr '\n' ' ')but ${label} pins ${expected} — rerun 'uv lock'"
    else
        pass "${lock}: all protocol revs match ${label} pin (${expected})"
    fi
}

check_uv_lock "tests/e2e/uv.lock" "${e2e_sdk_rev}" "tests/e2e/pyproject.toml"

# ----- 4. package-lock.json agrees with its package.json -----
# Sets the global npm_manifest_rev (no subshell: `failures` must accumulate
# in the main shell).
npm_manifest_rev=""
check_npm_pair() {
    local dir="$1"
    local revs stale
    npm_manifest_rev="$(package_json_rev "${repo_root}/${dir}/package.json" || true)"
    require_nonempty "${npm_manifest_rev}" "${dir}/package.json @fora-protocol/sdk-l1" || return 0
    revs="$(lock_revs "${repo_root}/${dir}/package-lock.json" || true)"
    if [ -z "${revs}" ]; then
        fail "${dir}/package-lock.json: no FORA-Protocol/protocol entries found (guard needs updating if the dep moved)"
        return 0
    fi
    stale="$(echo "${revs}" | grep -v "^${npm_manifest_rev}$" || true)"
    if [ -n "${stale}" ]; then
        fail "${dir}: package-lock.json resolves rev(s) $(echo "${stale}" | tr '\n' ' ')but package.json pins ${npm_manifest_rev} — rerun 'npm install'"
    else
        pass "${dir}: package-lock.json matches package.json pin (${npm_manifest_rev})"
    fi
}

check_npm_pair "src/edge"
edge_rev="${npm_manifest_rev}"
check_npm_pair "tests/e2e/fastly-edge"
fastly_rev="${npm_manifest_rev}"

# ----- 5. Cross-language manifest uniformity (Python vs TS) -----
if [ -n "${e2e_sdk_rev}" ] && [ -n "${edge_rev}" ] && [ -n "${fastly_rev}" ]; then
    if [ "${e2e_sdk_rev}" = "${edge_rev}" ] && [ "${e2e_sdk_rev}" = "${fastly_rev}" ]; then
        pass "cross-language manifests agree: Python + src/edge + fastly-edge all pin ${e2e_sdk_rev}"
    else
        fail "cross-language pin split-brain: Python=${e2e_sdk_rev} src/edge=${edge_rev} fastly-edge=${fastly_rev} — re-pin uniformly"
    fi
fi

# ----- 6. package-lock.json SDK metadata matches the pinned revision -----
# Compares every field the lock copies out of the SDK's packed package.json
# against what the installed copy declares. A hand-substituted rev leaves the
# two disagreeing on whatever that revision changed.
#
# This is the ONLY independent evidence the tree holds about the pinned
# revision, which is why it reads as much of the entry as it can rather than
# the dependency list alone. Section 8 explains what it is standing in for: the
# integrity hash cannot be verified locally at all, so a re-pin that kept the
# previous revision's metadata has to be caught here or nowhere.
#
# The installed copy is only evidence if npm actually fetched it at the rev the
# lock now names, and nothing about a directory on disk says when it was built.
# So the rev is checked first, out of node_modules/.package-lock.json, and a
# stale tree is reported as unverified rather than compared: comparing it would
# answer with the PREVIOUS revision's metadata, which of course equals the
# previous revision's recorded metadata, and the check would print a pass for
# the exact hand-substitution it exists to catch.
#
# A lock entry carrying a field this check does not know is reported, not
# ignored. npm writes a field only when the packed package.json has one, so a
# new key means the SDK grew something this comparison silently stopped
# covering — and a check that quietly narrowed is the failure this file exists
# to prevent.
check_npm_sdk_metadata() {
    local dir="$1"
    local lock="${repo_root}/${dir}/package-lock.json"
    local installed="${repo_root}/${dir}/node_modules/@fora-protocol/sdk-l1/package.json"
    local tree_lock="${repo_root}/${dir}/node_modules/.package-lock.json"
    if [ ! -f "${installed}" ]; then
        fail "${dir}: no installed copy at node_modules/@fora-protocol/sdk-l1 — run 'npm ci' so the lock's recorded SDK metadata can be checked against the pinned revision"
        return 0
    fi
    if [ ! -f "${tree_lock}" ]; then
        fail "${dir}: node_modules has no .package-lock.json, so there is no way to tell which revision it was built from — run 'npm ci'"
        return 0
    fi
    local result
    # `|| true`: a lockfile this check cannot parse is the very input it exists
    # to diagnose, so it must reach the case below and be reported by name.
    # Without the guard, set -euo pipefail ends the script here on a Python
    # traceback and every later check, section 5 included, never runs.
    result="$(python3 - "${lock}" "${installed}" "${tree_lock}" <<'PYEOF' || true
import json, sys

SDK = "node_modules/@fora-protocol/sdk-l1"
lock, installed, tree_lock = sys.argv[1], sys.argv[2], sys.argv[3]

# The fields npm copies VERBATIM out of the packed package.json into the lock
# entry. Each one is therefore comparable against the installed copy, and a
# revision that changed any of them shows the substitution.
COMPARED = (
    "version",
    "dependencies",
    "peerDependencies",
    "peerDependenciesMeta",
    "optionalDependencies",
    "engines",
    "os",
    "cpu",
    "license",
    "funding",
)

# Keys npm derives, or takes from somewhere other than the packed package.json,
# so the installed file has nothing to compare them to. Two are worth naming:
# npm normalises a string "bin" into an object, so the two spellings would differ
# for a reason that is not a substitution; and "deprecated" comes from registry
# metadata, which a git dependency has none of.
NOT_COMPARABLE = (
    "resolved", "integrity", "link", "dev", "optional", "peer",
    "devOptional", "inBundle", "extraneous", "hasInstallScript", "bin",
    "deprecated",
)


def rev_of(entry):
    return entry.get("resolved", "").rsplit("#", 1)[-1]


with open(lock) as f:
    entry = json.load(f).get("packages", {}).get(SDK)
if entry is None:
    print("MISSING the lock records no SDK entry")
    raise SystemExit
with open(tree_lock) as f:
    tree_entry = json.load(f).get("packages", {}).get(SDK, {})

want, have = rev_of(entry), rev_of(tree_entry)
if not have:
    print("UNVERIFIED node_modules records no revision for the SDK")
    raise SystemExit
if want != have:
    print("UNVERIFIED node_modules holds %s while the lock names %s" % (have[:12], want[:12]))
    raise SystemExit

uncovered = sorted(set(entry) - set(COMPARED) - set(NOT_COMPARABLE))
if uncovered:
    print("UNCOVERED the SDK's lock entry carries %s" % ", ".join(uncovered))
    raise SystemExit

with open(installed) as f:
    declared = json.load(f)

drift = [f for f in COMPARED if entry.get(f) != declared.get(f)]
if drift:
    print("DRIFT disagrees with revision %s on %s (lock %s, revision %s)" % (
        want[:12],
        ", ".join(drift),
        json.dumps({f: entry.get(f) for f in drift}, sort_keys=True),
        json.dumps({f: declared.get(f) for f in drift}, sort_keys=True),
    ))
else:
    print("OK " + ", ".join(f for f in COMPARED if f in entry))
PYEOF
)"
    case "${result}" in
        OK*)         pass "${dir}: package-lock.json SDK metadata matches the pinned revision (${result#OK })" ;;
        DRIFT*)      fail "${dir}: package-lock.json ${result#DRIFT } — the rev was substituted rather than regenerated; delete the SDK entry from the lock and rerun 'npm install'" ;;
        MISSING*)    fail "${dir}: ${result#MISSING } — run 'npm install' so the SDK's metadata is recorded" ;;
        UNCOVERED*)  fail "${dir}: ${result#UNCOVERED } — a field this check does not compare, so the pinned revision is only PARTLY verified. Add it to COMPARED (npm copies it from the packed package.json) or to NOT_COMPARABLE (npm derives it), and say which in the commit." ;;
        UNVERIFIED*) fail "${dir}: ${result#UNVERIFIED } — the installed copy cannot stand for the pinned revision, so nothing was checked. Run 'npm ci'." ;;
        *)           fail "${dir}: the SDK metadata check did not run (output: ${result:-none}) — a check that did not run is not a check that succeeded" ;;
    esac
}

check_npm_sdk_metadata "src/edge"
check_npm_sdk_metadata "tests/e2e/fastly-edge"

# ----- 7. Every corpus copy matches the pinned module's -----
# `|| true` on the module lookup: a toolchain that cannot resolve the module is
# the input this check exists to report, so it must reach the empty-value branch
# below rather than end the script through set -e.
#
# The list is every verbatim copy under testdata/. Adding a corpus means adding
# it here — a copy this loop does not name is a copy nothing refreshes, which is
# the failure mode the check exists to prevent.
corpora=(
    "licenseterm-vectors.json"
    "manifest-version-vectors.json"
)
module_dir="$(go list -m -f '{{.Dir}}' github.com/FORA-Protocol/protocol 2>/dev/null || true)"

for corpus_name in "${corpora[@]}"; do
    corpus_rel="testdata/${corpus_name}"
    corpus_local="${repo_root}/${corpus_rel}"
    corpus_module="${module_dir}/sdk/go/helpers/testdata/${corpus_name}"

    if [ ! -f "${corpus_local}" ]; then
        fail "${corpus_rel} is missing — it is the oracle the corpus replays read; restore it from the pinned module"
    elif [ -z "${module_dir}" ]; then
        fail "could not resolve the pinned protocol module's directory ('go list -m'), so ${corpus_rel} was NOT compared against it — a check that did not run is not a check that succeeded"
    elif [ ! -f "${corpus_module}" ]; then
        fail "the pinned module has no sdk/go/helpers/testdata/${corpus_name}, so ${corpus_rel} was NOT compared — the corpus moved upstream and this check needs its new path"
    elif ! cmp -s "${corpus_local}" "${corpus_module}"; then
        fail "${corpus_rel} differs from the pinned module's copy — refresh it from the module at every re-pin (never edit it by hand): cp \"${corpus_module}\" \"${corpus_local}\""
    else
        pass "${corpus_rel} is byte-identical to the pinned module's copy"
    fi
done

# ----- 8. The two package-locks agree on the SDK integrity hash -----
# One property, and the section says plainly what it is not.
#
# The locks must AGREE with each other: they pin one revision, one revision is
# one tarball, so a difference means one lock was regenerated and the other had
# its revision substituted by hand. npm never verifies integrity for a git
# dependency, so nothing else would notice.
#
# Agreement does NOT establish that the hash belongs to the pinned revision, and
# this check does not claim it does. A re-pin that substitutes the revision into
# BOTH locks leaves the previous revision's hash in both, the two agree, and this
# passes. There is no local oracle that would catch it: for a git dependency npm
# neither computes nor verifies an integrity at install time — pacote drops the
# value before extracting (npm/rfcs#525) — and node_modules/.package-lock.json is
# a serialization of the tree npm built FROM the committed lock, so its integrity
# is a copy of the committed lock's. Do not compare the two and call it
# provenance: that compares a value with itself and passes on every input.
#
# The stale-hash case is caught by check 6 instead, which reads the installed
# revision's own package.json — a file the lock does not write — and compares the
# metadata npm copies out of it. Whatever the pinned revision changed shows up
# there.
lock_sdk_integrity() {
    local lock="$1"
    # `|| true`: a lockfile this check cannot read is the very input it exists to
    # diagnose, so it must reach the empty-value branch below and be reported by
    # name rather than end the script through set -e.
    python3 - "${lock}" <<'PYEOF' || true
import json, sys

try:
    with open(sys.argv[1]) as f:
        entry = json.load(f).get("packages", {}).get("node_modules/@fora-protocol/sdk-l1", {})
except (OSError, ValueError) as exc:
    print("UNREADABLE %s" % exc)
    raise SystemExit
print(entry.get("integrity", ""))
PYEOF
}

edge_integrity="$(lock_sdk_integrity "${repo_root}/src/edge/package-lock.json")"
fastly_integrity="$(lock_sdk_integrity "${repo_root}/tests/e2e/fastly-edge/package-lock.json")"
case "${edge_integrity}${fastly_integrity}" in
    *UNREADABLE*)
        fail "a package-lock.json could not be read, so the SDK integrity was NOT compared (src/edge='${edge_integrity}' fastly-edge='${fastly_integrity}')"
        ;;
    "")
        fail "neither package-lock.json records an integrity for the SDK entry — rerun 'npm install' so the tarball hash is recorded"
        ;;
    *)
        if [ -z "${edge_integrity}" ] || [ -z "${fastly_integrity}" ]; then
            fail "a package-lock.json records no integrity for the SDK entry (src/edge='${edge_integrity}' fastly-edge='${fastly_integrity}') — rerun 'npm install' so the tarball hash is recorded"
        elif [ "${edge_integrity}" = "${fastly_integrity}" ]; then
            pass "both package-locks record one integrity hash for the SDK entry"
        else
            fail "package-locks disagree on the SDK tarball: src/edge records ${edge_integrity} while fastly-edge records ${fastly_integrity} — one lock kept a previous revision's hash; delete its SDK entry and rerun 'npm install'"
        fi
        ;;
esac

# ----- Result -----
echo ""
if [ "${failures}" -gt 0 ]; then
    echo "SDK pin guard: ${failures} failure(s). Re-pin uniformly and refresh every lockfile."
    exit 1
fi

echo "SDK pin guard: all pins agree."
exit 0
