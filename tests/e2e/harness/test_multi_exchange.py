"""E2E: four REAL exchanges with SEPARATE catalog DBs + distinct identities (S1).

Core invariant under test
--------------------------------------------------------
The multi-exchange e2e topology gives each publisher its OWN exchange instance,
with its OWN catalog database and its OWN Ed25519 offer-signing identity:

* exchange-a (DB ``fora``)   serves the PHILOSOPHY catalog; its offers carry
  ``offer.exchange == "exchange:8081"``.
* exchange-b (DB ``fora_b``) serves the MUSIC catalog;      ``"exchange-b:8081"``.
* exchange-c (DB ``fora_c``) serves the SFX catalog;        ``"exchange-c:8081"``.
* exchange-d (DB ``fora_d``) serves the ARCHIVE catalog on a seat that refuses
  the Broker relay; the money-path suite (``test_broker_money_path``) owns it.

Two properties prove the topology:

1. **Per-exchange catalog ISOLATION + identity.** A music URI discovered DIRECTLY
   against exchange-b returns an offer stamped with exchange-b's domain; a sfx URI
   on exchange-c returns exchange-c's domain; a philosophy URI on exchange-a
   returns exchange-a's domain. Because each exchange's DiscoverResources serves
   its OWN catalog DB (a global, un-tenant-filtered ListAll — catalog.go:373), the
   cross-catalog absence is the isolation signal: a music URI is ABSENT from
   exchange-a's catalog (no offers), and a philosophy URI is ABSENT from
   exchange-b's catalog. Separate DBs are what make that absence real — a shared DB
   would leak every catalog onto every exchange.

2. **Distinct signing identity.** The exchanges' Web Bot Auth directories serve
   DISTINCT Ed25519 public keys (each minted from its own keypair by the
   rsa-keygen one-shot), so an offer signed by one exchange cannot be verified
   against another's key.

This file owns the DIRECT per-exchange isolation leg, the broker fan-out legs and
the batch lifecycle capstone. Every Broker leg goes through the SDK's own
``BrokerClient``: ``resolve`` discovers and verifies the offers, ``execute``
sends ``BrokerService.ExecuteTransaction``. Usage reports go through the SDK's
``Client``, routed by the exchange the report names. Every test that buys does
so as its own agent (``fresh_agent``), funded at the Exchanges it buys from, so
the sales and the reporting obligations it leaves belong to no other test.

Round-trip honesty: each discover leg is a full protocol round-trip on that
exchange's ``ExchangeService/DiscoverResources`` RPC. Each Broker leg is a full
round-trip on ``BrokerService``: Broker, then the Exchanges it fans out to, then
the combined answer read back through the same RPC. Where a sale was recorded is
observed through ``ExchangeService/ReportUsage`` (see
``reporting.assert_report_recorded_only_at``). No test here reads a database.
"""

from __future__ import annotations

import uuid
from contextlib import closing

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import OfferGroupResult, VerifiedOffer
from wire.models import TransactionResultItem, UsageReport

from ._multi_exchange_common import _MUSIC_URI, _PHILOSOPHY_URI, _SFX_URI
from .broker_purchase import (
    discover_group,
    one_group_per_uri,
    only_item,
    only_verified_offer,
    resolve_offer,
    resolve_request,
)
from .conftest import StackURLs
from .edge_fetch import fetch_signed
from .exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_C_DOMAIN,
    SERVICE_IDENTITIES,
    exchange_url,
)
from .fresh_agent import FreshAgent
from .money_path import discover_offer
from .protocol_documents import wba_directory_at
from .reporting import assert_report_recorded_only_at, file_report, report_body
from .sdk_client import sdk_broker_client
from .seed import DEMO_PHILOSOPHY_DOMAIN, SeededFixture

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


