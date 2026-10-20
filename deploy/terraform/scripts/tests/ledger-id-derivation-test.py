#!/usr/bin/env python3
"""The operator scripts must derive the SAME ledger ids the Exchange does.

fund-staging-agent.sh and agent-balances.sh each carry a hand-written copy of
the Exchange's id derivation, because they talk to TigerBeetle directly and
there is no RPC to ask the Exchange for an account id. Nothing compiles those
copies, and a drifted copy fails silently in the worst possible way: the script
funds or reads an account the Exchange never touches, reports success, and the
money is stranded somewhere nothing will ever debit.

So the vectors are pinned on both sides. These cases run the scripts' OWN
embedded programs — extracted from the shell source, not re-implemented here —
and compare them against the same expected ids that
src/exchange/internal/billing/tigerbeetle/ids_test.go pins for the Go
derivation. A change on either side that is not mirrored on the other turns
both test files red.

The ids are keyed by currency because a TigerBeetle id is unique per cluster and
not per ledger: the currency in the hashed input is the only thing keeping an
agent's EUR and USD accounts apart.
"""
import re
import subprocess
import sys
import unittest
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parents[1]

REF = "00000000-0000-4000-8000-000000000000"

# The same vectors ids_test.go pins, as the decimal integers the tigerbeetle
# repl takes (little-endian low 16 bytes of the sha256, read as a u128).
AGENT_EUR = 61740688024638721964878053374359432155
AGENT_USD = 24395055794383215160657249705534797496
LIQUIDITY_EUR = 329437178540122795680299142206267258359
LIQUIDITY_USD = 26904454403670481704385551981549440973

# The welcome-slot transfer ids welcome_test.go pins.
WELCOME_EUR = 106424205941705102778718495573608357111
WELCOME_USD = 24565785845815050169005649922871103987


def embedded_program(script: str, marker: str) -> str:
    """The python heredoc that follows marker in a shell script."""
    source = (SCRIPTS / script).read_text()
    if marker not in source:
        raise AssertionError(f"{script} no longer contains {marker!r}")
    return source.split(marker, 1)[1].split("\nPY\n", 1)[0]


def run_program(program: str, *args: str) -> dict[str, str]:
    """Run an extracted program and parse its KEY=value output."""
    result = subprocess.run([sys.executable, "-c", program, *args],
                            capture_output=True, text=True)
    if result.returncode != 0:
        raise AssertionError(f"the script's own program failed: {result.stderr}")
    return dict(line.split("=", 1) for line in result.stdout.strip().splitlines())


class FundStagingAgentDerivation(unittest.TestCase):
    """fund-staging-agent.sh derives the accounts and the transfer it posts."""

    def setUp(self) -> None:
        self.program = embedded_program(
            "fund-staging-agent.sh", "read -r -d '' FUND_IDS_PY <<'PY' || true\n")

    def ids(self, currency: str, label: str = "service-welcome") -> dict[str, str]:
        return run_program(self.program, REF, "100", label, currency)

    def test_agent_and_liquidity_accounts_match_the_exchange(self):
        for currency, agent, liquidity in (
            ("EUR", AGENT_EUR, LIQUIDITY_EUR),
            ("USD", AGENT_USD, LIQUIDITY_USD),
        ):
            with self.subTest(currency=currency):
                ids = self.ids(currency)
                self.assertEqual(int(ids["AGENT_ACC"]), agent)
                self.assertEqual(int(ids["LIQ_ACC"]), liquidity)

    def test_the_two_currencies_derive_different_accounts(self):
        eur, usd = self.ids("EUR"), self.ids("USD")
        self.assertNotEqual(eur["AGENT_ACC"], usd["AGENT_ACC"])
        self.assertNotEqual(eur["LIQ_ACC"], usd["LIQ_ACC"])

    def test_service_welcome_transfer_matches_the_register_grant(self):
        for currency, want in (("EUR", WELCOME_EUR), ("USD", WELCOME_USD)):
            with self.subTest(currency=currency):
                self.assertEqual(int(self.ids(currency)["TRANSFER"]), want)

    def test_other_labels_stay_amount_bearing_and_per_currency(self):
        # A plain top-up label must NOT land in the reserved welcome slot, and
        # the same label in two currencies must be two transfers — otherwise one
        # top-up would block the other as a duplicate id.
        eur = self.ids("EUR", label="initial")
        usd = self.ids("USD", label="initial")
        self.assertNotEqual(int(eur["TRANSFER"]), WELCOME_EUR)
        self.assertNotEqual(eur["TRANSFER"], usd["TRANSFER"])

    def test_the_amount_is_converted_at_asset_scale_8(self):
        self.assertEqual(int(self.ids("EUR")["MINOR"]), 100 * 10**8)


class AgentBalancesDerivation(unittest.TestCase):
    """agent-balances.sh derives the agent accounts it reads."""

    def setUp(self) -> None:
        source = (SCRIPTS / "agent-balances.sh").read_text()
        # The derivation is a one-liner passed to `python3 -c`, spread over two
        # shell-continued lines.
        match = re.search(r'"(import hashlib,sys; print\(.*?\))"', source, re.S)
        if match is None:
            raise AssertionError("agent-balances.sh no longer derives account ids inline")
        self.program = match.group(1)

    def account(self, currency: str) -> int:
        result = subprocess.run([sys.executable, "-c", self.program, REF, currency],
                                capture_output=True, text=True)
        if result.returncode != 0:
            raise AssertionError(f"the script's own program failed: {result.stderr}")
        return int(result.stdout.strip())

    def test_agent_accounts_match_the_exchange(self):
        self.assertEqual(self.account("EUR"), AGENT_EUR)
        self.assertEqual(self.account("USD"), AGENT_USD)

    def test_it_agrees_with_the_funding_script(self):
        # The reader and the funder must land on one account per currency, or
        # the report would show "no account" for money that is really there.
        funder = embedded_program(
            "fund-staging-agent.sh", "read -r -d '' FUND_IDS_PY <<'PY' || true\n")
        for currency in ("EUR", "USD"):
            with self.subTest(currency=currency):
                funded = run_program(funder, REF, "100", "initial", currency)
                self.assertEqual(self.account(currency), int(funded["AGENT_ACC"]))


if __name__ == "__main__":
    unittest.main()
