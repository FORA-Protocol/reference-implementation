"""Obligation DELIVERY-009: an edge that cannot resolve its key directory answers 503.

The contract is the edge function's signed-URL verification page in the pinned
module ``github.com/FORA-Protocol/protocol``: "If the key-resolution leg throws --
the directory is unreachable, the response is malformed -- the edge answers ``503
verify_unavailable``. It never falls through to the origin."

THE OUTAGE. Each runtime's edge resolves a delivery URL's key from one Exchange's
key directory: exchange-a's for Cloudflare and Lambda@Edge, exchange-b's for
Fastly. An edge keeps the directory it fetched for an hour, so
``edge_runtimes.key_directory_unreachable`` first restarts the edge, which empties
that cache, and then stops the Exchange that serves the directory
(``_compose.stopped_service``). The URL and the agent's account are made before
anything stops. On exit the Exchange is started again and the block returns once
the directory is served and the Broker routes to every seeded Exchange again,
resolving as the test's own agent, so the rest of the suite finds the stack whole.
The e2e runner runs one test at a time, so nothing else runs against the stopped
Exchange.

THE SURFACE. Each runtime is read through ``harness.edge_runtimes``, with the
buyer's proof of possession minted by the SDK. On Lambda@Edge "never falls through
to the origin" is read directly: the answer is a generated response, not the
request handed back to CloudFront. The Cloudflare and Fastly readers see only the
answer, so there it is read as the edge's own 503 refusal, carrying none of the
publisher's bytes.
"""

from __future__ import annotations

from http import HTTPStatus

import pytest

from ..edge_runtimes import (
    RUNTIMES,
    EdgeRuntime,
    assert_refused,
    assert_served,
    bound_urls,
    key_directory_unreachable,
    proof,
    read,
)
from ..fresh_agent import FreshAgent
from ..lambda_edge import ExchangeDeliveryKey, load_exchange_delivery_key
from ..obligation_registry import Obligation, obligation
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_VERIFY_UNAVAILABLE = "verify_unavailable"


@pytest.fixture(scope="module")
def exchange_key(compose_stack: StackURLs) -> ExchangeDeliveryKey:
    """exchange-a's delivery-URL key, confirmed to be the one it publishes."""
    return load_exchange_delivery_key(compose_stack.exchange)


@obligation(Obligation.DELIVERY_009, negative=True)
@pytest.mark.parametrize("runtime", RUNTIMES, ids=str)
def test_an_edge_that_cannot_reach_its_key_directory_answers_503_and_not_the_origin(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """The buyer's URL and proof: 503 ``verify_unavailable`` while the directory is down, served after.

    With the edge's cache empty and the directory's Exchange stopped, the buyer's
    bound URL with a fresh proof is answered 503 ``verify_unavailable`` by the edge
    itself, and no content goes out. Once the directory is served again, the same
    URL with a fresh proof is served.
    """
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]

    with key_directory_unreachable(compose_stack, runtime, fresh_agent.broker):
        down = read(compose_stack, runtime, url, headers=proof(url, fresh_agent))
    assert_refused(runtime, down, HTTPStatus.SERVICE_UNAVAILABLE, _VERIFY_UNAVAILABLE)

    back = read(compose_stack, runtime, url, headers=proof(url, fresh_agent))
    assert_served(runtime, back, url)