# A string from the REAL body of each URI's content (deploy/content/demo/...), so
# the delivery leg can assert the licensed bytes actually arrived. Status 200 alone
# does not distinguish real delivery from an edge error-stub or a wrong-route body,
# both of which answer 200 — see edge_fetch.fetch_signed.
_URI_TO_MARKER = {
    _PHILOSOPHY_URI: "Socrates",
    _MUSIC_URI: "Paper Satellites",
    _SFX_URI: "Rain on Tin Roof",
}

# Which exchange sells each URI.
_URI_OWNERS = {
    _PHILOSOPHY_URI: EXCHANGE_A_DOMAIN,
    _MUSIC_URI: EXCHANGE_B_DOMAIN,
    _SFX_URI: EXCHANGE_C_DOMAIN,
}


def _offer_exchange(offer: dict[str, object]) -> str | None:
    """Read the canonical ``Offer.exchange`` (proto3-JSON camelCase ``exchange``)."""
    value = offer.get("exchange")
    return value if isinstance(value, str) and value else None


def _discover_uris(agent: FreshAgent, exchange: str, uri: str) -> set[str]:
    """Return the URIs the Exchange ``exchange`` carries an OFFER for, for one ``uri``.

    A full DiscoverResources protocol round-trip on that exchange's RPC, through
    the agent's SDK client. A group with no offer (the URI is not in THAT
    exchange's catalog DB) yields the empty set — the absence-of-side-effect
    signal the isolation legs assert on.

    The query names the exchange it is going to. Getting that wrong is refused
    outright rather than answered with an empty catalog, so the isolation legs
    below would read a refusal as "not in this exchange's catalog" if the
    addressing were sloppy; the SDK raises on a refusal, so it cannot be read
    that way.
    """
    group = discover_group(agent.exchange(exchange), exchange, uri, f"{uri} at {exchange}")
    return {uri} if group.result.verified else set()


def _wellknown_offer_key(exchange_url: str) -> str:
    """Return the distinguishing Ed25519 public-key material from an exchange's WBA directory.

    After the WBA split identity keys live in the pure WBA directory
    (``/.well-known/http-message-signatures-directory``) as inline JWKs whose ``x``
    is the base64url raw Ed25519 public key — the keyless fora.json overlay carries
    none. The ``x`` byte string is the identity fingerprint compared across exchanges.
    """
    directory = wba_directory_at(exchange_url).message
    keys = directory.keys or []
    assert keys, f"exchange WBA directory serves no keys: {directory!r}"
    x = keys[0].x
    assert x, f"exchange WBA key has no x: {keys[0]!r}"
    return x


def test_three_exchanges_isolated_catalogs(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests each catalog into its DB
    fresh_agent: FreshAgent,
) -> None:
    """Each publisher resolves on its OWN exchange (distinct identity) and nowhere else.

    (a) music URI → exchange-b's domain; (b) sfx URI → exchange-c's domain;
    (c) philosophy URI → exchange-a's domain; (d) every exchange's Web Bot Auth
    directory serves a DISTINCT offer key; PLUS cross-catalog absence (separate DBs).
    """
    # (a) music URI resolves on exchange-b, stamped with exchange-b's identity.
    music_offer = discover_offer(fresh_agent, _MUSIC_URI, EXCHANGE_B_DOMAIN).offer
    assert _offer_exchange(music_offer) == EXCHANGE_B_DOMAIN, music_offer

    # (b) sfx URI resolves on exchange-c, stamped with exchange-c's identity.
    sfx_offer = discover_offer(fresh_agent, _SFX_URI, EXCHANGE_C_DOMAIN).offer
    assert _offer_exchange(sfx_offer) == EXCHANGE_C_DOMAIN, sfx_offer

    # (c) philosophy URI resolves on exchange-a, stamped with exchange-a's identity.
    philo_offer = discover_offer(fresh_agent, _PHILOSOPHY_URI, EXCHANGE_A_DOMAIN).offer
    assert _offer_exchange(philo_offer) == EXCHANGE_A_DOMAIN, philo_offer

    # The isolation probe must also detect a present offer, or empty results
    # could hide a response-shape mismatch.
    assert _discover_uris(fresh_agent, EXCHANGE_A_DOMAIN, _PHILOSOPHY_URI) == {_PHILOSOPHY_URI}
    assert _discover_uris(fresh_agent, EXCHANGE_B_DOMAIN, _MUSIC_URI) == {_MUSIC_URI}

    # Catalog ISOLATION (separate DBs): a music URI is ABSENT from exchange-a's
    # catalog, and a philosophy URI is ABSENT from exchange-b's catalog. A shared
    # DB would leak both catalogs onto both exchanges (global ListAll).
    assert _discover_uris(fresh_agent, EXCHANGE_A_DOMAIN, _MUSIC_URI) == set(), (
        "music URI leaked into exchange-a's catalog — DBs are not isolated"
    )
    assert _discover_uris(fresh_agent, EXCHANGE_B_DOMAIN, _PHILOSOPHY_URI) == set(), (
        "philosophy URI leaked into exchange-b's catalog — DBs are not isolated"
    )

    # (d) every exchange in the stack serves its OWN offer-signing key.
    keys = {
        domain: _wellknown_offer_key(exchange_url(compose_stack, domain))
        for domain in SERVICE_IDENTITIES.values()
    }
    assert len(set(keys.values())) == len(keys), (
        f"exchanges must serve distinct offer keys; got {keys}"
    )


