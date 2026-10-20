"""What the money-path suites share: priced resources, the control requests, and the reads.

Every purchase is the SDK's own ``ExecuteTransaction``, directly at an Exchange
(``Client``) or through the Broker (``BrokerClient``), signed as the test's own
agent (``fresh_agent``). Every balance is that agent's ``GetAccountStatus`` at
the Exchange that holds it. Nothing here builds a request body or reads a
datastore.

The resources are the e2e catalog's flat-priced and free records. A metered
record is avoided on purpose: what a metered purchase is charged depends on its
estimate and its usage report, and a money-path test proves one thing about the
money path, not metered settlement.
"""

from __future__ import annotations

from collections.abc import Callable, Mapping
from decimal import Decimal
from typing import Any

from fora_sdk.client import CallError
from fora_sdk.core import VerifiedOffer
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import BrokerClient, Client
from wire.models import (
    BrokerTransactionResponse,
    TransactionResponse,
    TransactionResultItem,
)

from .broker_purchase import (
    one_group_per_uri,
    only_verified_offer,
    resolve_request,
    resource_query,
)
from .e2e_catalog import (
    DEMO_ARCHIVE_DOMAIN,
    DEMO_MUSIC_DOMAIN,
    DEMO_PHILOSOPHY_DOMAIN,
    DEMO_SFX_DOMAIN,
)
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, EXCHANGE_C_DOMAIN, EXCHANGE_D_DOMAIN
from .fresh_agent import FreshAgent
from .ledger import BILLED_CURRENCIES, Balances
from .malformed import Request, SeamHTTP
from .sdk_client import BeforeSign

# Flat-priced and free resources, at least one per Exchange. A cost is always read
# from the answer; the comment says what the catalog prices it at.
ARISTOTLE = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/aristotle.txt"  # a, 7.5 EUR
THALES = (
    f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/thales-of-miletus.txt"  # a, 9.99 EUR
)
HERACLITUS = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/heraclitus.txt"  # a, FREE
PAPER_SATELLITES = f"http://{DEMO_MUSIC_DOMAIN}/lyrics/paper-satellites.txt"  # b, 3.5 USD
TITANS = f"http://{DEMO_MUSIC_DOMAIN}/tracks/strength-of-the-titans.mp3"  # b, 29 EUR, metering NONE
SCI_FI_DOOR = f"http://{DEMO_SFX_DOMAIN}/sfx/sci-fi-door-whoosh.json"  # c, 12 USD
RAIN = f"http://{DEMO_SFX_DOMAIN}/sfx/rain-on-tin-roof.json"  # c, FREE USD
LECTURE_NOTES = f"http://{DEMO_ARCHIVE_DOMAIN}/archive/lyceum-lecture-notes.txt"  # d, 0.5 USD

#: The Exchange that sells each resource above.
ISSUER = {
    ARISTOTLE: EXCHANGE_A_DOMAIN,
    THALES: EXCHANGE_A_DOMAIN,
    HERACLITUS: EXCHANGE_A_DOMAIN,
    PAPER_SATELLITES: EXCHANGE_B_DOMAIN,
    TITANS: EXCHANGE_B_DOMAIN,
    SCI_FI_DOOR: EXCHANGE_C_DOMAIN,
    RAIN: EXCHANGE_C_DOMAIN,
    LECTURE_NOTES: EXCHANGE_D_DOMAIN,
}

SIGNATURE_INVALID = "DENIAL_REASON_SIGNATURE_INVALID"
REQUEST_SIGNATURE_INVALID = "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID"


def discover_offers(
    agent: FreshAgent, uri: str, exchange: str | None = None
) -> list[VerifiedOffer]:
    """Every verified offer ``exchange`` makes ``agent`` for ``uri``, in its order.

    ``exchange`` defaults to the Exchange :data:`ISSUER` names for ``uri``.
    """
    exchange = exchange or ISSUER[uri]
    found = agent.exchange(exchange).discover(resource_query(exchange, [uri]))
    group = one_group_per_uri(found, [uri], f"discovery of {uri} at {exchange}")[uri]
    rejected = [(r.offer.get("offer_id"), r.reason) for r in group.result.rejected]
    assert not rejected, f"offers for {uri} failed verification: {rejected}"
    return list(group.result.verified)


def discover_offer(agent: FreshAgent, uri: str, exchange: str | None = None) -> VerifiedOffer:
    """The one verified offer ``exchange`` (by default its issuer) makes ``agent`` for ``uri``."""
    offers = discover_offers(agent, uri, exchange)
    assert len(offers) == 1, f"{uri}: want one offer, got {len(offers)}"
    return offers[0]


def resolve_offers(agent: FreshAgent, uris: list[str]) -> list[VerifiedOffer]:
    """One Broker Resolve as ``agent``; the verified winner per URI, each from its issuer."""
    result = agent.broker.resolve(resolve_request(list(dict.fromkeys(uris))))
    by_uri = one_group_per_uri(result, set(uris), "broker resolve")
    offers = [only_verified_offer(by_uri[uri], f"broker resolve of {uri}") for uri in uris]
    for uri, offer in zip(uris, offers, strict=True):
        assert offer.offer.get("exchange") == ISSUER[uri], (
            f"{uri} was offered by {offer.offer.get('exchange')!r}, want {ISSUER[uri]!r}"
        )
    return offers


def pinned_key(verb: Callable[..., Any], idempotency_key: str | None) -> Callable[[Any], Any]:
    """``verb`` with its idempotency key pinned, as a one-argument verb."""
    if idempotency_key is None:
        return verb
    return lambda message: verb(message, idempotency_key=idempotency_key)


