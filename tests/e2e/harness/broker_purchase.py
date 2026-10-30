"""Discovering and buying through the Broker with the SDK's own client.

Every purchase the harness makes through the Broker is made with
``fora_sdk``'s ``BrokerClient``, built by ``sdk_client.sdk_broker_client``:
``resolve`` discovers offers and verifies each one against its issuing
Exchange's published key, and ``execute`` sends
``BrokerService.ExecuteTransaction`` with the agent's acceptances, signed by
the SDK. Nothing here signs anything or reads a raw response; the answers are
the SDK's typed results. The messages built here are generated models, a
DiscoveryRequest (``resolve_request``) and a ResourceQuery (``resource_query``),
carrying the fields a suite chooses. Neither names a requester (see
:func:`unstamped`): the SDK stamps the sending client's, then checks the whole
message against the generated model and the published strict schema before it
signs.

What this module adds is the small amount of reading every suite repeats: one
offer group per requested URI, the one verified offer a test buys, and the one
result item a single-offer purchase answers with. ``discover_group`` asks an
Exchange directly, through the SDK's Exchange client, for the suites that read
one URI's offers without the Broker in between. Each of those asserts the
shape it reads, so a test that gets something else fails on the sentence that
says what arrived instead of an ``IndexError``.
"""

from __future__ import annotations

from collections.abc import Iterable, Sequence
from typing import Any, TypeVar

from fora_sdk.core import DiscoveryResult, OfferGroupResult, VerifiedOffer
from fora_sdk.sync import BrokerClient, Client
from pydantic import BaseModel
from wire.models import (
    AcceptableRestriction,
    BrokerTransactionResponse,
    Cost,
    DiscoveryRequest,
    RequestConstraints,
    Requester,
    ResourceQuery,
    RestrictionKind,
    TransactionResultItem,
)

from .fresh_agent import FreshAgent
from .model_edits import require_declared

#: The intended use every demo term permits. Sent as the FUNCTION axis of
#: ``acceptable_restrictions``, which is how a DiscoveryRequest states a use.
AI_INPUT = "ai-input"

M = TypeVar("M", bound=BaseModel)


def unstamped(model: type[M], **fields: Any) -> M:
    """A ``model`` message with ``fields`` and no requester: the SDK stamps the sending client's.

    The protocol requires a requester on a query, so a validated model cannot leave
    it out, and the type checker refuses a ``model_construct`` call that leaves out
    a required field; this builds the message with ``model_construct`` all the same.
    The SDK then stamps ``ver`` and the configured requester, so the requester always
    names the identity the client signs as, and validates and strict-checks the
    whole message before it signs. A structured member is passed as its generated
    model. ``model_construct`` would drop a field name the model does not declare,
    and the type checker does not report one, so a misspelled name is refused here
    (:func:`harness.model_edits.require_declared`).
    """
    require_declared(model, fields)
    return model.model_construct(**fields)


def resolve_request(
    uris: Sequence[str] | None = None,
    *,
    intended_use: str | None = None,
    period_budget: tuple[str, str] | None = None,
    **fields: Any,
) -> DiscoveryRequest:
    """The DiscoveryRequest a suite sends to the Broker; the SDK stamps ``ver`` and the requester.

    ``uris`` left out sends none, for a search by ``query`` or ``search_filters``.
    ``period_budget`` is ``(amount, currency)``, the amount a decimal string the
    way ``Cost.amount`` travels. ``fields`` sets any other member, for a test that
    states one.
    """
    if intended_use is not None:
        fields["acceptable_restrictions"] = [
            AcceptableRestriction(
                axis=RestrictionKind.RESTRICTION_KIND_FUNCTION, values=[intended_use]
            )
        ]
    if period_budget is not None:
        amount, currency = period_budget
        fields["constraints"] = RequestConstraints(
            period_budget=Cost(amount=amount, currency=currency)
        )
    if uris is not None:
        fields["uris"] = list(uris)
    return unstamped(DiscoveryRequest, **fields)


def resource_query(exchange: str, uris: Sequence[str], **fields: Any) -> ResourceQuery:
    """The ResourceQuery a suite sends to ``exchange``; the SDK stamps ``ver`` and the requester.

    ``fields`` sets any other member, or replaces one, for a test that states one.
    """
    return unstamped(ResourceQuery, exchange=exchange, uris=list(uris), **fields)