def _issuers(group: OfferGroupResult) -> set[str]:
    """The exchanges that issued the verified offers in one group."""
    return {str(offer.offer.get("exchange")) for offer in group.result.verified}


def _resolve_one_offer_each(agent: FreshAgent, uris: list[str]) -> list[VerifiedOffer]:
    """One Broker resolve over ``uris`` as ``agent``; the verified winner for each, in order."""
    result = agent.broker.resolve(resolve_request(uris))
    by_uri = one_group_per_uri(result, uris, "broker resolve")
    return [only_verified_offer(by_uri[uri], f"broker resolve of {uri}") for uri in uris]


def test_broker_fans_out_batch_across_exchanges(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests each catalog into its DB
) -> None:
    """ONE broker Resolve over a 3-publisher batch returns a per-URI offer group from
    the right exchange, plus a typed-absence group for an uncatalogued URI.

    PROTOCOL round-trip: the agent sends ONE ``BrokerService/Resolve`` over
    ``uris=[philosophy, music, sfx, miss]`` through the SDK. The Broker probes each
    URI's publisher ``/.well-known/fora.json``, queries each named exchange, and
    MERGES the per-URI groups. The SDK verifies every offer against its issuing
    exchange's published key before the test sees it.

    Positive legs: each publisher's URI comes back in its OWN group whose every
    verified offer was issued by that publisher's exchange.

    Negative leg: a philosophy-domain URI absent from every authorized exchange's
    catalog comes back as a present-but-empty group carrying the typed absence
    reason NOT_IN_CATALOG, not as a dropped or silent miss.
    """
    miss_uri = (
        f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/nonexistent-{uuid.uuid4().hex}.txt"
    )
    uris = [*_URI_OWNERS, miss_uri]
    with closing(sdk_broker_client(compose_stack)) as client:
        result = client.resolve(resolve_request(uris))
    by_uri = one_group_per_uri(result, uris, "broker resolve")

    for uri, owner in _URI_OWNERS.items():
        only_verified_offer(by_uri[uri], f"broker resolve of {uri}")
        assert _issuers(by_uri[uri]) == {owner}, (
            f"{uri} came back with offers from {_issuers(by_uri[uri])}, want only {owner}"
        )

    miss = by_uri[miss_uri]
    assert not miss.result.verified and not miss.result.rejected, (
        f"the uncatalogued URI came back with offers: {miss}"
    )
    assert miss.absence_reason == "OFFER_ABSENCE_REASON_NOT_IN_CATALOG", miss


