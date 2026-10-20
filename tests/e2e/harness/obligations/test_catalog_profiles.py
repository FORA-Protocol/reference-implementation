"""Discovery obligations about extension profiles, acceptable restrictions and critical extensions.

PROFILE-002 to PROFILE-004, PROFILE-006 to PROFILE-008, PROFILE-010 and
PROFILE-013 to PROFILE-015, from the pinned protocol module ``github.com/FORA-Protocol/protocol``
(``fora.proto``, the extension-profiles page and the fora-comp-v1 and
fora-music-v1 profile pages).

Each test pushes a resource of its own through the SDK's ``CatalogClient`` and
discovers it as the test's own agent, directly at exchange-a through the SDK's
Exchange client or through the Broker's Resolve through the SDK's Broker client.
Every offer is verified against its Exchange's published key before the test
reads it, so every ext key read here is inside the signed offer. Every Exchange
of this stack declares fora-comp-v1 and fora-music-v1 in its manifest.
"""

from __future__ import annotations

import uuid
from typing import Any

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError

from ..broker_purchase import one_group_per_uri, resolve_request
from ..catalog_calls import (
    HOME_EXCHANGE,
    HOME_SEAT,
    accepted,
    answer,
    contributor,
    discover,
    entry_ext,
    fresh_entry,
    group,
    offers,
    stored,
)
from ..catalog_push import (
    PRICING_MODEL_FLAT,
    PRICING_MODEL_PER_UNIT,
    RESTRICTION_KIND_GEOGRAPHY,
    license_term,
    restriction,
)
from ..defects import (
    BROKER_DROPS_SUPPORTED_PROFILES,
    BROKER_IGNORES_EXT_CRITICAL,
    COMP_INPUT_UNVALIDATED,
    COMP_RENDERED_NESTED,
    EXCHANGE_IGNORES_EXT_CRITICAL,
    EXT_WITHHELD_WITHOUT_PROFILES,
)
from ..e2e_catalog import DEMO_MUSIC_DOMAIN
from ..fresh_agent import FreshAgent
from ..obligation_registry import Obligation, obligation
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

COMP = "fora-comp-v1"
UNKNOWN_CRITICAL_EXTENSION = "OFFER_ABSENCE_REASON_UNKNOWN_CRITICAL_EXTENSION"
RESTRICTION_FILTERED = "OFFER_ABSENCE_REASON_RESTRICTION_FILTERED"
NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"

#: An ext key no profile defines, and the critical-extension request that names it.
_CRITICAL = {"ext": {"acme.must": True}, "ext_critical": ["acme.must"]}


def _priced(stack: StackURLs, *, terms: int = 1, **entry: Any) -> str:
    """Push a fresh resource with ``terms`` priced terms; return its URI."""
    priced = tuple(license_term(model=PRICING_MODEL_FLAT, rate=0.3 + i) for i in range(terms))
    pushed = fresh_entry(terms=priced, **entry)
    accepted(contributor(stack).push(HOME_SEAT, [pushed]))
    return pushed.uri


def _ext_keys(agent: FreshAgent, uri: str, **fields: Any) -> set[str]:
    found = offers(agent, HOME_EXCHANGE, uri, **fields)
    assert found, f"no offer for {uri}"
    return {key for offer in found for key in entry_ext(offer)}


