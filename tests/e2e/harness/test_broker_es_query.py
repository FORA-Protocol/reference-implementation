"""A Broker on the elasticsearch catalog search, end to end.

The stack runs a second Broker, ``broker-es``, in front of a real Elasticsearch
node. The ``es-seed`` one-shot stands in for the index owner: it indexed the
philosophy feed exchange-a sells, each document under the URL that Exchange stores,
plus one document the Exchange does not hold, and it stored the example search
template the Broker runs. ``es-seed`` also minted the read-only key the Broker
reads. The Broker validates search filters against the example filter schema. No
model and no catalog file is involved.

Every search is a signed ``Resolve`` through the SDK's Broker client, and every
offer is verified against its Exchange's published key before the test reads it.
What MCP adds is the same on either provider and is covered by
``test_mcp_query_discovery.py``, so identity keeps talking to the other Broker.

``broker-es`` is exercised on Resolve alone. A purchase does not depend on how an
offer was found, and the purchase suites drive the other Broker.
"""

from __future__ import annotations

from collections.abc import Iterator
from typing import Any

import pytest
from fora_sdk.client import CallError
from fora_sdk.sync import BrokerClient

from ._compose import stopped_service
from .broker_purchase import one_group_per_uri, only_verified_offer, resolve_request
from .e2e_catalog import DEMO_PHILOSOPHY_DOMAIN
from .exchanges import EXCHANGE_A_DOMAIN
from .fresh_agent import FreshAgent
from .obligation_registry import Obligation, obligation
from .poll import poll_until

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEARCH = "DISCOVERY_METHOD_SEARCH"
_EXCHANGE = "DISCOVERY_METHOD_EXCHANGE"
_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"
_TEMPORARILY_UNAVAILABLE = "OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE"


def _article(name: str) -> str:
    return f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/{name}.txt"


_SOCRATES = _article("socrates")
# The two other articles whose text names Socrates.
_MENTION_SOCRATES = {_article("plato"), _article("zeno-of-citium")}
# In the index, under the title "Diogenes", and in no feed.
_STALE = _article("diogenes")

# The fields es-seed assigns by feed position (philosophy.jsonl order, the stale
# document last): an even position is "en" and an odd one "el", and position i was
# published on 2026-01-(i+1). Socrates is 0, Plato 1, Zeno of Citium 8.
_SOCRATES_EN = [_SOCRATES, _article("zeno-of-citium")]
_SOCRATES_EL = [_article("plato")]
# Every odd position, in URL order, which is the template's order when all scores
# are equal: Plato, Epicurus, Parmenides, Pythagoras, Thales of Miletus.
_ALL_EL = sorted(
    _article(name)
    for name in ("plato", "epicurus", "parmenides", "pythagoras", "thales-of-miletus")
)

#: How long a stopped node may take to answer searches again. It keeps its data on
#: a volume, so this is the JVM's start, on a host that may be busy.
_RECOVERY_DEADLINE_S = 120.0


@pytest.fixture
def es_broker(es_broker_ready: str, fresh_agent: FreshAgent) -> BrokerClient:
    """The Elasticsearch Broker, as this test's own agent."""
    return fresh_agent.broker_client(broker_url=es_broker_ready)


@obligation(Obligation.ESMODE_001)
def test_query_returns_the_matches_in_relevance_order(es_broker: BrokerClient) -> None:
    """The title match outranks the articles that only mention the name."""
    result = es_broker.resolve(resolve_request(query="Socrates"))

    uris = [group.uri for group in result.groups]
    assert uris[0] == _SOCRATES, f"groups = {uris}: the title match must come first"
    assert set(uris[1:]) == _MENTION_SOCRATES, f"groups = {uris}"
    for group in result.groups:
        assert group.discovery_method == _SEARCH, group
        offer = only_verified_offer(group, f"Elasticsearch search hit {group.uri}")
        assert offer.offer["exchange"] == EXCHANGE_A_DOMAIN, offer.offer


@obligation(Obligation.ESMODE_002)
def test_a_hit_the_exchange_does_not_hold_gets_no_offer(es_broker: BrokerClient) -> None:
    """The stale document is found, and the Exchange's catalog has the last word."""
    result = es_broker.resolve(resolve_request(query="Diogenes Socrates"))

    by_uri = {group.uri: group for group in result.groups}
    assert _STALE in by_uri, f"groups = {sorted(by_uri)}: the query must hit the stale document"
    stale = by_uri[_STALE]
    assert stale.discovery_method == _SEARCH, stale
    assert not stale.result.verified and not stale.result.rejected, stale
    assert stale.absence_reason == _NOT_IN_CATALOG, stale
    only_verified_offer(by_uri[_SOCRATES], "the article beside the stale document")


@obligation(Obligation.ESMODE_002)
def test_no_offer_for_any_hit_is_not_in_catalog(es_broker: BrokerClient) -> None:
    result = es_broker.resolve(resolve_request(query="Diogenes"))

    assert result.groups == [], result
    assert result.absence_reason == _NOT_IN_CATALOG, result


@obligation(Obligation.ESMODE_003)
def test_a_query_no_document_matches_is_not_in_catalog(es_broker: BrokerClient) -> None:
    result = es_broker.resolve(resolve_request(query="quantum chromodynamics"))

    assert result.groups == [], result
    assert result.absence_reason == _NOT_IN_CATALOG, result


# exchange-a sells Parmenides under one reference-only term gated on a
# subscription scope a fresh agent does not hold, so its group carries no offer and
# no absence reason (test_04_failure_03_scope_not_covered.py). A search still
# finds it, which is what these tests are about.
_SCOPE_GATED = frozenset({_article("parmenides")})


