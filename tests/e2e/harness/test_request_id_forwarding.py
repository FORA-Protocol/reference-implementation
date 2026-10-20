"""E2E: the X-Request-ID an agent sends the Broker reaches the Exchange that sells the item.

The Broker re-packages a purchase into a sub-request per Exchange. Correlation
holds only if the agent's X-Request-ID travels on that sub-request: the
Exchange writes the id it received into the transaction's evidence, and an
operator joins that row to the agent's own logs by it.

The test's own agent (``fresh_agent``), funded at exchange-b, resolves and buys
the music site's ``paper-satellites`` lyrics through the Broker with the SDK's
``BrokerClient``. The client carries an id the test chose, through the SDK's
own correlation hook (``ClientConfig.request_id``). The evidence exchange-b
recorded for the item's transaction, read on its admin listener, holds that id,
marked as received rather than minted.

Control: the same purchase from a client that sends no X-Request-ID. The Broker
mints an id for it and forwards that one, so its evidence holds an id that is not
the one chosen above and is marked as received, not minted by exchange-b: the
match above is the forwarded header and not a value the evidence always carries,
and the Broker forwards the id it mints as well as the one it was given.

The purchase goes in through the Broker's public RPCs. The evidence is read on
the Exchange's admin listener, the operator's read of what that Exchange stored:
no protocol RPC returns it. No leg reads a datastore.
"""

from __future__ import annotations

import secrets

import pytest

from .admin_plane import require_admin_plane, transaction_evidence
from .broker_purchase import only_item, resolve_offer
from .e2e_catalog import DEMO_MUSIC_DOMAIN
from .exchanges import EXCHANGE_B_DOMAIN
from .fresh_agent import FreshAgent
from .sdk_client import RequestID

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_PAPER_SATELLITES = f"http://{DEMO_MUSIC_DOMAIN}/lyrics/paper-satellites.txt"


def _bought_transaction(agent: FreshAgent, request_id: RequestID | None) -> str:
    """Resolve and buy the lyrics through the Broker; the transaction id exchange-b answered."""
    client = agent.broker_client(request_id=request_id)
    offer = resolve_offer(client, _PAPER_SATELLITES)
    assert offer.offer.get("exchange") == EXCHANGE_B_DOMAIN, (
        f"{_PAPER_SATELLITES} was offered by {offer.offer.get('exchange')!r}, "
        f"want {EXCHANGE_B_DOMAIN}"
    )
    item = only_item(client.execute([offer]), f"broker purchase of {_PAPER_SATELLITES}")
    assert item.denial_reason is None, f"the purchase was denied {item.denial_reason}: {item}"
    assert item.transaction_id, f"the purchase answered no transaction_id: {item}"
    return item.transaction_id


def test_the_agents_request_id_reaches_the_exchange_through_the_broker(
    fresh_agent: FreshAgent,
) -> None:
    """The chosen X-Request-ID is in exchange-b's evidence; without one, the Broker's own is."""
    require_admin_plane()
    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    chosen = f"e2e-request-{secrets.token_hex(8)}"

    forwarded = transaction_evidence(
        EXCHANGE_B_DOMAIN, _bought_transaction(fresh_agent, lambda: chosen)
    )
    assert forwarded.requester_id == fresh_agent.agent_id, forwarded
    assert forwarded.request_id == chosen, (
        f"exchange-b recorded request id {forwarded.request_id!r} for the purchase, "
        f"want the {chosen!r} the agent sent the Broker"
    )
    assert forwarded.request_id_minted is False, (
        f"exchange-b says it minted the id itself: {forwarded}"
    )

    unset = transaction_evidence(EXCHANGE_B_DOMAIN, _bought_transaction(fresh_agent, None))
    assert unset.request_id and unset.request_id != chosen, (
        f"a purchase sent with no X-Request-ID recorded {unset.request_id!r}; the "
        f"evidence must carry some id, and not the one another request chose"
    )
    assert unset.request_id_minted is False, (
        f"exchange-b minted the id itself, so the Broker forwarded none of its own: {unset}"
    )
