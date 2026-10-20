"""Obligations RELAY-007 to RELAY-010 and RELAY-012: the Broker's fault paths.

``BrokerService.ExecuteTransaction``, ``UpstreamRefusal`` and ``ExchangeOutcome``
in ``fora.proto`` of the pinned module ``github.com/FORA-Protocol/protocol``:

* an item whose Exchange the Broker cannot route to, or does not approve for
  purchases, refuses the whole call with FAILED_PRECONDITION naming that
  Exchange, before anything is sent;
* once the Broker has sent its sub-requests the call returns OK. An Exchange that
  refuses its whole sub-request, or gives no answer, has that refusal ride on
  each of its items as ``refusal``: its Connect code and its ErrorDetail
  unchanged, or the code the Broker's own call failed with, and only
  ``offer_id`` among the result fields. The other Exchanges' results stand;
* the Broker never stamps a ``denial_reason`` on an item its Exchange did not
  deny;
* an OK answer without exactly one result item per item sent cannot be
  attributed, so every item of that sub-request is refused "internal", with no
  detail;
* a retry under the same idempotency key is safe, because the Broker forwards the
  key unchanged.

The reference Exchanges never misbehave, so every fault here comes from the
stack's Exchange impostor (``harness.misbehaving_exchange``). Each test sets the
impostor's answer for its own agent, buys through the Broker with the SDK's
``BrokerClient``, and reads the Broker's answer through the SDK's types. Next to
each impostor item sits an item from exchange-b, a reference Exchange, bought
from the agent's own account there, so "the other Exchanges' results stand" is
read off a real ledger through ``GetAccountStatus``.

Round-trip honesty: each purchase goes agent → Broker → each Exchange and back
through the Broker's public RPC. The impostor's arrival log is read through its
control route; it is the record of what the Broker sent, which the Broker's own
answer does not show.
"""

from __future__ import annotations

import uuid
from decimal import Decimal
from typing import Any

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import VerifiedOffer
from fora_sdk.wire import to_wire
from wire.models import (
    BrokerTransactionResponse,
    DenialReason,
    ErrorDetail,
    TransactionDenial,
    TransactionResultItem,
)

from ..exchanges import EXCHANGE_B_DOMAIN
from ..fresh_agent import FreshAgent
from ..misbehaving_exchange import (
    FAULT_URIS,
    MISBEHAVING_DOMAIN,
    UNAPPROVED_DOMAIN,
    UNLISTED_DOMAIN,
    MisbehavingExchange,
    Mode,
)
from ..money_path import (
    PAPER_SATELLITES,
    assert_bought,
    assert_denied,
    assert_moved,
    charged,
    discover_offer,
    only_item,
)
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_ACCOUNT_NOT_REGISTERED = "DENIAL_REASON_ACCOUNT_NOT_REGISTERED"

#: The typed reason an Exchange might give for refusing a whole sub-request.
_INACTIVE_ACCOUNT = ErrorDetail(
    domain=MISBEHAVING_DOMAIN,
    message="the buyer's account is not active",
    metadata={"fault": "whole sub-request"},
    transaction_denial=TransactionDenial(reason=DenialReason.DENIAL_REASON_ACCOUNT_INACTIVE),
)


def _offer_id(offer: VerifiedOffer) -> str:
    return str(offer.offer.get("offer_id"))


def _populated(value: Any) -> Any:
    """``value`` in wire form with every unset field left out, at any depth.

    Unset in the proto3 sense: absent, an empty string, list or map, or an enum's
    zero value. The SDK decodes an absent repeated field as an empty list, so two
    messages that carry the same bytes compare equal only once both are pruned.
    """
    if isinstance(value, dict):
        pruned = {key: _populated(item) for key, item in value.items()}
        return {key: item for key, item in pruned.items() if not _unset(item)}
    if isinstance(value, list):
        return [_populated(item) for item in value]
    return value


def _unset(value: Any) -> bool:
    return value in (None, "", [], {}) or str(value).endswith("_UNSPECIFIED")


