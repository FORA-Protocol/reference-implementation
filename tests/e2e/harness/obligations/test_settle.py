"""Obligations SETTLE-001 to SETTLE-004: balances, exact amounts, and totals across currencies.

The contract is ``GetAccountStatusResponse.balances``, ``Cost`` and
``TransactionResponse.total_cost`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol``:

* a balance is what the account can spend now, one entry per currency, an exact
  decimal string, never negative, and "0" rather than absent for a currency the
  Exchange reports and the account holds nothing in;
* ``total_cost`` is the exact sum of the purchased items in their one shared
  currency, and unset when they span more than one currency.

Every purchase is the SDK's ``ExecuteTransaction`` as the test's own agent, and
every balance is that agent's ``GetAccountStatus``. A resource priced at exactly
a fresh agent's welcome credit, or above it, is pushed by the test to exchange-a.
"""

from __future__ import annotations

import re
from decimal import Decimal

import pytest
from fora_sdk import ProtocolVersion

from ..catalog_calls import HOME_SEAT, accepted, contributor, fresh_entry
from ..catalog_push import PRICING_MODEL_FLAT, license_term
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, EXCHANGE_C_DOMAIN
from ..fresh_agent import FreshAgent
from ..ledger import BILLED_CURRENCIES
from ..money_path import (
    PAPER_SATELLITES,
    RAIN,
    SCI_FI_DOOR,
    TITANS,
    assert_bought,
    assert_denied,
    assert_moved,
    charged,
    discover_offer,
    discover_offers,
)
from ..obligation_registry import Obligation, obligation
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_INSUFFICIENT_BALANCE = "DENIAL_REASON_INSUFFICIENT_BALANCE"
_EXACT_DECIMAL = re.compile(r"^[0-9]+([.][0-9]+)?$")
_ISO_4217 = re.compile(r"^[A-Z]{3}$")


def _push_flat(stack: StackURLs, rate: str) -> str:
    """Push a resource to exchange-a with one FLAT USD term at ``rate``; return its URI."""
    entry = fresh_entry(
        terms=(license_term(model=PRICING_MODEL_FLAT, rate=float(rate), currency="USD"),)
    )
    accepted(contributor(stack).push(HOME_SEAT, [entry]))
    return entry.uri


def _raw_balances(agent: FreshAgent, exchange: str) -> list[tuple[str, str]]:
    """The balances GetAccountStatus reports, as (currency, amount string) pairs."""
    status = agent.exchange(exchange).get_account_status(
        {"ver": ProtocolVersion, "exchange": exchange}
    )
    return [(str(cost.currency), str(cost.amount)) for cost in status.balances or []]


@obligation(Obligation.SETTLE_001, negative=True)
@obligation(Obligation.SETTLE_003)
def test_a_balance_drops_by_the_charge_and_never_below_zero(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """A 12 USD purchase lowers USD by 12 and nothing else; one above the balance is denied.

    The fresh agent holds its welcome credit. A purchase priced above what it
    holds in USD is denied INSUFFICIENT_BALANCE in the body, and the balance stays
    where it was. Every amount on the way is an exact decimal in an ISO 4217
    currency.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN, EXCHANGE_A_DOMAIN)
    before = fresh_agent.balances(EXCHANGE_C_DOMAIN)
    item = fresh_agent.exchange(EXCHANGE_C_DOMAIN).execute(discover_offer(fresh_agent, SCI_FI_DOOR))
    (bought,) = item.items or []
    assert_bought(bought, "the 12 USD purchase")
    currency, cost = charged(bought)
    assert_moved(
        before,
        fresh_agent.balances(EXCHANGE_C_DOMAIN),
        {EXCHANGE_C_DOMAIN: {currency: cost}},
    )
    assert bought.cost is not None and _EXACT_DECIMAL.match(str(bought.cost.amount)), bought.cost
    assert _ISO_4217.match(str(bought.cost.currency)), bought.cost

    held = fresh_agent.balance(EXCHANGE_A_DOMAIN, "USD")
    dear = _push_flat(compose_stack, str(held + Decimal(50)))
    offer = discover_offer(fresh_agent, dear, EXCHANGE_A_DOMAIN)
    answer = fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offer)
    (denied,) = answer.items or []
    assert_denied(denied, _INSUFFICIENT_BALANCE, "a purchase above the balance")
    for currency_code, amount in _raw_balances(fresh_agent, EXCHANGE_A_DOMAIN):
        assert _EXACT_DECIMAL.match(amount) and Decimal(amount) >= 0, (currency_code, amount)
        assert _ISO_4217.match(currency_code), currency_code
    assert fresh_agent.balance(EXCHANGE_A_DOMAIN, "USD") == held, "a denied purchase moved money"


@obligation(Obligation.SETTLE_002)
def test_a_currency_spent_to_nothing_is_reported_as_zero(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """After a purchase of exactly the USD the agent holds, USD is reported as "0", not left out."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    held = fresh_agent.balance(EXCHANGE_A_DOMAIN, "USD")
    offer = discover_offer(fresh_agent, _push_flat(compose_stack, str(held)), EXCHANGE_A_DOMAIN)
    (bought,) = fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offer).items or []
    assert_bought(bought, "the purchase of everything the agent held in USD")
    reported = dict(_raw_balances(fresh_agent, EXCHANGE_A_DOMAIN))
    assert set(reported) == BILLED_CURRENCIES, f"one entry per billed currency, got {reported}"
    assert Decimal(reported["USD"]) == 0 and _EXACT_DECIMAL.match(reported["USD"]), reported


@obligation(Obligation.SETTLE_004)
def test_total_cost_is_the_sum_in_one_currency_and_unset_across_two(
    fresh_agent: FreshAgent,
) -> None:
    """Two USD items: total_cost is their sum. A USD and a EUR item: total_cost is unset."""
    fresh_agent.fund(EXCHANGE_C_DOMAIN, EXCHANGE_B_DOMAIN)
    same = fresh_agent.exchange(EXCHANGE_C_DOMAIN).execute(
        [discover_offer(fresh_agent, SCI_FI_DOOR), discover_offer(fresh_agent, RAIN)]
    )
    for item in same.items or []:
        assert_bought(item, "an item of the one-currency purchase")
    total = same.total_cost
    assert total is not None, f"a one-currency purchase answers no total_cost: {same}"
    assert total.currency == "USD" and Decimal(str(total.amount)) == sum(
        (charged(item)[1] for item in same.items or []), Decimal(0)
    ), total
    assert total.unit_cost is None, total

    offers = [
        discover_offer(fresh_agent, PAPER_SATELLITES),
        *discover_offers(fresh_agent, TITANS)[:1],
    ]
    before = fresh_agent.balances(EXCHANGE_B_DOMAIN)
    mixed = fresh_agent.exchange(EXCHANGE_B_DOMAIN).execute(offers)
    items = mixed.items or []
    for item in items:
        assert_bought(item, "an item of the two-currency purchase")
    assert {charged(item)[0] for item in items} == {"USD", "EUR"}, items
    assert mixed.total_cost is None, f"total_cost summed two currencies: {mixed.total_cost}"
    spent: dict[str, Decimal] = {}
    for item in items:
        currency, amount = charged(item)
        spent[currency] = spent.get(currency, Decimal(0)) + amount
    assert_moved(
        before,
        fresh_agent.balances(EXCHANGE_B_DOMAIN),
        {EXCHANGE_B_DOMAIN: spent},
    )
