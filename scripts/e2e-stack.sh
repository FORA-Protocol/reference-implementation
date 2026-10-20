#!/usr/bin/env bash
# e2e-stack.sh — the network and the compose invocation of one e2e stack.
#
#   scripts/e2e-stack.sh network-up   <project>   create the stack's network (idempotent)
#   scripts/e2e-stack.sh network-down <project>   remove it (no-op when absent)
#   scripts/e2e-stack.sh compose      <project> [compose args...]
#   scripts/e2e-stack.sh prune        <workdir>   remove this checkout's networks no container uses
#
# Every e2e stack runs on a network of its own, named after its compose project,
# and publishes no host port: the test runner is a container on that network and
# reaches every service by name. Two stacks on one Docker daemon therefore share
# nothing, provided their networks do not claim the same addresses.
#
# They would, if the subnet were written into docker-compose.e2e.yml. A few
# services must hold a static address, because what points at them takes an IP
# and not a name: the services' `dns:` setting (the wildcard-DNS sidecar), the
# sidecar's own answers for the wildcard zones and the hosts-file entries that
# send calls through the capture proxy (both point at the capture proxy), the
# TigerBeetle client (it does not resolve hostnames), and each Exchange's admin
# allowlist, which admits the runner by source address. Docker refuses a second
# network whose subnet overlaps a first, so a fixed subnet allows one stack per
# daemon.
#
# So the subnet is chosen per stack, and the daemon chooses it. network-up asks
# Docker for a network with no subnet given, which Docker allocates from its own
# address pools (whatever the box's daemon.json configures) and never overlapping
# a network that exists. It reads that allocation, removes the probe, and creates
# the stack's network on the first /24 of it, with Docker's own automatic
# assignment confined to the lower half (--ip-range .0/25). The static addresses
# docker-compose.e2e.yml gives out all sit in the upper half, so no container
# started without one can be handed one of them, the runner's included. Another
# allocation can take the subnet between the probe's removal and the create; the
# create then fails on the overlap, and network-up tries again.
#
# compose exports the two values docker-compose.e2e.yml interpolates:
#   FORA_E2E_NETWORK  the network's name (the project name);
#   FORA_E2E_NET      the first three octets of its /24, e.g. 172.18.0.
# When the network does not exist (a `down` after a failed `up`, say) FORA_E2E_NET
# is empty: compose only reads the addresses when it attaches a container, and an
# empty prefix makes that attach fail rather than land somewhere else.
#
# E2E_COMPOSE_EXTRA is not read here: extra -f files are passed as compose args,
# before the subcommand, the way the Makefile passes E2E_COMPOSE_EXTRA.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$ROOT/docker-compose.e2e.yml"
# An optional service's overlay, added to every compose call when the tree
# carries it: the public reference tree does not, and its stack starts without
# the service. It goes right after the base file and before the caller's -f
# files, so a build-only overlay such as the CI cache merges onto its full
# definition.
# published-ref-allow: the stack adds the onboarding overlay when the tree carries it
OPTIONAL_OVERLAY="$ROOT/docker-compose.e2e.onboarding.yml"

# Marks every network this script creates, so prune touches nothing else.
STACK_LABEL="fora.e2e.stack"
WORKDIR_LABEL="fora.e2e.workdir"

die() {
    echo "e2e-stack: $*" >&2
    exit 1
}

usage() {
    sed -n '2,7p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
}

network_exists() {
    docker network inspect "$1" >/dev/null 2>&1
}

# The first IPv4 subnet of network $1.
ipv4_subnet() {
    docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}' "$1" \
        | tr ' ' '\n' | grep -m1 -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$' || true
}

# The /24 prefix (three octets) of network $1, or nothing when it does not exist.
net_prefix() {
    local subnet
    network_exists "$1" || return 0
    subnet="$(ipv4_subnet "$1")"
    [ -n "$subnet" ] || die "network $1 has no IPv4 subnet"
    printf '%s\n' "${subnet%.*/*}"
}

network_up() {
    local name="$1" probe="$1-probe" subnet bits base attempt out
    if network_exists "$name"; then
        net_prefix "$name"
        return 0
    fi
    for attempt in $(seq 1 20); do
        docker network rm "$probe" >/dev/null 2>&1 || true
        docker network create --label "$STACK_LABEL=probe" "$probe" >/dev/null
        subnet="$(ipv4_subnet "$probe")"
        docker network rm "$probe" >/dev/null
        [ -n "$subnet" ] || die "Docker allocated $probe no IPv4 subnet"
        bits="${subnet#*/}"
        [ "$bits" -le 24 ] || die "Docker allocates /$bits networks here; the stack needs a /24 (see the address plan in docker-compose.e2e.yml)"
        base="${subnet%.*/*}"
        if out="$(docker network create \
            --subnet "$base.0/24" --ip-range "$base.0/25" \
            --label "$STACK_LABEL=$name" \
            --label "$WORKDIR_LABEL=$ROOT" \
            --label "com.docker.compose.project=$name" \
            "$name" 2>&1)"; then
            printf '%s\n' "$base"
            return 0
        fi
        # Lost the subnet to a concurrent allocation, or the name to a concurrent
        # network-up of the same project: the latter is success.
        if network_exists "$name"; then
            net_prefix "$name"
            return 0
        fi
        echo "e2e-stack: attempt $attempt: $out" >&2
        sleep $((RANDOM % 3 + 1))
    done
    die "could not create network $name after 20 attempts"
}

network_down() {
    if network_exists "$1"; then
        docker network rm "$1" >/dev/null
    fi
}

# Networks this checkout created that no container is attached to: what a killed
# run leaves behind. Another checkout's networks, and any network in use, stay.
prune() {
    local workdir="$1" net
    for net in $(docker network ls -q --filter "label=$WORKDIR_LABEL=$workdir"); do
        if [ -z "$(docker network inspect -f '{{range .Containers}}x{{end}}' "$net")" ]; then
            docker network rm "$net" >/dev/null 2>&1 || true
        fi
    done
}

[ "$#" -ge 2 ] || usage
cmd="$1" target="$2"
shift 2
case "$target" in
    "" | -*) die "a project name (or, for prune, a directory) is required" ;;
esac

case "$cmd" in
    network-up) network_up "$target" ;;
    network-down) network_down "$target" ;;
    prune) prune "$target" ;;
    compose)
        FORA_E2E_NETWORK="$target"
        FORA_E2E_NET="$(net_prefix "$target")"
        export FORA_E2E_NETWORK FORA_E2E_NET
        overlay=()
        if [ -f "$OPTIONAL_OVERLAY" ]; then
            overlay=(-f "$OPTIONAL_OVERLAY")
        fi
        exec docker compose -p "$target" -f "$COMPOSE_FILE" ${overlay[@]+"${overlay[@]}"} "$@"
        ;;
    *) usage ;;
esac