def _assert_refused(
    item: TransactionResultItem,
    offer: VerifiedOffer,
    code: str,
    detail: ErrorDetail | None,
    leg: str,
) -> None:
    """The item carries the refusal of its whole sub-request, and no other result field.

    The refusal's ``party`` is the item's own signed offer.exchange, and it carries
    ``code`` and ``detail`` unchanged. ``offer_id`` is the only result field set: in
    particular there is no ``denial_reason``, because the Exchange decided
    nothing about the item.
    """
    assert item.offer_id == _offer_id(offer), f"{leg}: answered for {item.offer_id!r}"
    refusal = item.refusal
    assert refusal is not None, f"{leg}: carries no refusal: {item}"
    assert refusal.party == offer.offer.get("exchange"), (
        f"{leg}: the refusal names {refusal.party!r}, the offer {offer.offer.get('exchange')!r}"
    )
    assert refusal.code == code, f"{leg}: refused {refusal.code!r}, want {code!r}"
    got = _populated(to_wire(refusal.detail)) if refusal.detail is not None else None
    want = _populated(to_wire(detail)) if detail is not None else None
    assert got == want, f"{leg}: the refusal carries detail {got}, want {want}"
    assert item.denial_reason is None, f"{leg}: a refused item carries {item.denial_reason}"
    others = {
        name: value
        for name, value in _populated(to_wire(item)).items()
        if name not in ("offer_id", "refusal")
    }
    assert not others, f"{leg}: a refused item carries result fields {others}"


def _outcome(response: BrokerTransactionResponse, exchange: str) -> Any:
    matches = [o for o in response.exchanges or [] if o.exchange == exchange]
    assert len(matches) == 1, f"want one outcome for {exchange}: {response.exchanges}"
    return matches[0]


def _totals(response: BrokerTransactionResponse) -> list[tuple[str, Decimal]]:
    return [(str(c.currency), Decimal(str(c.amount))) for c in response.totals or []]


def _spent(item: TransactionResultItem) -> dict[str, dict[str, Decimal]]:
    """What one bought exchange-b item cost, keyed for ``assert_moved``."""
    currency, amount = charged(item)
    return {EXCHANGE_B_DOMAIN: {currency: amount}}


# ───── RELAY-007: an Exchange the Broker cannot route to or does not approve ─────


@obligation(Obligation.RELAY_007, negative=True)
@pytest.mark.parametrize(
    "identity",
    [UNLISTED_DOMAIN, UNAPPROVED_DOMAIN],
    ids=["an exchange in no registry", "an exchange registered but not approved"],
)
def test_an_item_from_an_exchange_the_broker_cannot_use_refuses_the_whole_purchase(
    fresh_agent: FreshAgent, misbehaving_exchange: MisbehavingExchange, identity: str
) -> None:
    """FAILED_PRECONDITION naming the Exchange, and nothing bought, even at exchange-b.

    The routable exchange-b item comes FIRST, so a Broker that sent sub-requests
    as it went would have bought it before reaching the unusable item. The offer
    from the unusable Exchange is validly signed: the SDK verified it against that
    identity's published key, so the only thing wrong with the purchase is where
    the item would be bought. Control: the same purchase without that item is
    bought, and exchange-b's balance drops by exactly its cost.
    """
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    routable = discover_offer(fresh_agent, PAPER_SATELLITES)
    unusable = misbehaving_exchange.offer(fresh_agent, identity, FAULT_URIS[0])
    before = fresh_agent.balances(EXCHANGE_B_DOMAIN)

    with pytest.raises(CallError) as refused:
        fresh_agent.broker.execute([routable, unusable])
    error = refused.value
    assert error.code == "failed_precondition", f"refused as {error.code!r}: {error}"
    named = (error.detail.metadata or {}).get("exchange") if error.detail is not None else None
    assert named == identity, f"the refusal names {named!r}, want {identity!r}: {error.detail}"
    assert fresh_agent.balances(EXCHANGE_B_DOMAIN) == before, (
        "a purchase the Broker refused moved money at exchange-b"
    )
    assert misbehaving_exchange.arrivals(fresh_agent) == [], (
        "the Broker sent a sub-request for a purchase it refused"
    )

    item = only_item(fresh_agent.broker.execute([routable]), "control: without the unusable item")
    assert_bought(item, "control: without the unusable item")
    assert_moved(before, fresh_agent.balances(EXCHANGE_B_DOMAIN), _spent(item))


# ───── RELAY-008: an Exchange refuses its whole sub-request ─────


