"""Catalog obligations about the terms a push carries and the term an offer sells.

TERMS-001 to TERMS-009, TERMS-011, TERMS-013 and TERMS-015, from the pinned
protocol module ``github.com/FORA-Protocol/protocol`` (``fora.proto``,
``vocab.proto``). The wire-tier refusals of TERMS-004 to TERMS-008 and TERMS-010
are in ``test_catalog_wire.py``.

A term is pushed by the operator's catalog contributor through the SDK's
``CatalogClient`` and read back as the offer an agent discovers, through the
SDK's Exchange client (and, for TERMS-013, through the Broker's Resolve). An
ingest-tier refusal is a well-formed push the Exchange refuses after the wire
tier, so it is sent as it is and read through the SDK's typed ``CallError``.
"""

from __future__ import annotations

from typing import Any

import pytest

from ..broker_purchase import one_group_per_uri, resolve_request
from ..catalog_calls import (
    HOME_EXCHANGE,
    HOME_SEAT,
    MALFORMED_ENTRY,
    UNKNOWN_VOCAB_TOKEN,
    accepted,
    assert_typed_refusal,
    contributor,
    fresh_entry,
    offers,
    only_term,
    refusal,
    sole,
    stored,
)
from ..catalog_push import (
    OBLIGATION_KIND_SHARE_ALIKE,
    OBLIGATION_TRIGGER_ON_DISTRIBUTION,
    OBLIGATION_TRIGGER_ON_USE,
    PRICING_MODEL_FLAT,
    PRICING_MODEL_PER_UNIT,
    RESTRICTION_KIND_FUNCTION,
    RESTRICTION_KIND_GEOGRAPHY,
    RESTRICTION_KIND_USER_TYPE,
    TERM_SEMANTICS_REFERENCE_ONLY,
    license_term,
    restriction,
)
from ..defects import CATALOG_REFUSAL_UNTYPED
from ..fresh_agent import FreshAgent
from ..obligation_registry import Obligation, obligation
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_LICENSE = {
    "id": "Stoa-Press-Reader-1.0",
    "name": "Stoa Press reader licence",
    "uri": "https://publisher.example/licences/reader-1.0",
    "uri_digest": "sha256:" + "ab" * 32,
}


def _flat(**kw: Any) -> dict[str, Any]:
    return license_term(model=PRICING_MODEL_FLAT, rate=0.4, **kw)


def _push_one(stack: StackURLs, term: dict[str, Any]) -> tuple[str, list[str]]:
    """Push one fresh resource selling ``term``; return its URI and the push's warnings."""
    entry = fresh_entry(terms=(term,))
    response = accepted(contributor(stack).push(HOME_SEAT, [entry]))
    return entry.uri, list(response.warnings or [])


def _offered_term(agent: FreshAgent, uri: str) -> dict[str, Any]:
    return only_term(sole(offers(agent, HOME_EXCHANGE, uri), f"offer for {uri}"))


def _restrictions_by_kind(term: dict[str, Any]) -> dict[str, dict[str, Any]]:
    return {str(r.get("kind")): r for r in term.get("restrictions") or []}


def _plain(value: Any) -> Any:
    """``value`` with every integer as its decimal string: proto JSON writes int64 as a string."""
    if isinstance(value, dict):
        return {k: _plain(v) for k, v in value.items()}
    if isinstance(value, list):
        return [_plain(v) for v in value]
    if isinstance(value, bool):
        return value
    if isinstance(value, int):
        return str(value)
    return value


