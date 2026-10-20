#!/usr/bin/env bash
# check-sdk-pin-consistency.sh
#
# Structural guard: the FORA protocol/SDK pins must not split-brain.
# The SDK is ONE repository (github.com/FORA-Protocol/protocol) consumed by four
# manifests; a partial bump silently runs different SDK revisions in different
# components, which is exactly the disease this guard pins shut.
#
# The pins take one of two forms, and go.mod decides which one this run checks.
#
# RELEASE FORM -- go.mod requires a semantic version. All four manifests then
# name one release: go.mod requires v<version>, the two PyPI distributions pin
# ==<version> and resolve from PyPI, and both TypeScript manifests pin
# @fora-protocol/sdk at that same version from the npm registry. The
# cross-language check is one string compared four ways, and invariants 1 to 8
# below are that form.
#
# PRE-RELEASE FORM -- go.mod requires a Go pseudo-version. Between protocol
# releases the Go services, the e2e harness and both edge workers are pinned to
# ONE commit of the protocol's integration branch rather than to a release, so
# all three SDKs build from the same commit. go.mod requires that commit's
# pseudo-version. tests/e2e/pyproject.toml keeps its ==<version> pins and adds a
# [tool.uv.sources] table that installs both distributions from the protocol
# repository at the same full 40-hex rev, and tests/e2e/uv.lock resolves both
# from that commit. Both TypeScript manifests pin @fora-protocol/sdk to
# `github:FORA-Protocol/protocol#<the same full 40-hex commit>`: npm installs the
# repository's root package.json, which presents the TypeScript SDK under sdk/ts,
# so the import specifiers stay "@fora-protocol/sdk/...". Each package-lock.json
# resolves that commit. Invariants P1 to P7 below are that form.
#
# Which form applies is read from go.mod and nowhere else: a pseudo-version means
# pre-release, anything else is checked as a release. A tree that is half in one
# form and half in the other is refused by name (P7), whichever half go.mod is in.
# At the next protocol release the [tool.uv.sources] table is deleted, go.mod
# requires the new tag, both TypeScript manifests go back to that exact npm
# version, and the tree is back in the release form.
#
# Some checks run in both forms: the refusal of a `replace` or `exclude` naming
# the protocol module (section 5), the TypeScript manifest and lock checks
# (sections 4, 6 and 8, each reading the pin in the form go.mod chose), and the
# corpus byte-identity check (section 7), which compares against whichever module
# version go.mod requires.
#
# This guard never asserts a specific version or SHA literal, so it stays valid
# across future re-pins in either form.
#
# What version-agreement alone does NOT catch, and check 6 below does: a lockfile
# entry edited by hand rather than regenerated. Every version string then agrees
# while the lock still records the PREVIOUS release's metadata — its integrity
# hash, and, with teeth, its dependency list. `npm ci` builds the tree from that
# list, so a dependency the new release added is never installed and the first
# import of it fails at runtime. This shipped once: the SDK gained a dependency on
# ajv, both package-locks kept a dependency list naming only canonicalize, and
# every check here passed.
#
# Invariants enforced. 1 to 8 are the release form. In the pre-release form
# P1 to P7 take the place of 3 and 5, sections 4, 6 and 8 read the commit pin
# instead of the version pin, and 1+2 and 7 run unchanged.
#
#   1+2. tests/e2e/pyproject.toml pins TWO published distributions from the
#      protocol repo (fora-protocol from gen/python, fora-protocol-sdk from
#      sdk/python) — their versions must be identical. fora_sdk.acceptance imports
#      wire.models, so a split between the two means the harness signs one payload
#      shape while the services verify another. (This pair used to live in the
#      Python MCP shim; the shim is gone — the MCP surface is the Go adapter — so
#      the harness is now the only Python consumer.)
#   3. Release form: tests/e2e/uv.lock resolves both distributions at the
#      version its own pyproject pins, from a registry, and from THE registry they
#      are published to. None of those three halves is decoration: a release tree
#      whose lock resolves them from git — or from an index that merely serves a
#      package of that name and version — installs something other than the
#      release while every version string still agrees.
#      Reading the two [[package]] tables is necessary and not sufficient, so two
#      whole-file properties sit beside it. No line of uv.lock may name the
#      protocol repository at all, which catches a `requires-dist` entry or an
#      extra package block reaching it over git. And tests/e2e/pyproject.toml may
#      carry no [tool.uv.sources] override for either distribution, because an
#      override wins over the ==version pin: a release tree that keeps or regains
#      that table runs the harness from a git checkout with every string in this
#      guard still agreeing. That is the half-converted state P7 names, reached
#      from the release side. The per-table read alone missed all three.
#   4. Each package.json @fora-protocol/sdk version equals every version its
#      package-lock.json resolves for the protocol repo (lock refreshed
#      after every re-pin; `npm ci` installs the LOCK's rev, so a drifted
#      lock ships a different SDK than the manifest claims). In the pre-release
#      form the same section reads the commit instead, which is P6's first half.
#      A manifest whose pin is in the other form's spelling is refused by name as
#      a half-converted pin (P7), in both forms.
#   5. Cross-language uniformity, in two halves, because the four manifests no
#      longer spell the pin the same way. Go and Python name a RELEASE — go.mod
#      requires v<version>, both Python distributions pin ==<version> — and the
#      release workflow refuses a tag whose version differs from any shipped
#      manifest, so those strings are the same string or one of them is wrong.
#      TypeScript names the same release: both edge manifests pin
#      @fora-protocol/sdk at an EXACT version, never a range, because `^1.0.4`
#      would let npm install a different release than the one go.mod and the
#      harness name while every string here still agreed.
#      go.mod is in this comparison in the release form only. In the pre-release
#      form it carries a pseudo-version, which names a commit rather than a
#      release, and P1 compares that commit with the Python rev instead.
#      The version-to-commit resolution runs `go mod download` for that exact
#      version and reads the .info file it names, which records the VCS ref and
#      hash the proxy resolved. On a warm module cache no network is touched; on
#      a cold one — a fresh CI checkout — that command is the fetch, and with
#      GOPROXY=off it fails rather than answering. It runs inside a throwaway
#      module so it cannot write to this repository's go.sum.
#      Four distinct outcomes are reported as four distinct failures, because
#      they have four different remedies: the module could not be resolved at all
#      (the toolchain or the proxy); it resolved but the proxy recorded no origin;
#      it resolved to a commit with no tag ref, which is what a pseudo-version
#      looks like and means the pin is not a release; or the ref names some other
#      tag. None of them passes. A pseudo-version the form decision recognises is
#      checked as the pre-release form instead, so no pseudo-version Go itself
#      recognises reaches the third outcome any more.
#      A `replace` or `exclude` naming the module is rejected before any of this,
#      because an explicit module@version download ignores `replace` while the Go
#      build does not — so without that rejection every comparison in section 5
#      would describe a module the build never reads.
#      What this section CANNOT check is the step between the Python version and
#      the commit: nothing on this machine witnesses that PyPI's fora-protocol-sdk
#      <version> was built from the v<version> tag. That link rests on the protocol
#      repository's release workflow refusing a tag whose version differs from any
#      shipped manifest. It is a cross-repository assumption, stated here rather
#      than left for a reader to reconstruct.
#   6. Each package-lock.json's SDK entry names the npm registry, carries a
#      sha512 integrity, and agrees with the installed copy on every field it
#      records. Three separate properties, and the first two are what the move off
#      the git pin bought.
#      This check used to carry the whole weight, because npm neither computes
#      nor verifies an integrity for a GIT dependency: nothing on the machine
#      witnessed the bytes, so the only handle on a hand-substituted revision was
#      that npm copied the packed package.json's fields into the lock verbatim,
#      leaving the lock and the installed tree disagreeing. For a REGISTRY
#      dependency npm records a real SRI and verifies it on every install, so a
#      substituted version or URL fails `npm ci` outright. That is a stronger
#      witness than any field comparison, and it is why this check now asserts the
#      integrity is there and well-formed rather than inferring one.
#      The field comparison stays, narrowed to what is actually comparable. A
#      registry entry's field set comes from the registry's ABBREVIATED metadata,
#      not from the packed package.json, so the lock legitimately records fewer
#      fields than the package declares — `license` is absent for this dependency
#      and for two others in the same tree. Every field the lock DOES record must
#      match the installed copy; a key belonging to neither list fails rather than
#      being skipped, so a field npm starts recording cannot slip past unread.
#      Read only after node_modules/.package-lock.json is shown to record the same
#      VERSION the top-level lock names. Without that step the check compares a
#      stale tree against the un-edited metadata it was built from, and passes.
#      A directory with no node_modules, or one built from another version, is
#      reported as a failure naming what was not checked: a check that did not
#      run is not a check that succeeded.
#      In the pre-release form the SDK is a git dependency again, and two of the
#      three properties change with it. The entry must resolve from NPM_GIT_URL at
#      the manifest's commit instead of from the registry. The integrity stays a
#      required sha512 SRI: npm records a hash for the commit's tarball, though
#      for a git dependency it does not verify that hash at install (invariant 8
#      says what follows from that), and section 8 holds the two locks to one
#      value. The commit is what pins the bytes here. And the installed copy is
#      matched to the lock by the URL it was resolved from, commit included,
#      because the version string is the last release's for every commit until
#      the next one. The git entry also records the root package's `name`
#      (@fora-protocol/sdk-l1, installed under the @fora-protocol/sdk alias), and
#      it is compared like any other field.
#   7. Every corpus copy under testdata/ matches the copy the pinned Go
#      module ships: byte for byte, or, for the one copy that wraps the
#      module's array beside a note (the delivery-proof corpus the edge
#      double replays), as JSON. Those files are the oracles the replay suites
#      read — the module-side guards under internal/guards/, the Exchange's
#      own corpus replay through PushResources, the edge double's proof
#      replay, and the e2e harness's
#      acceptance and thumbprint suites — so a re-pin that changes a
#      rule, a message or an alias without refreshing a copy makes those suites
#      assert the previous revision's answers and pass. The copy exists because testdata cannot
#      cross a module boundary and a module-cache read breaks in vendored or
#      air-gapped CI; that argument is about the TESTS, which must run
#      anywhere. This is a re-pin gate, so it may read the module directory —
#      and when it cannot, it fails naming what went unchecked rather than
#      passing.
#   8. Both package-lock.json files record the SAME integrity hash for the SDK
#      entry. They pin one release (invariant 5, or P6), and one release is one
#      tarball, so the hashes can only differ when one lock was regenerated and
#      the other had its entry edited by hand, and the two then disagree.
#      What this check can and cannot tell changed with the move to the published
#      npm package. While the SDK was a git dependency it could only compare the
#      two locks with each other: npm neither computes nor verifies an integrity
#      for a git dependency, so no file on the machine witnessed that value, and a
#      re-pin that wrote the previous revision's hash into both locks passed. For
#      a registry dependency npm verifies the SRI against the downloaded tarball
#      on every install, so a hash that belongs to no published artifact fails
#      `npm ci` rather than sitting in the lock. This check is what catches the
#      remaining case: one lock refreshed, the other not. In the pre-release form
#      the two locks pin one commit, one commit is one tarball, and the same
#      reasoning holds.
#
# Pre-release form, checked when go.mod requires a pseudo-version:
#   P1. The pseudo-version's 12-character commit prefix is the prefix of the
#      Python rev. Go and the harness then build from the same commit; a prefix
#      that differs means one of the two was re-pinned and the other was not.
#   P2. The two [tool.uv.sources] entries are each exactly { git, rev,
#      subdirectory }, name the protocol repository at the same full 40-hex rev,
#      and install fora-protocol from gen/python and fora-protocol-sdk from
#      sdk/python. A branch in place of the rev moves without anyone re-pinning,
#      and a tag can be moved, which is why only a full commit is accepted.
#   P3. tests/e2e/uv.lock resolves each distribution from exactly that git URL,
#      subdirectory and commit, and from nothing else: one [[package]] table per
#      distribution, no other package reaching the repository, and every line of
#      the lock that names the repository naming it at that commit. uv installs
#      what the lock resolved, so a pyproject re-pinned without re-locking still
#      installs the previous commit.
#   P4. The two distributions resolve at the same version string, which is
#      invariants 1+2 applied to the lock. The pre-release form does NOT compare
#      that string with the ==<version> pin: the commit carries whatever version
#      the integration branch's package metadata says, and this gate does not
#      require it to equal the last release.
#   P5. Both TypeScript manifests pin @fora-protocol/sdk to exactly
#      `github:FORA-Protocol/protocol#<commit>`, with a full 40-hex commit, the
#      same commit in both, equal to the Python rev P2 established and beginning
#      with the pseudo-version's prefix. All three SDKs then build from one
#      commit. A branch or a tag in place of the commit moves without anyone
#      re-pinning, an abbreviated commit is not what the lock records, and another
#      repository is not the protocol. The comparison with the Python rev is not
#      attempted when P2 failed, because there is then no single rev to compare
#      with; the comparison with go.mod's prefix still runs.
#   P6. Each package-lock.json records the manifest's spec as its root
#      dependency, and every SDK entry it records resolves from exactly
#      NPM_GIT_URL at the manifest's commit (section 4). npm installs what the
#      lock resolved, so a manifest re-pinned without re-locking still installs
#      the previous commit. The SDK entry carries a sha512 integrity, the
#      installed copy under node_modules was built from that same resolved commit
#      and agrees with the lock on every field the lock records (section 6), and
#      the two locks record one integrity for it (section 8). The version string
#      is no evidence here: every commit between two releases carries the last
#      release's version, so the installed copy is matched to the lock by the
#      commit it was resolved from.
#   P7. A half-converted tree is refused by name. go.mod on a pseudo-version
#      while the pyproject lacks either [tool.uv.sources] entry builds Go from a
#      commit and installs the harness from the last release on PyPI; go.mod on a
#      pseudo-version while a TypeScript manifest still pins an npm version runs
#      the edge on the last release while Go and the harness run the commit. The
#      reverse — go.mod on a release while the table is still there, or while a
#      TypeScript manifest still pins a commit — is invariant 3's override check
#      and section 4's pin check, and their messages name the same state.
#
# The numbered sections below run in this order. Section 1+2 covers invariants
# 1 and 2 in one pass, which is why there is no section headed 2.
#
# `--root <dir>` checks a tree other than this checkout. The argument, and why it
# is an argument, are described once in scripts/gate-args.sh. What is specific
# here: this gate decides whether a re-pin is coherent, so a redirected run that
# reported PASS would clear a tree nobody checked.
#
# Exits 0 when all pins agree, 1 otherwise.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scripts/gate-args.sh
source "${script_dir}/gate-args.sh"

