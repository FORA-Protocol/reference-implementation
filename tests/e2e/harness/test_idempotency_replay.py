"""E2E: idempotency keys on ExecuteTransaction, against the real Exchanges.

The rule is stated once in the file header of ``fora.proto`` in the pinned module
``github.com/FORA-Protocol/protocol`` ("Idempotency"):

* a key is scoped to the agent, meaning its key directory, whatever the route:
  the request signature authenticates the agent on a request it signed, and its
  acceptances on a purchase the Broker re-packaged. A key another agent used is a
  different key in a different scope, never a collision and never answered from
  another scope's stored result;
* within one scope, the same key with the same request is a replay, answered
  with the stored result, and executes nothing again;
* within one scope, the same key with a different request (different items, or
  the same items in another order) is refused with ``already_exists``, carrying
  an ``ErrorDetail`` with no typed reason. Nothing is executed and the stored
  result is unchanged.

Every purchase goes through the SDK's own clients as the test's own agents
(``fresh_agent``), and every charge is read through ``GetAccountStatus``. The
resources are flat-priced (``harness.money_path``).
"""

from __future__ import annotations

import uuid

from collections.abc import Callable

import pytest
from fora_sdk.client import CallError
from wire.models import TransactionResultItem

from .defects import FOREIGN_IDEMPOTENCY_KEY_REFUSED, RETRY_ON_ANOTHER_ROUTE_REFUSED
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, EXCHANGE_C_DOMAIN
from .fresh_agent import AgentFactory, FreshAgent
from .money_path import (
    ARISTOTLE,
    PAPER_SATELLITES,
    SCI_FI_DOOR,
    THALES,
    assert_bought,
    assert_moved,
    charged,
    discover_offer,
    only_item,
    resolve_offers,
    spent_per_exchange,
    typed_reasons,
)
from .obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _key(label: str) -> str:
    return f"idem-{label}-{uuid.uuid4().hex}"


@obligation(Obligation.RELAY_013)
def test_a_broker_purchase_retried_with_its_key_is_answered_from_each_exchanges_stored_result(
    fresh_agent: FreshAgent,
) -> None:
    """Two Broker purchases across two Exchanges under ONE key: one transaction per item.

    The retry must return the ORIGINAL result from each Exchange (same
    transaction_id, same signed retrieval_endpoint, same billing_id) rather than
    re-executing, and each Exchange must charge once: each balance drops by its
    item's cost on the first purchase and stays put on the retry.
    """
    uris = [SCI_FI_DOOR, PAPER_SATELLITES]
    exchanges = [EXCHANGE_C_DOMAIN, EXCHANGE_B_DOMAIN]
    fresh_agent.fund(*exchanges)
    offers = resolve_offers(fresh_agent, uris)
    idem = _key("relay-replay")

    before = fresh_agent.balances(*exchanges)
    # No pause between the two: every SDK request signature carries a fresh
    # nonce, so the retry is a new signature over the same body, as a real
    # retrying agent's would be.
    first = fresh_agent.broker.execute(offers, idempotency_key=idem).items or []
    after_first = fresh_agent.balances(*exchanges)
    second = fresh_agent.broker.execute(offers, idempotency_key=idem).items or []
    after_second = fresh_agent.balances(*exchanges)

    for uri, one, two in zip(uris, first, second, strict=True):
        assert_bought(one, f"first purchase of {uri}")
        assert (two.transaction_id, two.retrieval_endpoint, two.billing_id) == (
            one.transaction_id,
            one.retrieval_endpoint,
            one.billing_id,
        ), f"{uri}: the retry was executed again instead of replayed: {one} then {two}"
    assert_moved(before, after_first, spent_per_exchange(uris, first))
    assert after_second == after_first, (
        f"the retry moved money: {after_first} -> {after_second}; it must charge nothing"
    )