def direct_purchase(
    agent: FreshAgent,
    exchange: str,
    offers: VerifiedOffer | list[VerifiedOffer],
    *,
    idempotency_key: str | None = None,
) -> Request[Client, TransactionResponse]:
    """The control purchase of ``offers`` directly at ``exchange`` as ``agent``."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> Client:
        return agent.exchange_client(exchange, signer=signer, before_sign=before_sign, http=http)

    return Request(
        client=client,
        verb=lambda c: pinned_key(c.execute, idempotency_key),
        message=offers,
    )


def broker_purchase(
    agent: FreshAgent, offers: list[VerifiedOffer], *, idempotency_key: str | None = None
) -> Request[BrokerClient, BrokerTransactionResponse]:
    """The control purchase of ``offers`` through the Broker as ``agent``."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> BrokerClient:
        return agent.broker_client(signer=signer, before_sign=before_sign, http=http)

    return Request(
        client=client,
        verb=lambda c: pinned_key(c.execute, idempotency_key),
        message=offers,
    )


def charged(item: TransactionResultItem) -> tuple[str, Decimal]:
    """(currency, amount) one bought item was charged."""
    assert item.cost is not None, f"a bought item carries no cost: {item}"
    return str(item.cost.currency), Decimal(str(item.cost.amount))


def assert_bought(item: TransactionResultItem, leg: str) -> None:
    """The item was bought: no refusal, no denial, a transaction and a delivery URL."""
    assert item.refusal is None, f"{leg}: refused upstream: {item.refusal}"
    assert item.denial_reason is None, f"{leg}: denied: {item.denial_reason}"
    assert item.transaction_id, f"{leg}: no transaction_id: {item}"
    assert item.retrieval_endpoint, f"{leg}: no delivery URL: {item}"


def assert_not_sold(item: TransactionResultItem, leg: str) -> None:
    """Nothing was sold on the item: no transaction, billing record, cost or delivery URL.

    Unset in the proto3 sense: an empty string for the scalar fields, absent for
    the rest.
    """
    for name in ("transaction_id", "billing_id", "cost", "retrieval_endpoint", "expires_at"):
        assert not getattr(item, name), f"{leg}: an unsold item carries {name}: {item}"


def assert_denied(item: TransactionResultItem, reason: str, leg: str) -> None:
    """The Exchange denied the item in the body with ``reason``, and sold nothing on it."""
    assert item.refusal is None, f"{leg}: refused instead of denied: {item.refusal}"
    assert item.denial_reason is not None, f"{leg}: not denied: {item}"
    assert item.denial_reason.value == reason, (
        f"{leg}: denied {item.denial_reason.value}, want {reason}"
    )
    assert_not_sold(item, leg)


def only_item(
    response: TransactionResponse | BrokerTransactionResponse, leg: str
) -> TransactionResultItem:
    """The one item a one-offer purchase answers."""
    items = response.items or []
    assert len(items) == 1, f"{leg}: one offer bought, {len(items)} items answered: {response}"
    return items[0]


def assert_moved(
    before: Mapping[str, Balances],
    after: Mapping[str, Balances],
    spent: Mapping[str, Mapping[str, Decimal]],
) -> None:
    """Each balance moved by exactly what was spent there, in that currency, and no more."""
    for exchange, was in before.items():
        for currency in BILLED_CURRENCIES:
            cost = spent.get(exchange, {}).get(currency, Decimal(0))
            assert after[exchange][currency] == was[currency] - cost, (
                f"{currency} at {exchange} went from {was[currency]} to "
                f"{after[exchange][currency]}; the items bought there cost {cost}"
            )


def buy_at_issuer(agent: FreshAgent, uri: str) -> TransactionResultItem:
    """Fund ``agent`` at the Exchange that sells ``uri``, buy it there directly, return the item.

    For a test about what a purchase hands back (its delivery URL, its expiry)
    rather than about the purchase itself. The item must be bought.
    """
    issuer = ISSUER[uri]
    agent.fund(issuer)
    item = only_item(agent.exchange(issuer).execute(discover_offer(agent, uri)), uri)
    assert_bought(item, f"the purchase of {uri} at {issuer}")
    return item


def spent_per_exchange(
    uris: list[str], items: list[TransactionResultItem]
) -> dict[str, dict[str, Decimal]]:
    """What the bought items cost, per Exchange and currency."""
    spent: dict[str, dict[str, Decimal]] = {}
    for uri, item in zip(uris, items, strict=True):
        if item.refusal is not None or item.denial_reason is not None:
            continue
        currency, amount = charged(item)
        per_currency = spent.setdefault(ISSUER[uri], {})
        per_currency[currency] = per_currency.get(currency, Decimal(0)) + amount
    return spent


#: The members of ErrorDetail's ``reason`` oneof, each a typed reason block.
_REASON_BLOCKS = (
    "catalog_rejection",
    "dispute_failure",
    "domain_verification_failure",
    "registration_failure",
    "request_auth_failure",
    "retrieval_auth_failure",
    "transaction_denial",
    "usage_report_rejection",
)


def typed_reasons(error: CallError) -> list[str]:
    """The typed reason blocks a refusal's ErrorDetail carries; empty when it names none."""
    detail = error.detail
    if detail is None:
        return []
    return [name for name in _REASON_BLOCKS if getattr(detail, name) is not None]


def assert_auth_refusal(error: CallError) -> None:
    """The refusal of a request whose signature does not verify for the requester it names."""
    assert error.code == "unauthenticated", f"refused as {error.code!r}: {error}"
    block = error.detail.request_auth_failure if error.detail is not None else None
    assert block is not None, f"the refusal carries no request_auth_failure reason: {error}"
    assert block.reason.value == REQUEST_SIGNATURE_INVALID, block