parse_gate_args "$@"

failures=0

fail() {
    echo "FAIL  $1"
    failures=$((failures + 1))
}

pass() {
    echo "PASS  $1"
}

# The exact version a pyproject.toml pins for a named distribution:
# `"fora-protocol-sdk==<version>",` inside [project].dependencies.
pyproject_version() {
    local file="$1" dep="$2"
    grep -oE "\"${dep}==[0-9][^\"]*\"" "${file}" \
        | head -1 \
        | sed -E "s/.*==//; s/\"//"
}

# The index the two protocol distributions are published to. Named here rather
# than inline so the comparison below reads as a comparison.
EXPECTED_INDEX="https://pypi.org/simple"

# Read one top-level string field out of a JSON object on stdin.
#
# Prints nothing when stdin is not an object or the field is absent or not a
# string. It never raises: an uncaught exception here would print a Python
# traceback into the middle of a `make quality` run, ahead of the guard's own
# message, and the traceback is the one a reader would try to act on.
json_field() {
    python3 -c 'import json, sys
try:
    doc = json.load(sys.stdin)
except (ValueError, OSError):
    sys.exit(0)
if isinstance(doc, dict):
    value = doc.get(sys.argv[1])
    if isinstance(value, str):
        print(value)
' "$1"
}

# The version a uv.lock resolves for a named distribution AND the index it
# resolves it from, as "<version> <url>". Only a registry source answers; a git
# source answers empty, so the check below reports it as unpinned rather than
# comparing a version string that came from somewhere else entirely.
#
# The URL is returned because "resolved from a registry" is not the property the
# release move bought. "Resolved from the index everyone else installs from" is,
# and the two differ by one string in a lockfile nobody reads closely.
uv_lock_version() {
    local file="$1" dep="$2"
    awk -v dep="${dep}" '
        $0 == "[[package]]" { name=""; version=""; next }
        $0 ~ /^name = / { gsub(/^name = "|"$/, ""); name=$0; next }
        $0 ~ /^version = / { gsub(/^version = "|"$/, ""); version=$0; next }
        $0 ~ /^source = \{ registry = / {
            if (name == dep && version != "") {
                url = $0
                sub(/^source = \{ registry = "/, "", url)
                sub(/" \}$/, "", url)
                print version, url
                exit
            } }
    ' "${file}"
}

# Every line of a file that reaches the protocol repository over git, numbered.
#
# In the release form NOTHING in tests/e2e/uv.lock should name that repository
# at all -- not a package's own source, not a `requires-dist` entry, not a stray
# package someone added. This is deliberately a whole-file text sweep rather than
# a reuse of lock_revs: that one extracts 40-hex revisions, so a git source
# pinned to a BRANCH leaves it empty and the file reads clean.
#
# Case-insensitive because GitHub is. `github.com/fora-protocol/protocol.git`
# clones the same repository as the spelling this file uses, and a case-sensitive
# sweep reported that lock clean.
protocol_git_mentions() {
    grep -niE 'FORA-Protocol/protocol' "$1" || true
}

# The [tool.uv.sources] keys in a pyproject.toml that override one of the protocol
# distributions, one per line.
#
# A source override sits ABOVE the ==version pin: uv installs from the override
# and the `==<version>` specifier still reads correct, so every version string in this
# guard agrees while the harness is back on a git checkout. In the release form
# that is the cheapest way to undo the release pin, and it has to be named
# explicitly. In the pre-release form the same function finds the two entries P2
# then reads, so both forms agree on which keys name a distribution.
#
# The TOML is parsed rather than matched line by line, because the property being
# asserted belongs to the document and uv reads the document. A line matcher has
# to enumerate spellings, and it missed three of the ones uv honours: the dotted
# table header `[tool.uv.sources.fora-protocol-sdk]`, which a matcher anchored on
# the bare `[tool.uv.sources]` header treats as the end of the section; a quoted
# key; and an indented key. tomllib resolves all three into the same mapping, so
# there is one spelling left to get right instead of an open set.
#
# Keys are compared after PEP 503 normalisation, which is the rule a Python
# installer applies to a distribution name: `fora_protocol_sdk` and
# `Fora-Protocol-SDK` name the same distribution as `fora-protocol-sdk`.
#
# Takes the distribution as an argument and is called once per distribution, the
# way pyproject_version and check_uv_lock are. It used to write both names into
# its own matcher, which made it the one reader of that pair whose list was not
# visible at the call site -- and so the one a third distribution would be added
# without.
#
# Sets the globals uv_override and uv_override_error, for the reason
# gomod_redirects gives: a pyproject this cannot read is a third outcome, and
# reporting it as "no override" is the shape that let a `replace` through.
uv_override=""
uv_override_error=""
uv_source_overrides() {
    local file="$1" dep="$2" tmp
    uv_override=""
    uv_override_error=""
    tmp="$(mktemp -d)" || { uv_override_error="could not create a temp directory"; return 0; }
    if ! uv_override="$(python3 -c '
import re
import sys
import tomllib


def normalized(name):
    return re.sub(r"[-_.]+", "-", name).lower()


with open(sys.argv[1], "rb") as handle:
    doc = tomllib.load(handle)

target = normalized(sys.argv[2])
sources = ((doc.get("tool") or {}).get("uv") or {}).get("sources") or {}
if isinstance(sources, dict):
    for key in sources:
        if normalized(key) == target:
            print(key)
' "${file}" "${dep}" 2>"${tmp}/stderr")"; then
        uv_override=""
        uv_override_error="$(tr '\n' ' ' < "${tmp}/stderr" 2>/dev/null || true)"
        : "${uv_override_error:=the [tool.uv.sources] table could not be read}"
    fi
    rm -rf "${tmp}"
}

# The protocol module version go.mod requires, with its leading "v" stripped.
#
# Both spellings count: an indented line inside a `require ( ... )` block, and the
# single-line `require github.com/... v1.2.3` form `go mod edit -require` writes
# when no block exists. Lines carrying `=>` are dropped so a versioned `replace`
# cannot answer in the `require` line's place.
#
# The first grep matches whole lines on purpose. With `-o` it emitted only the
# matched part, which ends at the version token, so the `=>` was already gone by
# the time `grep -v` looked for it and that stage could never drop anything: an
# indented `github.com/... v9.9.9 => ./local` inside a `replace ( ... )` block
# answered as the require version. The trailing sed takes the version out of the
# whole line, and stops at the first space so a `// indirect` comment is not read
# as part of it.
#
# POSIX character classes only. `\s` is a GNU grep extension; BSD grep, which
# macOS ships, reads it as a literal "s", which would make this match nothing and
# report the pin missing on a tree where the pin is correct.
gomod_version() {
    grep -E "^([[:space:]]+|require[[:space:]]+)github\.com/FORA-Protocol/protocol v[^[:space:]]+" "$1" \
        | grep -v '=>' \
        | head -1 \
        | sed -E 's#^.*github\.com/FORA-Protocol/protocol[[:space:]]+v##; s#[[:space:]].*$##'
}

# Any `replace` or `exclude` directive naming the protocol module, one per line.
#
# A replace falsifies every comparison section 5 makes without touching the
# `require` line: the version still reads the release, `go mod download <module>@<release>`
# still resolves the tag because an explicit module@version query ignores replace,
# and the Go build reads the replacement tree instead. `go mod edit -json` is the
# exact reading of the directive list, needs no network, and writes nothing.
#
# Sets the globals gomod_redirect and gomod_redirect_error rather than printing,
# for the reason resolve_module gives below: a caller writing
# `x="$(gomod_redirects ...)"` runs the body in a subshell and the failure reason
# dies with it.
#
# Three outcomes, not two. "No directive names the module" and "the directive list
# could not be read" are different answers, and this function used to report the
# second as the first: every failure of `go mod edit -json` — the binary absent
# from PATH, a go.mod the parser rejects — produced empty output, and the caller
# read empty as a clean file and printed PASS while a `replace` sat in it. That is
# the one branch in this file that broke the rule the rest of it keeps: a check
# that did not run is not a check that succeeded.
gomod_redirect=""
gomod_redirect_error=""
gomod_redirects() {
    local file="$1" tmp raw
    gomod_redirect=""
    gomod_redirect_error=""
    if ! command -v go >/dev/null 2>&1; then
        gomod_redirect_error="the 'go' binary is not on PATH"
        return 0
    fi
    tmp="$(mktemp -d)" || { gomod_redirect_error="could not create a temp directory"; return 0; }
    if ! raw="$(go mod edit -json "${file}" 2>"${tmp}/stderr")" || [ -z "${raw}" ]; then
        gomod_redirect_error="$(tr '\n' ' ' < "${tmp}/stderr" 2>/dev/null || true)"
        : "${gomod_redirect_error:=\`go mod edit -json\` produced no output}"
        rm -rf "${tmp}"
        return 0
    fi
    # No try/except here. A payload `go mod edit -json` emitted and json refuses is
    # a state this function must report, not swallow; the traceback goes to the
    # temp file and only its text reaches the operator, inside the guard's own
    # message.
    if ! gomod_redirect="$(printf '%s' "${raw}" | python3 -c 'import json, sys
doc = json.load(sys.stdin)
module = "github.com/FORA-Protocol/protocol"
for entry in doc.get("Replace") or []:
    if ((entry.get("Old") or {}).get("Path")) == module:
        print("replace")
for entry in doc.get("Exclude") or []:
    if entry.get("Path") == module:
        print("exclude")
' 2>"${tmp}/stderr")"; then
        gomod_redirect=""
        gomod_redirect_error="the directive list did not parse: $(tr '\n' ' ' < "${tmp}/stderr" 2>/dev/null || true)"
    fi
    rm -rf "${tmp}"
}

# One resolution of the pinned protocol module, shared by sections 5 and 7.
#
# `go mod download -json <module>@v<version>` answers with the module cache paths
# for that exact version: `Info`, the .info file carrying the VCS origin the proxy
# recorded, and `Dir`, the extracted tree section 7 compares testdata against.
# Section 7 used to ask `go list -m` for that directory. `go list -m` resolves
# through the build list, so it followed a `replace` while this call does not, and
# the two could report on different copies of "the pinned module" with nothing in
# the output saying so. One call answers both questions now, and section 5 rejects
# a `replace` outright, so that divergence has no way back in.
#
# The call runs inside a throwaway module in a temp directory. `go mod download`
# records what it resolved in the CALLING module's go.sum, and a read-only guard
# must not write to a tracked file -- least of all on the split-brain path, where
# the version it resolves is the wrong one and the lines it appends are noise the
# developer then has to notice and undo.
#
# Sets the globals module_json and go_module_error rather than printing, because
# a caller writing `x="$(resolve_module ...)"` would run the whole thing in a
# subshell and the failure reason would die with it -- which it did, and the
# toolchain-absent case reported an empty reason until the meta-guard caught it.
# Section 4 keeps a global for the same reason, one function up.
module_json=""
go_module_error=""
resolve_module() {
    local version="$1" tmp raw resolve_error
    module_json=""
    go_module_error=""
    if ! command -v go >/dev/null 2>&1; then
        go_module_error="the 'go' binary is not on PATH"
        return 0
    fi
    tmp="$(mktemp -d)" || { go_module_error="could not create a temp directory"; return 0; }
    raw="$( { cd "${tmp}" \
        && go mod init sdkpinguard >/dev/null 2>&1 \
        && go mod download -json "github.com/FORA-Protocol/protocol@v${version}"; } \
        2>"${tmp}/stderr" )" || true
    resolve_error="$(tr '\n' ' ' < "${tmp}/stderr" 2>/dev/null || true)"
    rm -rf "${tmp}"
    if [ -z "${raw}" ]; then
        go_module_error="${resolve_error:-\'go mod download\' produced no output}"
        return 0
    fi
    # A resolution failure is reported inside the JSON, not on stderr.
    resolve_error="$(printf '%s' "${raw}" | json_field Error)"
    if [ -n "${resolve_error}" ]; then
        go_module_error="${resolve_error}"
        return 0
    fi
    module_json="${raw}"
}

# The commit and ref the proxy recorded for a version, as "<hash> <ref>", read
# from the .info file go_module_json names. No network: the file is already in the
# module cache by the time this runs.
#
# When the origin records a hash and NO ref -- which is exactly what a Go
# pseudo-version's .info file looks like -- the ref is printed as the literal
# "(none)". Collapsing that into "nothing resolved" is what used to send a
# developer who re-pinned go.mod to an untagged commit off to run `go mod
# download` for a module that was already downloaded, while the message naming
# the real cause sat in a branch nothing could reach.
tag_origin() {
    local info="$1"
    [ -f "${info}" ] || return 0
    python3 - "${info}" <<'PYORIGIN'
import json, sys
try:
    with open(sys.argv[1], encoding="utf-8") as handle:
        origin = json.load(handle).get("Origin") or {}
except (OSError, ValueError):
    sys.exit(0)
if isinstance(origin, dict) and origin.get("Hash"):
    print(origin["Hash"], origin.get("Ref") or "(none)")
PYORIGIN
}

# Every value a package-lock.json records in one field of the SDK entry,
# deduplicated: `version` in the release form, `resolved` in the pre-release form,
# where every commit between two releases carries the same version string.
#
# Reads the lock as JSON, because the entry is identified by its key
# (`node_modules/@fora-protocol/sdk`, at any nesting depth) and not by a string
# that happens to appear on the line.
lock_sdk_field() {
    python3 -c '
import json, sys

with open(sys.argv[1], "rb") as handle:
    doc = json.load(handle)

seen = set()
for path, entry in (doc.get("packages") or {}).items():
    if path.rsplit("node_modules/", 1)[-1] != "@fora-protocol/sdk":
        continue
    value = entry.get(sys.argv[2])
    if isinstance(value, str):
        seen.add(value)
for value in sorted(seen):
    print(value)
' "$1" "$2"
}

# The spec a package-lock.json records for the SDK as a dependency of the root
# package -- the copy of the manifest's spec npm writes when it re-locks. A lock
# whose root still names the previous spec was not regenerated after the re-pin.
lock_root_spec() {
    python3 -c '
import json, sys

with open(sys.argv[1], "rb") as handle:
    root = (json.load(handle).get("packages") or {}).get("") or {}
for field in ("dependencies", "devDependencies"):
    spec = (root.get(field) or {}).get("@fora-protocol/sdk")
    if isinstance(spec, str):
        print(spec)
        break
' "$1"
}

# How a package.json pins @fora-protocol/sdk, as one line:
#
#   version <v>    an EXACT version, the release form's pin
#   commit <sha>   github:FORA-Protocol/protocol#<full 40-hex commit>, the
#                  pre-release form's pin
#   other <spec>   anything else
#
# and nothing when the manifest names no SDK at all.
#
# Exact means no range operator: `^1.0.4` would let npm install a different
# release than the one go.mod and the harness name, and every string in this guard
# would still agree. Exactly one commit spelling is a pin, the one npm records as
# NPM_GIT_URL below: a branch or a tag in place of the commit moves without anyone
# re-pinning, an abbreviated commit is not what the lock records, and another
# repository is not the protocol. Everything else is reported as `other`, which
# the callers treat as no pin at all.
package_json_pin() {
    python3 -c '
import json, re, sys

with open(sys.argv[1], "rb") as handle:
    doc = json.load(handle)
for field in ("dependencies", "devDependencies"):
    spec = (doc.get(field) or {}).get("@fora-protocol/sdk")
    if not isinstance(spec, str):
        continue
    commit = re.fullmatch(r"github:FORA-Protocol/protocol#([0-9a-f]{40})", spec)
    if re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?", spec):
        print("version", spec)
    elif commit:
        print("commit", commit.group(1))
    else:
        print("other", spec)
    break
' "$1"
}

require_nonempty() {
    local value="$1" what="$2"
    if [ -z "${value}" ]; then
        fail "${what}: pin not found (guard needs updating if the pin moved)"
        return 1
    fi
}

# The repository both pre-release [tool.uv.sources] entries must install from, and
# the URL uv.lock must resolve them from. It is the Go module path as an https
# URL, spelled the way the pyproject and the lock spell it.
EXPECTED_GIT_URL="https://github.com/FORA-Protocol/protocol"

# The URL a package-lock.json resolves the pre-release TypeScript pin from, less
# its `#<commit>`. It is how npm records a `github:FORA-Protocol/protocol#<sha>`
# spec. `npm ci` still fetches the commit's tarball over HTTPS from codeload and
# clones only when that fails; the ssh spelling is npm's name for the repository,
# not the transport it uses.
NPM_GIT_URL="git+ssh://git@github.com/FORA-Protocol/protocol.git"

# Go's own definition of a pseudo-version, without the leading "v": the pattern
# golang.org/x/mod/module uses in IsPseudoVersion, with \d written as [0-9] for
# ERE. It accepts the three shapes Go writes -- v0.0.0-<time>-<hash>,
# vX.Y.(Z+1)-0.<time>-<hash> and vX.Y.Z-<pre>.0.<time>-<hash> -- and nothing a
# release tag such as v1.2.3 or v1.2.3-rc.1 looks like. Using Go's pattern rather
# than a narrower one means a version Go treats as a commit is never checked here
# as a release.
PSEUDO_VERSION_RE='^[0-9]+\.(0\.0-|[0-9]+\.[0-9]+-([^+]*\.)?0\.)[0-9]{14}-[A-Za-z0-9]+(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'

# ----- 0. Which form this tree is in -----
# Read from go.mod and nowhere else, as the header says. An unreadable go.mod pin
# is checked as the release form, where section 5 reports the pin missing; every
# other section still runs.
#
# `|| true`: a missing pin must reach require_nonempty's FAIL in section 5, not
# trip set -e/pipefail inside the extraction pipeline and abort silently.
gomod_ver="$(gomod_version "${repo_root}/go.mod" || true)"
pin_form="release"
if [[ "${gomod_ver}" =~ ${PSEUDO_VERSION_RE} ]]; then
    pin_form="pre-release"
fi
echo "FORM  ${pin_form}: go.mod requires ${gomod_ver:+v}${gomod_ver:-no readable version of the protocol module}"

# ----- 1+2. Python manifest version uniformity -----
# `|| true`: a missing pin must reach require_nonempty's FAIL, not trip
# set -e/pipefail inside the extraction pipeline and abort silently.
e2e_proto_ver="$(pyproject_version "${repo_root}/tests/e2e/pyproject.toml" 'fora-protocol' || true)"
e2e_sdk_ver="$(pyproject_version "${repo_root}/tests/e2e/pyproject.toml" 'fora-protocol-sdk' || true)"

require_nonempty "${e2e_proto_ver}" "tests/e2e/pyproject.toml fora-protocol" || true
require_nonempty "${e2e_sdk_ver}" "tests/e2e/pyproject.toml fora-protocol-sdk" || true

if [ -n "${e2e_proto_ver}" ] && [ -n "${e2e_sdk_ver}" ]; then
    if [ "${e2e_proto_ver}" = "${e2e_sdk_ver}" ]; then
        pass "tests/e2e/pyproject.toml: fora-protocol == fora-protocol-sdk (${e2e_sdk_ver})"
    else
        fail "tests/e2e/pyproject.toml: fora-protocol ${e2e_proto_ver} != fora-protocol-sdk ${e2e_sdk_ver}"
    fi
fi

# ----- 3. uv.lock agrees with its pyproject -----
# Release form: invariant 3. Pre-release form: P1 to P4 and P7, in
# check_prerelease_python below. Both readings of uv.lock are defined here and the
# form decides which one runs.
#
# Reads the version the lock RESOLVED and the index it resolved it from, and only
# from a registry source. A lock that went back to a git source answers empty and
# is reported as unpinned: the whole point of the release pin is that the harness
# installs what everyone else installs, and a git source is exactly the state this
# move left behind.
#
# The per-distribution reads below are necessary and NOT sufficient, which is what
# the two whole-file checks after them are for. Reading one [[package]] table says
# nothing about the rest of the lock, and nothing at all about the pyproject that
# feeds it -- and both of those are places the release pin can be undone while
# every version string here still agrees.
check_uv_lock() {
    local lock="$1" dep="$2" expected="$3" label="$4"
    [ -n "${expected}" ] || return 0
    local resolved version index
    resolved="$(uv_lock_version "${repo_root}/${lock}" "${dep}" || true)"
    if [ -z "${resolved}" ]; then
        fail "${lock}: ${dep} is not resolved from a registry (guard needs updating if the dep moved, or the lock went back to a git source) — rerun 'uv lock'"
        return 0
    fi
    version="${resolved%% *}"
    index="${resolved#* }"
    if [ "${version}" != "${expected}" ]; then
        fail "${lock}: resolves ${dep} ${version} but ${label} pins ${expected} — rerun 'uv lock'"
    elif [ "${index}" != "${EXPECTED_INDEX}" ]; then
        fail "${lock}: resolves ${dep} ${version} from ${index}, not ${EXPECTED_INDEX} — an index that happens to serve a package of that name and version serves a different artifact"
    else
        pass "${lock}: ${dep} resolves at the ${label} pin (${version}) from ${EXPECTED_INDEX}"
    fi
}

# The whole-file property the reads above do not carry. Before the release move
# this section swept the entire lock for protocol git revisions; the per-package
# read replaced that sweep and nothing replaced the property, so a `requires-dist`
# entry or an extra package block could still reach the repository over git while
# both reads above passed. In the release form NOTHING in this lock should name
# that repository, which makes the check a text sweep with no exceptions.
check_uv_lock_strays() {
    local strays
    strays="$(protocol_git_mentions "${repo_root}/tests/e2e/uv.lock" || true)"
    if [ -n "${strays}" ]; then
        fail "tests/e2e/uv.lock: still names FORA-Protocol/protocol at line(s) $(echo "${strays}" | cut -d: -f1 | tr '\n' ' ')— in the release form no entry should reach the repository over git — rerun 'uv lock'"
    else
        pass "tests/e2e/uv.lock: no entry reaches the protocol repository over git"
    fi
}

# ...and the same property one level up, in the file uv actually reads. A
# [tool.uv.sources] override sits ABOVE the ==version pin, so a release tree that
# keeps the pre-release table puts the harness on a git checkout while
# `==<version>` still reads correct in every check above. That is P7's
# half-converted state seen from the release side, and the message says so.
check_uv_override() {
    local dep="$1"
    uv_source_overrides "${repo_root}/tests/e2e/pyproject.toml" "${dep}"
    if [ -n "${uv_override_error}" ]; then
        fail "tests/e2e/pyproject.toml: the [tool.uv.sources] table was NOT read (${uv_override_error}) — so this check cannot say whether an override puts ${dep} back on a git checkout. Fix the reading, then re-run."
    elif [ -n "${uv_override}" ]; then
        fail "tests/e2e/pyproject.toml: [tool.uv.sources] overrides $(echo "${uv_override}" | tr '\n' ' ')while go.mod requires the release v${gomod_ver} — a half-converted pin. A source override wins over the ==version pin, so uv installs from it and every version string here still agrees. Delete the table to finish the release form, or re-pin go.mod to the commit's pseudo-version for the pre-release form."
    else
        pass "tests/e2e/pyproject.toml: no [tool.uv.sources] override for ${dep}"
    fi
}


# The pre-release reading of the Python pins: P7 first, then P2, P1, P3 and P4.
#
# P7 is decided here in bash, from the same key lookup the release form uses, so
# the two forms cannot disagree about whether an entry exists. The rest is one
# Python program, because each step reads parsed TOML and the steps share the
# commit P2 establishes. It prints one PASS or FAIL line per property, then the
# commit as "REV <sha>" when P2 established one, then "DONE". A run that stops
# before "DONE" is reported as a failure naming where it stopped, so the PASS
# lines it printed first cannot carry the run.
#
# Sets the global prerelease_rev, which section 5 reads for P5. It stays empty
# whenever P2 failed, and then P5 does not compare the TypeScript commit with it:
# there is no single rev to compare with, and the P2 failure has already failed
# the run.
prerelease_rev=""
check_prerelease_python() {
    local pyproject="${repo_root}/tests/e2e/pyproject.toml"
    local lock="${repo_root}/tests/e2e/uv.lock"
    local dep count missing="" proto_key="" sdk_key="" go_hash result line tmp
    prerelease_rev=""
    for dep in fora-protocol fora-protocol-sdk; do
        uv_source_overrides "${pyproject}" "${dep}"
        if [ -n "${uv_override_error}" ]; then
            fail "tests/e2e/pyproject.toml: the [tool.uv.sources] table was NOT read (${uv_override_error}) — so no pre-release pin was checked. Fix the reading, then re-run."
            return 0
        fi
        count="$(printf '%s' "${uv_override}" | grep -c . || true)"
        if [ "${count}" -eq 0 ]; then
            missing="${missing:+${missing}, }${dep}"
            continue
        fi
        if [ "${count}" -gt 1 ]; then
            fail "tests/e2e/pyproject.toml: [tool.uv.sources] has ${count} keys naming ${dep} ($(echo "${uv_override}" | tr '\n' ' ')) — they are one distribution after PEP 503 normalisation, so which entry uv installs from is not visible in the file"
            return 0
        fi
        if [ "${dep}" = "fora-protocol" ]; then proto_key="${uv_override}"; else sdk_key="${uv_override}"; fi
    done
    if [ -n "${missing}" ]; then
        fail "half-converted pin: go.mod requires the pseudo-version v${gomod_ver}, a commit of the protocol repository, but tests/e2e/pyproject.toml has no [tool.uv.sources] entry for ${missing} — uv installs a distribution without one from PyPI at its last release while Go builds against the commit. Add the entry at the commit go.mod names, or re-pin go.mod to a release."
        return 0
    fi
    # The commit prefix is the pseudo-version's last hyphen-separated field, less
    # any +build suffix.
    go_hash="${gomod_ver##*-}"
    go_hash="${go_hash%%+*}"
    tmp="$(mktemp -d)" || { fail "could not create a temp directory, so the pre-release pins were NOT checked"; return 0; }
    result="$(python3 - "${pyproject}" "${lock}" "${go_hash}" "${EXPECTED_GIT_URL}" "${proto_key}" "${sdk_key}" <<'PYPRE' 2>"${tmp}/stderr" || true
import re
import sys
import tomllib
from urllib.parse import parse_qs, urlsplit

pyproject, lock, go_hash, url, proto_key, sdk_key = sys.argv[1:7]
# distribution -> (its key in [tool.uv.sources], the subdirectory it builds from)
EXPECTED = {
    "fora-protocol": (proto_key, "gen/python"),
    "fora-protocol-sdk": (sdk_key, "sdk/python"),
}
SUBDIRS = {subdir for _, subdir in EXPECTED.values()}
ENTRY_KEYS = {"git", "rev", "subdirectory"}
FULL_REV = re.compile(r"[0-9a-f]{40}")
REPO = re.compile(r"fora-protocol/protocol", re.IGNORECASE)
URL_TOKEN = re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://[^\"\s]*")


def git_parts(value):
    parts = urlsplit(value)
    query = parse_qs(parts.query, keep_blank_values=True)
    return f"{parts.scheme}://{parts.netloc}{parts.path}", query, parts.fragment


def tables(dep):
    return [p for p in packages if isinstance(p, dict) and p.get("name") == dep]


with open(pyproject, "rb") as handle:
    sources = tomllib.load(handle)["tool"]["uv"]["sources"]
with open(lock, "rb") as handle:
    packages = tomllib.load(handle).get("package") or []
with open(lock, encoding="utf-8") as handle:
    lock_lines = handle.read().splitlines()

# P2: both entries name one commit of the protocol repository.
problems, revs = [], set()
for dep, (key, subdir) in EXPECTED.items():
    entry = sources.get(key)
    if (
        not isinstance(entry, dict)
        or set(entry) != ENTRY_KEYS
        or not all(isinstance(v, str) for v in entry.values())
    ):
        problems.append(f"{key} = {entry!r} is not exactly {{ git, rev, subdirectory }}")
        continue
    if entry["git"] != url:
        problems.append(f"{key} installs from {entry['git']}, not {url}")
    if entry["subdirectory"] != subdir:
        problems.append(f"{key} builds from subdirectory {entry['subdirectory']}, not {subdir}")
    if not FULL_REV.fullmatch(entry["rev"]):
        problems.append(f"{key} names rev {entry['rev']!r}, which is not a full 40-hex commit")
    revs.add(entry["rev"])
if len(revs) > 1:
    problems.append("the entries name different revs: " + ", ".join(sorted(revs)))
rev = None
if problems:
    print(
        "FAIL tests/e2e/pyproject.toml: the [tool.uv.sources] entries do not name one"
        " commit of the protocol repository: " + "; ".join(problems)
        + " — so the go.mod, uv.lock and ancestry checks against that commit were NOT run"
    )
else:
    rev = revs.pop()
    print(f"PASS tests/e2e/pyproject.toml: both [tool.uv.sources] entries install from {url} at {rev}")

# P1: go.mod names the same commit.
if rev is not None:
    if len(go_hash) == 12 and rev.startswith(go_hash):
        print(f"PASS pre-release pins agree: go.mod and both Python distributions name commit {go_hash}")
    else:
        print(
            f"FAIL pre-release pin split-brain: go.mod requires commit {go_hash} but"
            f" [tool.uv.sources] installs {rev} — re-pin both to one commit"
        )

# P3: the lock resolves both from that commit, and nothing else from the repository.
if rev is not None:
    problems = []
    for dep, (_, subdir) in EXPECTED.items():
        found = tables(dep)
        if len(found) != 1:
            problems.append(f"{len(found)} [[package]] tables name {dep}")
            continue
        source = found[0].get("source")
        git = source.get("git") if isinstance(source, dict) and set(source) == {"git"} else None
        if not isinstance(git, str):
            problems.append(f"{dep} resolves from {source!r}, not from git")
            continue
        base, query, fragment = git_parts(git)
        if base != url or query != {"subdirectory": [subdir], "rev": [rev]} or fragment != rev:
            problems.append(f"{dep} resolves from {git}")
    # A package's SOURCE, not the whole table: the harness's own table names the
    # repository in its requires-dist metadata, which the line sweep below reads.
    for package in packages:
        if not isinstance(package, dict) or package.get("name") in EXPECTED:
            continue
        if REPO.search(repr(package.get("source"))):
            problems.append(f"package {package.get('name')!r} also resolves from the protocol repository")
    for number, line in enumerate(lock_lines, 1):
        mentions = len(REPO.findall(line))
        if not mentions:
            continue
        urls = [u for u in URL_TOKEN.findall(line) if REPO.search(u)]
        stray = len(urls) != mentions
        for found_url in urls:
            base, query, fragment = git_parts(found_url)
            stray = stray or (
                base != url
                or set(query) != {"subdirectory", "rev"}
                or query["rev"] != [rev]
                or len(query["subdirectory"]) != 1
                or query["subdirectory"][0] not in SUBDIRS
                or fragment not in ("", rev)
            )
        if stray:
            problems.append(f"line {number} names the protocol repository other than at {rev}")
    if problems:
        print(
            f"FAIL tests/e2e/uv.lock: does not resolve both distributions from {url} at {rev}"
            " and from nothing else: " + "; ".join(problems) + " — rerun 'uv lock'"
        )
    else:
        print(
            f"PASS tests/e2e/uv.lock: both distributions resolve from {url} at {rev},"
            " and no other entry reaches the repository"
        )

# P4: the two distributions resolve at one version string.
versions = {}
for dep in EXPECTED:
    found = tables(dep)
    if len(found) == 1 and isinstance(found[0].get("version"), str):
        versions[dep] = found[0]["version"]
if len(versions) != len(EXPECTED):
    absent = ", ".join(sorted(set(EXPECTED) - set(versions)))
    print(f"FAIL tests/e2e/uv.lock: records no single version for {absent}, so the two distributions' versions were NOT compared")
elif versions["fora-protocol"] == versions["fora-protocol-sdk"]:
    print(f"PASS tests/e2e/uv.lock: fora-protocol == fora-protocol-sdk ({versions['fora-protocol']}) at the pinned commit")
else:
    print(
        f"FAIL tests/e2e/uv.lock: resolves fora-protocol {versions['fora-protocol']} but"
        f" fora-protocol-sdk {versions['fora-protocol-sdk']} — the harness would sign one"
        " payload shape while the services verify another"
    )

if rev is not None:
    print(f"REV {rev}")
print("DONE")
PYPRE
)"
    local done_seen=""
    while IFS= read -r line; do
        case "${line}" in
            "PASS "*) pass "${line#PASS }" ;;
            "FAIL "*) fail "${line#FAIL }" ;;
            "REV "*)  prerelease_rev="${line#REV }" ;;
            DONE)     done_seen=1 ;;
            "")       ;;
            *)        fail "the pre-release check printed an unexpected line: ${line}" ;;
        esac
    done <<< "${result}"
    if [ -z "${done_seen}" ]; then
        prerelease_rev=""
        fail "the pre-release Python pin check did not finish ($(tr '\n' ' ' < "${tmp}/stderr" 2>/dev/null || true)) — a check that did not run is not a check that succeeded"
    fi
    rm -rf "${tmp}"
}

