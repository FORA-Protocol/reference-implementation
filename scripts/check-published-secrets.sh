#!/usr/bin/env bash
# check-published-secrets.sh
#
# Secret scan over the files the publish ships, and over the paths it holds back
# from inside them.
#
# src/, internal/, tests/, testdata/, deploy/, schemas/, docs/architecture/, the
# root build files, a curated scripts/ set and a curated set of individual docs/
# files are published verbatim to the public reference implementation, minus the
# paths DENY_PATHS holds back. A secret that reaches one of those paths is public
# the moment the next snapshot is pushed, and a push cannot be taken back.
#
# The publish tool runs the same scanner over the curated tree it is about to
# push. That run is the last line of defence, and it is too late to be the only
# one: it fires after the work is committed, on a machine that is about to
# publish, and a finding there blocks the release rather than the commit that
# caused it. This gate is the same check moved forward to `make quality`, so the
# branch goes red on the commit that introduces the finding.
#
# WHAT IT SCANS, and why each choice is load-bearing:
#
#   - The search roots are DERIVED from scripts/published-paths.sh, the same file
#     the publish tool reads. A second hand-kept copy of "what ships" is how the
#     publish set and the enforced set drifted apart before.
#
#   - INHERIT_FROM_MAIN is deliberately NOT a search root. Those files reach the
#     public repo from the public base, not from this tree; this tree's copies
#     are different files. Scanning them would report on content that never
#     ships.
#
#   - DENY_PATHS is deliberately NOT subtracted, unlike in the reference gate.
#     A held-back path sits under a published root and never ships, so a key
#     there is not yet a leak — but it is one edit of that array away from
#     becoming one, and this gate is the only secret scan the tree gets.
#     Subtracting the array would leave a key committed under one of those paths
#     unscanned everywhere.
#
#   - TRACKED files only, via `git ls-files`. A directory walk also sees
#     gitignored local output — generated dev keys under deploy/, node_modules/,
#     a virtualenv — none of which is published. The publish checks out tracked
#     content, so the gate reads tracked content.
#
#   - WORKING-TREE content, not the committed blob. A key pasted into a tracked
#     file and not yet committed is exactly what this gate exists to catch before
#     it becomes history.
#
# HOW IT SCANS: the listed files are copied into a temporary tree at their
# repo-relative paths and the scanner runs from inside it. Repo-relative paths
# are what make a reported finding portable between machines, .gitleaks.toml is
# found because config resolution follows --source, and a .gitleaksignore in the
# tree is found because its default lookup path is the working directory. That is
# the same invocation shape the publish tool uses, so the two runs agree.
#
# `--root <dir>` scans a tree other than this checkout. The argument, and why it
# is an argument, are described once in scripts/gate-args.sh. What is specific
# here: there is deliberately no seam for the temporary directory — this script
# deletes that path, and the only path it will delete is one `mktemp -d` handed it.
#
# Exits 0 when clean, 1 otherwise.

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=scripts/gate-args.sh
source "${script_dir}/gate-args.sh"
parse_gate_args "$@"

# shellcheck source=scripts/published-paths.sh
source "${script_dir}/published-paths.sh"

# The search roots ARE the published set: the directories, every allowlisted
# docs/ file, every root build file, and every allowlisted script by name. Unlike
# the reference gate, .gitignore and .dockerignore are included — they are
# published files like any other, and a secret in one is a secret either way.
SEARCH_ROOTS=( "${ALLOW_DIRS[@]}" "${ALLOW_DOC_FILES[@]}" "${ALLOW_ROOT_FILES[@]}" "${ALLOW_SCRIPTS[@]}" )

command -v gitleaks >/dev/null 2>&1 \
  || die "gitleaks not installed — install it (https://github.com/gitleaks/gitleaks) or run 'make install-tools'.
      This gate is mandatory: the paths it scans are published verbatim, and a
      push to the public repository cannot be taken back."

cd "${repo_root}"

git rev-parse --git-dir >/dev/null 2>&1 \
  || die "'${repo_root}' is not a git repository, so the published file set cannot be listed."

# Every search root must exist. A renamed or removed top-level directory would
# otherwise contribute no files, and the run would report PASS having scanned
# less than it claims. A green run has to mean the whole published set was read.
missing_roots=""
for r in "${SEARCH_ROOTS[@]}"; do
  [ -e "${r}" ] || missing_roots="${missing_roots} ${r}"
done
[ -z "${missing_roots}" ] \
  || die "search root(s) missing from the tree:${missing_roots}
      This gate scans the published path set; a root it cannot find is coverage
      it silently loses. Restore the path, or update scripts/published-paths.sh."

work="$(mktemp -d)" || die "cannot create a temporary directory for the scan"
trap 'rm -rf "${work}"' EXIT
staged="${work}/tree"
list_file="${work}/published-files"
mkdir -p "${staged}"

# Written to a file rather than read through a pipe or a command substitution.
# A pipe would hide git's exit status behind the reader's, and command
# substitution DROPS NUL bytes, which is the separator that makes -z safe.
git ls-files -z -- "${SEARCH_ROOTS[@]}" > "${list_file}" \
  || die "listing the published files failed — no conclusion can be drawn"

copied=0
last_dir=""
while IFS= read -r -d '' f; do
  d="${f%/*}"
  [ "${d}" = "${f}" ] && d="."
  # git ls-files emits sorted paths, so consecutive files share a directory and
  # this skips almost every mkdir.
  if [ "${d}" != "${last_dir}" ]; then
    mkdir -p "${staged}/${d}"
    last_dir="${d}"
  fi
  cp "${f}" "${staged}/${f}"
  copied=$((copied + 1))
done < "${list_file}"

# Zero files is not "clean", it is a broken run. Reaching here with an empty tree
# means the roots resolved to nothing tracked, and the scanner would happily
# report success over it.
[ "${copied}" -gt 0 ] \
  || die "no tracked files under the published roots — the scan would prove nothing"

[ -f "${staged}/.gitleaks.toml" ] \
  || die ".gitleaks.toml is missing from the staged tree, so the scan would run
      with the default ruleset and none of this repository's reviewed exemptions.
      It is listed in ALLOW_ROOT_FILES; restore it at the repository root."

# Three distinct meanings, and collapsing them is how a scanner gate fails open:
# 0 = clean, 1 = findings, anything else = the scan itself did not run.
set +e
( cd "${staged}" && gitleaks detect --no-git --source . --redact --no-banner )
scan_status=$?
set -e

if [ "${scan_status}" -gt 1 ]; then
  die "the secret scan itself failed (gitleaks exit ${scan_status}) — no conclusion can be drawn"
fi

if [ "${scan_status}" -ne 0 ]; then
  echo ""
  echo "FAIL  potential secrets in the published tree (scanner output above)."
  echo ""
  echo "      Every path listed is published verbatim to the public reference"
  echo "      implementation. Remove the material, or — if the finding describes"
  echo "      the SHAPE of a secret rather than one, rephrase the file so it does"
  echo "      not read as key material."
  echo ""
  echo "      An exemption in .gitleaks.toml is the last resort, not the first."
  echo "      Read that file's header before adding one: a content exemption must"
  echo "      be anchored to a single line, or it suppresses everything between"
  echo "      the two anchors of a multi-line match."
  echo ""
  echo "published-secrets guard: fix the findings above before merging."
  exit 1
fi

echo "PASS  no secrets in the published tree (${copied} files scanned)"
echo "published-secrets guard: all checks passed."
exit 0