def test_broker_batch_execute_fans_out_across_three_exchanges(
    fresh_agent: FreshAgent,
) -> None:
    """ONE Broker ExecuteTransaction over offers from three exchanges answers every
    item, in the original order, each with its own delivery URL and transaction.

    PROTOCOL round-trip: the agent resolves one offer per exchange through the
    Broker and buys all three in ONE ``BrokerService.ExecuteTransaction``, through
    the SDK. The Broker groups the items by each signed ``offer.exchange``, sends
    one sub-request per exchange signed with its own key, and combines the answers
    back in request order.

    Two assertions of the retired hand-built version are dropped, because the SDK
    builds the request now and each one checked the harness rather than the
    product. The body-size check pinned the harness's own body under the Broker's
    64 KiB pre-auth bound. The ``requester.domain`` check pinned that the harness
    named the buyer's directory, which the SDK refuses to get wrong locally and
    the Broker refuses with request_auth_failure, so a purchase that succeeds
    already proves it.
    """
    order = [_MUSIC_URI, _SFX_URI, _PHILOSOPHY_URI]
    fresh_agent.fund(*(_URI_OWNERS[uri] for uri in order))
    offers = _resolve_one_offer_each(fresh_agent, order)
    buyer = fresh_agent.broker_client()
    response = buyer.execute(offers)

    items = response.items or []
    want_ids = [offer.offer.get("offer_id") for offer in offers]
    assert [item.offer_id for item in items] == want_ids, (
        f"the combined answer must carry one item per offer, in request order: "
        f"{[item.offer_id for item in items]} != {want_ids}"
    )
    for uri, item in zip(order, items, strict=True):
        assert item.refusal is None, f"{uri}: the exchange refused upstream: {item.refusal}"
        assert item.denial_reason is None, f"{uri}: item unexpectedly denied: {item}"
        assert item.retrieval_endpoint, f"{uri}: item carries no delivery URL: {item}"

    # Three DISTINCT transactions: the shared request idempotency key did not
    # collapse the items onto one.
    tx_ids = {item.transaction_id for item in items}
    assert len(tx_ids) == 3, f"expected 3 distinct transaction ids, got {tx_ids}"


def _report_for(agent: FreshAgent, issuer: str, item: TransactionResultItem) -> UsageReport:
    """``agent``'s usage report for one bought item, addressed to the exchange that sold it.

    consumed quantity is 0: the demo terms carry no reporting estimate, and the
    validator strict-rejects a positive quantity against a zero-estimate obligation.
    """
    return report_body(
        exchange=issuer,
        transaction_id=str(item.transaction_id or ""),
        billing_id=item.billing_id,
    )


