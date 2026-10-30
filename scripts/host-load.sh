#!/usr/bin/env bash
# Record the host's load during an e2e run, and summarise the record.
#
#   scripts/host-load.sh record FILE [--interval SECONDS] [--count SAMPLES]
#   scripts/host-load.sh summary FILE
#
# The CI job that runs the e2e suite starts `record` in the background before
# anything else, prints `summary` at the end and keeps FILE as an artifact, so
# every run shows what it cost the machine it ran on. Inside a container /proc
# describes the host, so the record is the host's, not the job container's. It
# works the same way on a laptop, next to a local `make test-e2e`.
#
# record writes one line every INTERVAL seconds (default 5) and stops by itself
# after COUNT lines (default 720: an hour at the default interval). Each line is
# vmstat's line for that interval -- tasks running or waiting for a CPU (r),
# tasks blocked on I/O (b), memory and swap in MiB, CPU percentages -- with the
# UTC time in front and five columns after it: the three load averages, the
# memory the kernel counts as available to new work (MemAvailable, in MiB), and
# the number of running containers that belong to a compose project. A BuildKit
# builder belongs to none, so the first non-zero count is when the stack started.
#
# Every five seconds is cheap: vmstat reads kernel counters and the container
# count is one `docker ps` call, about 20 ms of CPU. It is also often enough: a
# stack start that takes 20 seconds still spans four samples. The load average is
# a one-minute average already; the other columns are per interval.
#
# A value record cannot read is written as "-". When vmstat itself is missing or
# fails, the reason goes into FILE as a "# error:" line, which summary prints.
#
# summary prints the peaks: load average, tasks waiting for a CPU, CPU busy time,
# the lowest available memory, swapping, and when the stack's containers first
# ran. It finds the columns by their names, so the column sets of different
# procps versions both work, and it skips a line cut short by a kill.
set -euo pipefail
export LC_ALL=C TZ=UTC

usage() {
    cat >&2 <<'MSG'
usage: scripts/host-load.sh record FILE [--interval SECONDS] [--count SAMPLES]
       scripts/host-load.sh summary FILE
MSG
    exit 2
}

# positive_int VALUE: whether VALUE is a whole number of at least 1.
positive_int() {
    case "$1" in
        '' | *[!0-9]* | 0*) return 1 ;;
    esac
}

# mem_kib KEY: the value of KEY in /proc/meminfo, in KiB; nothing when absent.
mem_kib() {
    local key value rest
    while read -r key value rest; do
        if [ "$key" = "$1:" ]; then
            printf '%s' "$value"
            return 0
        fi
    done </proc/meminfo
}

# containers: how many running containers belong to a compose project, or "-"
# when the Docker daemon cannot be asked.
containers() {
    local ids id n=0
    if ! ids="$(timeout 3 docker ps -q --filter label=com.docker.compose.project 2>/dev/null)"; then
        printf '%s' -
        return 0
    fi
    while read -r id; do
        if [ -n "$id" ]; then
            n=$((n + 1))
        fi
    done <<<"$ids"
    printf '%s' "$n"
}

# sample LINE: one record line, vmstat's LINE with the time in front and the
# load, memory and container columns after it.
sample() {
    local now l1=- l5=- l15=- rest kib avail=-
    printf -v now '%(%Y-%m-%dT%H:%M:%SZ)T' -1
    read -r l1 l5 l15 rest 2>/dev/null </proc/loadavg || true
    kib="$(mem_kib MemAvailable 2>/dev/null || true)"
    if positive_int "$kib"; then
        avail=$((kib / 1024))
    fi
    printf '%-20s %s %6s %6s %6s %9s %10s\n' "$now" "$1" "$l1" "$l5" "$l15" "$avail" "$(containers)"
}

# write_lines: vmstat's output on stdin, as record lines on stdout. vmstat's
# first line names the column groups and stays as a comment above the columns;
# its second line names the columns.
write_lines() {
    local line n=0
    while IFS= read -r line; do
        n=$((n + 1))
        case "$n" in
            1) printf '%-20s %s\n' '#' "$line" ;;
            2) printf '%-20s %s %6s %6s %6s %9s %10s\n' time "$line" load1 load5 load15 avail_mib containers ;;
            *) sample "$line" ;;
        esac
    done
}

