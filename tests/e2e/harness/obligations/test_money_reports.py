"""Obligations MONEY-022 to MONEY-031: usage reports.

The contract is ``ExchangeService.ReportUsage``, ``UsageReport``,
``UsageReportRejectionReason`` and ``Pricing`` in ``fora.proto`` of the pinned
module ``github.com/FORA-Protocol/protocol``, with the reporting policy of
``fora.admin.v1.AdminService``:

* a report is refused for time, never for its quantity: past its window it is
  refused ``failed_precondition`` with WINDOW_EXPIRED; a refusal always carries
  ``ErrorDetail.usage_report_rejection``;
* a report whose billing_id is not the billing_id of the transaction it names is
  malformed: refused ``invalid_argument`` with MALFORMED;
* the idempotency key dedupes a replayed report, and the same key with a
  different report is refused ``already_exists`` with no typed reason;
* a report is a record of the quantity consumed, accepted whatever that
  quantity is, with an estimate on the offer or without one;
* a sale with metering NONE owes no report, and a window change applies only to
  obligations minted after it.

Every purchase and report goes through the SDK's own client as the test's own
agent; charges are read through ``GetAccountStatus``; the policy is set through
the SDK's ``AdminClient`` and put back when the test ends. The archive's lecture
notes (exchange-d, flat 0.5 USD) mint a reporting obligation on every purchase.
"""

from __future__ import annotations

import uuid
from collections.abc import Iterator
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from typing import Any

import pytest
from fora_sdk.client import CallError
from wire.models import TransactionResultItem, UsageReport, UsageReportResponse

from ..admin_plane import reporting_policy, require_admin_plane, transaction_evidence
from ..catalog_calls import HOME_SEAT, accepted, contributor, fresh_entry
from ..catalog_push import PRICING_MODEL_PER_UNIT, license_term
from ..defects import (
    LATE_REPORT_ACCEPTED,
    REPORT_REFUSED_FOR_QUANTITY,
    REUSED_REPORT_KEY_ANSWERED,
    USAGE_REPORT_REFUSAL_UNTYPED,
)
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, EXCHANGE_C_DOMAIN, EXCHANGE_D_DOMAIN
from ..fresh_agent import FreshAgent
from ..malformed import Alteration, Mutation, Request, SeamHTTP, malformed, patch_body
from ..money_path import (
    LECTURE_NOTES,
    TITANS,
    assert_bought,
    discover_offer,
    discover_offers,
    only_item,
    typed_reasons,
)
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..reporting import report_body
from ..sdk_client import BeforeSign
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEAT = EXCHANGE_D_DOMAIN
_WINDOW_SECONDS = 3
#: How long a test waits for a short window to pass.
_WAIT_SECONDS = 30.0

_NOT_FOUND = "USAGE_REPORT_REJECTION_REASON_TRANSACTION_NOT_FOUND"
_DUPLICATE = "USAGE_REPORT_REJECTION_REASON_DUPLICATE"
_WINDOW_EXPIRED = "USAGE_REPORT_REJECTION_REASON_WINDOW_EXPIRED"
_MISSING_FIELDS = "USAGE_REPORT_REJECTION_REASON_MISSING_REQUIRED_FIELDS"
_MALFORMED = "USAGE_REPORT_REJECTION_REASON_MALFORMED"


def _buy(
    agent: FreshAgent, uri: str = LECTURE_NOTES, exchange: str = _SEAT
) -> TransactionResultItem:
    item = only_item(agent.exchange(exchange).execute(discover_offer(agent, uri, exchange)), uri)
    assert_bought(item, f"the purchase of {uri}")
    return item


def _report(
    item: TransactionResultItem,
    *,
    exchange: str = _SEAT,
    key: str | None = None,
    quantity: int = 0,
    function: list[str] | None = None,
    billing: bool = True,
) -> UsageReport:
    return report_body(
        exchange=exchange,
        transaction_id=str(item.transaction_id),
        billing_id=item.billing_id if billing else None,
        idempotency_key=key or f"report-{uuid.uuid4().hex}",
        consumed_quantity=quantity,
        function=function,
    )


def _file(agent: FreshAgent, report: UsageReport, exchange: str = _SEAT) -> str:
    report_id = agent.exchange(exchange).report_usage(report).report_id
    assert report_id, f"the report was accepted with no report_id: {report}"
    return report_id