@obligation(Obligation.MONEY_008, negative=True)
def test_a_direct_replay_is_answered_once_and_a_reused_key_with_other_items_is_refused(
    fresh_agent: FreshAgent,
) -> None:
    """The same key and items replay the purchase; the same key with another offer is refused.

    The agent buys Aristotle under key K, and buys it again under K: the second
    answer is the first transaction, charged once. It then sends Thales under the
    same K, a different request in the same scope: refused already_exists, with
    an ErrorDetail carrying no typed reason, and nothing is charged. A last replay
    of the Aristotle purchase under K still answers the first transaction, so the
    refused request left the stored result unchanged.
    """
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    exchange = fresh_agent.exchange(EXCHANGE_A_DOMAIN)
    aristotle, thales = discover_offer(fresh_agent, ARISTOTLE), discover_offer(fresh_agent, THALES)
    idem = _key("direct")

    before = fresh_agent.balances(EXCHANGE_A_DOMAIN)
    first = only_item(exchange.execute(aristotle, idempotency_key=idem), "first purchase")
    assert_bought(first, "first purchase")
    replay = only_item(exchange.execute(aristotle, idempotency_key=idem), "replay")
    assert replay.transaction_id == first.transaction_id, f"replay executed again: {replay}"
    after_replay = fresh_agent.balances(EXCHANGE_A_DOMAIN)
    currency, cost = charged(first)
    assert_moved(before, after_replay, {EXCHANGE_A_DOMAIN: {currency: cost}})

    try:
        answered = exchange.execute(thales, idempotency_key=idem)
    except CallError as refused:
        assert refused.code == "already_exists", f"refused as {refused.code!r}: {refused}"
        assert refused.detail is not None, f"the refusal carries no ErrorDetail: {refused}"
        assert not typed_reasons(refused), (
            f"the refusal carries a typed reason, and the protocol gives it none: {refused.detail}"
        )
    else:
        raise AssertionError(f"the reused key with another offer was answered: {answered}")
    assert fresh_agent.balances(EXCHANGE_A_DOMAIN) == after_replay, "the refused reuse moved money"

    last = only_item(exchange.execute(aristotle, idempotency_key=idem), "replay after the reuse")
    assert last.transaction_id == first.transaction_id, (
        f"the stored result under the key changed: {first.transaction_id} -> {last.transaction_id}"
    )


