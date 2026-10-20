"""E2E: fora_status reports the available balance, and a paid purchase lowers it.

The agent is the bootstrap's third login (``spending_agent``), which only this
suite registers and spends as, so no other suite moves the balance measured
here. It is funded by the welcome credit exchange-a grants at its first
Register there; exchange-a sells the paid EUR resource the test buys. A stack
left up keeps its account and balance, so the test measures the DROP around one
purchase and never an absolute balance.

Two purchases are measured. The first buys a resource delivered on the Ed25519
scheme, through the Cloudflare edge. The second buys a resource delivered as a
CloudFront RSA signed URL, through the AWS edge. The protocol leaves verifying a
delivery URL to the edge that serves it, so the identity service returns and
fetches either kind as the Exchange issued it. While the agent-side delivery-URL
check was in place, the second purchase could not be made through MCP at all: a
CloudFront URL carries no Ed25519 signature, and the service refused the answer.

Money is compared as ``Decimal``, never as float: the Exchange sends exact
decimal strings, and float arithmetic would turn an exact 0.25 difference into
an approximate one.
"""

from __future__ import annotations

from decimal import Decimal
from typing import Any

import pytest

from .conftest import StackURLs
from .e2e_catalog import DEMO_PHILOSOPHY_DOMAIN, DEMO_SFX_DOMAIN
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN
from .mcp_client import McpSession, mcp_session
from .seed import SeededFixture

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: Sold by exchange-a at a flat 7.5 EUR, delivered through the Cloudflare edge.
_ARISTOTLE = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/aristotle.txt"
_ARISTOTLE_CURRENCY = "EUR"

#: Sold by exchange-c at 0.25 USD per access, delivered as a CloudFront RSA signed
#: URL through the AWS edge.
_WOODEN_DOOR = f"http://{DEMO_SFX_DOMAIN}/sfx/wooden-door-creak.json"
_WOODEN_DOOR_CURRENCY = "USD"

# Each exchange publishes its own registration schema.
_REGISTRATION_FIELDS = {
    EXCHANGE_A_DOMAIN: {
        "legal_entity": "Example Buyer",
        "address": "3 Example Street",
        "jurisdiction_country": "DE",
    },
    # exchange-c publishes a registration schema that requires these two members.
    EXCHANGE_C_DOMAIN: {
        "company_name": "Example Buyer",
        "billing_email": "billing@buyer.example",
    },
}


async def _status_entry(mcp: McpSession, exchange: str) -> dict[str, Any]:
    """The one authoritative fora_status entry for ``exchange``."""
    status = (await mcp.call("fora_status", {"exchange": exchange})).structured
    assert len(status["accounts"]) == 1, status
    entry: dict[str, Any] = status["accounts"][0]
    return entry


def _available(entry: dict[str, Any], currency: str) -> Decimal:
    """The available balance in ``currency``; fails when it is not reported."""
    amounts = [b["amount"] for b in entry["balances"] if b["currency"] == currency]
    assert len(amounts) == 1, f"want one {currency} balance, got {entry['balances']}"
    return Decimal(amounts[0])


async def _purchase_moves_balance_by_price(
    mcp: McpSession, uri: str, exchange: str, currency: str
) -> dict[str, Any]:
    """fora_status, fora_execute on the paid offer for ``uri``, fora_status again.

    Asserts the available balance in ``currency`` at ``exchange`` dropped by
    exactly the price the purchase answered, and returns the execute output.
    """
    discovered = (await mcp.call("fora_discover", {"uris": [uri]})).structured
    groups = discovered["offer_groups"]
    assert groups and groups[0]["offers"], f"no offers for {uri!r}: {discovered}"
    offer = groups[0]["offers"][0]
    assert offer.get("exchange") == exchange, f"the paid offer is sold elsewhere: {offer}"

    await mcp.call(
        "fora_register", {"exchange": exchange, "fields": _REGISTRATION_FIELDS[exchange]}
    )
    before_entry = await _status_entry(mcp, exchange)
    assert before_entry["registered"] is True, before_entry
    before = _available(before_entry, currency)

    executed = (await mcp.call("fora_execute", {"offers": [offer]})).structured
    item = executed["items"][0]
    assert not item.get("denial_reason"), f"execute denied: {item}"
    price = Decimal(item["cost"]["amount"])
    assert price > 0, f"the offer was expected to be paid: {item}"

    after = _available(await _status_entry(mcp, exchange), currency)
    assert before - after == price, (
        f"balance went from {before} to {after}; the purchase cost {price}"
    )
    return executed


async def test_paid_purchase_lowers_the_reported_balance_by_the_price(
    compose_stack: StackURLs,
    spending_bearer: str,
    seeded: SeededFixture,  # The demo catalog must be ingested before discovery.
) -> None:
    """fora_status → fora_execute on a paid offer → fora_status drops by the price."""
    async with mcp_session(compose_stack, spending_bearer) as mcp:
        await _purchase_moves_balance_by_price(
            mcp, _ARISTOTLE, EXCHANGE_A_DOMAIN, _ARISTOTLE_CURRENCY
        )


async def test_cloudfront_delivered_purchase_lowers_the_reported_balance_by_the_price(
    compose_stack: StackURLs,
    spending_bearer: str,
    seeded: SeededFixture,  # The demo catalog must be ingested before discovery.
) -> None:
    """An MCP purchase of a resource delivered as a CloudFront RSA signed URL.

    The purchase is answered, the balance drops by the price, and the URL the
    agent receives is the CloudFront signed URL the Exchange issued. The service
    then fetches it through the AWS edge, and that fetch succeeds too.
    """
    async with mcp_session(compose_stack, spending_bearer) as mcp:
        executed = await _purchase_moves_balance_by_price(
            mcp, _WOODEN_DOOR, EXCHANGE_C_DOMAIN, _WOODEN_DOOR_CURRENCY
        )

    endpoint = executed["items"][0].get("retrieval_endpoint", "")
    for name in ("Expires", "Signature", "Key-Pair-Id"):
        assert name in endpoint, f"not a CloudFront signed URL, {name} missing: {endpoint}"
    assert not executed.get("delivery_failures"), (
        f"the AWS edge refused the delivery: {executed.get('delivery_failures')}"
    )


async def test_balances_are_an_empty_list_where_nothing_is_reported(
    compose_stack: StackURLs,
    unregistered_bearer: str,
    spending_bearer: str,
    seeded: SeededFixture,  # Default tenant must exist before any status call.
) -> None:
    """An unregistered account and every local hint carry a literal balances == []."""
    async with mcp_session(compose_stack, unregistered_bearer) as mcp:
        entry = await _status_entry(mcp, EXCHANGE_A_DOMAIN)
    assert entry["registered"] is False, entry
    assert entry["balances"] == [], entry

    async with mcp_session(compose_stack, spending_bearer) as mcp:
        await mcp.call(
            "fora_register",
            {
                "exchange": EXCHANGE_A_DOMAIN,
                "fields": _REGISTRATION_FIELDS[EXCHANGE_A_DOMAIN],
            },
        )
        hints = (await mcp.call("fora_status", {})).structured
    assert hints["accounts"], hints
    for hint in hints["accounts"]:
        assert hint["source"] == "local_hint", hint
        assert hint["balances"] == [], hint