@obligation(Obligation.PROFILE_002)
@EXT_WITHHELD_WITHOUT_PROFILES.mark
def test_omitting_supported_profiles_withholds_no_ext_field(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Every ext key the fora-comp-v1 discovery carries is on the discovery that names no profile."""
    uri = _priced(compose_stack)
    declared = _ext_keys(fresh_agent, uri, supported_profiles=[COMP])
    assert declared, "the discovery declaring fora-comp-v1 carries no ext at all"
    undeclared = _ext_keys(fresh_agent, uri)
    EXT_WITHHELD_WITHOUT_PROFILES.check(
        declared <= undeclared, f"withheld {sorted(declared - undeclared)}"
    )


@obligation(Obligation.PROFILE_003)
@COMP_RENDERED_NESTED.mark
def test_every_comp_offer_carries_a_flat_package_id(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Two terms, two offers to a fora-comp-v1 caller: each carries its own comp.package_id."""
    uri = _priced(compose_stack, terms=2)
    found = offers(fresh_agent, HOME_EXCHANGE, uri, supported_profiles=[COMP])
    assert len(found) == 2, f"two terms, {len(found)} offers"
    exts = [entry_ext(offer) for offer in found]
    COMP_RENDERED_NESTED.check(
        all("comp.package_id" in ext for ext in exts),
        f"the offers' ext keys: {[sorted(e) for e in exts]}",
    )
    ids = [ext["comp.package_id"] for ext in exts]
    assert all(isinstance(i, str) and i for i in ids), ids
    assert len(set(ids)) == 2, f"two offers share a package id: {ids}"


@obligation(Obligation.PROFILE_004)
def test_every_comp_key_a_publisher_pushes_comes_out_on_the_offer(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """comp.title, comp.seller and comp.citation_required come back unchanged to a comp caller."""
    pushed = {
        "comp.title": f"Probe {uuid.uuid4().hex[:6]}",
        "comp.seller": "publisher.example",
        "comp.citation_required": 1,
    }
    uri = _priced(compose_stack, ext=pushed)
    for offer in offers(fresh_agent, HOME_EXCHANGE, uri, supported_profiles=[COMP]):
        ext = entry_ext(offer)
        for key, value in pushed.items():
            assert ext.get(key) == value, f"{key}: pushed {value!r}, offered {ext.get(key)!r}"


@obligation(Obligation.PROFILE_004, negative=True)
@COMP_INPUT_UNVALIDATED.mark
def test_a_comp_value_breaking_the_profile_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """comp.citation_required is an integer in the profile; the string "yes" refuses the push."""
    entry = fresh_entry(
        terms=(license_term(model=PRICING_MODEL_FLAT, rate=0.3),),
        ext={"comp.citation_required": "yes"},
    )
    outcome = answer(contributor(compose_stack).push(HOME_SEAT, [entry]))
    COMP_INPUT_UNVALIDATED.check(
        not hasattr(outcome, "accepted"), f"the push was accepted: {outcome!r}"
    )
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the refused entry was stored"


@obligation(Obligation.PROFILE_006)
def test_music_keys_are_forwarded_unchanged_and_unvalidated(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """An unknown music.iswc and an upper-case music.genre are accepted and offered as pushed."""
    pushed = {
        "music.genre": "Jazz",
        "music.bpm": 120,
        "music.vocals": False,
        "music.iswc": "T-123.456.789-0",
    }
    uri = _priced(compose_stack, ext=pushed)
    for offer in offers(fresh_agent, HOME_EXCHANGE, uri):
        ext = entry_ext(offer)
        for key, value in pushed.items():
            assert ext.get(key) == value, f"{key}: pushed {value!r}, offered {ext.get(key)!r}"


@obligation(Obligation.PROFILE_010)
@BROKER_DROPS_SUPPORTED_PROFILES.mark
def test_the_broker_forwards_the_agents_supported_profiles(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Resolve declaring fora-comp-v1 returns every ext key a direct comp discovery returns.

    This reads the forwarding off what comes back: the Exchange renders the
    profile's fields only for a query that declares it, so an offer without them
    is the answer to a query the Broker sent without the agent's profiles. The
    query itself, as the Broker sent it, is read off the wire in
    ``test_broker_wire.py``.
    """
    uri = _priced(compose_stack)
    direct = _ext_keys(fresh_agent, uri, supported_profiles=[COMP])
    request = resolve_request([uri])
    request["supported_profiles"] = [COMP]
    resolved = one_group_per_uri(fresh_agent.broker.resolve(request), [uri], "broker resolve")[uri]
    assert resolved.result.verified, f"Resolve offered nothing for {uri}: {resolved}"
    brokered = {key for offer in resolved.result.verified for key in entry_ext(offer)}
    BROKER_DROPS_SUPPORTED_PROFILES.check(
        direct <= brokered, f"missing through the Broker: {sorted(direct - brokered)}"
    )


@obligation(Obligation.PROFILE_013)
def test_acceptable_restrictions_drop_no_offer_or_say_why(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A US requester's acceptable GEOGRAPHY, a term prohibiting US: the offer, or RESTRICTION_FILTERED."""
    entry = fresh_entry(
        terms=(
            license_term(
                model=PRICING_MODEL_PER_UNIT,
                rate=0.1,
                unit="accesses",
                estimated_quantity=1,
                restrictions=(restriction(RESTRICTION_KIND_GEOGRAPHY, prohibited=("US",)),),
            ),
        )
    )
    accepted(contributor(compose_stack).push(HOME_SEAT, [entry]))
    found = group(
        fresh_agent,
        HOME_EXCHANGE,
        entry.uri,
        acceptable_restrictions=[{"axis": RESTRICTION_KIND_GEOGRAPHY, "values": ["US"]}],
    )
    if found.result.verified:
        assert found.absence_reason is None, found
        return
    assert found.absence_reason == RESTRICTION_FILTERED, found
    assert RESTRICTION_KIND_GEOGRAPHY in found.restriction_filters, found


@obligation(Obligation.PROFILE_014)
def test_an_unknown_non_critical_ext_key_is_ignored_by_the_exchange(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """ext {"acme.trace": "x"} with no ext_critical: the same offers as the plain query."""
    uri = _priced(compose_stack, terms=2)
    plain = offers(fresh_agent, HOME_EXCHANGE, uri)
    with_ext = offers(fresh_agent, HOME_EXCHANGE, uri, ext={"acme.trace": "x"})
    assert len(with_ext) == len(plain) == 2, (len(plain), len(with_ext))


@obligation(Obligation.PROFILE_014, negative=True)
@EXCHANGE_IGNORES_EXT_CRITICAL.mark
def test_an_unknown_critical_ext_key_is_answered_unknown_critical_extension(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """ext_critical ["acme.must"]: answered OK, no offers, UNKNOWN_CRITICAL_EXTENSION."""
    uri = _priced(compose_stack)
    found = one_group_per_uri(
        discover(fresh_agent, HOME_EXCHANGE, [uri], **_CRITICAL), [uri], "critical discovery"
    )[uri]
    EXCHANGE_IGNORES_EXT_CRITICAL.check(
        found.absence_reason == UNKNOWN_CRITICAL_EXTENSION,
        f"absence_reason {found.absence_reason}, {len(found.result.verified)} offers",
    )
    assert not found.result.verified and not found.result.rejected, found


@obligation(Obligation.PROFILE_015)
def test_an_unknown_non_critical_ext_key_is_ignored_by_the_broker(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A Resolve whose ext carries an unknown key that ext_critical does not list resolves normally."""
    uri = _priced(compose_stack)
    request = resolve_request([uri])
    request["ext"] = {"acme.trace": "x"}
    resolved = one_group_per_uri(fresh_agent.broker.resolve(request), [uri], "broker resolve")[uri]
    assert resolved.result.verified, f"Resolve offered nothing for {uri}: {resolved}"


@obligation(Obligation.PROFILE_015, negative=True)
@BROKER_IGNORES_EXT_CRITICAL.mark
def test_the_broker_answers_an_unknown_critical_ext_key_unknown_critical_extension(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A Resolve with ext_critical ["acme.must"]: answered OK, no offer, UNKNOWN_CRITICAL_EXTENSION."""
    uri = _priced(compose_stack)
    request = resolve_request([uri])
    request.update(_CRITICAL)
    result = fresh_agent.broker.resolve(request)
    reasons = {result.absence_reason} | {g.absence_reason for g in result.groups}
    BROKER_IGNORES_EXT_CRITICAL.check(
        UNKNOWN_CRITICAL_EXTENSION in reasons, f"absence reasons {reasons}"
    )
    assert not result.verified(), f"offers came back: {result}"


def _track(name: str) -> str:
    """A track of the music publisher's feed, which the Broker's catalog search indexes."""
    return f"http://{DEMO_MUSIC_DOMAIN}/tracks/{name}.mp3"


def _music_search(agent: FreshAgent, **fields: Any) -> Any:
    request: dict[str, Any] = {"ver": ProtocolVersion}
    request.update(fields)
    return agent.broker.resolve(request)


@obligation(Obligation.PROFILE_007, negative=True)
def test_a_music_filter_is_a_required_constraint(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed pushes the music feed
    fresh_agent: FreshAgent,
) -> None:
    """An ISRC alone names exactly one track; a label no track carries returns NOT_IN_CATALOG."""
    named = _music_search(fresh_agent, search_filters={"music.isrc": "USUAN2300011"})
    assert [g.uri for g in named.groups] == [_track("mesmerizing-galaxy-loop")], named
    unmatched = _music_search(fresh_agent, search_filters={"music.instruments": "ukulele"})
    assert unmatched.groups == [], unmatched
    assert unmatched.absence_reason == NOT_IN_CATALOG, unmatched


@obligation(Obligation.PROFILE_008)
def test_explicit_uris_come_before_a_malformed_filter(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed pushes the music feed
    fresh_agent: FreshAgent,
) -> None:
    """uris and "music.bpm_range": "fast": the URI is resolved and the filter ignored."""
    uri = _track("strength-of-the-titans")
    result = _music_search(fresh_agent, uris=[uri], search_filters={"music.bpm_range": "fast"})
    found = one_group_per_uri(result, [uri], "resolve of a named track")[uri]
    assert found.result.verified, found


@obligation(Obligation.PROFILE_008, negative=True)
@pytest.mark.parametrize(
    "filters",
    [{"music.bpm_range": "fast"}, {"music.tempo": "fast"}],
    ids=["malformed-value", "no-defined-key"],
)
def test_a_search_with_a_bad_value_or_no_input_is_refused(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed pushes the music feed
    fresh_agent: FreshAgent,
    filters: dict[str, Any],
) -> None:
    """No uris and no query: a malformed value, or only an undefined key, is invalid_argument."""
    with pytest.raises(CallError) as refused:
        _music_search(fresh_agent, search_filters=filters)
    assert refused.value.code == "invalid_argument", (
        f"refused as {refused.value.code!r}: {refused.value}"
    )