if [ "${pin_form}" = "release" ]; then
    check_uv_lock "tests/e2e/uv.lock" "fora-protocol-sdk" "${e2e_sdk_ver}" "tests/e2e/pyproject.toml"
    check_uv_lock "tests/e2e/uv.lock" "fora-protocol" "${e2e_proto_ver}" "tests/e2e/pyproject.toml"
    check_uv_lock_strays
    check_uv_override "fora-protocol-sdk"
    check_uv_override "fora-protocol"
else
    check_prerelease_python
fi

# ----- 4. package-lock.json agrees with its package.json -----
# Release form: invariant 4, the version the manifest pins against every version
# the lock resolves. Pre-release form: the first half of P6, the commit the
# manifest pins against the lock's root spec and every URL it resolves. A pin in
# the other form's spelling is P7's half-converted state and is named so.
#
# Sets the globals npm_manifest_ver (release form) and npm_manifest_commit
# (pre-release form), each empty unless the manifest pins in that form (no
# subshell: `failures` must accumulate in the main shell).
npm_manifest_ver=""
npm_manifest_commit=""
check_npm_pair() {
    local dir="$1"
    local manifest="${repo_root}/${dir}/package.json"
    local lock="${repo_root}/${dir}/package-lock.json"
    local pin kind value
    npm_manifest_ver=""
    npm_manifest_commit=""
    pin="$(package_json_pin "${manifest}" || true)"
    require_nonempty "${pin}" "${dir}/package.json @fora-protocol/sdk" || return 0
    kind="${pin%% *}"
    value="${pin#* }"
    case "${pin_form}:${kind}" in
        release:version)
            npm_manifest_ver="${value}"
            check_npm_lock_versions "${dir}" "${lock}" "${value}"
            ;;
        pre-release:commit)
            npm_manifest_commit="${value}"
            check_npm_lock_commit "${dir}" "${lock}" "${value}"
            ;;
        release:commit)
            fail "half-converted pin: ${dir}/package.json pins @fora-protocol/sdk to the protocol commit ${value} while go.mod requires the release v${gomod_ver} — the edge would build from a commit while Go and the harness run the release. Pin @fora-protocol/sdk at ${gomod_ver:-the release go.mod requires}, or re-pin go.mod to the commit's pseudo-version for the pre-release form."
            ;;
        pre-release:version)
            fail "half-converted pin: go.mod requires the pseudo-version v${gomod_ver}, a commit of the protocol repository, but ${dir}/package.json pins @fora-protocol/sdk ${value} from the npm registry — the edge would run the last release while Go and the harness run the commit. Pin github:FORA-Protocol/protocol#<the full commit go.mod names>, or re-pin go.mod to a release."
            ;;
        release:*)
            fail "${dir}/package.json @fora-protocol/sdk: pin not found — '${value}' is not an exact version, and a range lets npm install a release nothing else here names"
            ;;
        *)
            fail "${dir}/package.json @fora-protocol/sdk: pin not found — '${value}' is not github:FORA-Protocol/protocol#<full 40-hex commit>. A branch or a tag moves without anyone re-pinning, an abbreviated commit is not what the lock records, and another repository is not the protocol."
            ;;
    esac
}

