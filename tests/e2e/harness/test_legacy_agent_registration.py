"""The Exchange serves no unauthenticated registration route.

The pinned protocol defines how an agent becomes known to an Exchange, in the
"Agent Account Registration" section of
github.com/FORA-Protocol/protocol/proto/fora/v1/fora.proto: "An agent becomes
KNOWN to an Exchange just by sending a signed request." Its account is opened
through the signed Register RPC, where
"The caller's identity is proven by the request signature". No route registers an
agent without a signature.

The reference Exchange still mounts the legacy self-signup
POST /exchange/v1/agents/register. It is unsigned and rate-limited per client
address, and it writes the identity record that later signatures are verified
against. This test posts to it, unsigned, naming a fresh agent whose key
directory is served, so the route has everything it needs to register the agent.
The protocol defines no such route, so the Exchange must answer 404. Today the
route answers instead; that is the strict xfail, and its check holds only for the
route still answering.

Surface: plain HTTP to exchange-a, because the route is outside the protocol and
no SDK verb reaches it.
"""

from __future__ import annotations

from http import HTTPStatus

import pytest

from .conftest import StackURLs
from .defects import LEGACY_UNSIGNED_REGISTRATION_ROUTE
from .edge_fetch import post_unsigned_json
from .fresh_agent import FreshAgent
from .obligation_registry import Obligation, obligation

# A fresh agent per test, and nothing shared is read or written.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_LEGACY_REGISTER_PATH = "/exchange/v1/agents/register"


@obligation(Obligation.IDENTITY_026, negative=True)
@LEGACY_UNSIGNED_REGISTRATION_ROUTE.mark
def test_an_unsigned_registration_route_does_not_exist(
    compose_stack: StackURLs, fresh_agent: FreshAgent
) -> None:
    """An unsigned POST naming a servable agent finds no route: 404."""
    answer = post_unsigned_json(
        f"{compose_stack.exchange}{_LEGACY_REGISTER_PATH}",
        {"agent_id": fresh_agent.agent_id, "discovery_url": fresh_agent.host},
    )

    LEGACY_UNSIGNED_REGISTRATION_ROUTE.check(
        answer.status == HTTPStatus.NOT_FOUND,
        f"the route answered {answer.status.value} {answer.text[:200]!r}",
    )