def _refused(agent: FreshAgent, report: UsageReport, exchange: str = _SEAT) -> CallError:
    with pytest.raises(CallError) as refused:
        agent.exchange(exchange).report_usage(report)
    return refused.value


def _assert_rejection(refusal: CallError, reason: str) -> None:
    """The refusal carries usage_report_rejection naming ``reason``."""
    block = refusal.detail.usage_report_rejection if refusal.detail else None
    USAGE_REPORT_REFUSAL_UNTYPED.check(
        block is not None, f"refused {refusal.code} with typed reasons {typed_reasons(refusal)}"
    )
    assert block is not None and block.reason.value == reason, f"rejected as {block}, want {reason}"


@obligation(Obligation.MONEY_022, negative=True)
@REUSED_REPORT_KEY_ANSWERED.mark
def test_a_replayed_report_is_answered_once_and_a_reused_key_is_refused(
    fresh_agent: FreshAgent,
) -> None:
    """The same report twice answers one report_id; the same key with another report is refused."""
    fresh_agent.fund(_SEAT)
    item = _buy(fresh_agent)
    key = f"report-{uuid.uuid4().hex}"
    first = _file(fresh_agent, _report(item, key=key))
    assert _file(fresh_agent, _report(item, key=key)) == first, "a replayed report got a new id"

    try:
        answered = fresh_agent.exchange(_SEAT).report_usage(
            _report(item, key=key, function=["search"])
        )
    except CallError as refusal:
        assert refusal.code == "already_exists", f"refused as {refusal.code!r}: {refusal}"
        assert not typed_reasons(refusal), f"a reused key carries no typed reason: {refusal}"
    else:
        REUSED_REPORT_KEY_ANSWERED.check(False, f"the different report was answered: {answered}")
    assert _file(fresh_agent, _report(item, key=key)) == first, "the stored report changed"


def _report_request(agent: FreshAgent, report: UsageReport) -> Request[Any, UsageReportResponse]:
    def client(*, signer: Any, before_sign: BeforeSign | None, http: SeamHTTP) -> Any:
        return agent.exchange_client(_SEAT, signer=signer, before_sign=before_sign, http=http)

    return Request(client=client, verb=lambda c: c.report_usage, message=report)


@obligation(Obligation.MONEY_023, negative=True)
def test_a_report_addressed_to_another_exchange_is_refused(fresh_agent: FreshAgent) -> None:
    """A report reaching exchange-d that names exchange-c: invalid_argument, no typed reason."""
    fresh_agent.fund(_SEAT)
    item = _buy(fresh_agent)
    report = _report(item)
    accepted: dict[str, str] = {}

    def recorded(answer: UsageReportResponse) -> None:
        accepted["id"] = str(answer.report_id)

    def name_exchange_c(body: dict[str, Any]) -> None:
        body["exchange"] = EXCHANGE_C_DOMAIN

    refusal = malformed(
        Mutation.IDENTITY,
        "the report names exchange-c and reaches exchange-d",
        control=_report_request(fresh_agent, report),
        alter=Alteration(before_sign=patch_body(name_exchange_c)),
        accepted=recorded,
    )
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
    assert not typed_reasons(refusal), f"a mis-addressed report carries no typed reason: {refusal}"
    assert _file(fresh_agent, report) == accepted["id"], "the refused report changed the stored one"


@obligation(Obligation.MONEY_024, negative=True)
@USAGE_REPORT_REFUSAL_UNTYPED.mark
def test_a_report_for_an_unknown_transaction_is_refused_transaction_not_found(
    fresh_agent: FreshAgent,
) -> None:
    """The agent's own report is accepted; one for an unknown id is refused, every time."""
    fresh_agent.fund(_SEAT)
    item = _buy(fresh_agent)
    _file(fresh_agent, _report(item))
    unknown = report_body(exchange=_SEAT, transaction_id=f"tx-{uuid.uuid4().hex}")
    for attempt in ("first", "again"):
        refusal = _refused(fresh_agent, unknown)
        assert refusal.code == "not_found", f"{attempt}: refused as {refusal.code!r}: {refusal}"
        _assert_rejection(refusal, _NOT_FOUND)


@contextmanager
def _short_window(seat: str) -> Iterator[None]:
    require_admin_plane()
    with reporting_policy(seat, window_seconds=_WINDOW_SECONDS) as policy:
        assert policy.window_seconds == _WINDOW_SECONDS, f"{seat} echoed {policy}"
        yield


