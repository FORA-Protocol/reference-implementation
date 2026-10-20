"""Critical extensions on the requests an Exchange or the Broker receives, and on the entries sold.

CATALOG-022, CATALOG-023, MONEY-036, MONEY-037, MONEY-039 and RELAY-030, from
"Critical extensions" in ``fora.proto`` and the extension-profiles page of the
pinned protocol module ``github.com/FORA-Protocol/protocol``:

* a request's ``ext_critical`` is for the party that receives the request. An
  Exchange that does not understand a key the list names does not act on the
  request, and answers with its RPC's UNKNOWN_CRITICAL_EXTENSION reason: a push
  is refused with a catalog_rejection, a purchase answers OK with every item
  denied in the body, and a usage report and a registration, a repeat included,
  are refused invalid_argument. A Broker that does not understand a key the
  agent's purchase lists answers OK with every item its own denial, and
  contacts no Exchange;
* ``ResourceEntry.ext_critical`` is for the agent. The Exchange carries it onto
  the offer with the ``ext`` keys it names, and never refuses a push for it.

Each negative sends the request the SDK built with one change: ``ext`` gains a key
no profile defines and ``ext_critical`` names it. Its control is the same request
with that key in ``ext`` and no ``ext_critical``, which the Exchange must accept,
because a key ``ext_critical`` does not name is safe to ignore. A purchase is
changed in the SDK's pre-signing hook, so its signature covers what is sent.
"""

from __future__ import annotations

import json
import uuid
from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import VerifiedOffer
from wire.models import (
    BrokerTransactionResponse,
    TransactionRequest,
    TransactionResultItem,
    UsageReport,
)

from ..catalog_calls import (
    HOME_EXCHANGE,
    HOME_SEAT,
    UNKNOWN_CRITICAL_EXTENSION,
    answer,
    contributor,
    entry_ext,
    fresh_entry,
    offers,
    rejection,
    sole,
    stored,
)
from ..capture_proxy import CaptureProxy
from ..defects import (
    BROKER_PURCHASE_IGNORES_EXT_CRITICAL,
    CATALOG_PUSH_IGNORES_EXT_CRITICAL,
    PURCHASE_IGNORES_EXT_CRITICAL,
    REGISTRATION_IGNORES_EXT_CRITICAL,
    REPORT_IGNORES_EXT_CRITICAL,
)
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN, EXCHANGE_D_DOMAIN
from ..fresh_agent import FreshAgent
from ..malformed import patch_body
from ..money_path import (
    LECTURE_NOTES,
    SCI_FI_DOOR,
    assert_bought,
    assert_denied,
    discover_offer,
    only_item,
    resolve_offers,
)
from ..obligation_registry import Obligation, obligation
from ..reporting import report_body
from ..seed import SeededFixture
from ..seed_accounts import has_account, registration_request
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: An ext key no profile defines, named critical: the receiver cannot understand it.
_CRITICAL: dict[str, Any] = {"ext": {"acme.must": True}, "ext_critical": ["acme.must"]}
#: The same unknown key, not named critical: safe to ignore.
_IGNORABLE: dict[str, Any] = {"ext": {"acme.must": True}}

_DENIED = "DENIAL_REASON_UNKNOWN_CRITICAL_EXTENSION"
_REPORT_REFUSED = "USAGE_REPORT_REJECTION_REASON_UNKNOWN_CRITICAL_EXTENSION"
_REGISTRATION_REFUSED = "REGISTRATION_FAILURE_REASON_UNKNOWN_CRITICAL_EXTENSION"


def _with(fields: dict[str, Any]) -> Callable[[dict[str, Any]], None]:
    """A body patch that adds a copy of ``fields`` to the request."""

    def add(body: dict[str, Any]) -> None:
        body.update(json.loads(json.dumps(fields)))

    return add


