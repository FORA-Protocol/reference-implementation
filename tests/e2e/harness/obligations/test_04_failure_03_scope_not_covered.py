"""Obligation PUBLIC-004: a resource the agent cannot reach gets no offers and no reason.

The protocol author's decision: when a requester's scopes leave no presentable
offer for a resource, the Exchange answers exactly as for any resource with
nothing to offer, with no offers and no absence reason. The requester never
learns the resource exists, which is what the Requester.scopes comment in the
pinned protocol module requires. OFFER_ABSENCE_REASON_SCOPE_INSUFFICIENT is never
sent, because naming the scope as the cause would tell the requester exactly that.

Driven against the demo ``parmenides`` resource, which exchange-a sells under one
term gated on the subscription scope ``subscription:premium``
(``fixtures/catalog/philosophy.jsonl``). A subscription scope is never satisfied by
stating it: the Exchange presents the term only to a requester whose delegation
chain verifies for it (``test_subscription_gated.py``). The test's own agent
(``fresh_agent``) asks for it through the SDK, each time in one batch with the
public ``socrates`` resource:

* at exchange-a, holding no scope. The parmenides group carries no offer and no
  absence reason. The socrates group carries its offer: the registry's positive
  case, and proof that the batch was answered and not refused as a whole;
* through the Broker's Resolve, holding no scope. The Broker relays exchange-a's
  groups unchanged, so the agent sees the same thing;
* at exchange-a again, now STATING the scope in ``requester.scopes``. The answer
  is the same: a stated subscription scope is a claim, not a grant, and the
  resource stays hidden with no reason;
* through the Broker's Resolve again, stating the scope. The Broker forwards the
  scopes unchanged and the agent still sees nothing for parmenides.

An agent that receives no offer has nothing to accept, so no retrieval URL is
issued and the resource is not served. Every leg goes in through a public RPC and
is read back through the SDK's typed result; no leg reads a datastore.
"""

from __future__ import annotations

import pytest
from fora_sdk.core import DiscoveryResult, OfferGroupResult
from wire.models import Requester

from ..broker_purchase import (
    one_group_per_uri,
    only_verified_offer,
    resolve_request,
    resource_query,
)
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


def _who(agent: FreshAgent, scopes: tuple[str, ...]) -> Requester:
    """The requester ``agent`` stating ``scopes``.

    The requester is written out rather than left to the SDK, because the SDK fills
    in the agent's identity only and the scopes are the variable under test. An
    empty ``scopes`` is left off entirely, which is what the SDK itself would send.
    """
    return Requester.model_validate(
        requester(agent.agent_id, scopes=list(scopes) if scopes else None)
    )


def _discover(agent: FreshAgent, *scopes: str) -> DiscoveryResult:
    """The batch discovered directly at exchange-a by ``agent`` stating ``scopes``."""
    query = resource_query(EXCHANGE_A_DOMAIN, _BATCH, requester=_who(agent, scopes))
    return agent.exchange(EXCHANGE_A_DOMAIN).discover(query)


def _resolve(agent: FreshAgent, *scopes: str) -> DiscoveryResult:
    """The batch resolved through the Broker by ``agent`` stating ``scopes``."""
    return agent.broker.resolve(resolve_request(_BATCH, requester=_who(agent, scopes)))


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
        f"{leg}: {_GATED} is gated on {_SCOPE} and the agent holds no verified grant, yet "
        f"the answer carries offers: verified={len(gated.result.verified)} rejected={rejected}"
    )
    assert gated.absence_reason is None, (
        f"{leg}: absence_reason = {gated.absence_reason!r} for {_GATED}, want none. A "
        f"resource outside the requester's reach is answered as one with nothing to "
        f"offer, so the reason must not name the scope or anything else"
    )
    _assert_offered(groups[_PUBLIC], f"{leg}, the public resource")


@obligation(Obligation.PUBLIC_004)
def test_a_resource_the_scopes_do_not_cover_gets_no_offers_and_no_reason(
    fresh_agent: FreshAgent,
) -> None:
    """No scope: no offer, no reason. The scope merely stated: the same. Exchange and Broker alike."""
    _assert_hidden(_discover(fresh_agent), "exchange-a, no scope")
    _assert_hidden(_resolve(fresh_agent), "Broker resolve, no scope")
    _assert_hidden(_discover(fresh_agent, _SCOPE), f"exchange-a, stating {_SCOPE}")
    _assert_hidden(_resolve(fresh_agent, _SCOPE), f"Broker resolve, stating {_SCOPE}")