@obligation(Obligation.RELAY_008, negative=True)
@pytest.mark.parametrize(
    ("code", "detail"),
    [("permission_denied", _INACTIVE_ACCOUNT), ("failed_precondition", None)],
    ids=["with a typed reason", "with no detail"],
)
def test_a_whole_sub_request_refusal_rides_on_its_items_and_the_rest_is_bought(
    fresh_agent: FreshAgent,
    misbehaving_exchange: MisbehavingExchange,
    code: str,
    detail: ErrorDetail | None,
) -> None:
    """OK; the refused item carries the Exchange's code and detail unchanged; exchange-b's is bought.

    The refused item comes first. Its outcome lists its offer and carries no
    agent_identity_hash, because the Exchange answered nothing about the agent.
    The totals hold only the exchange-b item, and exchange-b's balance drops by
    exactly its cost.
    """
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    refused_offer = misbehaving_exchange.offer(fresh_agent, MISBEHAVING_DOMAIN, FAULT_URIS[0])
    bought_offer = discover_offer(fresh_agent, PAPER_SATELLITES)
    misbehaving_exchange.answer(fresh_agent, Mode.REFUSE, code=code, detail=detail)
    before = fresh_agent.balances(EXCHANGE_B_DOMAIN)

    response = fresh_agent.broker.execute([refused_offer, bought_offer])
    refused, bought = response.items or []
    _assert_refused(refused, refused_offer, code, detail, "the refused Exchange's item")
    assert_bought(bought, "exchange-b's item beside the refusal")
    outcome = _outcome(response, MISBEHAVING_DOMAIN)
    assert list(outcome.offer_ids or []) == [_offer_id(refused_offer)], outcome
    assert not outcome.agent_identity_hash, (
        f"the refusing Exchange's outcome names an identity: {outcome.agent_identity_hash!r}"
    )
    assert _totals(response) == [charged(bought)], (
        f"totals {_totals(response)}: want exchange-b's item only"
    )
    assert_moved(before, fresh_agent.balances(EXCHANGE_B_DOMAIN), _spent(bought))
    arrivals = misbehaving_exchange.arrivals(fresh_agent)
    assert [a.offer_ids for a in arrivals] == [(_offer_id(refused_offer),)], (
        f"the refusal must come from the Exchange, after the Broker sent its item: {arrivals}"
    )


# ───── RELAY-009: an Exchange that does not answer, and the retry ─────


@obligation(Obligation.RELAY_009, negative=True)
@pytest.mark.parametrize(
    ("mode", "code"),
    [(Mode.DROP, "unavailable"), (Mode.STALL, "deadline_exceeded")],
    ids=["unreachable", "too slow to answer"],
)
def test_an_unanswered_sub_request_is_a_refusal_and_its_retry_is_bought_once(
    fresh_agent: FreshAgent, misbehaving_exchange: MisbehavingExchange, mode: Mode, code: str
) -> None:
    """The silent Exchange's item carries the transport code; the retry with the same key is safe.

    First attempt: the impostor drops the connection, or answers only after the
    Broker stopped waiting. Its item carries a refusal with the code the Broker's
    call failed with and no detail, and no denial_reason. The exchange-b item is
    bought. Then the impostor answers again, and the agent retries the same
    purchase under the same idempotency key. The impostor's item is answered,
    exchange-b answers its item from its stored result, with the same
    transaction_id, and exchange-b's balance does not move a second time. The
    impostor saw the agent's key, unchanged, on both attempts.
    """
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    silent_offer = misbehaving_exchange.offer(fresh_agent, MISBEHAVING_DOMAIN, FAULT_URIS[0])
    bought_offer = discover_offer(fresh_agent, PAPER_SATELLITES)
    misbehaving_exchange.answer(fresh_agent, mode)
    key = f"relay-silent-{uuid.uuid4().hex}"
    before = fresh_agent.balances(EXCHANGE_B_DOMAIN)

    first = fresh_agent.broker.execute([silent_offer, bought_offer], idempotency_key=key)
    unanswered, bought = first.items or []
    _assert_refused(unanswered, silent_offer, code, None, "the silent Exchange's item")
    assert_bought(bought, "exchange-b's item beside the silent Exchange")
    after_first = fresh_agent.balances(EXCHANGE_B_DOMAIN)
    assert_moved(before, after_first, _spent(bought))

    misbehaving_exchange.answer(fresh_agent, Mode.HONEST)
    retry = fresh_agent.broker.execute([silent_offer, bought_offer], idempotency_key=key)
    answered, replayed = retry.items or []
    assert_bought(answered, "the retry, once the Exchange answers")
    assert_bought(replayed, "exchange-b's item on the retry")
    assert replayed.transaction_id == bought.transaction_id, (
        f"exchange-b answered the retry with {replayed.transaction_id!r}, not its stored "
        f"{bought.transaction_id!r}"
    )
    assert fresh_agent.balances(EXCHANGE_B_DOMAIN) == after_first, (
        "the retry charged exchange-b's item a second time"
    )
    arrivals = misbehaving_exchange.arrivals(fresh_agent)
    assert [a.idempotency_key for a in arrivals] == [key, key], (
        f"the Broker must forward the agent's key unchanged on both attempts: {arrivals}"
    )