def one_group_per_uri(
    result: DiscoveryResult, wanted: Iterable[str], leg: str
) -> dict[str, OfferGroupResult]:
    """Index a discovery result by URI, refusing anything but one group each."""
    by_uri: dict[str, OfferGroupResult] = {}
    for group in result.groups:
        assert group.uri not in by_uri, f"{leg} returned two groups for {group.uri}"
        by_uri[group.uri] = group
    wanted = set(wanted)
    assert set(by_uri) == wanted, (
        f"{leg} answered for {sorted(by_uri)}, asked about {sorted(wanted)}; "
        f"absence_reason={result.absence_reason}"
    )
    return by_uri


def only_verified_offer(group: OfferGroupResult, leg: str) -> VerifiedOffer:
    """The ranked winner of one group, refusing a group with any rejected offer.

    Reports what was rejected and why, because a fail-closed rejection is the
    shape a mis-wired key resolver takes and it is otherwise indistinguishable
    from an Exchange that offered nothing. An EMPTY rejected list is asserted as
    well: a result carrying one verified offer beside a rejected one is a key
    resolver that reached some Exchange and not another, and reading index 0 and
    moving on would call that a pass.
    """
    # A verified or rejected offer wraps the offer as the canonical snake_case
    # proto-JSON object it arrived as, not a parsed model, so it is read by key.
    rejected = [(r.offer.get("offer_id"), r.reason) for r in group.result.rejected]
    assert group.result.verified, (
        f"{leg} verified no offers for {group.uri}: rejected={rejected} "
        f"absence_reason={group.absence_reason}. A rejection here usually means the "
        f"offer-key resolver never reached the Exchange's Web Bot Auth directory."
    )
    assert not rejected, (
        f"{leg} rejected {rejected} for {group.uri} alongside what it verified. "
        f"Every offer here is signed by an Exchange this verifier holds a key for, so a "
        f"rejection means one directory was not read."
    )
    return group.result.verified[0]


def discover_group(
    client: Client,
    exchange: str,
    uri: str,
    leg: str,
    *,
    requester: dict[str, object] | None = None,
) -> OfferGroupResult:
    """Discover ``uri`` directly at the Exchange ``exchange`` and return its one offer group.

    ``requester`` replaces the one the client fills in, for a test that states a
    requester of its own. An offer that failed verification fails the call here,
    with its reason, so the group's verified offers are every offer the Exchange
    made.
    """
    stated = {} if requester is None else {"requester": Requester.model_validate(requester)}
    query = resource_query(exchange, [uri], **stated)
    group = one_group_per_uri(client.discover(query), [uri], leg)[uri]
    rejected = [(r.offer.get("offer_id"), r.reason) for r in group.result.rejected]
    assert not rejected, f"{leg}: offers for {uri} failed verification: {rejected}"
    return group


def resolve_offer(
    client: BrokerClient, uri: str, *, intended_use: str | None = AI_INPUT
) -> VerifiedOffer:
    """Resolve one URI at the Broker and return its ranked, verified winner."""
    result = client.resolve(resolve_request([uri], intended_use=intended_use))
    group = one_group_per_uri(result, [uri], f"broker resolve of {uri}")[uri]
    return only_verified_offer(group, f"broker resolve of {uri}")


def only_item(response: BrokerTransactionResponse, leg: str) -> TransactionResultItem:
    """The single result item of a one-offer purchase, which must not be refused upstream.

    A per-item denial is the Exchange's own answer and is returned for the caller
    to assert on; an upstream refusal means the Exchange never answered the item
    at all, which no single-offer caller here expects.
    """
    items = response.items or []
    assert len(items) == 1, f"{leg}: one offer bought, {len(items)} items answered: {response}"
    item = items[0]
    assert item.refusal is None, (
        f"{leg}: the Exchange refused the purchase upstream: {item.refusal}"
    )
    return item


def resolve_and_buy(agent: FreshAgent, uri: str) -> TransactionResultItem:
    """Resolve ``uri`` at the Broker as ``agent`` and buy the winner through the Broker.

    Both calls go through the agent's own Broker client, so the Broker resolves the
    buyer from the signature on each, and the Exchange binds the delivery URL to the
    key that signed the agent's acceptance. A per-item denial is returned for the
    caller to assert on.
    """
    offer = resolve_offer(agent.broker, uri)
    return only_item(agent.broker.execute([offer]), f"broker purchase of {uri}")
