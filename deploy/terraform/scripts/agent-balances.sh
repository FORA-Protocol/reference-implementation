#!/usr/bin/env bash
# Reports every registered agent's prepaid balance. READ-ONLY: it lists agents
# and looks their ledger accounts up, and posts no transfer.
#
# WHY A SCRIPT AND NOT A REQUEST. An agent can read its own balance: the
# Exchange's GetAccountStatus and the MCP fora_status tool report the AVAILABLE
# balance per currency, with holds for pending transactions subtracted. That
# answers one agent, about itself. This script is the operator view: the POSTED
# balance (credits_posted - debits_posted, holds not subtracted) of every
# registered agent at once. No RPC lists every agent's balance, so it reads
# TigerBeetle directly, the same way fund-staging-agent.sh verifies its own work.
# The two figures differ while a hold is live.
#
# Money still arrives the operator-mediated way (per ADR-009 D2 and its
# 2026-08-13 amendment): an operator adds money out of band, and the only
# service-side credit is the tenant-configured default the Register flow grants.
#
# How it works: reads every non-null billing_ref from the Exchange's fora.agents
# table over SSH — the same read-only operator-SQL path the funding sweep uses —
# then derives each ledger account id and asks TigerBeetle for it. The derivation
# matches the Exchange adapter and the funding script exactly: the little-endian
# low 16 bytes of sha256("agent:" || currency || ":" || billing_ref). Amounts are
# stored at asset scale 8, so 10^8 minor units make one whole currency unit.
#
# ONE ROW PER (AGENT, CURRENCY), NEVER A TOTAL. The Exchange keeps one ledger per
# currency and converts nothing between them, so an agent holding 10 EUR and
# 10 USD holds two separate balances and "20" would be a number that means
# nothing. The currencies come from the RUNNING Exchange's own
# EXCHANGE_BILLING_LEDGER, so this report follows the deployment instead of
# assuming a currency.
#
# Env (all optional):
#   STACK_DIR    default deploy/terraform/stacks/staging-aws
#   CURRENCIES   comma-separated ISO 4217 alpha codes to report, overriding what
#                the running Exchange is configured with. For reading balances
#                on a ledger the Exchange no longer bills in.
#
# Usage:
#   deploy/terraform/scripts/agent-balances.sh

set -euo pipefail

. "$(dirname "${BASH_SOURCE[0]}")/lib/staging-env.sh"

command -v terraform >/dev/null 2>&1 || { echo "missing: terraform" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "missing: python3" >&2; exit 2; }

load_ssh_cmd

# The currencies to report. Read from the running Exchange so the report cannot
# drift from the deployment; the numeric-to-alpha table is the same two entries
# the Exchange supports (src/exchange/internal/billing/tigerbeetle/currency.go).
if [ -z "${CURRENCIES:-}" ]; then
    echo "== read the Exchange's configured ledgers =="
    LEDGERS="$("${SSH_CMD[@]}" "sudo docker inspect fora-exchange-1 --format '{{json .Config.Env}}' | jq -er '.[] | select(startswith(\"EXCHANGE_BILLING_LEDGER=\")) | ltrimstr(\"EXCHANGE_BILLING_LEDGER=\")'")"
    CURRENCIES=""
    for code in ${LEDGERS//,/ }; do
        case "${code}" in
            978) alpha=EUR ;;
            840) alpha=USD ;;
            *)   echo "the Exchange reports an unsupported ledger '${code}'" >&2; exit 2 ;;
        esac
        CURRENCIES="${CURRENCIES:+${CURRENCIES},}${alpha}"
    done
fi
if [ -z "${CURRENCIES}" ]; then
    echo "could not determine which currencies to report — set CURRENCIES" >&2
    exit 2
fi
echo "== reporting balances in: ${CURRENCIES} =="

LIST_SQL="SELECT DISTINCT billing_ref FROM fora.agents WHERE billing_ref IS NOT NULL ORDER BY billing_ref;"
echo "== list registered agents (read-only, via ${SSH_CMD[*]}) =="
echo "${LIST_SQL}"
REFS="$(remote_psql fora -tA -v ON_ERROR_STOP=1 -c "${LIST_SQL}")"

if [ -z "${REFS}" ]; then
    echo "no agent has a billing_ref yet — agents get one at Register"
    exit 0
fi