@obligation(Obligation.CATALOG_022, negative=True)
@CATALOG_PUSH_IGNORES_EXT_CRITICAL.mark
def test_a_push_naming_an_unknown_critical_key_is_refused_unknown_critical_extension(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The push's own ext_critical names acme.must: refused UNKNOWN_CRITICAL_EXTENSION.

    Control: the same caller's push with acme.must in ext and no ext_critical is
    stored. The refused push stores nothing.
    """
    catalog = contributor(compose_stack)
    control = fresh_entry()
    push = catalog.push(HOME_SEAT, [control])
    _with(_IGNORABLE)(push.message)
    outcome = answer(push)
    assert not isinstance(outcome, CallError), f"the control push was refused: {outcome}"
    assert stored(fresh_agent, HOME_EXCHANGE, control.uri), "the control push was not stored"

    entry = fresh_entry()
    push = catalog.push(HOME_SEAT, [entry])
    _with(_CRITICAL)(push.message)
    outcome = answer(push)
    # The one check is the obligation itself: refused with this typed reason. A
    # push applied and a push refused without catalog_rejection both fail it, so
    # the test does not also wait on the untyped-refusal defect.
    reason = rejection(outcome)[0] if isinstance(outcome, CallError) else None
    CATALOG_PUSH_IGNORES_EXT_CRITICAL.check(
        reason == UNKNOWN_CRITICAL_EXTENSION, f"the push came back as {outcome!r}"
    )
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the refused push was stored"


@obligation(Obligation.CATALOG_023)
def test_an_entrys_critical_ext_is_carried_onto_its_offer(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """An entry whose ext_critical names acme.must is stored, and its offer carries both.

    The Exchange does not understand acme.must either, and that never refuses the
    push: an entry's ext_critical is for the agent, not for the Exchange.
    """
    entry = fresh_entry(ext={"acme.must": True}, ext_critical=("acme.must",))
    outcome = answer(contributor(compose_stack).push(HOME_SEAT, [entry]))
    assert not isinstance(outcome, CallError), f"the push was refused: {outcome}"
    offer = sole(offers(fresh_agent, HOME_EXCHANGE, entry.uri), f"offer for {entry.uri}")
    assert offer.offer.get("ext_critical") == ["acme.must"], offer.offer
    assert entry_ext(offer).get("acme.must") is True, entry_ext(offer)


def _buy(agent: FreshAgent, fields: dict[str, Any]) -> TransactionResultItem:
    """One purchase of the lecture notes at exchange-d, with ``fields`` added to the request."""
    offer = discover_offer(agent, LECTURE_NOTES, EXCHANGE_D_DOMAIN)
    client = agent.exchange_client(EXCHANGE_D_DOMAIN, before_sign=patch_body(_with(fields)))
    return only_item(client.execute(offer), LECTURE_NOTES)


@obligation(Obligation.MONEY_036, negative=True)
@PURCHASE_IGNORES_EXT_CRITICAL.mark
def test_a_purchase_naming_an_unknown_critical_key_is_denied_and_charges_nothing(
    fresh_agent: FreshAgent,
) -> None:
    """The purchase's ext_critical names acme.must: OK, its item denied, the balance unchanged.

    Control: the same purchase with acme.must in ext and no ext_critical is bought.
    """
    fresh_agent.fund(EXCHANGE_D_DOMAIN)
    assert_bought(_buy(fresh_agent, _IGNORABLE), "the control purchase")
    before = fresh_agent.balances(EXCHANGE_D_DOMAIN)

    item = _buy(fresh_agent, _CRITICAL)
    PURCHASE_IGNORES_EXT_CRITICAL.check(
        item.denial_reason is not None and item.denial_reason.value == _DENIED,
        f"the item was answered {item}",
    )
    assert_denied(item, _DENIED, "the critical purchase")
    assert fresh_agent.balances(EXCHANGE_D_DOMAIN) == before, "a denied item moved money"


def _report(agent: FreshAgent, fields: dict[str, Any]) -> UsageReport:
    """A usage report for a fresh purchase of the lecture notes, with ``fields`` added."""
    item = _buy(agent, {})
    assert_bought(item, "the purchase reported on")
    report = report_body(
        exchange=EXCHANGE_D_DOMAIN,
        transaction_id=str(item.transaction_id),
        billing_id=item.billing_id,
        idempotency_key=f"report-{uuid.uuid4().hex}",
    )
    return report.model_copy(update=json.loads(json.dumps(fields)))


@obligation(Obligation.MONEY_037, negative=True)
@REPORT_IGNORES_EXT_CRITICAL.mark
def test_a_report_naming_an_unknown_critical_key_is_refused_unknown_critical_extension(
    fresh_agent: FreshAgent,
) -> None:
    """The report's ext_critical names acme.must: refused invalid_argument, typed.

    Control: a report with acme.must in ext and no ext_critical, for another
    purchase, is accepted.
    """
    fresh_agent.fund(EXCHANGE_D_DOMAIN)
    client = fresh_agent.exchange(EXCHANGE_D_DOMAIN)
    accepted = client.report_usage(_report(fresh_agent, _IGNORABLE))
    assert accepted.report_id, f"the control report was accepted with no report_id: {accepted}"

    critical = _report(fresh_agent, _CRITICAL)
    try:
        outcome: object = client.report_usage(critical)
    except CallError as exc:
        outcome = exc
    # The one check is the obligation itself: refused with this typed reason. A
    # report accepted and a report refused without usage_report_rejection both fail
    # it, so the test does not also wait on the untyped-refusal defect.
    block = (
        outcome.detail.usage_report_rejection
        if isinstance(outcome, CallError) and outcome.detail is not None
        else None
    )
    REPORT_IGNORES_EXT_CRITICAL.check(
        block is not None and block.reason.value == _REPORT_REFUSED,
        f"the report came back as {outcome!r}",
    )
    assert isinstance(outcome, CallError)
    assert outcome.code == "invalid_argument", f"refused as {outcome.code!r}: {outcome}"


def _broker_buy(agent: FreshAgent, fields: dict[str, Any]) -> BrokerTransactionResponse:
    """One purchase of the Sci-Fi Door through the Broker, with ``fields`` added to the request."""
    offers: list[VerifiedOffer] = resolve_offers(agent, [SCI_FI_DOOR])
    return agent.broker_client(before_sign=patch_body(_with(fields))).execute(offers)


@obligation(Obligation.RELAY_030, negative=True)
@BROKER_PURCHASE_IGNORES_EXT_CRITICAL.mark
def test_a_broker_purchase_naming_an_unknown_critical_key_is_denied_and_reaches_no_exchange(
    fresh_agent: FreshAgent, capture_proxy: CaptureProxy
) -> None:
    """The purchase's ext_critical names acme.must: OK, every item the Broker's denial, no Exchange.

    Control: the same purchase through the Broker with acme.must in ext and no
    ext_critical is bought, and its sub-request is seen arriving at exchange-c, so
    the watch reads that leg. The critical purchase sends no sub-request at all and
    moves no money.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    traffic = capture_proxy.watch(fresh_agent.host)
    assert_bought(
        only_item(_broker_buy(fresh_agent, _IGNORABLE), "control"), "the control purchase"
    )
    relayed = len(traffic.await_carrying(TransactionRequest, caller="broker"))
    before = fresh_agent.balances(EXCHANGE_C_DOMAIN)

    answer = _broker_buy(fresh_agent, _CRITICAL)
    items = answer.items or []
    BROKER_PURCHASE_IGNORES_EXT_CRITICAL.check(
        bool(items)
        and all(
            item.denial_reason is not None and item.denial_reason.value == _DENIED for item in items
        )
        and not answer.exchanges
        and not answer.totals,
        f"the purchase came back as {answer}",
    )
    sent = traffic.carrying(TransactionRequest, caller="broker")
    assert len(sent) == relayed, f"the denied purchase reached an Exchange: {sent[relayed:]}"
    assert fresh_agent.balances(EXCHANGE_C_DOMAIN) == before, "a denied purchase moved money"


def _register(agent: FreshAgent, fields: dict[str, Any]) -> object:
    """One Register at exchange-a as ``agent`` with ``fields`` added: its answer, or its refusal."""
    client = agent.exchange_client(EXCHANGE_A_DOMAIN, before_sign=patch_body(_with(fields)))
    try:
        return client.register(registration_request(EXCHANGE_A_DOMAIN))
    except CallError as refused:
        return refused


def _refused_unknown_critical(outcome: object) -> bool:
    """Whether a Register came back invalid_argument with the UNKNOWN_CRITICAL_EXTENSION reason."""
    if not isinstance(outcome, CallError) or outcome.code != "invalid_argument":
        return False
    block = outcome.detail.registration_failure if outcome.detail is not None else None
    return block is not None and block.reason.value == _REGISTRATION_REFUSED


@obligation(Obligation.MONEY_039, negative=True)
@REGISTRATION_IGNORES_EXT_CRITICAL.mark
def test_a_first_registration_naming_an_unknown_critical_key_is_refused_and_opens_no_account(
    fresh_agent: FreshAgent,
) -> None:
    """The first Register's ext_critical names acme.must: refused, typed, and no account opened.

    The refusal is sent first, because a control sent first would make it a repeat.
    The control, the same Register with acme.must in ext and no ext_critical, then
    opens the account, which shows the refusal was the extension's. The check runs
    last, so the control runs whichever way the Exchange answered.
    """
    exchange = fresh_agent.exchange(EXCHANGE_A_DOMAIN)
    outcome = _register(fresh_agent, _CRITICAL)
    opened = has_account(exchange, EXCHANGE_A_DOMAIN)
    control = _register(fresh_agent, _IGNORABLE)
    assert not isinstance(control, CallError), f"the control registration was refused: {control}"
    assert has_account(exchange, EXCHANGE_A_DOMAIN), "the control registration opened no account"

    REGISTRATION_IGNORES_EXT_CRITICAL.check(
        _refused_unknown_critical(outcome), f"the registration came back as {outcome!r}"
    )
    assert not opened, "the refused registration opened an account"


@obligation(Obligation.MONEY_039, negative=True)
@REGISTRATION_IGNORES_EXT_CRITICAL.mark
def test_a_repeat_registration_naming_an_unknown_critical_key_is_refused(
    fresh_agent: FreshAgent,
) -> None:
    """A repeat Register whose ext_critical names acme.must: refused, typed, not answered.

    Control: the first Register, with acme.must in ext and no ext_critical, opens
    the account.
    """
    control = _register(fresh_agent, _IGNORABLE)
    assert not isinstance(control, CallError), f"the control registration was refused: {control}"

    outcome = _register(fresh_agent, _CRITICAL)
    REGISTRATION_IGNORES_EXT_CRITICAL.check(
        _refused_unknown_critical(outcome), f"the repeat came back as {outcome!r}"
    )