def _search(broker: BrokerClient, **fields: Any) -> list[str]:
    """The URLs a resolve returns, each stamped SEARCH.

    Every group carries a verified offer, except a scope-gated one, which carries
    none.
    """
    result = broker.resolve(resolve_request(**fields))
    for group in result.groups:
        assert group.discovery_method == _SEARCH, group
        if group.uri in _SCOPE_GATED:
            assert not group.result.verified and not group.result.rejected, group
            continue
        only_verified_offer(group, f"Elasticsearch search hit {group.uri}")
    return [group.uri for group in result.groups]


def _refusal(broker: BrokerClient, **fields: Any) -> CallError:
    with pytest.raises(CallError) as refused:
        broker.resolve(resolve_request(**fields))
    return refused.value


def _field(error: CallError) -> str | None:
    detail = error.detail
    return None if detail is None else (detail.metadata or {}).get("field")


@obligation(Obligation.ESMODE_004)
def test_a_declared_language_filter_narrows_the_search(es_broker: BrokerClient) -> None:
    """The Socrates matches split by language, and the title match stays first."""
    english = _search(es_broker, query="Socrates", search_filters={"news.language": "en"})
    greek = _search(es_broker, query="Socrates", search_filters={"news.language": "el"})

    assert english == _SOCRATES_EN, english
    assert greek == _SOCRATES_EL, greek


@obligation(Obligation.ESMODE_004)
def test_a_declared_date_filter_narrows_the_search(es_broker: BrokerClient) -> None:
    """Published after the Socrates article: only the later mentions remain."""
    later = _search(
        es_broker,
        query="Socrates",
        search_filters={"news.date_published_after": "2026-01-02T00:00:00Z"},
    )

    assert set(later) == _MENTION_SOCRATES, later


@obligation(Obligation.ESMODE_004)
def test_a_filter_only_request_answers(es_broker: BrokerClient) -> None:
    """No query takes the template's match_all branch, narrowed by the filter."""
    greek = _search(es_broker, search_filters={"news.language": "el"})

    assert greek == _ALL_EL, greek


@obligation(Obligation.ESMODE_004)
def test_a_well_formed_value_no_document_has_is_not_in_catalog(es_broker: BrokerClient) -> None:
    result = es_broker.resolve(
        resolve_request(query="Socrates", search_filters={"news.language": "de"})
    )

    assert result.groups == [], result
    assert result.absence_reason == _NOT_IN_CATALOG, result


@obligation(Obligation.ESMODE_007, negative=True)
def test_an_undeclared_filter_key_is_unimplemented(es_broker: BrokerClient) -> None:
    """The declared key answers, so the refusal is about the key."""
    assert _search(es_broker, query="Socrates", search_filters={"news.language": "en"})

    refused = _refusal(es_broker, query="Socrates", search_filters={"music.genre": "jazz"})

    assert refused.code == "unimplemented", f"refused as {refused.code!r}: {refused}"
    assert _field(refused) == "search_filters.music.genre", refused.detail


@obligation(Obligation.ESMODE_008, negative=True)
def test_a_malformed_filter_value_is_invalid_argument(es_broker: BrokerClient) -> None:
    """The well-formed value answers, so the refusal is about the value."""
    assert _search(es_broker, query="Socrates", search_filters={"news.language": "en"})

    refused = _refusal(es_broker, query="Socrates", search_filters={"news.language": "english"})

    assert refused.code == "invalid_argument", f"refused as {refused.code!r}: {refused}"
    assert _field(refused) == "search_filters.news.language", refused.detail
    assert "english" not in str(refused), refused


@obligation(Obligation.ESMODE_005)
def test_a_named_url_wins_over_the_query_and_the_filters(es_broker: BrokerClient) -> None:
    result = es_broker.resolve(
        resolve_request([_SOCRATES], query="Diogenes", search_filters={"music.genre": "jazz"})
    )

    group = one_group_per_uri(result, [_SOCRATES], "named URL")[_SOCRATES]
    assert group.discovery_method == _EXCHANGE, group
    only_verified_offer(group, "the named URL")


def _searches(broker: BrokerClient) -> bool:
    return bool(broker.resolve(resolve_request(query="Socrates")).groups)


@pytest.fixture
def elasticsearch_outage(es_broker: BrokerClient) -> Iterator[None]:
    """Elasticsearch stopped for the test, and answering again before the next one.

    The wait is also what shows the Broker recovers by itself: nothing restarts it,
    and it read its settings once, at boot.
    """
    with stopped_service("elasticsearch"):
        yield
    poll_until(
        lambda: _searches(es_broker),
        _RECOVERY_DEADLINE_S,
        failure=lambda: "the Elasticsearch Broker did not answer searches again",
    )


@obligation(Obligation.ESMODE_005, Obligation.ESMODE_006)
@pytest.mark.usefixtures("elasticsearch_outage")
def test_a_cluster_outage_fails_queries_and_spares_named_urls(es_broker: BrokerClient) -> None:
    searched = es_broker.resolve(resolve_request(query="Socrates"))
    assert searched.groups == [], searched
    assert searched.absence_reason == _TEMPORARILY_UNAVAILABLE, searched

    named = es_broker.resolve(resolve_request([_SOCRATES]))
    group = one_group_per_uri(named, [_SOCRATES], "named URL during the outage")[_SOCRATES]
    assert group.discovery_method == _EXCHANGE, group
    only_verified_offer(group, "the named URL during the outage")
