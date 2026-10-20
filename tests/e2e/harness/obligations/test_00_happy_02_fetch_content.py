"""Obligation PAID-003: fetching the delivered URL returns the owner's content.

Driven against the demo ``epicurus`` resource (PER_UNIT USD) as the USD demo
buyer through the Broker, with the SDK's own ``BrokerClient``: the agent
resolves at the Broker, buys the winning offer through
``BrokerService.ExecuteTransaction`` (the Exchange returns the agent-bound
signed URL on the item's ``retrieval_endpoint``), then fetches that signed URL. The demo domain resolves to its
Cloudflare edge, which verifies the ed25519 signature (PoP) and proxies to the
publisher origin — the body is delivered.

This previously carried a strict xfail for a wire-format gap that is now
closed (the signed URL rides on the canonical ``retrievalEndpoint`` and the
demo edges deliver content end-to-end), so it asserts the obligation strictly.
"""

from __future__ import annotations

import pytest

from ..broker_purchase import buy_through_broker
from ..conftest import StackURLs
from ..edge_fetch import fetch_signed
from ..seed import DEMO_PHILOSOPHY_DOMAIN, USD_AGENT_ID, SeededFixture
from ..e2e_catalog import USD_AGENT_KEY_PATH
from ..obligation_registry import Obligation, obligation

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"


@obligation(Obligation.PAID_003)
def test_agent_fetches_signed_url_and_receives_publisher_content(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests the demo catalog
) -> None:
    """Broker purchase → Edge PoP fetch returns the demo publisher's bytes."""
    _, item = buy_through_broker(
        compose_stack, _RESOURCE_URI, agent_id=USD_AGENT_ID, key_path=USD_AGENT_KEY_PATH
    )
    signed_url = item.retrieval_endpoint
    assert signed_url, f"the purchase answered no delivery URL: {item}"

    # Fetch the signed URL via the shared content-delivery seam: asserts 200 and
    # the ACTUAL delivered article (not merely non-empty) — a 200 edge error-stub
    # or wrong-route body must fail. "Epicurus" is the title/subject of
    # deploy/content/demo/stoa-press/.../epicurus.txt.
    # The URL is bound to the buying agent — fetch with PoP.
    fetch_signed(
        signed_url,
        compose_stack,
        expect_marker="Epicurus",
        key_path=USD_AGENT_KEY_PATH,
    )
