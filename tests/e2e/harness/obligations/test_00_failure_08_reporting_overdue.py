"""Obligation PAID-007: a buyer with overdue usage reports is refused new purchases.

The protocol leaves this to the Exchange (a MAY): ``DENIAL_REASON_REPORTING_OVERDUE``
is the reason a refusal names when the requester is behind on its usage reports.
This Exchange implements it: an agent whose unreported, past-deadline obligations
exceed one in five of its past-deadline obligations at a seat is refused its next
purchase there, per item, before any money is reserved.

The seat's reporting window is a day by default, so the window is shortened for
this test through the admin plane (``AdminService.SetReportingPolicy``, through
the SDK's ``AdminClient``) and put back when the test ends. The seat is
exchange-d's: the policy is per seat, the suite runs one test at a time, and
the only other test that buys there makes one purchase, which this test is
never running beside. Only obligations minted while the window is short get the
short deadline.

Two agents of this test's own, both funded at exchange-d, buy the archive's
lecture notes (flat 0.5 USD, so every purchase mints a reporting obligation):

* the punctual agent buys first, reports at once, and the evidence shows the
  report accepted before the deadline;
* the late agent buys next and never reports. Its purchase's obligation is read back
  from the Exchange's evidence: still pending, minted with the shortened window.
  It then tries to buy again until the deadline has passed, through the harness
  poller. The purchase that is refused names DENIAL_REASON_REPORTING_OVERDUE,
  carries no delivery URL, no transaction and no charge, and the agent's
  balances, read through GetAccountStatus around that attempt, did not move;
* once the late agent is refused, the punctual agent's own deadline, which is
  earlier, has passed as well. It buys again and is charged. So the refusal is
  the missing report's doing, not the clock's or the seat's.

The purchases and the report go in through the Exchange's public RPCs and are
read back through them. The policy goes in through the admin plane. The evidence
is read on the admin listener: no protocol RPC returns a buyer's obligations.
No leg reads a datastore.
"""

from __future__ import annotations

from decimal import Decimal

import pytest
from wire.models import TransactionResultItem

from ..admin_plane import reporting_window, require_admin_plane, transaction_evidence
from ..broker_purchase import one_group_per_uri, only_verified_offer, resource_query
from ..e2e_catalog import DEMO_ARCHIVE_DOMAIN
from ..exchanges import EXCHANGE_D_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..ledger import Balances
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..reporting import file_report, report_body

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEAT = EXCHANGE_D_DOMAIN
_LECTURE_NOTES = f"http://{DEMO_ARCHIVE_DOMAIN}/archive/lyceum-lecture-notes.txt"
_WINDOW_SECONDS = 5
_OVERDUE = "DENIAL_REASON_REPORTING_OVERDUE"
_PENDING = "PENDING"

#: How long the late agent keeps trying to buy before the test gives up. Several
#: windows, so a loaded stack still sees the deadline pass well inside it.
_REFUSAL_DEADLINE_SECONDS = 60.0


def _buy(agent: FreshAgent) -> TransactionResultItem:
    """Discover the lecture notes at exchange-d and buy the verified offer; the one item."""
    exchange = agent.exchange(_SEAT)
    found = exchange.discover(resource_query(_SEAT, [_LECTURE_NOTES]))
    group = one_group_per_uri(found, [_LECTURE_NOTES], "exchange-d discovery")[_LECTURE_NOTES]
    items = exchange.execute(only_verified_offer(group, "exchange-d discovery")).items or []
    assert len(items) == 1, f"one offer bought, {len(items)} items answered: {items}"
    return items[0]


def _assert_bought(item: TransactionResultItem, leg: str) -> str:
    """The item was bought; return its transaction id."""
    assert item.denial_reason is None, f"{leg}: denied {item.denial_reason}: {item}"
    assert item.transaction_id, f"{leg}: no transaction_id: {item}"
    assert item.retrieval_endpoint, f"{leg}: no delivery URL: {item}"
    return item.transaction_id


def _charge(item: TransactionResultItem) -> tuple[str, Decimal]:
    assert item.cost is not None, f"a bought item carries no cost: {item}"
    return str(item.cost.currency), Decimal(str(item.cost.amount))


