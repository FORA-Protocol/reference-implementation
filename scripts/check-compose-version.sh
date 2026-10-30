#!/usr/bin/env bash
# Refuse to bring the e2e stack up with Docker tools too old for its health
# checks.
#
# Every health check in the stack sets start_interval: it runs every 2 seconds
# while its container starts and every minute once it is healthy. Docker Engine
# older than 25 (API 1.44) has no start interval, and Compose releases before
# 2.24 are reported to drop the setting without a word. Then every check runs at
# the one-minute interval from the start, each service the stack waits for takes
# up to a minute to be seen healthy, and a stack-up that takes about a minute
# takes many, with no error to say why. A newer Compose talking to an older
# Engine fails loudly instead; this check fails first, and names both tools.
#
# The check reads the versions rather than any stack behaviour, because the
# versions are what differ between machines: a laptop's Docker Desktop, a CI
# image's Compose plugin, a remote test box.
set -euo pipefail

NEED_API=1.44
NEED_COMPOSE=2.24.0

# at_least WANT HAVE: whether version HAVE is WANT or later.
at_least() {
    [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -n 1)" = "$1" ]
}

api="$(docker version --format '{{.Server.APIVersion}}' 2>/dev/null || true)"
compose="$(docker compose version --short 2>/dev/null || true)"
compose="${compose#v}"

problems=""
if [ -z "$api" ] || ! at_least "$NEED_API" "$api"; then
    problems="${problems}      Docker Engine API ${api:-unknown}; the stack needs ${NEED_API} or later (Engine 25+).\n"
fi
if [ -z "$compose" ] || ! at_least "$NEED_COMPOSE" "$compose"; then
    problems="${problems}      Docker Compose ${compose:-unknown}; the stack needs ${NEED_COMPOSE} or later.\n"
fi
[ -z "$problems" ] && exit 0

{
    echo "FAIL  the Docker tools cannot pace the e2e stack's health checks:"
    printf '%b' "$problems"
    cat <<'MSG'
      Without start_interval every check runs once a minute from the start, so
      the stack comes up minutes late with no error to say why.

      Upgrade Docker Engine and the Compose plugin, then re-run:

        Debian/Ubuntu  sudo apt install docker-ce docker-compose-plugin
        Arch/Manjaro   sudo pacman -S docker docker-compose
        Docker Desktop update it from its own settings

      Verify with `docker version` and `docker compose version` before re-running.
MSG
} >&2
exit 1
