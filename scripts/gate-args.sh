#!/usr/bin/env bash
# gate-args.sh — the argument seam the structural gates under scripts/ share.
#
# Four gates take the same single argument and parse it identically. This file is
# that parsing, written once. It is sourced, never executed.
#
# `--root <dir>` points a gate at a tree other than this checkout; each gate's own
# test suite uses it to drive synthetic trees, and the publish tool uses it to
# check the tree it is about to push.
#
# Two properties of this seam are load-bearing, and both are the reason it is one
# file rather than four copies — a later hardening has one place to land.
#
# It is an ARGUMENT and never an environment variable. These gates decide whether
# a tree is fit to publish or to build from, so an ambient variable that selected
# the tree could redirect a `make quality` run at a different one while it reported
# PASS. Nothing in this file reads the environment.
#
# An unknown argument is refused rather than ignored. A gate invoked with a
# misspelled flag must not run as though no flag had been given, which is the
# shape that makes a redirected check look like a clean one.
#
# `die` lives here too, because every caller defined the same one and the failure
# text a gate prints when its own invocation is wrong should not vary by gate.

die() { printf 'FAIL  %s\n' "$*" >&2; exit 1; }

# Reads the caller's arguments and sets the global repo_root.
#
# The default is derived from THIS file's location, not the caller's. Every gate
# that sources this sits beside it, so the two resolve to the same directory, and
# taking it from here is one fewer thing a caller can get wrong.
parse_gate_args() {
    repo_root=""
    while [ $# -gt 0 ]; do
        case "$1" in
            --root)
                [ $# -ge 2 ] || die "--root requires a directory"
                repo_root="$(cd "$2" && pwd)" || die "--root: no such directory: $2"
                shift 2
                ;;
            *) die "unknown argument: $1" ;;
        esac
    done
    [ -n "${repo_root}" ] || repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
}
