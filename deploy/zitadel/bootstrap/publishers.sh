#!/usr/bin/env sh
# The publishers' side in one command: makes sure the publisher instance
# exists (instance.sh), then provisions it (bootstrap.sh with the publishers
# role). The order matters. Right after the System API creates the instance,
# Zitadel answers its owner 403 for a few seconds, and instance.sh waits
# through that before bootstrap.sh's first check of the instance.
#
# Every stack runs the publishers' side through this script, so the steps and
# their order are written here only. Takes the inputs of both scripts (see
# their headers); ZITADEL_INSTANCE_ROLE is set here.
#
# Usage:
#   publishers.sh                  create or check the instance, then provision it
#   publishers.sh --emit-exports   print an earlier run's exports (bootstrap.sh
#                                  --emit-exports), touching neither Zitadel nor
#                                  any token: the instance step is skipped
#
# Exit codes: those of the step that stopped.
set -eu

here="$(dirname "$0")"
ZITADEL_INSTANCE_ROLE=publishers
export ZITADEL_INSTANCE_ROLE
if [ "${1:-}" != --emit-exports ]; then
    sh "$here/instance.sh"
fi
exec sh "$here/bootstrap.sh" "$@"
