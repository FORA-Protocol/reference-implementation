"""Obligation PUBLIC-004: a resource the agent's scopes do not cover gets no offers and no reason.

The protocol author's decision: when a requester's scopes leave no presentable
offer for a resource, the Exchange answers exactly as for any resource with
nothing to offer, with no offers and no absence reason. The requester never
learns the resource exists, which is what the Requester.scopes comment in the
pinned protocol module requires. OFFER_ABSENCE_REASON_SCOPE_INSUFFICIENT is never
sent, because naming the scope as the cause would tell the requester exactly that.

Driven against the demo ``parmenides`` resource, which exchange-a sells under one
term gated on the scope ``subscription:premium`` (``fixtures/catalog/philosophy.jsonl``).
The test's own agent (``fresh_agent``) asks for it through the SDK, each time in
one batch with the public ``socrates`` resource:

* at exchange-a, holding no scope. The parmenides group carries no offer and no
  absence reason. The socrates group carries its offer: the registry's positive
  case, and proof that the batch was answered and not refused as a whole;
* through the Broker's Resolve, holding no scope. The Broker relays exchange-a's
  groups unchanged, so the agent sees the same thing: no offer and no reason for
  parmenides, the offer for socrates;
* at exchange-a again, now stating the scope in ``requester.scopes``. The same
  parmenides URI is offered. This is the control: the term is there to be sold,
  so the empty group above is the scope's doing and nothing else's;
* through the Broker's Resolve again, stating the scope. The Broker forwards the
  scopes to exchange-a unchanged, and the agent receives the parmenides offer.

An agent that receives no offer has nothing to accept, so no retrieval URL is
issued and the resource is not served. Every leg goes in through a public RPC and
is read back through the SDK's typed result; no leg reads a datastore.
"""

from __future__ import annotations

from typing import Any

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.core import DiscoveryResult, OfferGroupResult

from ..broker_purchase import one_group_per_uri, only_verified_offer, resolve_request
from ..constants import requester
from ..e2e_catalog import DEMO_PHILOSOPHY_DOMAIN
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SCOPE = "subscription:premium"
_GATED = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/parmenides.txt"
_PUBLIC = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/socrates.txt"
_BATCH = [_GATED, _PUBLIC]


def _who(agent: FreshAgent, scopes: tuple[str, ...]) -> dict[str, Any]:
    """The requester ``agent`` stating ``scopes``.

    The requester is written out rather than left to the SDK, because the SDK fills
    in the agent's identity only and the scopes are the variable under test. An
    empty ``scopes`` is left off entirely, which is what the SDK itself would send.
    """
    return requester(agent.agent_id, scopes=list(scopes) if scopes else None)


def _discover(agent: FreshAgent, *scopes: str) -> DiscoveryResult:
    """The batch discovered directly at exchange-a by ``agent`` stating ``scopes``."""
    query = {
        "ver": ProtocolVersion,
        "exchange": EXCHANGE_A_DOMAIN,
        "uris": _BATCH,
        "requester": _who(agent, scopes),
    }
    return agent.exchange(EXCHANGE_A_DOMAIN).discover(query)


def _resolve(agent: FreshAgent, *scopes: str) -> DiscoveryResult:
    """The batch resolved through the Broker by ``agent`` stating ``scopes``."""
    request = resolve_request(_BATCH)
    request["requester"] = _who(agent, scopes)
    return agent.broker.resolve(request)


def _assert_offered(group: OfferGroupResult, leg: str) -> None:
    """``group`` carries its verified offer and states no reason for an absence."""
    only_verified_offer(group, leg)
    assert group.absence_reason is None, (
        f"{leg}: {group.uri} is offered, so no absence reason applies; got {group.absence_reason!r}"
    )


def _assert_hidden(result: DiscoveryResult, leg: str) -> None:
    """The gated resource gets no offer and no reason; the public one keeps its offer."""
    groups = one_group_per_uri(result, _BATCH, leg)
    gated = groups[_GATED]
    rejected = [(r.offer.get("offer_id"), r.reason) for r in gated.result.rejected]
    assert not gated.result.verified and not rejected, (
        f"{leg}: {_GATED} is gated on {_SCOPE} and the agent holds no scope, yet the "
        f"answer carries offers: verified={len(gated.result.verified)} rejected={rejected}"
    )
    assert gated.absence_reason is None, (
        f"{leg}: absence_reason = {gated.absence_reason!r} for {_GATED}, want none. A "
        f"resource outside the requester's scopes is answered as one with nothing to "
        f"offer, so the reason must not name the scope or anything else"
    )
    _assert_offered(groups[_PUBLIC], f"{leg}, the public resource")


@obligation(Obligation.PUBLIC_004)
def test_a_resource_the_scopes_do_not_cover_gets_no_offers_and_no_reason(
    fresh_agent: FreshAgent,
) -> None:
    """No scope: no offer, no reason. The scope: the offer. Exchange and Broker alike."""
    _assert_hidden(_discover(fresh_agent), "exchange-a, no scope")
    _assert_hidden(_resolve(fresh_agent), "Broker resolve, no scope")

    scoped = f"scope {_SCOPE}"
    direct = one_group_per_uri(_discover(fresh_agent, _SCOPE), _BATCH, f"exchange-a, {scoped}")
    _assert_offered(direct[_GATED], f"exchange-a, {scoped}")
    brokered = one_group_per_uri(_resolve(fresh_agent, _SCOPE), _BATCH, f"Broker resolve, {scoped}")
    _assert_offered(brokered[_GATED], f"Broker resolve, {scoped}")