check_npm_lock_versions() {
    local dir="$1" lock="$2" version="$3" versions stale
    versions="$(lock_sdk_field "${lock}" version || true)"
    if [ -z "${versions}" ]; then
        fail "${dir}/package-lock.json: no @fora-protocol/sdk entry found (guard needs updating if the dep moved)"
        return 0
    fi
    stale="$(echo "${versions}" | grep -vxF "${version}" || true)"
    if [ -n "${stale}" ]; then
        fail "${dir}: package-lock.json resolves @fora-protocol/sdk $(echo "${stale}" | tr '\n' ' ')but package.json pins ${version} — rerun 'npm install'"
    else
        pass "${dir}: package-lock.json matches package.json pin (@fora-protocol/sdk ${version})"
    fi
}

check_npm_lock_commit() {
    local dir="$1" lock="$2" commit="$3" resolved stale root_spec
    local want="${NPM_GIT_URL}#${commit}" spec="github:FORA-Protocol/protocol#${commit}"
    root_spec="$(lock_root_spec "${lock}" || true)"
    resolved="$(lock_sdk_field "${lock}" resolved || true)"
    if [ -z "${resolved}" ]; then
        fail "${dir}/package-lock.json: no @fora-protocol/sdk entry records where it resolves from (guard needs updating if the dep moved) — rerun 'npm install'"
        return 0
    fi
    stale="$(echo "${resolved}" | grep -vxF "${want}" || true)"
    if [ "${root_spec}" != "${spec}" ]; then
        fail "${dir}: package-lock.json records the root dependency on @fora-protocol/sdk as '${root_spec}' but package.json pins ${spec} — the lock was not regenerated after the re-pin; rerun 'npm install'"
    elif [ -n "${stale}" ]; then
        fail "${dir}: package-lock.json resolves @fora-protocol/sdk from $(echo "${stale}" | tr '\n' ' ')but package.json pins commit ${commit}, which npm records as ${want} — npm installs what the lock resolved; rerun 'npm install'"
    else
        pass "${dir}: package-lock.json resolves @fora-protocol/sdk from ${want}, the commit package.json pins"
    fi
}

