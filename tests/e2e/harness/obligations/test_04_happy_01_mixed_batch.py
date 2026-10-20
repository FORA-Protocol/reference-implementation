"""Obligations PUBLIC-001, PUBLIC-002 and BROWSE-003, proved on one batch discovery.

The scenario this test was written for, a batch of URIs with mixed verdicts,
was moved out of the public-resource obligations into a batch-browse
obligation that is not in the registry. The test still proves three that are:
a free offer for a public URI is accepted and yields a signed URL (PUBLIC-001),
the URL is fetched through the edge (PUBLIC-002), and a URI the platform does
not recognise gets no offer and a reason (BROWSE-003).

V1 reading: the structural failure mode the catalog produces for an unknown
URI is ``OFFER_ABSENCE_REASON_NOT_IN_CATALOG``. Driven against two known demo
philosophy resources (socrates + heraclitus, both FREE EUR) and one
never-seeded demo URI, as the test's own agent (``fresh_agent``, academic, EU),
registered at exchange-a. Every call is the SDK's own, signed with the agent's own
key and read through the SDK's typed results. The free purchase owes a usage report this test
never files, which is why the agent is the test's own: the unreported
obligation stays with an agent no other test buys as.
"""

from __future__ import annotations

import uuid

import pytest
from fora_sdk import ProtocolVersion
from wire.models import ResourceQuery

from ..broker_purchase import one_group_per_uri, only_verified_offer
from ..conftest import StackURLs
from ..constants import requester
from ..edge_fetch import fetch_signed
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from ..obligation_registry import Obligation, obligation

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"
_EXCHANGE_METHOD = "DISCOVERY_METHOD_EXCHANGE"

# socrates: FREE EUR academic/EU. heraclitus: FREE EUR (no user-type), EU.
_KNOWN_URI_A = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/socrates.txt"
_KNOWN_URI_B = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/heraclitus.txt"


@obligation(Obligation.BROWSE_003, negative=True)
@obligation(Obligation.PUBLIC_001, Obligation.PUBLIC_002)
def test_mixed_batch_groups_known_offers_and_not_in_catalog_reason(
    compose_stack: StackURLs,
    fresh_agent: FreshAgent,
) -> None:
    """Batch DiscoverResources returns per-URI offer groups for known + unseeded demo URIs."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    client = fresh_agent.exchange(EXCHANGE_A_DOMAIN)
    unseeded_uri = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/never-seeded-{uuid.uuid4().hex}.txt"
    uris = [_KNOWN_URI_A, _KNOWN_URI_B, unseeded_uri]
    result = client.discover(
        ResourceQuery(
            ver=ProtocolVersion,
            exchange=EXCHANGE_A_DOMAIN,
            uris=uris,
            requester=requester(fresh_agent.agent_id, user_type="academic", geography="EU"),
        )
    )
    groups = one_group_per_uri(result, uris, "batch discovery")

    # Every group says how its URI was found, the two hits and the miss alike.
    # This request goes straight to the Exchange, which answers only out of its
    # own catalog, so the answer is EXCHANGE on all three.
    for uri, reported in groups.items():
        assert reported.discovery_method == _EXCHANGE_METHOD, (
            f"group {uri!r} must report {_EXCHANGE_METHOD!r}; got {reported}"
        )

    # Both known URI groups carry a verified offer and nothing rejected.
    for uri in (_KNOWN_URI_A, _KNOWN_URI_B):
        only_verified_offer(groups[uri], f"known URI {uri!r}")

    # Unseeded URI group is empty and reports NOT_IN_CATALOG.
    unseeded_group = groups[unseeded_uri]
    assert not unseeded_group.result.verified and not unseeded_group.result.rejected, (
        f"unseeded URI must have no offers, got {unseeded_group}"
    )
    assert unseeded_group.absence_reason == _NOT_IN_CATALOG, (
        f"unseeded URI group must report {_NOT_IN_CATALOG!r}; got {unseeded_group}"
    )

    # Agent proceeds with one known URI: accept + fetch content. The SDK signs the
    # agent acceptance with the agent's own key and sends the single offer as a
    # one-item batch.
    response = client.execute(only_verified_offer(groups[_KNOWN_URI_A], "socrates"))
    items = response.items or []
    assert len(items) == 1, f"one offer bought, {len(items)} items answered: {response}"
    signed_url = items[0].retrieval_endpoint
    assert signed_url, f"signed URL missing from the bought item: {response}"

    # The agent proceeded with _KNOWN_URI_A (socrates). Fetch via the shared
    # content-delivery seam: asserts 200 + the ACTUAL delivered socrates article
    # (deploy/content/demo/stoa-press/.../socrates.txt).
    # The accept-minted URL is bound to the executing agent — fetch with PoP:
    # ``key_path`` makes fetch_signed mint X-FORA-Agent-Key + an RFC 9421 GET
    # signature when the URL carries ``agent_id=`` (ADR-013).
    fetch_signed(
        signed_url,
        compose_stack,
        expect_marker="Socrates",
        key_path=fresh_agent.key_path,
    )
