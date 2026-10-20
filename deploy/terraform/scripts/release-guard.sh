#!/usr/bin/env bash
# Completed-release guard: refuse to publish a version that is already
# published. A version is published once its release manifest exists in the
# project's generic package registry, so this script asks the registry for
# that one file and exits 0 only when the answer is an explicit 404. Every
# job that writes a release artifact runs it first; the publication lock in
# the pipeline keeps two publications from interleaving, and this guard is
# what makes a single job re-run after the lock was released (a retry of a
# finished pipeline, a fresh pipeline for the same tag) fail before it writes
# anything.
#
# It fails closed. Only a 404 means "unpublished". A 200 means the version is
# published and must not be rewritten; so does a 302, which is how GitLab
# answers for a file it serves from object storage (the redirect is to the
# file, so the file exists; nothing here follows it). Anything else — 401,
# 403, a 5xx, a timeout (curl prints 000), an empty or non-numeric status —
# means the answer is unknown, and an unknown answer is a refusal: treating
# an error as "absent" is exactly how a released version would get
# overwritten during an outage.
#
# The credential is the job's own token, read from CI_JOB_TOKEN: a token on
# the command line is visible to every process on the runner. Note that GitLab
# answers 404 for a project the token cannot see at all, so a wrong project id
# in the URL reads as "unpublished"; the upload that follows then fails with
# the same 404, and nothing is written.
#
# Usage:
#   CI_JOB_TOKEN=... deploy/terraform/scripts/release-guard.sh --url <manifest download url>
#
# Exit status: 0 unpublished (404); 1 published (200, 302), or the answer is unknown;
# 2 bad usage or no token.

set -euo pipefail

usage() { echo "usage: $0 --url <release manifest download url>" >&2; exit 2; }

url=""
while [ $# -gt 0 ]; do
    case "$1" in
        --url) url="${2:-}"; shift ;;
        *) usage ;;
    esac
    shift || usage
done
[ -n "${url}" ] || usage
[ -n "${CI_JOB_TOKEN:-}" ] || { echo "CI_JOB_TOKEN is not set — the guard has no credential to ask the registry with" >&2; exit 2; }
command -v curl >/dev/null 2>&1 || { echo "missing: curl" >&2; exit 2; }

# The status code only: the body is the manifest when it exists, and its
# content is not what decides here. --max-time bounds a hung registry; the
# status curl prints on a transport failure is 000, which the case below
# treats like every other non-answer.
rc=0
code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 30 -H "JOB-TOKEN: ${CI_JOB_TOKEN}" "${url}" 2>&1)" || rc=$?
case "${code}" in
    404)
        echo "no release manifest at ${url}: this version is unpublished, publication may proceed"
        exit 0 ;;
    200|302)
        echo "release manifest already exists at ${url}: this version is published and is never rewritten — cut the next version instead" >&2
        exit 1 ;;
    *)
        echo "cannot tell whether the release manifest exists (HTTP status '${code}', curl exit ${rc}): refusing to publish" >&2
        exit 1 ;;
esac