check_npm_pair "src/edge"
edge_ver="${npm_manifest_ver}"
edge_commit="${npm_manifest_commit}"
check_npm_pair "tests/e2e/fastly-edge"
fastly_ver="${npm_manifest_ver}"
fastly_commit="${npm_manifest_commit}"

# ----- 5. Cross-language uniformity -----
# Release form: invariant 5, one release named by all four manifests. Pre-release
# form: P5, both TypeScript manifests on the one commit go.mod and the Python pins
# name. The redirect refusal runs first in both forms. The reasoning for
# each shape is in the header; it is not restated here, because a design argument
# written in two places is a design argument that gets edited in one.
#
# gomod_ver was read in section 0, where it decided the form.
require_nonempty "${gomod_ver}" "go.mod github.com/FORA-Protocol/protocol" || true

# Before comparing anything: a `replace` or `exclude` naming the protocol module
# makes every comparison below describe a module the Go build does not use. The
# require line still reads the pin, the pin still resolves, and the build reads
# somewhere else entirely.
gomod_redirects "${repo_root}/go.mod"
if [ -n "${gomod_redirect_error}" ]; then
    fail "go.mod: the replace/exclude directive list was NOT read (${gomod_redirect_error}) — so this check cannot say whether a directive redirects github.com/FORA-Protocol/protocol, and every comparison below may be describing a module the Go build never opens. Fix the reading, then re-run."
