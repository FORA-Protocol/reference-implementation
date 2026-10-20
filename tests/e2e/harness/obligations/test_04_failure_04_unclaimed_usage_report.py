"""Obligation PUBLIC-005: a report for another agent's transaction is answered as for an unknown one.

``UsageReport`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol``: a report for a transaction the Exchange
recorded under a different agent is answered exactly as a report for an unknown
transaction, ``not_found`` with ``usage_report_rejection`` TRANSACTION_NOT_FOUND, so
the answer does not reveal that the transaction exists.

Two agents of the test's own, both funded at exchange-d. The buyer buys the
archive's lecture notes, a sale that owes a usage report. The stranger then files
two reports, each through its own SDK client and signed with its own key:

* one for the buyer's transaction, carrying the buyer's real billing_id, so the
  report is right in every field except whose transaction it names;
* one for a transaction id no one holds.

Both must be refused the same way, with the same Connect code and the same typed
reason, and that answer must be not_found with TRANSACTION_NOT_FOUND. The report
for the buyer's transaction is sent a second time and refused the same way again.

"Not recorded" is read through ReportUsage itself. A transaction takes one report:
a second report under a new key is refused DUPLICATE (MONEY-028). The buyer's own
report, filed after the stranger's two attempts, is accepted, so neither attempt
was recorded against the transaction. That control holds today and after the fix
alike, as does the second attempt being answered like the first, so both run
before the answer itself is judged.

A report for a transaction id no one holds, filed by any agent, is MONEY-024.
"""

from __future__ import annotations

import uuid

import pytest
from fora_sdk.client import CallError
from wire.models import UsageReport

from ..defects import FOREIGN_REPORT_NOT_HIDDEN
from ..exchanges import EXCHANGE_D_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..money_path import LECTURE_NOTES, assert_bought, discover_offer, only_item
from ..obligation_registry import Obligation, obligation
from ..reporting import report_body

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEAT = EXCHANGE_D_DOMAIN
_NOT_FOUND = ("not_found", "USAGE_REPORT_REJECTION_REASON_TRANSACTION_NOT_FOUND")


def _refusal(agent: FreshAgent, report: UsageReport) -> tuple[str | None, str | None]:
    """The Connect code and the usage_report_rejection reason ``agent``'s report is refused with."""
    with pytest.raises(CallError) as refused:
        agent.exchange(_SEAT).report_usage(report)
    detail = refused.value.detail
    block = detail.usage_report_rejection if detail is not None else None
    return refused.value.code, block.reason.value if block is not None else None


@obligation(Obligation.PUBLIC_005, negative=True)
@FOREIGN_REPORT_NOT_HIDDEN.mark
def test_a_report_for_another_agents_transaction_is_answered_as_for_an_unknown_one(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Refused not_found TRANSACTION_NOT_FOUND, as an unknown id is; the buyer's report then stands."""
    buyer, stranger = fresh_agent_factory(), fresh_agent_factory()
    buyer.fund(_SEAT)
    stranger.fund(_SEAT)
    item = only_item(
        buyer.exchange(_SEAT).execute(discover_offer(buyer, LECTURE_NOTES, _SEAT)),
        "the buyer's purchase",
    )
    assert_bought(item, "the buyer's purchase")
    assert item.reporting_obligation is not None and item.reporting_obligation.required, (
        f"the purchase must owe a usage report for a report to be filed against it: {item}"
    )

    foreign = report_body(
        exchange=_SEAT, transaction_id=str(item.transaction_id), billing_id=item.billing_id
    )
    unknown = report_body(exchange=_SEAT, transaction_id=f"tx-{uuid.uuid4().hex}")

    answered = _refusal(stranger, foreign)
    unknown_answer = _refusal(stranger, unknown)
    assert _refusal(stranger, foreign) == answered, "the second attempt was answered differently"

    own = report_body(
        exchange=_SEAT, transaction_id=str(item.transaction_id), billing_id=item.billing_id
    )
    report_id = buyer.exchange(_SEAT).report_usage(own).report_id
    assert report_id, (
        f"the buyer's own report was not accepted after the stranger's refused attempts: {own}"
    )

    FOREIGN_REPORT_NOT_HIDDEN.check(
        answered == _NOT_FOUND,
        f"the stranger's report for the buyer's transaction was refused {answered}",
    )
    assert unknown_answer == answered, (
        "a report for an unknown transaction is answered differently from one for another "
        "agent's transaction, which reveals that the transaction exists"
    )