# ───── RELAY-010: never a denial the Exchange did not give ─────


@obligation(Obligation.RELAY_010, negative=True)
@pytest.mark.parametrize(
    ("mode", "code"),
    [(Mode.REFUSE, "permission_denied"), (Mode.DROP, "unavailable")],
    ids=["refused", "unanswered"],
)
def test_the_broker_stamps_no_denial_on_an_item_its_exchange_did_not_deny(
    fresh_agent: FreshAgent, misbehaving_exchange: MisbehavingExchange, mode: Mode, code: str
) -> None:
    """A refused or unanswered item has a refusal and no denial; a denied item keeps its denial.

    The agent holds no account at exchange-b, so exchange-b denies its item
    ACCOUNT_NOT_REGISTERED in the body; that denial reaches the agent unchanged.
    The impostor's item, in the same purchase, is refused or never answered: it
    carries a refusal, and no denial_reason, CONTENT_UNAVAILABLE or any other.
    """
    silent_offer = misbehaving_exchange.offer(fresh_agent, MISBEHAVING_DOMAIN, FAULT_URIS[0])
    denied_offer = discover_offer(fresh_agent, PAPER_SATELLITES)
    misbehaving_exchange.answer(fresh_agent, mode, code=code if mode is Mode.REFUSE else None)

    response = fresh_agent.broker.execute([silent_offer, denied_offer])
    undecided, denied = response.items or []
    _assert_refused(undecided, silent_offer, code, None, "the item its Exchange did not decide")
    assert_denied(
        denied, _ACCOUNT_NOT_REGISTERED, "exchange-b's item, from an agent with no account"
    )
    assert not response.totals, f"nothing was charged and the totals say {response.totals}"


# ───── RELAY-012: an answer the Broker cannot attribute ─────


@obligation(Obligation.RELAY_012, negative=True)
@pytest.mark.parametrize("mode", [Mode.FEWER, Mode.MORE], ids=["one item short", "one item extra"])
def test_an_answer_with_the_wrong_item_count_is_an_internal_refusal_on_every_item(
    fresh_agent: FreshAgent, misbehaving_exchange: MisbehavingExchange, mode: Mode
) -> None:
    """Every item sent there is refused "internal" with no detail; exchange-b's item is bought.

    Two items go to the impostor, so an answer one item short still answers one
    of them: the Broker must report neither as bought. Control first: the same
    purchase, answered with one result item per item sent, has both impostor items
    reported as answered.
    """
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    impostor_offers = [
        misbehaving_exchange.offer(fresh_agent, MISBEHAVING_DOMAIN, uri) for uri in FAULT_URIS
    ]
    bought_offer = discover_offer(fresh_agent, PAPER_SATELLITES)

    control = fresh_agent.broker.execute([*impostor_offers, bought_offer])
    for item in control.items or []:
        assert_bought(item, "control: an answer with one result item per item sent")

    misbehaving_exchange.answer(fresh_agent, mode)
    before = fresh_agent.balances(EXCHANGE_B_DOMAIN)
    response = fresh_agent.broker.execute([*impostor_offers, bought_offer])
    *unattributed, bought = response.items or []
    assert len(unattributed) == len(impostor_offers), response.items
    for item, offer in zip(unattributed, impostor_offers, strict=True):
        _assert_refused(item, offer, "internal", None, "an item of the unattributable answer")
    assert_bought(bought, "exchange-b's item beside the unattributable answer")
    assert _totals(response) == [charged(bought)], (
        f"totals {_totals(response)}: want exchange-b's item only"
    )
    assert_moved(before, fresh_agent.balances(EXCHANGE_B_DOMAIN), _spent(bought))
