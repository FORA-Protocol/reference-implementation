"""A buyer's balances, read the way the buyer reads them: GetAccountStatus.

Every Exchange in this stack bills on the stack's one TigerBeetle cluster, under
its own id namespace, with one ledger per currency. ``GetAccountStatus`` reports
the calling agent's available balance in every currency the Exchange bills in,
read live from TigerBeetle, and it is the only way a suite here observes money.
Nothing reads the ledger, the database or the Exchange's process.

Other suites spend the same buyers' money and a stack left up keeps it, so a
claim made with these readings is about the CHANGE around one action, never
about an absolute balance.

Money is ``Decimal``: the Exchange sends exact decimal strings, and float
arithmetic would turn an exact 0.25 into an approximate one.
"""

from __future__ import annotations

from decimal import Decimal

from fora_sdk.sync import Client
from wire.models import Cost

from .seed_accounts import account_status_request

# Every Exchange bills in exactly these currencies (EXCHANGE_BILLING_LEDGER in
# docker-compose.e2e.yml), so every registered buyer has a balance in each.
BILLED_CURRENCIES = frozenset({"USD", "EUR"})

#: One buyer's balance per currency at one Exchange.
Balances = dict[str, Decimal]


def balances(client: Client, exchange: str) -> Balances:
    """The calling agent's available balance per currency at ``exchange``."""
    status = client.get_account_status(account_status_request(exchange))
    assert status.billing_ref and status.active, (
        f"the buyer has no active account at {exchange}, so the seed's Register did "
        f"not run or was refused: {status}"
    )
    out: Balances = {}
    for cost in status.balances or []:
        assert cost.currency not in out, f"{cost.currency} reported twice: {status}"
        out[str(cost.currency)] = Decimal(str(cost.amount))
    assert set(out) == BILLED_CURRENCIES, (
        f"{exchange} reports balances in {sorted(out)}; it bills in "
        f"{sorted(BILLED_CURRENCIES)}, and Register opens an account in each"
    )
    return out


def assert_charged(before: Balances, after: Balances, cost: Cost | None, what: str) -> Decimal:
    """``after`` is ``before`` less ``cost`` in its currency, and equal in every other.

    ``cost`` is the item's ``Cost`` as ExecuteTransaction answered it. Returns the
    amount charged, so a caller can hold it against the price it expected.
    """
    assert cost is not None, f"{what}: the item carries no cost"
    currency = str(cost.currency)
    amount = Decimal(str(cost.amount))
    assert currency in BILLED_CURRENCIES, f"{what}: charged in {currency!r}: {cost}"
    assert amount > 0, f"{what}: the item was expected to be paid: {cost}"
    assert after[currency] == before[currency] - amount, (
        f"{what}: {currency} went from {before[currency]} to {after[currency]}; "
        f"the item cost {amount}"
    )
    for other in BILLED_CURRENCIES - {currency}:
        assert after[other] == before[other], (
            f"{what}: a {currency} charge moved the {other} balance from "
            f"{before[other]} to {after[other]}"
        )
    return amount