def _await_deadline(seat: str, item: TransactionResultItem) -> None:
    """Wait until the obligation ``item`` minted is past its deadline, by the Exchange's record."""
    owed = transaction_evidence(seat, str(item.transaction_id)).obligation
    assert owed is not None, f"the purchase minted no obligation: {item}"
    passed = owed.window_end + timedelta(seconds=1)
    poll_until(
        lambda: datetime.now(UTC) > passed,
        _WAIT_SECONDS,
        failure=lambda: f"the deadline {owed.window_end} did not pass",
    )


@obligation(Obligation.MONEY_026, negative=True)
@LATE_REPORT_ACCEPTED.mark
def test_a_report_after_its_window_is_refused_window_expired(fresh_agent: FreshAgent) -> None:
    """Inside the window: accepted. After it: refused WINDOW_EXPIRED, also on replay.

    The one obligation is a late report refused with its typed reason, so the one
    check asks for exactly that: a refusal carrying usage_report_rejection
    WINDOW_EXPIRED. A late report answered, and a late report refused with no typed
    reason, both fail that one check, so the test needs one fix to pass and never
    depends on the untyped-refusal defect the other report tests record.
    """
    fresh_agent.fund(_SEAT)
    with _short_window(_SEAT):
        _file(fresh_agent, _report(_buy(fresh_agent)))
        late = _buy(fresh_agent)
        _await_deadline(_SEAT, late)
        report = _report(late)
        for attempt in ("first", "replay"):
            try:
                outcome: object = fresh_agent.exchange(_SEAT).report_usage(report)
            except CallError as refusal:
                outcome = refusal
            reason = _rejection_reason(outcome)
            LATE_REPORT_ACCEPTED.check(
                reason == _WINDOW_EXPIRED, f"{attempt}: the late report came back as {outcome!r}"
            )
            assert isinstance(outcome, CallError)
            assert outcome.code == "failed_precondition", (
                f"{attempt}: refused as {outcome.code!r}: {outcome}"
            )


def _rejection_reason(outcome: object) -> str | None:
    """The usage_report_rejection reason of a refusal; None for an answer or an untyped one."""
    if not isinstance(outcome, CallError) or outcome.detail is None:
        return None
    block = outcome.detail.usage_report_rejection
    return block.reason.value if block is not None else None


@obligation(Obligation.MONEY_027, negative=True)
@USAGE_REPORT_REFUSAL_UNTYPED.mark
def test_a_report_missing_a_required_field_is_refused(fresh_agent: FreshAgent) -> None:
    """billing_id required: a report without it is refused, the complete one then accepted."""
    require_admin_plane()
    fresh_agent.fund(_SEAT)
    with reporting_policy(_SEAT, required_fields=("billing_id",)) as policy:
        assert list(policy.required_fields or []) == ["billing_id"], f"{_SEAT} echoed {policy}"
        item = _buy(fresh_agent)
    _assert_rejection(_refused(fresh_agent, _report(item, billing=False)), _MISSING_FIELDS)
    _file(fresh_agent, _report(item))


@obligation(Obligation.MONEY_028, negative=True)
@USAGE_REPORT_REFUSAL_UNTYPED.mark
def test_a_second_report_under_a_new_key_is_refused_duplicate(fresh_agent: FreshAgent) -> None:
    """A second report for a reported transaction is refused DUPLICATE; the first one stands."""
    fresh_agent.fund(_SEAT)
    item = _buy(fresh_agent)
    first = _report(item)
    report_id = _file(fresh_agent, first)
    _assert_rejection(_refused(fresh_agent, _report(item)), _DUPLICATE)
    assert _file(fresh_agent, first) == report_id, "the first report no longer stands"


@obligation(Obligation.MONEY_025, negative=True)
@USAGE_REPORT_REFUSAL_UNTYPED.mark
def test_a_report_naming_another_transactions_billing_id_is_refused_malformed(
    fresh_agent: FreshAgent,
) -> None:
    """A report whose billing_id is the agent's other purchase's: MALFORMED, nothing recorded.

    The wrong billing_id is a real one, issued to the same agent for its second
    purchase, so the report is well formed and only its pairing is wrong. The same
    report with the transaction's own billing_id, under a new key, is then accepted:
    had the refused report been recorded, it would be refused DUPLICATE. That holds
    today and after the typed-reason fix alike, so it runs before the typed reason
    is checked.
    """
    fresh_agent.fund(_SEAT)
    item, other = _buy(fresh_agent), _buy(fresh_agent)
    assert item.billing_id and other.billing_id and item.billing_id != other.billing_id, (
        f"two paid purchases need two billing_ids: {item.billing_id!r}, {other.billing_id!r}"
    )
    mismatched = _report(item).model_copy(update={"billing_id": other.billing_id})
    refusal = _refused(fresh_agent, mismatched)
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
    _file(fresh_agent, _report(item))
    _assert_rejection(refusal, _MALFORMED)


