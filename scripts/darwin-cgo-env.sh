#!/usr/bin/env bash
# darwin-cgo-env.sh — macOS (Darwin) linker workaround for TigerBeetle.
#
# TigerBeetle client static archive (libtb_client_x86_64-macos.a) on macOS has alignment
# issues with newer Xcode ld_prime ("64-bit mach-o member 'compiler_rt.o' not 8-byte aligned").
# The classic linker flag (-Wl,-ld_classic) is needed on Darwin to link CGO binaries cleanly.
# Apple deprecated -ld_classic in Xcode 15+, but ld_prime fails with unaligned segment errors.
#
# Two invocation modes:
#   --print:  Output only the -Wl,-ld_classic flag for Makefile consumption.
#   (no args): Source mode — append the flag to caller's CGO_LDFLAGS.
#
# IMPORTANT: Both modes APPEND the flag; they never overwrite ambient CGO_LDFLAGS.

if [[ "$(uname -s)" == "Darwin" ]]; then
  DARWIN_CGO_FLAG="-Wl,-ld_classic"
  case "${1:-}" in
    --print)
      printf "%s" "${DARWIN_CGO_FLAG}"
      ;;
    *)
      if [[ -z "${CGO_LDFLAGS:-}" ]]; then
        CGO_LDFLAGS="${DARWIN_CGO_FLAG}"
      else
        CGO_LDFLAGS="${CGO_LDFLAGS} ${DARWIN_CGO_FLAG}"
      fi
      export CGO_LDFLAGS
      ;;
  esac
fi