def test_full_multi_exchange_batch_lifecycle(
    compose_stack: StackURLs,
    fresh_agent: FreshAgent,
) -> None:
    """HEADLINE capstone: ONE narrative drives the whole multi-exchange batch
    lifecycle over the real stack and asserts every leg end to end.

    An agent resolves an offer from each of three exchanges, buys all three in ONE
    Broker purchase, fetches the delivered content from each publisher's edge, and
    reports usage to each issuing exchange. Each exchange recorded the report, and
    no other exchange holds the transaction (topology isolation). Every leg is a
    real protocol round-trip driven the way an agent drives it, through the SDK.

    Where a sale was recorded was previously read from each exchange's database.
    It is now observed through ReportUsage, which is the public surface that
    answers it: the issuer replays the report from its stored record, and every
    other exchange refuses the same report not_found (see
    ``reporting.assert_report_recorded_only_at``). The ed25519 edges enforce
    delivery-URL binding, so each content fetch presents the buyer's key; the
    CloudFront-RSA leg carries no ``agent_id=`` and needs none.
    """
    # ---- Leg 1: RESOLVE one offer from each of the 3 exchanges. -------------
    uris = list(_URI_OWNERS)
    fresh_agent.fund(*_URI_OWNERS.values())
    offers = _resolve_one_offer_each(fresh_agent, uris)
    assert [o.offer.get("exchange") for o in offers] == list(_URI_OWNERS.values()), (
        "the 3 chosen offers must route to 3 distinct exchanges"
    )

    # ---- Leg 2: PURCHASE all three in ONE Broker call. -----------------------
    # One of the three is the sfx site, sold on the CloudFront RSA scheme; the
    # edges verify every URL below when each is fetched.
    buyer = fresh_agent.broker_client()
    response = buyer.execute(offers)
    items = response.items or []
    assert len(items) == 3, f"the combined answer must carry 3 items, got {response}"
    assert len({item.transaction_id for item in items}) == 3, (
        f"expected 3 distinct transactions, got {[item.transaction_id for item in items]}"
    )

    for uri, item in zip(uris, items, strict=True):
        issuer = _URI_OWNERS[uri]
        assert item.refusal is None and item.denial_reason is None, (
            f"item for {uri} on {issuer} was not bought: {item}"
        )

        # ---- Leg 3: CONTENT DELIVERED — fetch the signed URL from its edge. --
        assert item.retrieval_endpoint, f"item for {uri} carried no delivery URL: {item}"
        # The marker is what makes this a DELIVERY assertion: an edge error-stub or
        # a wrong-route body also answers 200, so status alone would pass for both.
        fetch_signed(
            item.retrieval_endpoint,
            compose_stack,
            expect_marker=_URI_TO_MARKER[uri],
            key_path=fresh_agent.key_path,
        )

        # ---- Leg 4: REPORT usage to the issuing exchange, which recorded it, -
        # ---- and which alone holds the transaction. --------------------------
        report = _report_for(fresh_agent, issuer, item)
        reporter = fresh_agent.agent_id
        report_id = file_report(
            compose_stack, report, agent_id=reporter, key_path=fresh_agent.key_path
        )
        assert_report_recorded_only_at(
            compose_stack, report, report_id, agent_id=reporter, key_path=fresh_agent.key_path
        )


def test_cross_agent_usage_report_refused_no_ledger_side_effect(
    compose_stack: StackURLs,
    fresh_agent: FreshAgent,
) -> None:
    """A usage report for a transaction an exchange does not hold is refused, and
    leaves nothing behind.

    Control first: a report for a sale exchange-b really made is accepted, so the
    agent, its key and the report shape are all ones exchange-b takes. Then the
    same agent reports a fabricated transaction id to exchange-b. No obligation
    holds that transaction, so the service layer refuses it not_found.

    The absent side effect is observed through the same RPC. The refused report is
    sent again, unchanged. Had the first one created anything for that
    transaction, the second would find it and be answered from it; it is refused
    not_found again, so nothing was created.
    """
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    offer = resolve_offer(fresh_agent.broker, _MUSIC_URI)
    sold = only_item(fresh_agent.broker.execute([offer]), "broker purchase of a lyric")
    control = _report_for(fresh_agent, EXCHANGE_B_DOMAIN, sold)
    assert file_report(
        compose_stack, control, agent_id=fresh_agent.agent_id, key_path=fresh_agent.key_path
    ), "control: the real sale's report was refused"

    phantom = report_body(
        exchange=EXCHANGE_B_DOMAIN,
        transaction_id=f"tx-phantom-{uuid.uuid4().hex}",
    )
    for attempt in ("first", "repeated"):
        # The refusal must be the one this test is named for: no obligation holds
        # this transaction, which the service layer answers with not_found. Wire
        # validation answers invalid_argument, a different refusal.
        with pytest.raises(CallError) as refused:
            file_report(
                compose_stack,
                phantom,
                agent_id=fresh_agent.agent_id,
                key_path=fresh_agent.key_path,
            )
        assert refused.value.code == "not_found", (
            f"the {attempt} report for a phantom transaction was refused "
            f"{refused.value.code!r}, want not_found: {refused.value}"
        )