def _push_metered(stack: StackURLs, *, rate: float, estimate: int | None) -> str:
    """Push one resource to exchange-a with a PER_UNIT term; ``estimate`` None states none."""
    term = license_term(
        model=PRICING_MODEL_PER_UNIT,
        rate=rate,
        estimated_quantity=estimate,
        unit="accesses",
        currency="USD",
    )
    entry = fresh_entry(terms=(term,))
    accepted(contributor(stack).push(HOME_SEAT, [entry]))
    return entry.uri


def _file_whatever_quantity(agent: FreshAgent, report: UsageReport) -> None:
    try:
        _file(agent, report, EXCHANGE_A_DOMAIN)
    except CallError as refused:
        REPORT_REFUSED_FOR_QUANTITY.check(
            refused.code != "invalid_argument",
            f"a report of {report.usage.consumed_quantity if report.usage else None} was "
            f"refused: {refused}",
        )
        raise


@obligation(Obligation.MONEY_029)
@REPORT_REFUSED_FOR_QUANTITY.mark
def test_a_report_is_accepted_whatever_quantity_it_reports(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """Ten accesses estimated: reports of 100 and of 1 are both accepted.

    They are filed while the seat's deprecated quantity_tolerance is set to 0,
    which an Exchange must not refuse a report on.
    """
    require_admin_plane()
    uri = _push_metered(compose_stack, rate=0.01, estimate=10)
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    with reporting_policy(EXCHANGE_A_DOMAIN, quantity_tolerance=0.0):
        above, below = (
            _buy(fresh_agent, uri, EXCHANGE_A_DOMAIN),
            _buy(fresh_agent, uri, EXCHANGE_A_DOMAIN),
        )
        _file_whatever_quantity(
            fresh_agent, _report(above, exchange=EXCHANGE_A_DOMAIN, quantity=100)
        )
        _file_whatever_quantity(fresh_agent, _report(below, exchange=EXCHANGE_A_DOMAIN, quantity=1))


@obligation(Obligation.MONEY_030)
def test_a_one_time_sale_owes_no_report(fresh_agent: FreshAgent) -> None:
    """Strength of the Titans is sold with metering NONE: the item carries no required report."""
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    offer = discover_offers(fresh_agent, TITANS)[0]
    pricing = offer.offer.get("pricing") or {}
    assert pricing.get("metering") == "PRICING_METERING_NONE", pricing
    item = only_item(fresh_agent.exchange(EXCHANGE_B_DOMAIN).execute(offer), "the one-time sale")
    assert_bought(item, "the one-time sale")
    owed = item.reporting_obligation
    assert owed is None or not owed.required, f"a one-time sale owes a report: {owed}"


@obligation(Obligation.MONEY_031)
def test_a_window_change_applies_only_to_obligations_minted_after_it(
    fresh_agent: FreshAgent,
) -> None:
    """Minted before the window is shortened: still reportable after the short window passed."""
    require_admin_plane()
    fresh_agent.fund(_SEAT)
    before = _buy(fresh_agent)
    with _short_window(_SEAT):
        after = _buy(fresh_agent)
        told = after.reporting_obligation.window if after.reporting_obligation else None
        assert told == f"{_WINDOW_SECONDS}s", f"a purchase after the change was told {told!r}"
        _await_deadline(_SEAT, after)
        _file(fresh_agent, _report(before))


@obligation(Obligation.MONEY_029)
@REPORT_REFUSED_FOR_QUANTITY.mark
def test_a_report_without_an_estimate_is_accepted_whatever_quantity_it_reports(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """No estimate anywhere: a report of 3 accesses on the purchase is accepted.

    With no estimate there is nothing to compare a quantity against, and a report
    is never refused for its quantity.
    """
    uri = _push_metered(compose_stack, rate=0.10, estimate=None)
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    item = _buy(fresh_agent, uri, EXCHANGE_A_DOMAIN)
    _file_whatever_quantity(fresh_agent, _report(item, exchange=EXCHANGE_A_DOMAIN, quantity=3))
