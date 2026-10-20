"""E2E: the discovery method a Broker Resolve states on each offer group.

``OfferGroup.discovery_method`` tells the agent how a URI came to be in the
answer. On ``BrokerService.Resolve`` the Broker states it, because only the
Broker knows whether the agent named the URI or the Broker chose it. The
Exchange it then queries is handed a URI list either way, and always says
EXCHANGE. So:

* every group for a URI the agent named says EXCHANGE, the group that carries
  offers and the group that reports an absence alike;
* every group for a URI the Broker's own catalog search chose says SEARCH, even
  though the Exchange that answered for that URI says EXCHANGE.

The second test reads the same URI at the Exchange directly as its control:
the Exchange says EXCHANGE, so the SEARCH the agent receives through the Broker
is the Broker's own statement, not a value carried through. Everything is read
through the SDK's typed results, as each test's own agent.
"""

from __future__ import annotations

import uuid

import pytest
from fora_sdk import ProtocolVersion

from .broker_purchase import (
    one_group_per_uri,
    only_verified_offer,
    resolve_request,
    resource_query,
)
from .e2e_catalog import DEMO_MUSIC_DOMAIN, DEMO_PHILOSOPHY_DOMAIN
from .exchanges import EXCHANGE_B_DOMAIN
from .fresh_agent import FreshAgent

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_EXCHANGE = "DISCOVERY_METHOD_EXCHANGE"
_SEARCH = "DISCOVERY_METHOD_SEARCH"
_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"

_SOCRATES = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/socrates.txt"

# A query fake-bedrock answers with tags only strength-of-the-titans matches in
# full (fixtures/bedrock_script.json; the music feed exchange-b sells).
_QUERY = "epic soundtrack for a documentary trailer"
_TITANS = f"http://{DEMO_MUSIC_DOMAIN}/tracks/strength-of-the-titans.mp3"


def test_every_group_for_a_uri_the_agent_named_says_exchange(fresh_agent: FreshAgent) -> None:
    """A mixed batch: the offer group and the absence group both say EXCHANGE.

    The two groups take different routes to the answer, one with offers and one
    with only an absence reason, so the test first checks they really have the
    two shapes, then reads the method off both.
    """
    unseeded = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/never-seeded-{uuid.uuid4().hex}.txt"
    result = fresh_agent.broker.resolve(resolve_request([_SOCRATES, unseeded]))
    groups = one_group_per_uri(result, [_SOCRATES, unseeded], "mixed batch")

    only_verified_offer(groups[_SOCRATES], "the cataloged URI")
    absent = groups[unseeded]
    assert not absent.result.verified and not absent.result.rejected, absent
    assert absent.absence_reason == _NOT_IN_CATALOG, absent

    for uri, group in groups.items():
        assert group.discovery_method == _EXCHANGE, (
            f"{uri}: discovery_method = {group.discovery_method!r}, want {_EXCHANGE}: "
            f"the agent named this URI"
        )


def test_every_group_for_a_uri_the_broker_chose_says_search(fresh_agent: FreshAgent) -> None:
    """A free-text Resolve: the Broker chose the URI, so its group says SEARCH.

    Control: the same URI discovered directly at exchange-b, the Exchange the
    Broker queried for it, says EXCHANGE.
    """
    direct = fresh_agent.exchange(EXCHANGE_B_DOMAIN).discover(
        resource_query(EXCHANGE_B_DOMAIN, [_TITANS])
    )
    at_exchange = one_group_per_uri(direct, [_TITANS], "exchange-b discovery")[_TITANS]
    assert at_exchange.discovery_method == _EXCHANGE, (
        f"exchange-b says {at_exchange.discovery_method!r} for {_TITANS}; the control "
        f"needs the Exchange to say {_EXCHANGE}"
    )

    result = fresh_agent.broker.resolve({"ver": ProtocolVersion, "query": _QUERY})
    group = one_group_per_uri(result, [_TITANS], f"Broker search for {_QUERY!r}")[_TITANS]
    assert group.discovery_method == _SEARCH, (
        f"{_TITANS}: discovery_method = {group.discovery_method!r}, want {_SEARCH}: the "
        f"Broker chose this URI from the query, whatever the Exchange said"
    )
