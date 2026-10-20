"""Catalog obligations the wire tier decides: the caps, the host and path shapes, the term rules.

CATALOG-009 to CATALOG-012 and CATALOG-019, and the wire-tier halves of
TERMS-004 to TERMS-008 and TERMS-010, from the pinned protocol module
``github.com/FORA-Protocol/protocol``.

Every negative is a declared ``malformed(Mutation.MESSAGE, ...)``: the control is
the push of one valid entry, and the alteration, made in the SDK's pre-signing
hook after its own schema check, adds beside a fresh valid sibling one entry that
breaks exactly one rule. The seam confirms the sent push fails the published
strict check and the control passes it. Each test then reads, through
``DiscoverResources`` as the test's own agent, that the control was stored and
neither added entry was, and reads the refusal through the SDK's typed
``CallError``.

A refused push carries ``ErrorDetail.catalog_rejection`` (``fora.proto``,
PushResourcesResponse.accepted), and the request-flow page names
MALFORMED_ENTRY as the reason the envelope rules produce. The reference Exchange
refuses these at its protovalidate interceptor with no typed reason, so every
entry-rule test here is a strict xfail on that one defect, checked last: the code
and the absence of every side effect are asserted before it, and fail plainly.

The two CATALOG-009 bounds, no entries and more than 256, are rules on the
request itself rather than on an entry. Such a push is malformed: it is refused
invalid_argument by wire validation, with no catalog_rejection.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.client import CallError

from ..catalog_calls import (
    HOME_EXCHANGE,
    HOME_SEAT,
    MALFORMED_ENTRY,
    accepted,
    added,
    assert_typed_refusal,
    broken,
    contributor,
    entry_wire,
    free_term,
    fresh_entry,
    offers,
    sole,
    stored,
)
from ..catalog_push import (
    OBLIGATION_KIND_SHARE_ALIKE,
    OBLIGATION_TRIGGER_ON_DISTRIBUTION,
    PRICING_MODEL_PER_UNIT,
    RESTRICTION_KIND_FUNCTION,
    TERM_SEMANTICS_REFERENCE_ONLY,
)
from ..defects import CATALOG_REFUSAL_UNTYPED
from ..fresh_agent import FreshAgent
from ..malformed import Alteration, Mutation, malformed, patch_body
from ..money_path import typed_reasons
from ..obligation_registry import Obligation, obligation
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

Edit = Callable[[dict[str, Any]], None]


def _refused_beside_sibling(
    stack: StackURLs, agent: FreshAgent, why: str, edit: Edit
) -> tuple[CallError, dict[str, Any]]:
    """Add a valid sibling and an entry broken by ``edit``; return the refusal and that entry."""
    catalog = contributor(stack)
    control, sibling, bad = fresh_entry(), fresh_entry(), fresh_entry()
    bad_wire = broken(bad, edit)
    error = malformed(
        Mutation.MESSAGE,
        why,
        control=catalog.push(HOME_SEAT, [control]),
        alter=Alteration(before_sign=added(entry_wire(sibling), bad_wire)),
    )
    assert error.code == "invalid_argument", f"{why}: refused as {error.code!r}: {error}"
    assert stored(agent, HOME_EXCHANGE, control.uri), f"{why}: the control push was not stored"
    assert not stored(agent, HOME_EXCHANGE, sibling.uri), f"{why}: the valid sibling was stored"
    assert not stored(agent, HOME_EXCHANGE, bad.uri), f"{why}: the broken entry was stored"
    return error, bad_wire


def _assert_refused(stack: StackURLs, agent: FreshAgent, why: str, edit: Edit) -> None:
    error, bad_wire = _refused_beside_sibling(stack, agent, why, edit)
    assert_typed_refusal(error, MALFORMED_ENTRY, [str(bad_wire["path"])])


def _term(edit: Edit) -> Edit:
    """An entry edit that applies ``edit`` to the entry's first term."""

    def apply(entry: dict[str, Any]) -> None:
        edit(entry["terms"][0])

    return apply


