#!/usr/bin/env python3
"""The four copies of the supported-currency table must agree.

The Exchange bills on one TigerBeetle ledger per currency, and which currencies
exist is decided by a table in Go. Three other places enumerate the same set and
cannot read that table:

  * the Terraform validation on the billing_ledger variable, which rejects an
    unlisted code at plan time;
  * the currency-to-ledger table in the funding script, which is shell;
  * the ledger-to-currency table in the balance script, same.

None of that duplication is removable — Terraform and shell cannot import Go —
so it is guarded instead. Adding a currency to Go alone leaves a deployment that
refuses the code at `terraform plan`, or operator scripts that cannot fund or
report it, and each of those failures surfaces far from its cause.

This is also the check that keeps the add-a-currency procedure honest: that
procedure lists exactly these edits, and it was wrong once, claiming the Go
entry was sufficient.
"""
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]

GO_TABLE = ROOT / "src/exchange/internal/billing/tigerbeetle/currency.go"
TERRAFORM = ROOT / "deploy/terraform/modules/compose-stack/variables.tf"
FUND_SCRIPT = ROOT / "deploy/terraform/scripts/fund-staging-agent.sh"
BALANCE_SCRIPT = ROOT / "deploy/terraform/scripts/agent-balances.sh"


def go_table() -> dict[str, str]:
    """{numeric: alpha} from the Go map literal, the authoritative table."""
    body = re.search(r"ledgerCurrencies\s*=\s*map\[uint32\]string\{(.*?)\n\}",
                     GO_TABLE.read_text(), re.S)
    if body is None:
        raise AssertionError(f"{GO_TABLE.name} no longer declares ledgerCurrencies")
    table = dict(re.findall(r'(\d+):\s*"([A-Z]{3})"', body.group(1)))
    if not table:
        raise AssertionError(f"{GO_TABLE.name}: no currency entries found")
    return table


def terraform_numerics() -> set[str]:
    """The numeric codes the billing_ledger validation accepts."""
    block = re.search(r'variable\s+"billing_ledger"\s*\{(.*?)\n\}\n',
                      TERRAFORM.read_text(), re.S)
    if block is None:
        raise AssertionError(f"{TERRAFORM.name} no longer declares billing_ledger")
    allowed = re.search(r"contains\(\[([^\]]*)\]", block.group(1))
    if allowed is None:
        raise AssertionError(
            f"{TERRAFORM.name}: billing_ledger no longer validates against a code list")
    codes = set(re.findall(r'"(\d+)"', allowed.group(1)))
    if not codes:
        raise AssertionError(f"{TERRAFORM.name}: the accepted code list is empty")
    return codes


def fund_script_table() -> dict[str, str]:
    """{numeric: alpha} from the funding script's `EUR) DEFAULT_LEDGER=978 ;;`."""
    pairs = re.findall(r"([A-Z]{3})\)\s*DEFAULT_LEDGER=(\d+)", FUND_SCRIPT.read_text())
    if not pairs:
        raise AssertionError(f"{FUND_SCRIPT.name}: no currency-to-ledger table found")
    return {numeric: alpha for alpha, numeric in pairs}


def balance_script_table() -> dict[str, str]:
    """{numeric: alpha} from the balance script's `978) alpha=EUR ;;`."""
    pairs = re.findall(r"(\d+)\)\s*alpha=([A-Z]{3})", BALANCE_SCRIPT.read_text())
    if not pairs:
        raise AssertionError(f"{BALANCE_SCRIPT.name}: no ledger-to-currency table found")
    return dict(pairs)


class CurrencyTablesAgree(unittest.TestCase):
    def setUp(self) -> None:
        self.go = go_table()

    def test_the_funding_script_matches_go(self):
        self.assertEqual(
            fund_script_table(), self.go,
            "fund-staging-agent.sh's currency table disagrees with the Go table: "
            "funding would derive the wrong ledger, or refuse a currency the "
            "Exchange bills in")

    def test_the_balance_script_matches_go(self):
        self.assertEqual(
            balance_script_table(), self.go,
            "agent-balances.sh's currency table disagrees with the Go table: the "
            "report would skip a currency the Exchange bills in, or fail on a "
            "ledger it reports")

    def test_the_terraform_validation_matches_go(self):
        self.assertEqual(
            terraform_numerics(), set(self.go),
            "the billing_ledger validation disagrees with the Go table: a "
            "currency the Exchange supports would be refused at `terraform "
            "plan`, or one it does not support would be accepted and then fail "
            "the Exchange's boot")

    def test_every_alpha_code_is_distinct(self):
        # Two numerics mapping to one alpha code would make the account-id
        # derivation ambiguous: the id carries the alpha code, not the numeric.
        alphas = list(self.go.values())
        self.assertEqual(len(alphas), len(set(alphas)),
                         f"two ledgers share an alpha code: {self.go}")


if __name__ == "__main__":
    unittest.main()
