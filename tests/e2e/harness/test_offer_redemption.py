"""E2E: stateless offer redemption — reflect the signed Offer through the public RPC.

Mirrors, over the wire and cross-process, the Go integration negatives in
``src/exchange/internal/transport/execute_offer_redemption_integration_test.go``
(``PresentedOfferAccepted`` / ``TamperedOfferPriceRejected`` plus the tampered-
expiry leg).

Core invariant
-------------------------------------------------------
An agent redeems an offer end-to-end by RE-PRESENTING the exact Exchange-signed
Offer it received at discovery. The request carries the FULL signed ``offer`` on
its item, and the Exchange honours it ONLY while the signature still covers the
presented bytes. Any post-discovery mutation — price, expiry — is observable
through the public ``ExchangeService/ExecuteTransaction`` RPC as a denial with NO
delivery URL.

Why this surface (decisive)
---------------------------
Tampering is only expressible where the agent holds the offer object. The agent
gets it from ``DiscoverResources`` and reflects it in ExecuteTransaction; the
Broker ``Resolve`` path reflects the winning offer INTERNALLY (``resolve.go``
``tx.Offer = winner.Offer``) and the agent never touches it on the wire, so a
tampered offer CANNOT be expressed through Resolve. Therefore every leg drives
``ExchangeService/ExecuteTransaction`` agent-direct, through the SDK's own client
as a fresh agent funded at exchange-c.

How the tamper is sent
----------------------
Each negative is a declared mutation (``malformed_answer``): the SDK builds,
stamps and validates the purchase of the genuine verified offer, ``patch_body``
edits one field of the reflected offer, and the SDK signs the patched bytes. The
request signature and the agent acceptance are therefore valid over what is
sent, and the edited offer field is the only thing wrong with it. The same
purchase, unaltered, is sent first as the control and must be bought.

Round-trip honesty
------------------
Each leg is a full discover→reflect→execute PROTOCOL round-trip on the Exchange
RPC. The positive additionally fetches the signed URL through the edge (delivery
round-trip). Negatives assert through the ANSWER ONLY — the per-item
``denial_reason`` and the absence of ``retrieval_endpoint`` — because there is NO
public transaction-read RPC by design and the e2e runner cannot reach the
Exchange's repo in a separate process (Testing Doctrine §9): the strongest
side-effect-absence observable through the public surface is the absent signed
URL on the denied item.

Where the rejection lands (verified against exchange_batch.go / denial.go): on
the items[] batch path the Exchange CLASSIFIES a tampered offer as a PER-ITEM
denial (``verifyPresentedOffer`` → ``ErrOfferSignatureInvalid`` →
``KindSignatureInvalid`` → ``denialReasonByKind``) rather than aborting the
envelope with a Connect error. So a 1-item tampered request is answered with
``items[0].denial_reason == DENIAL_REASON_SIGNATURE_INVALID`` and no
``items[0].retrieval_endpoint``, which is why the negatives read an answer
rather than a refusal.

Expiry scope (SCOPING DECISION)
-------------------------------------------
A genuinely-signed-but-stale offer (→ ``DENIAL_REASON_OFFER_EXPIRED``) is NOT
e2e-producible: the harness holds no Exchange offer-signing key and the
production Exchange clock has no override (hard-coded 5m OfferTTL). That case is
covered by the Go integration ``TestExecuteTransaction_ExpiredOfferRejected``.
Here, expiry is exercised via a TAMPERED ``expires_at`` (set to a past instant),
which the discovered offer's signature does NOT cover → denied as
``SIGNATURE_INVALID`` (the signature check fires independently of the freshness
check).
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.core import VerifiedOffer
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import Client
from wire.models import DenialReason, TransactionResponse, TransactionResultItem

from .broker_purchase import discover_group, only_verified_offer
from .conftest import StackURLs
from .edge_fetch import fetch_signed
from .exchanges import EXCHANGE_C_DOMAIN
from .fresh_agent import FreshAgent
from .malformed import Alteration, Mutation, Request, SeamHTTP, malformed_answer, patch_body
from .sdk_client import BeforeSign
from .seed import DEMO_SFX_DOMAIN, SeededFixture

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

# The priced PER_UNIT demo resource (wooden-door-creak, 0.25 USD) — the only
# PRICED demo resource, so a price tamper is meaningful AND the happy path
# delivers a real fetch + an observable cost. Sold by exchange-c on the
# CloudFront RSA scheme.
_RESOURCE_URI = f"http://{DEMO_SFX_DOMAIN}/sfx/wooden-door-creak.json"


def _verified_offer(agent: FreshAgent) -> VerifiedOffer:
    """Discover the priced resource at exchange-c and return its verified offer."""
    leg = f"discovery of {_RESOURCE_URI} at exchange-c"
    group = discover_group(agent.exchange(EXCHANGE_C_DOMAIN), EXCHANGE_C_DOMAIN, _RESOURCE_URI, leg)
    return only_verified_offer(group, leg)


def _only_item(response: TransactionResponse, what: str) -> TransactionResultItem:
    items = response.items or []
    assert len(items) == 1, f"{what}: one offer bought, {len(items)} items answered: {response}"
    return items[0]


def _redeem(agent: FreshAgent, edit: Callable[[dict[str, Any]], None], what: str) -> None:
    """Buy the offer unaltered (the control), then with ``edit`` applied to the reflected offer.

    The altered purchase must be answered with a per-item SIGNATURE_INVALID denial
    and no delivery URL: the offer signature does not cover the edited bytes.
    """
    agent.fund(EXCHANGE_C_DOMAIN)
    offer = _verified_offer(agent)

    def exchange_c(
        *,
        signer: SigningTransport | None,
        before_sign: BeforeSign | None,
        http: SeamHTTP,
    ) -> Client:
        return agent.exchange_client(
            EXCHANGE_C_DOMAIN,
            signer=signer,
            before_sign=before_sign,
            http=http,
        )

    def bought(response: TransactionResponse) -> None:
        item = _only_item(response, "control")
        assert item.denial_reason is None, f"control: the genuine offer was denied: {item}"
        assert item.retrieval_endpoint, f"control: the genuine offer issued no URL: {item}"

    def edit_offer(body: dict[str, Any]) -> None:
        edit(body["items"][0]["offer"])

    answer = malformed_answer(
        Mutation.SIGNATURE,
        f"{what} differs from the bytes the Exchange signed",
        control=Request(client=exchange_c, verb=lambda c: c.execute, message=offer),
        alter=Alteration(before_sign=patch_body(edit_offer)),
        accepted=bought,
    )
    item = _only_item(answer, f"tampered {what}")
    assert item.denial_reason is DenialReason.DENIAL_REASON_SIGNATURE_INVALID, (
        f"a tampered {what} must be denied SIGNATURE_INVALID (the signature does not "
        f"cover the presented bytes); got {item.denial_reason!r}: {item}"
    )
    assert not item.retrieval_endpoint, f"a denied tamper issued a delivery URL: {item}"


def test_reflected_offer_redeems_and_delivers_content(
    compose_stack: StackURLs,
    seeded: SeededFixture,
    fresh_agent: FreshAgent,
) -> None:
    """HAPPY: discover → reflect the signed offer unchanged → execute → fetch bytes.

    Full discover→reflect→execute protocol round-trip on the Exchange RPC, then a
    delivery round-trip fetching the signed URL through the edge. Asserts a
    non-empty transaction_id, a non-empty retrieval_endpoint, the signed unit cost,
    and that the edge delivers real bytes.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    buyer = fresh_agent.exchange_client(EXCHANGE_C_DOMAIN)
    item = _only_item(buyer.execute(_verified_offer(fresh_agent)), "purchase")
    assert item.denial_reason is None, f"reflecting the genuine signed offer was denied: {item}"
    assert item.transaction_id, f"accepted transaction carried no transaction_id: {item}"
    assert item.retrieval_endpoint, f"accepted transaction returned no retrieval_endpoint: {item}"

    # The charged unit cost equals the SIGNED rate (the agent pays what it signed).
    # Money is a decimal string on the wire, so compare the numeric value.
    assert item.cost is not None, item
    assert float(item.cost.unit_cost or "nan") == seeded.priced_per_unit_rate, item.cost
    assert item.cost.currency == "USD", item.cost

    # Delivery round-trip: the signed URL fetches real bytes through the edge.
    content = fetch_signed(item.retrieval_endpoint, compose_stack, key_path=fresh_agent.key_path)
    assert content.body.strip(), content.body[:256]