# Into arrays FIRST, then loop over them: the loop below runs ssh, and ssh
# forwards its stdin to the remote command, so a loop feeding refs through its
# own stdin would have the remaining refs eaten by the first child. This
# read-loop is safe because it runs no child that reads stdin. (Built without
# mapfile on purpose — macOS ships bash 3.2.)
#
# One entry per (agent, currency): each pair is a distinct ledger account, so
# each one is looked up and reported on its own.
REFS_LIST=()
CURS_LIST=()
ACCS=()
while IFS= read -r ref; do
    [ -n "${ref}" ] || continue
    for cur in ${CURRENCIES//,/ }; do
        REFS_LIST+=("${ref}")
        CURS_LIST+=("${cur}")
        ACCS+=("$(python3 -c \
            "import hashlib,sys; print(int.from_bytes(hashlib.sha256(('agent:'+sys.argv[2]+':'+sys.argv[1]).encode()).digest()[:16],'little'))" \
            "${ref}" "${cur}")")
    done
done <<< "${REFS}"

# One repl invocation per account: the repl is interactive-only in this
# TigerBeetle version — it refuses a piped stdin with "ANSI escape sequences not
# supported" — so each statement goes through its non-interactive --command flag,
# which runs exactly one statement per call. All of them share one SSH session.
REPL="sudo docker compose -f ${VM_COMPOSE_FILE} exec -T tigerbeetle /tigerbeetle repl --cluster=0 --addresses=3000"
REMOTE=""
for acc in "${ACCS[@]}"; do
    REMOTE+="${REPL} --command=\"lookup_accounts id=${acc};\"; "
done
echo "== read ${#ACCS[@]} ledger account(s) =="
REPL_OUT="$("${SSH_CMD[@]}" "${REMOTE}" 2>&1)"

# Pairs, so the parser matches each answer to its (agent, currency) by ACCOUNT ID
# rather than by position. The repl prints a connection log line before every
# answer, and an account that does not exist produces no object at all — so
# counting answers against the input order would silently shift each later row
# onto the wrong agent or the wrong currency.
PAIRS=""
for i in "${!REFS_LIST[@]}"; do
    PAIRS+="${REFS_LIST[$i]}:${CURS_LIST[$i]}=${ACCS[$i]} "
done

# The repl output reaches python through the ENVIRONMENT, not a pipe: the heredoc
# is itself a stdin redirection and would override any pipe on this command,
# leaving the program to read its own source as input. PAIRS is unquoted on
# purpose: it must word-split into one argument per (agent, currency).
REPL_OUT="${REPL_OUT}" python3 - ${PAIRS} <<'PY'
import json
import os
import re
import sys
from decimal import Decimal

# Each argument is "<billing_ref>:<currency>=<account_id>". The billing_ref is a
# UUID and the currency an alpha code, so neither contains ':' or '=' and the
# split is unambiguous.
pairs = []
for arg in sys.argv[1:]:
    handle, account_id = arg.split("=", 1)
    ref, currency = handle.rsplit(":", 1)
    pairs.append((ref, currency, account_id))
text = os.environ["REPL_OUT"]

# The repl answers with one JSON object per account, every number quoted as a
# string, preceded by a connection log line. Pull the objects out and key them by
# the id they report rather than by the order they arrive in.
found = {}
for match in re.finditer(r'\{[^{}]*"credits_posted"[^{}]*\}', text, re.S):
    try:
        account = json.loads(match.group(0))
    except json.JSONDecodeError:
        continue
    # Asset scale 8: 10^8 minor units per whole currency unit.
    found[account["id"]] = (
        Decimal(account["credits_posted"]) - Decimal(account["debits_posted"])
    ).scaleb(-8)

print()
print("%-40s %-9s %16s" % ("BILLING_REF", "CURRENCY", "BALANCE"))
missing = 0
for ref, currency, account_id in pairs:
    balance = found.get(account_id)
    if balance is None:
        # "no account" rather than 0.00 on purpose. An account that was never
        # created and one holding nothing are different facts, and only the first
        # means the agent has never been funded in this currency.
        missing += 1
        print("%-40s %-9s %16s" % (ref, currency, "no account"))
    else:
        print("%-40s %-9s %16s" % (ref, currency, f"{balance:.2f}"))

# Deliberately no total: the balances are in different currencies, so summing
# them would produce a number that means nothing.
print()
print("%d (agent, currency) account(s); %d not created" % (len(pairs), missing))
PY
