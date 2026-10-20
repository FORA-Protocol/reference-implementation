"""Obligation BROWSE-002: discovery has no side effects.

``ExchangeService.DiscoverResources`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol``: discovery creates no transaction, no billing
authorization and no reporting obligation; an agent commits to an offer only on the
execute path. No RPC lists a buyer's transactions or obligations, so each is read
through what it would move:

* **No billing authorization.** ``GetAccountStatus`` reports the balance the account
  can spend now, with the holds of pending transactions already subtracted
  (SETTLE-001). A browse that authorized a charge would lower it. The balance is
  read around a browse of the demo ``epicurus`` resource at exchange-a, and the
  control buys the offer the browse returned: the same reading drops by exactly the
  item's cost.
* **No reporting obligation.** exchange-d refuses a purchase with
  DENIAL_REASON_REPORTING_OVERDUE once more than one in five of an agent's
  past-deadline obligations there is unreported (PAID-007), and every sale of the
  archive's lecture notes owes a report. With the seat's reporting window shortened
  through the admin plane, the agent browses the lecture notes three times and waits
  until a window opened at the last browse has passed. Had any browse minted an
  obligation, it would now be unreported and past its deadline, and the agent's next
  purchase would be refused. It is bought. The control leaves that purchase
  unreported, and the agent's purchases are refused REPORTING_OVERDUE once its window
  passes, so the same gate does see an unreported obligation.
* **No transaction.** On the lecture notes, a transaction holds the buyer's money and
  mints a reporting obligation, so the two readings above cover it.

Every leg goes in through the Exchange's public RPCs and is read back through them;
the reporting window is set through the admin plane and put back when the test ends.
No leg reads the deployment's datastore.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest
from wire.models import TransactionResultItem

from ..admin_plane import reporting_window, require_admin_plane
from ..broker_purchase import discover_group
from ..constants import requester
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_D_DOMAIN
from ..fresh_agent import FreshAgent
from ..money_path import LECTURE_NOTES, discover_offer, only_item
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from .flow import buy_verified_and_observe_charge

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"

#: exchange-d sells the lecture notes, and every sale of them owes a usage report.
_SEAT = EXCHANGE_D_DOMAIN
_WINDOW_SECONDS = 5
_BROWSES = 3
_OVERDUE = "DENIAL_REASON_REPORTING_OVERDUE"
#: How long the test waits for a short window to pass, and then for the control's
#: refusal. Several windows, so a loaded stack still sees each deadline pass.
_WAIT_SECONDS = 60.0


@obligation(Obligation.BROWSE_002)
def test_browse_moves_no_money_while_a_purchase_does(fresh_agent: FreshAgent) -> None:
    """A successful DiscoverResources leaves the buyer's balance where it was."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    before = fresh_agent.balances(EXCHANGE_A_DOMAIN)

    # (a) the browse succeeded: the SDK client returns only a successful answer
    # that passed the strict response schema.
    group = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN),
        EXCHANGE_A_DOMAIN,
        _RESOURCE_URI,
        "browse",
        requester=requester(fresh_agent.agent_id, user_type="individual", geography="US"),
    )
    # (b) browse returned an offer the agent could have accepted
    assert group.result.verified, f"browse must surface at least one offer; got {group}"

    # (c) the browse authorized no charge: the balance it can spend did not move.
    after_browse = fresh_agent.balances(EXCHANGE_A_DOMAIN)
    assert after_browse == before, (
        f"a browse moved the buyer's money: {before} -> {after_browse} "
        f"(BROWSE-002: discovery creates no billing authorization)"
    )

    # Control: accepting the offer the agent walked away from DOES move the same
    # reading, by exactly the item's cost.
    buy_verified_and_observe_charge(
        fresh_agent, EXCHANGE_A_DOMAIN, group.result.verified[0], "control purchase"
    )


def _overdue_refusal(agent: FreshAgent) -> TransactionResultItem | None:
    """One purchase of the lecture notes: the item when it is refused, None when bought."""
    item = only_item(
        agent.exchange(_SEAT).execute(discover_offer(agent, LECTURE_NOTES, _SEAT)),
        "a purchase while the agent's report is overdue",
    )
    return item if item.denial_reason is not None else None


@obligation(Obligation.BROWSE_002)
def test_browses_owe_no_report_while_an_unreported_purchase_does(
    fresh_agent: FreshAgent,
) -> None:
    """Browses past a short window leave no overdue obligation; an unreported purchase does."""
    require_admin_plane()
    fresh_agent.fund(_SEAT)

    with reporting_window(_SEAT, _WINDOW_SECONDS) as policy:
        assert policy.window_seconds == _WINDOW_SECONDS, f"{_SEAT} echoed {policy}"
        before = fresh_agent.balances(_SEAT)
        for _ in range(_BROWSES):
            discover_offer(fresh_agent, LECTURE_NOTES, _SEAT)
        assert fresh_agent.balances(_SEAT) == before, (
            f"{_BROWSES} browses moved the buyer's money: {before} -> {fresh_agent.balances(_SEAT)}"
        )
        passed = datetime.now(UTC) + timedelta(seconds=_WINDOW_SECONDS + 1)
        poll_until(
            lambda: datetime.now(UTC) > passed,
            _WAIT_SECONDS,
            failure=lambda: f"{passed} did not pass",
        )

        # Had a browse minted an obligation, it is now unreported and past its
        # deadline, and this purchase is denied REPORTING_OVERDUE.
        bought = buy_verified_and_observe_charge(
            fresh_agent,
            _SEAT,
            discover_offer(fresh_agent, LECTURE_NOTES, _SEAT),
            "the first purchase after the browses",
        )
        told = bought.reporting_obligation.window if bought.reporting_obligation else None
        assert told == f"{_WINDOW_SECONDS}s", (
            f"the purchase owes a report within {told!r}, want the {_WINDOW_SECONDS}s the "
            f"policy set, which the browses ran under too"
        )

        # Control: that purchase is left unreported, and once its window passes the
        # same gate refuses the agent.
        refused = poll_until(
            lambda: _overdue_refusal(fresh_agent),
            _WAIT_SECONDS,
            failure=lambda: (
                f"the agent was still not refused {_OVERDUE} after {_WAIT_SECONDS:.0f}s, "
                f"though its purchase {bought.transaction_id} was never reported"
            ),
        )
        assert refused.denial_reason is not None and refused.denial_reason.value == _OVERDUE, (
            f"the agent's purchase was denied {refused.denial_reason}, want {_OVERDUE}"
        )