elif [ -n "${gomod_redirect}" ]; then
    fail "go.mod: a $(echo "${gomod_redirect}" | tr '\n' ' ')directive names github.com/FORA-Protocol/protocol — the require line below is then not what the Go build reads, so nothing in this section describes the built module. Remove it; this repository pins the module and does not redirect it."
else
    pass "go.mod: no replace or exclude redirects the protocol module"
fi

# P5: both TypeScript manifests pin one commit, and it is the commit go.mod and the
# Python pins name. Each comparison is reported on its own, because each names a
# different pin to move. The Python rev is compared only when P2 established one.
check_prerelease_typescript() {
    local edge="$1" fastly="$2" go_hash agreed="yes"
    if [ "${edge}" != "${fastly}" ]; then
        fail "TypeScript pin split-brain: src/edge pins commit ${edge} but fastly-edge pins ${fastly} — both edge manifests pin one commit, and neither was compared with go.mod or the Python pins"
        return 0
    fi
    go_hash="${gomod_ver##*-}"
    go_hash="${go_hash%%+*}"
    if [ "${#go_hash}" -ne 12 ] || [ "${edge:0:12}" != "${go_hash}" ]; then
        fail "pre-release pin split-brain: go.mod requires commit ${go_hash} but both TypeScript manifests pin ${edge} — re-pin all three SDKs to one commit"
        agreed=""
    fi
    if [ -n "${prerelease_rev}" ] && [ "${edge}" != "${prerelease_rev}" ]; then
        fail "pre-release pin split-brain: [tool.uv.sources] installs ${prerelease_rev} but both TypeScript manifests pin ${edge} — re-pin all three SDKs to one commit"
        agreed=""
    fi
    if [ -n "${agreed}" ] && [ -n "${prerelease_rev}" ]; then
        pass "all three SDKs build from one commit: both TypeScript manifests pin ${edge}, the commit go.mod and the Python pins name"
    elif [ -n "${agreed}" ]; then
        pass "both TypeScript manifests pin ${edge}, the commit go.mod names (the Python rev was not established, so it was not compared)"
    fi
}