@obligation(Obligation.TERMS_001)
def test_aliases_and_case_are_canonicalised_and_never_emitted(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """ "generative-ai", "Search" and "business" are offered as "ai-input", "search", "commercial_entity"."""
    uri, warnings = _push_one(
        compose_stack,
        _flat(
            restrictions=(
                restriction(RESTRICTION_KIND_FUNCTION, permitted=("generative-ai", "Search")),
                restriction(RESTRICTION_KIND_USER_TYPE, permitted=("business",)),
            )
        ),
    )
    for spelling in ("generative-ai", "Search", "business"):
        assert not any(spelling in w for w in warnings), f"{spelling} was warned about: {warnings}"
    by_kind = _restrictions_by_kind(_offered_term(fresh_agent, uri))
    assert by_kind[RESTRICTION_KIND_FUNCTION].get("permitted") == ["ai-input", "search"], by_kind
    assert by_kind[RESTRICTION_KIND_USER_TYPE].get("permitted") == ["commercial_entity"], by_kind


@obligation(Obligation.TERMS_002)
def test_an_unregistered_bare_token_is_warned_and_a_namespaced_one_is_not(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """ "totally-made-up-use" is accepted and warned; "acme:summarize" is accepted silently."""
    uri, warnings = _push_one(
        compose_stack,
        _flat(
            restrictions=(
                restriction(
                    RESTRICTION_KIND_FUNCTION, permitted=("totally-made-up-use", "acme:summarize")
                ),
            )
        ),
    )
    assert any("totally-made-up-use" in w for w in warnings), warnings
    assert not any("acme:summarize" in w for w in warnings), warnings
    by_kind = _restrictions_by_kind(_offered_term(fresh_agent, uri))
    assert by_kind[RESTRICTION_KIND_FUNCTION].get("permitted") == [
        "totally-made-up-use",
        "acme:summarize",
    ], by_kind


@obligation(Obligation.TERMS_003)
def test_a_registered_or_namespaced_unit_is_accepted(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Units "accesses" and "acme:widgets", and the quota metric "accesses", are accepted."""
    for unit in ("accesses", "acme:widgets"):
        term = license_term(model=PRICING_MODEL_PER_UNIT, rate=0.1, unit=unit, estimated_quantity=2)
        term["quotas"] = [{"metric": "accesses", "limit": 3, "window": "QUOTA_WINDOW_TOTAL"}]
        uri, _ = _push_one(compose_stack, term)
        assert stored(fresh_agent, HOME_EXCHANGE, uri), f"the term with unit {unit} was not stored"


@obligation(Obligation.TERMS_003, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
@pytest.mark.parametrize("where", ["unit", "metric"])
def test_an_unregistered_bare_unit_or_metric_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
    where: str,
) -> None:
    """Unit "widgets", or quota metric "views": refused UNKNOWN_VOCAB_TOKEN, nothing stored."""
    term = license_term(
        model=PRICING_MODEL_PER_UNIT,
        rate=0.1,
        unit="widgets" if where == "unit" else "accesses",
        estimated_quantity=2,
    )
    if where == "metric":
        term["quotas"] = [{"metric": "views", "limit": 3, "window": "QUOTA_WINDOW_TOTAL"}]
    entry = fresh_entry(terms=(term,))
    error = refusal(contributor(compose_stack).push(HOME_SEAT, [entry]), f"an unregistered {where}")
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the refused entry was stored"
    assert_typed_refusal(error, UNKNOWN_VOCAB_TOKEN, [entry.path])


@obligation(Obligation.TERMS_004)
def test_a_disjoint_restriction_is_accepted_and_offered_unchanged(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Permitting "ai-input" and prohibiting "ai-train" is no contradiction."""
    uri, _ = _push_one(
        compose_stack,
        _flat(
            restrictions=(
                restriction(
                    RESTRICTION_KIND_FUNCTION, permitted=("ai-input",), prohibited=("ai-train",)
                ),
            )
        ),
    )
    function = _restrictions_by_kind(_offered_term(fresh_agent, uri))[RESTRICTION_KIND_FUNCTION]
    assert function.get("permitted") == ["ai-input"], function
    assert function.get("prohibited") == ["ai-train"], function


@obligation(Obligation.TERMS_004, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_restriction_contradicting_itself_once_folded_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Permitting "scrape" and prohibiting "crawl": one token once the alias resolves."""
    entry = fresh_entry(
        terms=(
            _flat(
                restrictions=(
                    restriction(
                        RESTRICTION_KIND_FUNCTION, permitted=("scrape",), prohibited=("crawl",)
                    ),
                )
            ),
        )
    )
    error = refusal(
        contributor(compose_stack).push(HOME_SEAT, [entry]), "a self-contradiction once folded"
    )
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the refused entry was stored"
    assert_typed_refusal(error, MALFORMED_ENTRY, [entry.path])


@obligation(Obligation.TERMS_005)
def test_one_restriction_per_axis_is_offered_on_every_axis(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """One FUNCTION, one GEOGRAPHY and one USER_TYPE restriction: all three offered."""
    uri, _ = _push_one(
        compose_stack,
        _flat(
            restrictions=(
                restriction(RESTRICTION_KIND_FUNCTION, permitted=("ai-input",)),
                restriction(RESTRICTION_KIND_GEOGRAPHY, permitted=("EU",)),
                restriction(RESTRICTION_KIND_USER_TYPE, permitted=("academic",)),
            )
        ),
    )
    kinds = set(_restrictions_by_kind(_offered_term(fresh_agent, uri)))
    assert kinds == {
        RESTRICTION_KIND_FUNCTION,
        RESTRICTION_KIND_GEOGRAPHY,
        RESTRICTION_KIND_USER_TYPE,
    }, kinds


@obligation(Obligation.TERMS_006, Obligation.TERMS_015)
def test_a_reference_only_term_with_only_its_price_and_document_is_offered(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """REFERENCE_ONLY, a license with uri and digest, a price, nothing else: no warning, offered."""
    term = license_term(model=PRICING_MODEL_FLAT, rate=0.4, semantics=TERM_SEMANTICS_REFERENCE_ONLY)
    term["license"] = dict(_LICENSE)
    uri, warnings = _push_one(compose_stack, term)
    assert warnings == [], f"a REFERENCE_ONLY term with no machine fields was warned: {warnings}"
    offered = _offered_term(fresh_agent, uri)
    assert offered.get("semantics") == TERM_SEMANTICS_REFERENCE_ONLY, offered
    assert offered.get("license") == _LICENSE, offered


@obligation(Obligation.TERMS_007)
def test_a_share_alike_obligation_with_its_license_is_offered_unchanged(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """SHARE_ALIKE naming CC-BY-SA-4.0 is accepted and offered as pushed."""
    obligation_ = {
        "kind": OBLIGATION_KIND_SHARE_ALIKE,
        "trigger": OBLIGATION_TRIGGER_ON_DISTRIBUTION,
        "scope_license": {"id": "CC-BY-SA-4.0"},
    }
    uri, _ = _push_one(compose_stack, _flat(obligations=(obligation_,)))
    assert _offered_term(fresh_agent, uri).get("obligations") == [obligation_]


@obligation(Obligation.TERMS_008)
def test_a_term_with_a_quota_and_an_obligation_is_offered_unchanged(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A quota of one access in total and an ATTRIBUTION ON_USE obligation, as pushed."""
    quota = {"metric": "accesses", "limit": 1, "window": "QUOTA_WINDOW_TOTAL"}
    attribution = {"kind": "OBLIGATION_KIND_ATTRIBUTION", "trigger": OBLIGATION_TRIGGER_ON_USE}
    term = _flat(obligations=(attribution,))
    term["quotas"] = [quota]
    uri, _ = _push_one(compose_stack, term)
    offered = _offered_term(fresh_agent, uri)
    assert _plain(offered.get("quotas")) == _plain([quota]), offered
    assert offered.get("obligations") == [attribution], offered


@obligation(Obligation.TERMS_009)
def test_an_other_obligation_without_detail_is_accepted_and_warned(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """OBLIGATION_KIND_OTHER with no detail: accepted, one warning, and the offer carries it."""
    other = {"kind": "OBLIGATION_KIND_OTHER", "trigger": OBLIGATION_TRIGGER_ON_USE}
    uri, warnings = _push_one(compose_stack, _flat(obligations=(other,)))
    assert warnings, "an OTHER obligation without detail produced no warning"
    assert _offered_term(fresh_agent, uri).get("obligations") == [other]


@obligation(Obligation.TERMS_011)
def test_the_offered_term_is_the_publishers_term_without_its_price(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Every field of a fully stated term but its pricing comes back unchanged on the offer."""
    term = _flat(
        restrictions=(
            restriction(
                RESTRICTION_KIND_FUNCTION, permitted=("ai-input",), prohibited=("ai-train",)
            ),
            restriction(RESTRICTION_KIND_GEOGRAPHY, prohibited=("US",), advisory=True),
        ),
        obligations=(
            {
                "kind": "OBLIGATION_KIND_ATTRIBUTION",
                "trigger": OBLIGATION_TRIGGER_ON_USE,
                "detail": "Credit Stoa Press",
            },
            {
                "kind": OBLIGATION_KIND_SHARE_ALIKE,
                "trigger": OBLIGATION_TRIGGER_ON_DISTRIBUTION,
                "scope_license": {"id": "CC-BY-SA-4.0"},
            },
        ),
    )
    term["license"] = dict(_LICENSE)
    term["quotas"] = [{"metric": "accesses", "limit": 5, "window": "QUOTA_WINDOW_MONTHLY"}]
    term["part_label"] = "Chapter 1"
    uri, _ = _push_one(compose_stack, term)
    offered = _offered_term(fresh_agent, uri)
    assert "pricing" not in offered, f"the offered term repeats a price: {offered}"
    pushed = {k: v for k, v in term.items() if k != "pricing"}
    assert _plain(offered) == _plain(pushed), f"offered {offered}, pushed {pushed}"


@obligation(Obligation.TERMS_013)
def test_restrictions_never_hide_a_term_at_the_exchange_or_through_the_broker(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A US-prohibited term and an academic-only term: both offered, directly and via Resolve."""
    entry = fresh_entry(
        terms=(
            _flat(restrictions=(restriction(RESTRICTION_KIND_GEOGRAPHY, prohibited=("US",)),)),
            license_term(
                model=PRICING_MODEL_FLAT,
                rate=0.1,
                restrictions=(restriction(RESTRICTION_KIND_USER_TYPE, permitted=("academic",)),),
            ),
        )
    )
    accepted(contributor(compose_stack).push(HOME_SEAT, [entry]))
    direct = offers(fresh_agent, HOME_EXCHANGE, entry.uri)
    assert len(direct) == 2, f"two terms, {len(direct)} offers at the Exchange"

    resolved = fresh_agent.broker.resolve(resolve_request([entry.uri]))
    group = one_group_per_uri(resolved, [entry.uri], "broker resolve")[entry.uri]
    assert len(group.result.verified) == 2, (
        f"two terms, {len(group.result.verified)} offers through the Broker: {group}"
    )
