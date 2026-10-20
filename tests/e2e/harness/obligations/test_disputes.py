"""Obligations SETTLE-006 to SETTLE-009: filing a dispute, and the checks that refuse one.

``ExchangeService.DisputeTransaction`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol`` is part of the core protocol: every Exchange
implements it. A filing the Exchange accepts is answered with a dispute_id and a
status; a filing it refuses is a non-OK error carrying
``ErrorDetail.dispute_failure``, whose reason names the one check that failed.
The protocol names each reason and states no Connect code for it, so a refusal is
read by its typed reason.

The reference Exchange answers the RPC unimplemented, so each test is a strict
xfail on that defect, raised the first time a filing that should be accepted is
answered unimplemented. Every filing goes through the SDK's own client as the
test's own agent, after a purchase of the archive's lecture notes and, where the
check needs one, an accepted usage report.
"""

from __future__ import annotations

import uuid
from typing import Any

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from wire.models import DisputeReason, DisputeRequest, DisputeResponse, TransactionResultItem

from ..defects import DISPUTE_UNIMPLEMENTED
from ..exchanges import EXCHANGE_C_DOMAIN, EXCHANGE_D_DOMAIN
from ..fresh_agent import FreshAgent
from ..malformed import Alteration, Mutation, Request, SeamHTTP, malformed, patch_body
from ..money_path import LECTURE_NOTES, assert_bought, discover_offer, only_item, typed_reasons
from ..obligation_registry import Obligation, obligation
from ..reporting import report_body
from ..sdk_client import BeforeSign

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEAT = EXCHANGE_D_DOMAIN


def _bought(agent: FreshAgent) -> TransactionResultItem:
    item = only_item(agent.exchange(_SEAT).execute(discover_offer(agent, LECTURE_NOTES)), "buy")
    assert_bought(item, "the purchase to dispute")
    return item


def _reported(agent: FreshAgent, item: TransactionResultItem) -> str:
    report = report_body(
        exchange=_SEAT, transaction_id=str(item.transaction_id), billing_id=item.billing_id
    )
    report_id = agent.exchange(_SEAT).report_usage(report).report_id
    assert report_id, "the report was accepted with no report_id"
    return report_id


def _new_key() -> str:
    return f"dispute-{uuid.uuid4().hex}"


def _filing(item: TransactionResultItem | None, report_id: str = "") -> DisputeRequest:
    """A CONTENT_MISMATCH filing for ``item`` (an unknown transaction when None), under a new key."""
    return DisputeRequest(
        ver=ProtocolVersion,
        exchange=_SEAT,
        idempotency_key=_new_key(),
        transaction_id=str(item.transaction_id) if item else f"tx-{uuid.uuid4().hex}",
        billing_id=(item.billing_id if item else "") or "",
        reason=DisputeReason.DISPUTE_REASON_CONTENT_MISMATCH,
        report_id=report_id,
    )


def _file(agent: FreshAgent, filing: DisputeRequest, key: str | None = None) -> DisputeResponse:
    """File ``filing``; a filing that should be accepted and is answered unimplemented is the defect."""
    try:
        answer = agent.exchange(_SEAT).dispute(filing, idempotency_key=key)
    except CallError as refused:
        DISPUTE_UNIMPLEMENTED.check(refused.code != "unimplemented", f"answered {refused}")
        raise
    assert answer.dispute_id, f"an accepted dispute carries no dispute_id: {answer}"
    return answer


def _refusal_reason(agent: FreshAgent, filing: DisputeRequest, key: str | None = None) -> str:
    """File ``filing``, which must be refused; return the typed reason it names."""
    with pytest.raises(CallError) as refused:
        agent.exchange(_SEAT).dispute(filing, idempotency_key=key)
    block = refused.value.detail.dispute_failure if refused.value.detail else None
    assert block is not None, f"a refused filing carries no dispute_failure: {refused.value}"
    return str(block.reason.value)


@obligation(Obligation.SETTLE_006, negative=True)
@DISPUTE_UNIMPLEMENTED.mark
def test_a_dispute_needs_an_accepted_report_first(fresh_agent: FreshAgent) -> None:
    """With its report_id, a dispute is accepted; without a report, it is refused REPORT_NOT_FILED."""
    fresh_agent.fund(_SEAT)
    reported = _bought(fresh_agent)
    _file(fresh_agent, _filing(reported, _reported(fresh_agent, reported)))
    unreported = _bought(fresh_agent)
    assert (
        _refusal_reason(fresh_agent, _filing(unreported))
        == "DISPUTE_FAILURE_REASON_REPORT_NOT_FILED"
    )


@obligation(Obligation.SETTLE_007, negative=True)
@DISPUTE_UNIMPLEMENTED.mark
def test_a_replayed_filing_opens_no_second_case(fresh_agent: FreshAgent) -> None:
    """The same filing under one key: one dispute_id. Under a new key: refused DUPLICATE."""
    fresh_agent.fund(_SEAT)
    item = _bought(fresh_agent)
    filing = _filing(item, _reported(fresh_agent, item))
    key = _new_key()
    first = _file(fresh_agent, filing, key)
    assert _file(fresh_agent, filing, key).dispute_id == first.dispute_id, "a replay opened a case"
    assert _refusal_reason(fresh_agent, filing, _new_key()) == "DISPUTE_FAILURE_REASON_DUPLICATE"


@obligation(Obligation.SETTLE_008, negative=True)
@DISPUTE_UNIMPLEMENTED.mark
def test_a_dispute_for_an_unknown_transaction_is_refused(fresh_agent: FreshAgent) -> None:
    """The caller's own transaction is disputable; an unknown one is refused TRANSACTION_NOT_FOUND."""
    fresh_agent.fund(_SEAT)
    item = _bought(fresh_agent)
    _file(fresh_agent, _filing(item, _reported(fresh_agent, item)))
    assert (
        _refusal_reason(fresh_agent, _filing(None))
        == "DISPUTE_FAILURE_REASON_TRANSACTION_NOT_FOUND"
    )


@obligation(Obligation.SETTLE_009, negative=True)
@DISPUTE_UNIMPLEMENTED.mark
def test_a_dispute_addressed_to_another_exchange_is_refused(fresh_agent: FreshAgent) -> None:
    """A filing reaching exchange-d that names exchange-c: invalid_argument, no typed reason."""
    fresh_agent.fund(_SEAT)
    first = _bought(fresh_agent)
    _file(fresh_agent, _filing(first, _reported(fresh_agent, first)))
    item = _bought(fresh_agent)
    filing = _filing(item, _reported(fresh_agent, item))

    def client(*, signer: Any, before_sign: BeforeSign | None, http: SeamHTTP) -> Any:
        return fresh_agent.exchange_client(_SEAT, signer=signer, before_sign=before_sign, http=http)

    def name_exchange_c(body: dict[str, Any]) -> None:
        body["exchange"] = EXCHANGE_C_DOMAIN

    refusal = malformed(
        Mutation.IDENTITY,
        "the filing names exchange-c and reaches exchange-d",
        control=Request(client=client, verb=lambda c: c.dispute, message=filing),
        alter=Alteration(before_sign=patch_body(name_exchange_c)),
    )
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
    assert not typed_reasons(refusal), f"a mis-addressed filing carries no typed reason: {refusal}"