def test_tampered_price_on_reflected_offer_is_rejected(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests the demo catalog
    fresh_agent: FreshAgent,
) -> None:
    """TAMPERED PRICE: edit offer.pricing.rate after discovery → denied, no URL."""

    def lower_rate(offer: dict[str, Any]) -> None:
        pricing = offer.get("pricing")
        assert isinstance(pricing, dict) and "rate" in pricing, (
            f"discovered offer must carry pricing.rate to tamper; got {offer!r}"
        )
        # A value the Exchange never signed — strictly different from the signed rate.
        pricing["rate"] = "0.01"

    _redeem(fresh_agent, lower_rate, "offer.pricing.rate")


def test_tampered_expires_at_on_reflected_offer_is_rejected(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests the demo catalog
    fresh_agent: FreshAgent,
) -> None:
    """TAMPERED EXPIRY: edit offer.expires_at after discovery → denied, no URL.

    A genuinely-signed-but-stale offer (→ OFFER_EXPIRED) is NOT e2e-producible
    (no exchange offer key in the harness, no clock override); that case is
    covered by the Go integration ExpiredOfferRejected test. See module docstring.
    """

    def expire_in_the_past(offer: dict[str, Any]) -> None:
        assert offer.get("expires_at"), (
            f"discovered offer must carry expires_at to tamper; got {offer!r}"
        )
        # An instant the Exchange never signed (and which is also strictly past).
        offer["expires_at"] = "2000-01-01T00:00:00Z"

    _redeem(fresh_agent, expire_in_the_past, "offer.expires_at")
