"""Full-path term-eligibility E2E over the live stack (demo catalog).

Exercises the COMPLETE vertical through public surfaces only, against the
demo-catalog resources the real ``fora-ingest`` binary ingested. Push validation
is not driven here: a hard-invalid term refusing the whole push and an unknown
vocabulary token warned about but accepted are the catalog obligations CATALOG-001
and TERMS-002, proved in ``obligations/test_catalog_push.py`` and
``obligations/test_catalog_terms.py``.

* QUERY (agent): Broker → Exchange → Edge on the demo ``plato`` resource, which
  carries an academic-FREE term (pushed first) and a commercial-PER_UNIT 0.03
  term. Per ADR-014's 2026-06-15 amendment the Exchange filters by resource_id +
  scopes ONLY — it no longer excludes a term by the requester's self-declared
  user_type / geography / intended_use. So EVERY requester discovers BOTH terms,
  and each term comes back as its own signed offer: two offers for the one plato
  URI, one term each, each priced from the term it sells. The agent picks the
  offer it wants — the free one here — and pays that offer's price. A requester
  in any geography (US included) is licensed and delivered bytes; the
  restrictions ride on the offer for the agent to self-honour, and are enforced
  downstream at accept→report→reconcile.

The QUERY path pushes NOTHING — it reads the demo catalog the seed ingested.
"""

from __future__ import annotations

from contextlib import closing
from decimal import Decimal
from http import HTTPStatus

from wire.models import TransactionResultItem

from .broker_purchase import buy_through_broker, one_group_per_uri, only_item, resolve_request
from .catalog_push import PRICING_MODEL_FREE
from .conftest import StackURLs
from .edge_fetch import fetch_signed, fetch_without_proof, tamper_query_param
from .sdk_client import sdk_broker_client
from .seed import SeededFixture


def _deliver_demo(compose_stack: StackURLs, res) -> TransactionResultItem:
    """Resolve ``res`` at the Broker and buy the winner through the Broker.

    Broker Resolve is discovery-only; the signed URL is minted on the purchase.
    user_type / geography are not sent: the Broker stopped relaying them and the
    Exchange no longer filters on them (ADR-014, 2026-06-15). Only the native
    intended_use rides through.
    """
    _, item = buy_through_broker(
        compose_stack, res.uri, agent_id=res.buyer_agent_id, key_path=res.buyer_key_path
    )
    assert item.denial_reason is None, f"the purchase of {res.uri} was denied: {item}"
    return item


# ---------------------------------------------------------------------------
# QUERY (agent) — full path Broker → Exchange → Edge on the demo plato resource.
# plato (demo.fora-protocol.org/articles/philosophers/plato.txt): term (a)
# academic FREE EUR geo[EU,EEA] (pushed FIRST); term (b) commercial PER_UNIT 0.03
# EUR geo[EU]. Post-ADR-014 (2026-06-15) BOTH terms project for EVERY requester,
# each as its own signed offer. The agent self-selects among the offers; the
# Exchange no longer differentiates requesters by user_type / geography.
# ---------------------------------------------------------------------------


def test_query_returns_an_offer_per_term_and_gets_content(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """Any requester resolves plato (both terms returned) and is delivered bytes.

    The Exchange returns each of plato's terms as its own offer; the agent
    self-selects among them and is delivered bytes. Which offer carries which
    price is asserted by the test below, which picks the free one explicitly;
    this one asserts the protocol-meaningful licensed outcome of taking the
    ranked winner, which is what ADR-019 left on the agent-facing wire.
    """
    res = seeded.restricted  # plato
    signed_url = _deliver_demo(compose_stack, res).retrieval_endpoint
    assert signed_url, f"the purchase minted no delivery URL for {res.uri}"

    content = fetch_signed(signed_url, compose_stack, key_path=res.buyer_key_path)
    assert content.body.strip(), content.body[:256]


def test_query_agent_picks_the_free_offer_and_pays_nothing(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """The agent chooses plato's free term by price and is charged nothing.

    plato is licensed two ways, so discovery answers with TWO offers for the one
    URI, each selling one term. This is the whole point of the split: the agent
    reads both alternatives, picks the FREE one by its pricing model rather than
    by its position, executes that exact signed offer, and the Exchange charges
    what that offer says — unit cost 0. Before the split both terms rode on one
    offer priced from the first term, and the second term could never be bought
    at its own price.
    """
    res = seeded.restricted  # plato
    buyer = sdk_broker_client(
        compose_stack, agent_id=res.buyer_agent_id, key_path=res.buyer_key_path
    )
    with closing(buyer) as client:
        result = client.resolve(resolve_request([res.uri], intended_use="ai-input"))
        group = one_group_per_uri(result, [res.uri], "broker resolve of plato")[res.uri]
        assert not group.result.rejected, f"plato offers rejected: {group.result.rejected}"
        offers = group.result.verified
        assert len(offers) == 2, f"expected one offer per plato term, got {len(offers)}: {offers!r}"
        for offer in offers:
            assert len(offer.offer.get("terms") or []) == 1, (
                f"an offer carries more than one term: {offer.offer!r}"
            )

        free = [
            o for o in offers if (o.offer.get("pricing") or {}).get("model") == PRICING_MODEL_FREE
        ]
        assert len(free) == 1, f"expected exactly one FREE offer among {offers!r}"
        item = only_item(client.execute(free), "broker purchase of plato's free offer")

    assert item.denial_reason is None, item
    signed_url = item.retrieval_endpoint
    assert signed_url, item

    # Money is a decimal string on the wire.
    assert item.cost is not None, item
    assert Decimal(str(item.cost.unit_cost)) == 0, item.cost
    assert Decimal(str(item.cost.amount)) == 0, item.cost

    content = fetch_signed(signed_url, compose_stack, key_path=res.buyer_key_path)
    assert content.body.strip(), content.body[:256]


def test_query_us_requester_now_licensed(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """A requester that declares no permitted geography is STILL licensed (flip).

    Pre-ADR-014 a US commercial requester got no offer (plato's terms permit only
    EU). The Exchange no longer excludes by geography — the geography restriction
    rides on the returned offer for the agent to self-honour — so the requester
    is licensed and receives a signed URL. (user_type / geography are no longer
    even sent on the wire; this asserts the behaviour the inversion produces.)
    """
    res = seeded.restricted  # plato
    assert _deliver_demo(compose_stack, res).retrieval_endpoint, res.uri


def test_query_tampered_signature_is_rejected(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """Mutating the signature on a term-derived signed URL flips edge verify to 403."""
    res = seeded.restricted  # plato
    signed_url = _deliver_demo(compose_stack, res).retrieval_endpoint
    assert signed_url, res.uri
    bad = fetch_without_proof(tamper_query_param(signed_url))
    assert bad.status == HTTPStatus.FORBIDDEN, bad.text