record() {
    local file interval=5 count=720 total swap
    [ $# -ge 1 ] || usage
    file=$1
    shift
    while [ $# -gt 0 ]; do
        case "$1" in
            --interval) [ $# -ge 2 ] || usage; interval=$2; shift 2 ;;
            --count) [ $# -ge 2 ] || usage; count=$2; shift 2 ;;
            *) usage ;;
        esac
    done
    positive_int "$interval" || usage
    positive_int "$count" || usage

    mkdir -p "$(dirname "$file")"
    exec 3>>"$file"
    if ! command -v vmstat >/dev/null 2>&1; then
        echo "# error: vmstat (procps) is not installed, so nothing was recorded" >&3
        return 1
    fi
    total="$(mem_kib MemTotal || true)"
    swap="$(mem_kib SwapTotal || true)"
    printf '# host-load record: interval=%s cpus=%s mem_mib=%s swap_mib=%s source=%s\n' \
        "$interval" "$(grep -c '^cpu[0-9]' /proc/stat || true)" \
        "$((${total:-0} / 1024))" "$((${swap:-0} / 1024))" \
        "$(vmstat -V 2>&1 | head -n 1 | tr ' ' '_')" >&3
    if ! vmstat -n -w -S M "$interval" "$count" | write_lines >&3; then
        echo "# error: vmstat stopped early, so the record ends here" >&3
        return 1
    fi
}

summary() {
    local file=$1
    if [ ! -f "$file" ]; then
        echo "host load: no record at $file"
        return 0
    fi
    awk '
        function newer(v, best) { return best == "" || v + 0 > best + 0 }
        function dur(s) { return sprintf("%dm%02ds", int(s / 60), s % 60) }
        /^#/ {
            if ($0 ~ /^# error:/) err[++errors] = $0
            else if ($0 ~ /^# host-load record:/)
                for (i = 4; i <= NF; i++) { split($i, kv, "="); host[kv[1]] = kv[2] }
            next
        }
        !width {
            if ($1 != "time") next
            for (i = 1; i <= NF; i++) col[$i] = i
            split("time r b si so id wa load1 avail_mib containers", need, " ")
            for (i = 1; i <= 10; i++) if (!(need[i] in col)) missing = missing " " need[i]
            width = missing == "" ? NF : -1
            next
        }
        NF != width { next }
        {
            rows++
            t = substr($col["time"], 12, 8)
            if (rows == 1) first = t
            last = t
            if ($col["load1"] != "-" && newer($col["load1"], peak_load)) { peak_load = $col["load1"]; peak_load_at = t }
            if (newer($col["r"], peak_r)) { peak_r = $col["r"]; peak_r_at = t }
            if (newer($col["b"], peak_b)) peak_b = $col["b"]
            a = $col["avail_mib"]
            if (a != "-" && (low_avail == "" || a + 0 < low_avail + 0)) { low_avail = a; low_avail_at = t }
            c = $col["containers"]
            if (c ~ /^[0-9]+$/) { counted = 1; if (c + 0 > 0 && stack_at == "") stack_at = t }
            # vmstat averages its first line over everything since boot.
            if (rows == 1) next
            cpu_rows++
            busy = 100 - $col["id"] - $col["wa"]
            if (newer(busy, peak_busy)) { peak_busy = busy; peak_busy_at = t }
            if (busy >= 90) hot++
            if (newer($col["wa"], peak_wa)) peak_wa = $col["wa"]
            if (newer($col["si"], peak_si)) peak_si = $col["si"]
            if (newer($col["so"], peak_so)) peak_so = $col["so"]
        }
        END {
            for (i = 1; i <= errors; i++) print err[i]
            if (missing != "") { print "host load: the record lacks the column(s)" missing; exit }
            if (!rows) { print "host load: no samples recorded"; exit }
            printf "host load: %d samples every %ss from %s to %s UTC; host has %s CPUs, %s MiB memory, %s MiB swap\n", rows, host["interval"], first, last, host["cpus"], host["mem_mib"], host["swap_mib"]
            if (peak_load == "") print "  1-minute load average: unknown"
            else printf "  1-minute load average: peak %s at %s\n", peak_load, peak_load_at
            printf "  tasks running or waiting for a CPU: peak %d at %s; tasks blocked on I/O: peak %d\n", peak_r, peak_r_at, peak_b
            if (!cpu_rows) print "  CPU and swapping: one sample only, which covers the time since boot"
            else {
                printf "  CPU busy (not idle, not waiting for I/O): peak %d%% at %s; 90%% or more for %s; I/O wait peak %d%%\n", peak_busy, peak_busy_at, dur(hot * host["interval"]), peak_wa
                printf "  swapping: in peak %d MiB/s, out peak %d MiB/s\n", peak_si, peak_so
            }
            if (low_avail == "") print "  memory available: unknown"
            else printf "  memory available: lowest %s MiB at %s\n", low_avail, low_avail_at
            if (stack_at != "") printf "  stack containers first running at %s\n", stack_at
            else if (counted) print "  stack containers: none ran"
            else print "  stack containers: unknown (the Docker daemon could not be asked)"
        }
    ' "$file"
}

case "${1:-}" in
    record) shift; record "$@" ;;
    summary) shift; [ $# -eq 1 ] || usage; summary "$1" ;;
    *) usage ;;
esac