def _refused_attempt(
    agent: FreshAgent,
) -> tuple[TransactionResultItem, Balances, Balances] | None:
    """One purchase attempt: None if it was bought, else the item and the balances around it.

    An attempt that is bought before the deadline passes is one more unreported
    obligation, which is the condition under test growing, not a disturbance of it.
    """
    before = agent.balances(_SEAT)[_SEAT]
    item = _buy(agent)
    if item.denial_reason is None:
        _assert_bought(item, "the late agent, before its deadline passed")
        return None
    return item, before, agent.balances(_SEAT)[_SEAT]


@obligation(Obligation.PAID_007, negative=True)
def test_a_buyer_behind_on_its_reports_is_refused_and_one_that_reported_is_not(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Unreported past its window: refused REPORTING_OVERDUE, nothing charged. Reported: bought."""
    require_admin_plane()
    late, punctual = fresh_agent_factory(), fresh_agent_factory()
    late.fund(_SEAT)
    punctual.fund(_SEAT)

    with reporting_window(_SEAT, _WINDOW_SECONDS) as policy:
        assert policy.window_seconds == _WINDOW_SECONDS, f"exchange-d echoed {policy}"

        bought = _buy(punctual)
        reported = _assert_bought(bought, "the punctual agent's first purchase")
        file_report(
            punctual.stack,
            report_body(
                exchange=_SEAT,
                transaction_id=reported,
                billing_id=bought.billing_id,
            ),
            agent_id=punctual.agent_id,
            key_path=punctual.key_path,
        )
        kept = transaction_evidence(_SEAT, reported).obligation
        assert kept is not None and kept.fulfilled_at is not None, (
            f"the punctual agent's report was not recorded against {reported}: {kept}"
        )
        assert kept.fulfilled_at < kept.window_end, (
            f"the report was accepted at {kept.fulfilled_at}, after the deadline "
            f"{kept.window_end}; the control needs a report filed on time"
        )

        first = _buy(late)
        unreported = _assert_bought(first, "the late agent's first purchase")
        told = first.reporting_obligation.window if first.reporting_obligation else None
        assert told == f"{_WINDOW_SECONDS}s", (
            f"the agent was told to report within {told!r}, want the "
            f"{_WINDOW_SECONDS}s the policy set"
        )
        owed = transaction_evidence(_SEAT, unreported).obligation
        assert owed is not None, f"the purchase of {_LECTURE_NOTES} minted no obligation"
        assert owed.state == _PENDING, f"an unreported obligation is {owed.state}"
        # created_at is stamped by the database on insert, a few microseconds
        # after the service clock that wrote the deadline, so the stored window
        # is compared to the nearest second.
        minted_with = (owed.window_end - owed.created_at).total_seconds()
        assert round(minted_with) == _WINDOW_SECONDS, (
            f"the obligation was stored with a {minted_with}s window, want the "
            f"{_WINDOW_SECONDS}s the policy set"
        )

        refused, before, after = poll_until(
            lambda: _refused_attempt(late),
            _REFUSAL_DEADLINE_SECONDS,
            failure=lambda: (
                f"the late agent's purchase was still not refused after "
                f"{_REFUSAL_DEADLINE_SECONDS}s, though its report was overdue"
            ),
            interval_s=1.0,
        )
        assert refused.denial_reason is not None and refused.denial_reason.value == _OVERDUE, (
            f"the late agent was refused {refused.denial_reason}, want {_OVERDUE}: {refused}"
        )
        for field in ("transaction_id", "billing_id", "cost", "retrieval_endpoint"):
            assert not getattr(refused, field), f"the refused item carries {field}: {refused}"
        assert after == before, f"the refused purchase moved money: {before} -> {after}"

        # Control. The punctual agent bought first, so its deadline is earlier
        # than the late agent's first one, on the Exchange's own clock. The
        # refusal above means that later deadline has passed, so the punctual
        # agent's has passed too, and it is now past due and reported.
        assert kept.window_end < owed.window_end, (
            f"the punctual agent's deadline {kept.window_end} is not before the late "
            f"agent's {owed.window_end}, so the refusal does not show it has passed"
        )
        was = punctual.balances(_SEAT)[_SEAT]
        control = _buy(punctual)
        _assert_bought(control, "the punctual agent, after its deadline passed")
        currency, cost = _charge(control)
        now = punctual.balances(_SEAT)[_SEAT]
        assert cost > 0 and now[currency] == was[currency] - cost, (
            f"the punctual agent's {currency} went from {was[currency]} to {now[currency]}; "
            f"the item cost {cost}"
        )
