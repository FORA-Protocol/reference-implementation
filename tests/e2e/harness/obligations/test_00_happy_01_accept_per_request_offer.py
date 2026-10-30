"""Obligation PAID-002: accepting a per-request offer confirms it and returns a URL.

Driven against the demo ``epicurus`` resource (PER_UNIT USD, individual,
US/GB), which exchange-a sells, as the test's own agent (``fresh_agent``, with an
account at exchange-a) through the Broker, with the SDK's own
``BrokerClient``: the agent resolves at the Broker (discovery-only ranked
offers, each verified by the SDK against its issuing Exchange's key), then buys
the winning offer through ``BrokerService.ExecuteTransaction``, which the Broker
re-packages to the Exchange.

(a) the agent selects a per-request offer: Broker Resolve, then buy the winning
    offer.
(b) the platform confirms acceptance: the item carries a non-empty,
    Exchange-assigned transaction_id. (The response does not echo the request's
    idempotency_key: ADR-019 dropped the response id; the durable identity of
    what the call created is the Exchange-assigned transaction_id.)
(c) the agent gets a URL it can fetch: the item's ``retrieval_endpoint`` is
    non-empty.
"""

from __future__ import annotations

import pytest

from ..broker_purchase import resolve_and_buy
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"


@obligation(Obligation.PAID_002)
def test_agent_accepts_per_request_offer_and_receives_signed_url(fresh_agent: FreshAgent) -> None:
    """Broker purchase of a per-request offer → a transaction_id and a delivery URL."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    # (a) Broker Resolve, then buy the winning offer through the Broker.
    item = resolve_and_buy(fresh_agent, _RESOURCE_URI)
    assert item.denial_reason is None, f"the per-request offer was denied: {item}"
    # (b)
    assert item.transaction_id, f"the item must carry a non-empty transaction_id: {item}"
    # (c)
    assert item.retrieval_endpoint, f"the item must carry a delivery URL: {item}"