if [ "${pin_form}" = "release" ]; then
    if [ -n "${gomod_ver}" ] && [ -n "${e2e_sdk_ver}" ]; then
        if [ "${gomod_ver}" = "${e2e_sdk_ver}" ]; then
            pass "release pins agree: go.mod v${gomod_ver} == Python ${e2e_sdk_ver}"
        else
            fail "release pin split-brain: go.mod v${gomod_ver} != Python ${e2e_sdk_ver} — re-pin uniformly"
        fi
    fi

    # The module resolution section 7 reads. Leaves module_json empty when the module
    # could not be resolved at all, with the reason in go_module_error. Called WITHOUT
    # a command substitution on purpose — see the note on resolve_module.
    if [ -n "${gomod_ver}" ]; then
        resolve_module "${gomod_ver}"
    fi

    # The Go pin must be a RELEASED TAG, not merely a version string the other three
    # happen to match. A version string that matches all three and resolves to some
    # other ref would otherwise pass as a release.
    #
    # A pseudo-version Go recognises never reaches this: section 0 checks it as the
    # pre-release form, using Go's own pattern. The "(none)" branch below stays so
    # that a version which resolves to an untagged commit anyway is refused here
    # rather than passed.
    #
    # It reads the resolution section 7 already performs, so it costs no extra work.
    # Four outcomes, each with its own remedy.
    if [ -n "${gomod_ver}" ]; then
        tag_line=""
        if [ -n "${module_json}" ]; then
            tag_line="$(tag_origin "$(printf '%s' "${module_json}" | json_field Info)" || true)"
        fi
        tag_ref="$(echo "${tag_line}" | awk '{print $2}')"
        if [ -z "${module_json}" ]; then
            fail "could not resolve v${gomod_ver} through the Go toolchain, so go.mod's pin was NOT shown to be a released tag: ${go_module_error} — a check that did not run is not a check that succeeded"
        elif [ -z "${tag_ref}" ]; then
            fail "the module cache holds v${gomod_ver} but records no VCS origin for it, so go.mod's pin was NOT shown to be a released tag — the proxy that served this version did not report one; a check that did not run is not a check that succeeded"
        elif [ "${tag_ref}" = "(none)" ]; then
            fail "the module cache resolves v${gomod_ver} with no tag ref — the pin is a pseudo-version, not a release. Re-pin go.mod to a v* tag."
        elif [ "${tag_ref}" != "refs/tags/v${gomod_ver}" ]; then
            fail "the module cache resolves v${gomod_ver} from ${tag_ref}, not refs/tags/v${gomod_ver} — the pin is not a released tag"
        else
            pass "go.mod pins a released tag (refs/tags/v${gomod_ver})"
        fi
    fi

    # All four pins name the same release now, so this is one string compared four
    # ways. It used to resolve the Go tag to the commit it points at, because the two
    # TypeScript manifests installed from git and named a revision instead of a
    # version; that resolution went when the last git pin did.
    if [ -n "${gomod_ver}" ] && [ -n "${edge_ver}" ] && [ -n "${fastly_ver}" ]; then
        if [ "${gomod_ver}" = "${edge_ver}" ] && [ "${gomod_ver}" = "${fastly_ver}" ]; then
            pass "cross-language pins agree: v${gomod_ver} in go.mod, and both TypeScript manifests pin @fora-protocol/sdk ${gomod_ver}"
        else
            fail "cross-language pin split-brain: go.mod v${gomod_ver}, but src/edge=${edge_ver} fastly-edge=${fastly_ver} — re-pin every manifest to one release"
        fi
    fi

else
    if [ -n "${edge_commit}" ] && [ -n "${fastly_commit}" ]; then
        check_prerelease_typescript "${edge_commit}" "${fastly_commit}"
    fi
    # The module resolution section 7 reads, for go.mod's pseudo-version.
    resolve_module "${gomod_ver}"
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
# So the rev is checked first, out of node_modules/.package-lock.json (the
# version in the release form, the resolved commit in the pre-release form), and a
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
    local dir="$1" commit="$2"
    local lock="${repo_root}/${dir}/package-lock.json"
    local installed="${repo_root}/${dir}/node_modules/@fora-protocol/sdk/package.json"
    local tree_lock="${repo_root}/${dir}/node_modules/.package-lock.json"
    if [ ! -f "${installed}" ]; then
        fail "${dir}: no installed copy at node_modules/@fora-protocol/sdk — run 'npm ci' so the lock's recorded SDK metadata can be checked against the pinned revision"
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
    local expected=""
    if [ "${pin_form}" = "pre-release" ] && [ -n "${commit}" ]; then
        expected="${NPM_GIT_URL}#${commit}"
    fi
    result="$(python3 - "${lock}" "${installed}" "${tree_lock}" "${pin_form}" "${expected}" <<'PYEOF' || true
import json, sys

SDK = "node_modules/@fora-protocol/sdk"
REGISTRY = "https://registry.npmjs.org/"
lock, installed, tree_lock, form, expected = sys.argv[1:6]