@obligation(Obligation.MONEY_009)
@FOREIGN_IDEMPOTENCY_KEY_REFUSED.mark
def test_another_agents_key_is_a_different_key_and_buys_as_its_own(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Agent B uses the key agent A already bought under: B's purchase is its own.

    A buys Aristotle directly under key K. B, funded at the same Exchange, buys
    the same offer directly under the same K, with its own acceptance. Keys are
    scoped to the agent, so B's request is not a replay of A's: B gets a transaction
    of its own and a delivery URL that is not A's, B is charged the cost, and A's
    balance does not move.
    """
    owner, other = fresh_agent_factory(), fresh_agent_factory()
    owner.fund(EXCHANGE_A_DOMAIN)
    other.fund(EXCHANGE_A_DOMAIN)
    offer = discover_offer(owner, ARISTOTLE)
    idem = _key("per-agent")

    first = only_item(owner.exchange(EXCHANGE_A_DOMAIN).execute(offer, idempotency_key=idem), "A")
    assert_bought(first, "A's purchase")
    owner_before = owner.balances(EXCHANGE_A_DOMAIN)
    other_before = other.balances(EXCHANGE_A_DOMAIN)

    try:
        answer = other.exchange(EXCHANGE_A_DOMAIN).execute(offer, idempotency_key=idem)
    except CallError as refused:
        FOREIGN_IDEMPOTENCY_KEY_REFUSED.check(
            refused.code != "permission_denied", f"B's purchase was refused: {refused}"
        )
        raise
    second = only_item(answer, "B's purchase under A's key")
    assert_bought(second, "B's purchase under A's key")
    assert second.transaction_id != first.transaction_id, "B was answered from A's stored result"
    assert second.retrieval_endpoint != first.retrieval_endpoint, "B received A's delivery URL"
    currency, cost = charged(second)
    assert_moved(
        {"A": owner_before[EXCHANGE_A_DOMAIN], "B": other_before[EXCHANGE_A_DOMAIN]},
        {
            "A": owner.balances(EXCHANGE_A_DOMAIN)[EXCHANGE_A_DOMAIN],
            "B": other.balances(EXCHANGE_A_DOMAIN)[EXCHANGE_A_DOMAIN],
        },
        {"B": {currency: cost}},
    )


@obligation(Obligation.RELAY_014)
@FOREIGN_IDEMPOTENCY_KEY_REFUSED.mark
def test_two_agents_behind_the_broker_with_one_key_each_buy_as_themselves(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Two agents relay through the one Broker under the same key: each buys as itself.

    The Broker is the verified signer of both sub-requests, but the Exchange
    scopes each key to the agent the sub-request's acceptances authenticate,
    whatever the route. A buys Sci-Fi Door through the Broker under K, then B buys
    the same offer through the Broker under K: B's item is its own transaction with
    its own delivery URL, and B is charged.
    """
    owner, other = fresh_agent_factory(), fresh_agent_factory()
    owner.fund(EXCHANGE_C_DOMAIN)
    other.fund(EXCHANGE_C_DOMAIN)
    offers = resolve_offers(owner, [SCI_FI_DOOR])
    idem = _key("relay-per-agent")

    first = only_item(owner.broker.execute(offers, idempotency_key=idem), "A through the Broker")
    assert_bought(first, "A through the Broker")
    other_before = other.balances(EXCHANGE_C_DOMAIN)
    second = only_item(other.broker.execute(offers, idempotency_key=idem), "B through the Broker")
    FOREIGN_IDEMPOTENCY_KEY_REFUSED.check(
        second.refusal is None or second.refusal.code != "permission_denied",
        f"B's relayed purchase was refused: {second.refusal}",
    )
    assert_bought(second, "B through the Broker under A's key")
    assert second.transaction_id != first.transaction_id, "B was answered from A's stored result"
    assert second.retrieval_endpoint != first.retrieval_endpoint, "B received A's delivery URL"
    currency, cost = charged(second)
    assert_moved(
        other_before,
        other.balances(EXCHANGE_C_DOMAIN),
        {EXCHANGE_C_DOMAIN: {currency: cost}},
    )


@obligation(Obligation.RELAY_014, Obligation.RELAY_009)
@RETRY_ON_ANOTHER_ROUTE_REFUSED.mark
@pytest.mark.parametrize(
    "broker_first", [True, False], ids=["broker-then-direct", "direct-then-broker"]
)
def test_a_purchase_retried_on_another_route_is_answered_from_the_stored_result(
    fresh_agent: FreshAgent, broker_first: bool
) -> None:
    """The same key and item from the same agent name one purchase, whichever route carries them.

    The agent buys the Sci-Fi Door (exchange-c) and Paper Satellites (exchange-b) in
    one Broker purchase under key K, and Paper Satellites directly at exchange-b under
    K, in either order. Both times exchange-b sees the same item under the same key
    from the same agent, so the second answer is the first: the same transaction_id,
    delivery URL and billing_id, and exchange-b charges once.

    The charge is checked first: exchange-b charging once holds today, when the retry
    is refused, and after the fix alike. The defect check is keyed on the refusal
    itself, already_exists, so a retry that was sold a second time fails the test
    outright instead of passing as the known defect.
    """
    uris = [SCI_FI_DOOR, PAPER_SATELLITES]
    fresh_agent.fund(EXCHANGE_C_DOMAIN, EXCHANGE_B_DOMAIN)
    offers = resolve_offers(fresh_agent, uris)
    idem = _key("cross-route")

    def through_broker() -> TransactionResultItem | CallError:
        items = fresh_agent.broker.execute(offers, idempotency_key=idem).items or []
        assert len(items) == len(uris), f"one purchase of {len(uris)} offers answered {items}"
        return items[uris.index(PAPER_SATELLITES)]

    def direct() -> TransactionResultItem | CallError:
        exchange = fresh_agent.exchange(EXCHANGE_B_DOMAIN)
        try:
            answer = exchange.execute(offers[uris.index(PAPER_SATELLITES)], idempotency_key=idem)
        except CallError as refused:
            return refused
        return only_item(answer, "the direct purchase")

    routes: list[Callable[[], TransactionResultItem | CallError]] = [through_broker, direct]
    first_route, second_route = routes if broker_first else routes[::-1]
    first = first_route()
    assert isinstance(first, TransactionResultItem), f"the first purchase was refused: {first}"
    assert_bought(first, "the first purchase of Paper Satellites")
    after_first = fresh_agent.balances(EXCHANGE_B_DOMAIN)

    second = second_route()
    assert fresh_agent.balances(EXCHANGE_B_DOMAIN) == after_first, (
        "the retry charged exchange-b again"
    )
    RETRY_ON_ANOTHER_ROUTE_REFUSED.check(
        not _refused_already_exists(second),
        f"the retry on the other route came back as {second!r}",
    )
    assert isinstance(second, TransactionResultItem) and second.refusal is None, (
        f"the retry on the other route was refused: {second!r}"
    )
    assert (second.transaction_id, second.retrieval_endpoint, second.billing_id) == (
        first.transaction_id,
        first.retrieval_endpoint,
        first.billing_id,
    ), f"the retry was not answered from the stored result: {second!r}"


def _refused_already_exists(answer: TransactionResultItem | CallError) -> bool:
    """Whether a retry was refused already_exists: directly, or as the Broker's per-item refusal."""
    if isinstance(answer, CallError):
        return answer.code == "already_exists"
    return answer.refusal is not None and answer.refusal.code == "already_exists"