def _set(field: str, value: object) -> Edit:
    return lambda target: target.__setitem__(field, value)


@obligation(Obligation.CATALOG_009, negative=True)
def test_an_empty_push_is_refused(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A push of no entries is refused, never answered with zero counts.

    The bound is a wire rule, so the refusal is wire validation's: invalid_argument
    with no catalog_rejection, because no entry was examined.
    """
    control = fresh_entry()
    error = malformed(
        Mutation.MESSAGE,
        "the push carries no entries",
        control=contributor(compose_stack).push(HOME_SEAT, [control]),
        alter=Alteration(before_sign=patch_body(_set("entries", []))),
    )
    _assert_wire_validation_refusal(error)


@obligation(Obligation.CATALOG_009, negative=True)
def test_a_push_of_257_entries_is_refused_whole(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """257 valid entries, one over the cap: refused by wire validation, none of them stored."""
    many = [fresh_entry() for _ in range(257)]
    error = malformed(
        Mutation.MESSAGE,
        "the push carries 257 entries",
        control=contributor(compose_stack).push(HOME_SEAT, [fresh_entry()]),
        alter=Alteration(before_sign=patch_body(_set("entries", [entry_wire(e) for e in many]))),
    )
    for entry in (many[0], many[-1]):
        assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), f"{entry.uri} was stored"
    _assert_wire_validation_refusal(error)


def _assert_wire_validation_refusal(error: CallError) -> None:
    """A malformed request: invalid_argument, and no catalog_rejection or other typed reason."""
    assert error.code == "invalid_argument", f"refused as {error.code!r}: {error}"
    assert typed_reasons(error) == [], f"a wire-validation refusal carries a typed reason: {error}"


@obligation(Obligation.CATALOG_010, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_an_entry_with_33_terms_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """33 terms on one entry, beside a valid sibling: nothing stored."""
    _assert_refused(
        compose_stack,
        fresh_agent,
        "an entry carries 33 terms",
        _set("terms", [free_term()] * 33),
    )


@obligation(Obligation.CATALOG_010, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_an_entry_with_a_513_character_title_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A title one character over its 512-character cap: nothing stored."""
    _assert_refused(
        compose_stack, fresh_agent, "an entry's title is 513 characters", _set("title", "t" * 513)
    )


@obligation(Obligation.CATALOG_011, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
@pytest.mark.parametrize("shape", ["{host}/evil", "user@{host}"])
def test_an_entry_whose_domain_is_not_a_bare_host_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
    shape: str,
) -> None:
    """A path or userinfo in ResourceEntry.domain: nothing stored, under any URI."""

    def edit(entry: dict[str, Any]) -> None:
        entry["domain"] = shape.format(host=entry["domain"])

    _assert_refused(compose_stack, fresh_agent, f"domain {shape!r}", edit)


@obligation(Obligation.CATALOG_012, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
@pytest.mark.parametrize("path", ["relative/page", "/page?x=1", "/a b"])
def test_an_entry_whose_path_breaks_the_path_rule_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
    path: str,
) -> None:
    """A relative path, a query delimiter or whitespace in ResourceEntry.path: nothing stored."""
    _assert_refused(compose_stack, fresh_agent, f"path {path!r}", _set("path", path))


@obligation(Obligation.CATALOG_019)
def test_an_omitted_mutability_is_offered_static_and_dynamic_as_pushed(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """No resource_mutability: the offer says STATIC. DYNAMIC: the offer says DYNAMIC."""
    plain = fresh_entry()
    dynamic = fresh_entry(fields={"resource_mutability": "RESOURCE_MUTABILITY_DYNAMIC"})
    accepted(contributor(compose_stack).push(HOME_SEAT, [plain, dynamic]))
    for entry, want in (
        (plain, "RESOURCE_MUTABILITY_STATIC"),
        (dynamic, "RESOURCE_MUTABILITY_DYNAMIC"),
    ):
        offer = sole(offers(fresh_agent, HOME_EXCHANGE, entry.uri), f"offer for {entry.uri}")
        identity = offer.offer.get("identity") or {}
        assert identity.get("resource_mutability") == want, f"{entry.uri}: {identity}"


@obligation(Obligation.CATALOG_019, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_an_explicit_unspecified_mutability_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """resource_mutability RESOURCE_MUTABILITY_UNSPECIFIED: nothing stored."""
    _assert_refused(
        compose_stack,
        fresh_agent,
        "resource_mutability is UNSPECIFIED",
        _set("resource_mutability", "RESOURCE_MUTABILITY_UNSPECIFIED"),
    )


#: One wire-tier term rule each: what is broken, and the edit to the first term that breaks it.
_TERM_RULES = [
    pytest.param(
        "permitted and prohibited share a token",
        _set(
            "restrictions",
            [{"kind": RESTRICTION_KIND_FUNCTION, "permitted": ["crawl"], "prohibited": ["crawl"]}],
        ),
        marks=obligation(Obligation.TERMS_004, negative=True),
        id="TERMS-004-not-disjoint",
    ),
    pytest.param(
        "two FUNCTION restrictions on one term",
        _set(
            "restrictions",
            [
                {"kind": RESTRICTION_KIND_FUNCTION, "permitted": ["crawl"]},
                {"kind": RESTRICTION_KIND_FUNCTION, "permitted": ["search"]},
            ],
        ),
        marks=obligation(Obligation.TERMS_005, negative=True),
        id="TERMS-005-two-per-axis",
    ),
    pytest.param(
        "REFERENCE_ONLY with no license",
        _set("semantics", TERM_SEMANTICS_REFERENCE_ONLY),
        marks=obligation(Obligation.TERMS_006, negative=True),
        id="TERMS-006-reference-only-no-license",
    ),
    pytest.param(
        "a license uri with no uri_digest",
        _set("license", {"uri": "https://publisher.example/license"}),
        marks=obligation(Obligation.TERMS_006, negative=True),
        id="TERMS-006-uri-without-digest",
    ),
    pytest.param(
        "SHARE_ALIKE with no scope_license",
        _set(
            "obligations",
            [{"kind": OBLIGATION_KIND_SHARE_ALIKE, "trigger": OBLIGATION_TRIGGER_ON_DISTRIBUTION}],
        ),
        marks=obligation(Obligation.TERMS_007, negative=True),
        id="TERMS-007-share-alike-no-license",
    ),
    pytest.param(
        "a quota of 0",
        _set("quotas", [{"metric": "accesses", "limit": 0, "window": "QUOTA_WINDOW_TOTAL"}]),
        marks=obligation(Obligation.TERMS_008, negative=True),
        id="TERMS-008-zero-quota",
    ),
    pytest.param(
        "semantics UNSPECIFIED",
        _set("semantics", "TERM_SEMANTICS_UNSPECIFIED"),
        marks=obligation(Obligation.TERMS_008, negative=True),
        id="TERMS-008-semantics-unspecified",
    ),
    pytest.param(
        "PER_UNIT with no unit",
        _set("pricing", {"model": PRICING_MODEL_PER_UNIT, "rate": "0.1", "currency": "USD"}),
        marks=obligation(Obligation.TERMS_010, negative=True),
        id="TERMS-010-per-unit-no-unit",
    ),
    pytest.param(
        "FREE with a non-zero rate",
        _set("pricing", {"model": "PRICING_MODEL_FREE", "rate": "0.01", "currency": "USD"}),
        marks=obligation(Obligation.TERMS_010, negative=True),
        id="TERMS-010-free-with-rate",
    ),
]


@CATALOG_REFUSAL_UNTYPED.mark
@pytest.mark.parametrize(("why", "edit"), _TERM_RULES)
def test_a_term_breaking_a_wire_rule_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
    why: str,
    edit: Edit,
) -> None:
    """One term rule broken on an added entry: refused, and nothing of the push stored."""
    _assert_refused(compose_stack, fresh_agent, why, _term(edit))