# Fields that describe the PACKAGE, so the installed copy can be asked about them.
# Compared only when the lock records one: a registry entry's field set comes from
# the registry's abbreviated metadata rather than from the packed package.json, so
# the lock legitimately carries fewer of these than the package declares.
COMPARED = (
    "name",
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

# Fields that must be present. Without them there is nothing holding the entry to
# one artifact, and "the lock records fewer fields" would be a way past the
# comparison above rather than a fact about the registry.
REQUIRED = ("version", "resolved", "integrity")

# Keys npm derives, or takes from somewhere other than the package, so the
# installed file has nothing to compare them to. Two are worth naming: npm
# normalises a string "bin" into an object, so the two spellings would differ for
# a reason that is not a substitution; and "deprecated" comes from registry
# metadata, which the package itself does not declare.
NOT_COMPARABLE = (
    "resolved", "integrity", "link", "dev", "optional", "peer",
    "devOptional", "inBundle", "extraneous", "hasInstallScript", "bin",
    "deprecated",
)

with open(lock) as f:
    entry = json.load(f).get("packages", {}).get(SDK)
if entry is None:
    print("MISSING the lock records no SDK entry")
    raise SystemExit
with open(tree_lock) as f:
    tree_entry = json.load(f).get("packages", {}).get(SDK, {})

missing = [f for f in REQUIRED if not entry.get(f)]
if missing:
    print("MISSING the lock's SDK entry has no %s" % ", ".join(missing))
    raise SystemExit

# The release form resolves from the registry. The pre-release form resolves from
# the protocol repository at the commit the manifest pins, and a manifest that
# names no commit has already failed section 4, which leaves nothing to hold the
# entry to. (This heredoc holds exactly two apostrophes, in the MISSING and
# UNCOVERED messages: bash 3.2 pairs apostrophes up while it scans the enclosing
# command substitution, so an odd one breaks the whole script under it.)
resolved = entry["resolved"]
if form == "pre-release" and not expected:
    print("UNVERIFIED package.json names no commit, so the SDK entry of the lock was NOT checked against one")
    raise SystemExit
if form == "pre-release" and resolved != expected:
    print("SOURCE the lock resolves the SDK from %s, not %s" % (resolved, expected))
    raise SystemExit
if form != "pre-release" and not resolved.startswith(REGISTRY):
    print("SOURCE the lock resolves the SDK from %s, not %s" % (resolved, REGISTRY))
    raise SystemExit

integrity = entry["integrity"]
if not integrity.startswith("sha512-"):
    print("SOURCE the lock records integrity %r, which is not a sha512 SRI" % integrity)
    raise SystemExit

# The installed copy stands for the pinned artifact only if npm built it from the
# artifact the lock names. A release is named by its version. A commit is not:
# every commit between two releases carries the version of the last release, so
# the pre-release form matches the URL the tree was resolved from, commit included.
key = "resolved" if form == "pre-release" else "version"
want, have = entry[key], tree_entry.get(key)
if not have:
    print("UNVERIFIED node_modules records no %s for the SDK" % key)
    raise SystemExit
if want != have:
    print("UNVERIFIED node_modules holds %s while the lock names %s" % (have, want))
    raise SystemExit

uncovered = sorted(set(entry) - set(COMPARED) - set(NOT_COMPARABLE))
if uncovered:
    print("UNCOVERED the SDK's lock entry carries %s" % ", ".join(uncovered))
    raise SystemExit

with open(installed) as f:
    declared = json.load(f)

present = [f for f in COMPARED if f in entry]
drift = [f for f in present if entry.get(f) != declared.get(f)]
if drift:
    print("DRIFT disagrees with the installed %s on %s (lock %s, installed %s)" % (
        want,
        ", ".join(drift),
        json.dumps({f: entry.get(f) for f in drift}, sort_keys=True),
        json.dumps({f: declared.get(f) for f in drift}, sort_keys=True),
    ))
elif form == "pre-release":
    print("OK from %s with a sha512 integrity; " % resolved + ", ".join(present))
else:
    print("OK from the npm registry with a sha512 integrity; " + ", ".join(present))
PYEOF
)"
    case "${result}" in
        OK*)         pass "${dir}: package-lock.json SDK entry is ${result#OK }" ;;
        DRIFT*)      fail "${dir}: package-lock.json ${result#DRIFT } — the entry was edited rather than regenerated; delete it from the lock and rerun 'npm install'" ;;
        SOURCE*)     fail "${dir}: ${result#SOURCE } — a source other than the pinned one serves a different artifact under the same name and version" ;;
        MISSING*)    fail "${dir}: ${result#MISSING } — run 'npm install' so the SDK's metadata is recorded" ;;
        UNCOVERED*)  fail "${dir}: ${result#UNCOVERED } — a field this check does not compare, so the pinned release is only PARTLY verified. Add it to COMPARED (the package declares it) or to NOT_COMPARABLE (npm derives it), and say which in the commit." ;;
        UNVERIFIED*) fail "${dir}: ${result#UNVERIFIED } — the installed copy cannot stand for the pinned release, so nothing was checked. Run 'npm ci'." ;;
        *)           fail "${dir}: the SDK metadata check did not run (output: ${result:-none}) — a check that did not run is not a check that succeeded" ;;
    esac
}

check_npm_sdk_metadata "src/edge" "${edge_commit}"
check_npm_sdk_metadata "tests/e2e/fastly-edge" "${fastly_commit}"

# ----- 7. Every corpus copy matches the pinned module's -----
# The module directory comes from the SAME resolution section 5 made, rather than
# from a second `go list -m`. Two lookups asking different questions is how this
# file could have compared testdata against one copy of "the pinned module" while
# section 5 compared revisions against another: `go list -m` resolves through the
# build list and follows a `replace`, and an explicit module@version download does
# not. Section 5 now rejects a replace outright, and one resolution serves both.
#
# The lists are every copy under testdata/. Adding a corpus means adding it to
# one of them — a copy this section does not name is a copy nothing refreshes,
# which is the failure mode the check exists to prevent. corpora are verbatim
# copies; wrapped_corpora pair a local file whose "vectors" member is the
# module file's whole array with that module file's name.
corpora=(
    "acceptance-vectors.json"
    "licenseterm-vectors.json"
    "manifest-version-vectors.json"
    "request-acceptance-vectors.json"
    "thumbprint-vectors.json"
)
module_dir=""
if [ -n "${module_json}" ]; then
    module_dir="$(printf '%s' "${module_json}" | json_field Dir)"
fi

wrapped_corpora=(
    "pop-sign-vectors.json:pop-vectors.json"
)

# same_vectors succeeds when the "vectors" member of the local file $1 equals
# the module's whole array in $2, compared as JSON values.
same_vectors() {
    python3 -c 'import json, sys
with open(sys.argv[1]) as local, open(sys.argv[2]) as module:
    sys.exit(0 if json.load(local)["vectors"] == json.load(module) else 1)' "$1" "$2" 2>/dev/null
}

# check_corpus compares testdata/$1 with the module's sdk/go/helpers/testdata/$2:
# byte for byte when $3 is "bytes", its "vectors" member as JSON when "vectors".
check_corpus() {
    local corpus_rel="testdata/$1" module_name="$2" mode="$3"
    local corpus_local="${repo_root}/${corpus_rel}"
    local corpus_module="${module_dir}/sdk/go/helpers/testdata/${module_name}"

    if [ ! -f "${corpus_local}" ]; then
        fail "${corpus_rel} is missing — it is the oracle the corpus replays read; restore it from the pinned module"
    elif [ -z "${module_dir}" ]; then
        fail "could not resolve the pinned protocol module's directory, so ${corpus_rel} was NOT compared against it: ${go_module_error:-the go.mod pin could not be read} — a check that did not run is not a check that succeeded"
    elif [ ! -f "${corpus_module}" ]; then
        fail "the pinned module has no sdk/go/helpers/testdata/${module_name}, so ${corpus_rel} was NOT compared — the corpus moved upstream and this check needs its new path"
    elif [ "${mode}" = bytes ] && ! cmp -s "${corpus_local}" "${corpus_module}"; then
        fail "${corpus_rel} differs from the pinned module's copy — refresh it from the module at every re-pin (never edit it by hand): cp \"${corpus_module}\" \"${corpus_local}\""
    elif [ "${mode}" = vectors ] && ! same_vectors "${corpus_local}" "${corpus_module}"; then
        fail "${corpus_rel}'s \"vectors\" differ from the pinned module's ${module_name} — at every re-pin replace that member with the whole array of \"${corpus_module}\" (never edit it by hand)"
    elif [ "${mode}" = bytes ]; then
        pass "${corpus_rel} is byte-identical to the pinned module's copy"
    else
        pass "${corpus_rel}'s \"vectors\" equal the pinned module's ${module_name}"
    fi
}

for corpus_name in "${corpora[@]}"; do
    check_corpus "${corpus_name}" "${corpus_name}" bytes
done
for pair in "${wrapped_corpora[@]}"; do
    check_corpus "${pair%%:*}" "${pair#*:}" vectors
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
        entry = json.load(f).get("packages", {}).get("node_modules/@fora-protocol/sdk", {})
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
