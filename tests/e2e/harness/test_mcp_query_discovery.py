"""Music discovery by free-text query, end to end through the MCP endpoint.

An agent calls ``fora_discover`` with a query and no URL. The identity service
relays it to the Broker, which has catalog search enabled in this stack: the
Broker sends the query to the model, matches the returned tags against its
index of the music feed, and routes every matched URI to exchange-b through the
normal per-URL path. The model is the ``fake-bedrock`` service, which answers
the Broker's real Converse calls from ``fixtures/bedrock_script.json``; every
other component is the real one.

What reaches the agent is checked against the feed the seed ingested:

* the best-matching tracks, in the Broker's order, each stamped SEARCH;
* all three license alternatives per track, none rejected by the MCP
  adapter's offer verifier (an offer whose signature does not verify is listed
  under ``rejected`` instead of ``offers``);
* the track's ``ext`` metadata and each term's price, document reference,
  restrictions and obligation texts, unchanged.

The failure paths assert the absence reason the agent receives. A named URL,
a track for the broadcast follow-up or a text resource, still takes the URL
path and reads EXCHANGE.

``search_filters`` (the fora-music-v1 profile) is checked the same way: alone
as an exact ISRC lookup, and next to a query, where each filter must remove a
track the query alone would return.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from .conftest import StackURLs
from .e2e_catalog import DEMO_MUSIC_DOMAIN
from .mcp_client import mcp_session
from .seed import SeededFixture

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_FIXTURES = Path(__file__).resolve().parent / "fixtures"
_SCRIPT: dict[str, Any] = json.loads((_FIXTURES / "bedrock_script.json").read_text())
_FEED = {
    record["path"]: record
    for record in (
        json.loads(line)
        for line in (_FIXTURES / "catalog" / "music.jsonl").read_text().splitlines()
        if line.strip()
    )
}

_SEARCH = "DISCOVERY_METHOD_SEARCH"
_EXCHANGE = "DISCOVERY_METHOD_EXCHANGE"
_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"
_TEMPORARILY_UNAVAILABLE = "OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE"


def _track(name: str) -> str:
    return f"http://{DEMO_MUSIC_DOMAIN}/tracks/{name}.mp3"


# Expected results follow the Broker's ranking over the feed's tags: only the
# tracks matching the most facets are returned, ordered by tags matched, then
# URI.
_MATCHES = {
    # adeste-fideles: calm + vlog, two facets. mesmerizing-galaxy is vlog only,
    # one facet, so it is left out. ancient-rite is calm but has vocals, so the
    # exclusion removes it.
    "calm background music, no vocals, for my personal YouTube travel vlog": [
        _track("adeste-fideles-shorter"),
    ],
    # Paraphrases of the request above: the same extraction, the same result.
    "For my personal YouTube travel vlog, find calm music without vocals": [
        _track("adeste-fideles-shorter"),
    ],
    "I need calm music for a travel vlog, with no singing": [
        _track("adeste-fideles-shorter"),
    ],
    # Exclusion only: every track marked vocals:false passes with score 0, so
    # the order is by URI; ancient-rite has vocals.
    "music with no vocals": [
        _track("adeste-fideles-shorter"),
        _track("mesmerizing-galaxy-loop"),
        _track("strength-of-the-titans"),
    ],
    # strength-of-the-titans: epic + soundtrack + documentary/trailer, three
    # facets. ancient-rite (soundtrack + documentary) matches two and is left out.
    "epic soundtrack for a documentary trailer": [
        _track("strength-of-the-titans"),
    ],
    # mesmerizing-galaxy: fast (124 BPM) + electronica + podcast, three facets.
    # ancient-rite (podcast only) is left out.
    "fast electronic track for a podcast intro": [
        _track("mesmerizing-galaxy-loop"),
    ],
    # Only adeste-fideles (105 BPM, calm, 60 s) passes the numeric bounds;
    # ancient-rite is calm but 70 BPM.
    "calm music at 100-120 BPM, lasting 60-90 seconds": [
        _track("adeste-fideles-shorter"),
    ],
}


async def _discover(compose_stack: StackURLs, bearer: str, **arguments: Any) -> dict[str, Any]:
    """Call fora_discover with these tool arguments and return its structured result.

    An argument passed as None is left out of the call.
    """
    arguments = {name: value for name, value in arguments.items() if value is not None}
    async with mcp_session(compose_stack, bearer, timeout=60) as mcp:
        return (await mcp.call("fora_discover", arguments)).structured


# The restriction each feed field becomes, and which of its two lists it fills.
_RESTRICTION_FIELDS = {
    "RESTRICTION_KIND_FUNCTION": ("functions", "prohibited_functions"),
    "RESTRICTION_KIND_USER_TYPE": ("user_types", None),
    "RESTRICTION_KIND_GEOGRAPHY": ("geos", None),
}


def _restrictions(
    restrictions: list[dict[str, Any]],
) -> dict[str, tuple[list[str], list[str]]]:
    """Restriction kind -> (permitted, prohibited), each sorted."""
    return {
        r["kind"]: (sorted(r.get("permitted", [])), sorted(r.get("prohibited", [])))
        for r in restrictions
    }


def _expected_restrictions(term: dict[str, Any]) -> dict[str, tuple[list[str], list[str]]]:
    """What the feed term's restriction fields should arrive as, kind by kind."""
    out = {}
    for kind, (permitted, prohibited) in _RESTRICTION_FIELDS.items():
        allowed = sorted(term.get(permitted, []))
        denied = sorted(term.get(prohibited, [])) if prohibited else []
        if allowed or denied:
            out[kind] = (allowed, denied)
    return out


def _assert_three_alternatives(group: dict[str, Any]) -> None:
    """All three terms of the track arrive, verified, carrying the feed's data."""
    record = _FEED[group["uri"].removeprefix(f"http://{DEMO_MUSIC_DOMAIN}")]
    assert not group.get("rejected"), f"offers failed verification: {group['rejected']}"
    offers = group["offers"]
    assert len(offers) == 3, f"{group['uri']}: {len(offers)} offers, want all three alternatives"
    # The offer states its price once, in its own pricing; the term it sells
    # carries none (the protocol's offer.terms.pricing_unset rule).
    by_rate = {offer["pricing"]["rate"]: offer for offer in offers}
    for term in record["terms"]:
        rate = term["pricing"]["rate"]
        assert rate in by_rate, f"{group['uri']}: no offer for the EUR {rate} alternative"
        offer = by_rate[rate]
        assert offer["ext"] == record["ext"], f"ext metadata changed on the way: {offer['ext']}"
        assert offer["identity"]["canonical_url"] == group["uri"], offer["identity"]
        (sold,) = offer["terms"]
        assert "pricing" not in sold, f"the offer's term repeats a price: {sold['pricing']}"
        assert offer["pricing"]["currency"] == term["pricing"]["currency"], offer["pricing"]
        assert sold["license"]["uri"] == record["license"]["uri"], sold["license"]
        assert sold["license"]["uri_digest"] == record["license"]["uri_digest"], sold["license"]
        details = [o["detail"] for o in sold.get("obligations", [])]
        assert details == [o["detail"] for o in term["obligations"]], details
        # Kind by kind, with permitted and prohibited kept apart: a prohibited
        # use that arrives as permitted must fail here.
        assert _restrictions(sold.get("restrictions", [])) == _expected_restrictions(term), (
            f"restrictions changed on the way: {sold.get('restrictions')}"
        )


@pytest.mark.parametrize("query", list(_MATCHES))
async def test_query_returns_matched_tracks_with_all_alternatives(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
    query: str,
) -> None:
    result = await _discover(identity_ready, mcp_bearer, query=query)
    groups = result["offer_groups"]
    assert [g["uri"] for g in groups] == _MATCHES[query], result
    for group in groups:
        assert group.get("discovery_method") == _SEARCH, group
        assert group["licensed"] is True, group
        _assert_three_alternatives(group)


@pytest.mark.parametrize(
    ("query", "reason"),
    [
        ("news article about interest rates", _NOT_IN_CATALOG),
        # Numeric bounds with no known tag match nothing.
        ("music at 100-120 BPM", _NOT_IN_CATALOG),
        ("cheerful ukulele music", _NOT_IN_CATALOG),
        ("the model fails for this query", _TEMPORARILY_UNAVAILABLE),
        ("the model is too slow for this query", _TEMPORARILY_UNAVAILABLE),
        ("the model answers this query with prose", _TEMPORARILY_UNAVAILABLE),
    ],
)
async def test_query_without_result_reports_why(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
    query: str,
    reason: str,
) -> None:
    assert query in _SCRIPT, f"the fake model has no scripted answer for {query!r}"
    result = await _discover(identity_ready, mcp_bearer, query=query)
    assert result["offer_groups"] == [], result
    assert result.get("absence_reason") == reason, result


@pytest.mark.parametrize(
    "uri",
    [
        # The broadcast follow-up: the agent names the track it already chose.
        _track("strength-of-the-titans"),
        # Text discovery by URL is unchanged by catalog search. The lyrics carry a
        # metered term.
        f"http://{DEMO_MUSIC_DOMAIN}/lyrics/midnight-harbour.txt",
    ],
)
async def test_named_url_takes_priority_over_the_query(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
    uri: str,
) -> None:
    result = await _discover(
        identity_ready, mcp_bearer, uris=[uri], query="news article about interest rates"
    )
    groups = result["offer_groups"]
    assert [g["uri"] for g in groups] == [uri], result
    assert groups[0].get("discovery_method") == _EXCHANGE, groups[0]
    assert groups[0]["offers"], groups[0]
    if "/tracks/" in uri:
        _assert_three_alternatives(groups[0])


# Filtered searches. Each expected list is the exact result, in the Broker's
# order, so a filter that is ignored or applied too widely fails the test.
_FILTERED = [
    # No query at all: the ISRC alone names one track.
    pytest.param(
        None,
        {"music.isrc": "USUAN2300011"},
        [_track("mesmerizing-galaxy-loop")],
        id="isrc-without-query",
    ),
    # adeste-fideles also has a cello and no vocals; only the BPM bound
    # (105 > 90) removes it.
    pytest.param(
        "epic soundtrack for a documentary trailer",
        {"music.instruments": "cello", "music.bpm_range": "-90", "music.vocals": False},
        [_track("strength-of-the-titans")],
        id="query-with-instrument-bpm-vocals",
    ),
    # The query alone returns strength-of-the-titans too (85 BPM); the filter
    # must remove it.
    pytest.param(
        "music with no vocals",
        {"music.bpm_range": "100-"},
        [_track("adeste-fideles-shorter"), _track("mesmerizing-galaxy-loop")],
        id="bpm-range-removes-a-query-match",
    ),
]


@pytest.mark.parametrize(("query", "filters", "expected"), _FILTERED)
async def test_search_filters_narrow_the_result(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
    query: str | None,
    filters: dict[str, Any],
    expected: list[str],
) -> None:
    assert query is None or query in _SCRIPT, f"the fake model has no scripted answer for {query!r}"
    result = await _discover(identity_ready, mcp_bearer, query=query, search_filters=filters)
    groups = result["offer_groups"]
    assert [g["uri"] for g in groups] == expected, result
    for group in groups:
        assert group.get("discovery_method") == _SEARCH, group
        _assert_three_alternatives(group)


async def test_search_filters_without_a_match_report_not_in_catalog(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
) -> None:
    # A well-formed label that no track in the feed carries.
    result = await _discover(
        identity_ready, mcp_bearer, search_filters={"music.instruments": "ukulele"}
    )
    assert result["offer_groups"] == [], result
    assert result.get("absence_reason") == _NOT_IN_CATALOG, result


async def test_named_url_ignores_a_malformed_filter(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
) -> None:
    uri = _track("strength-of-the-titans")
    result = await _discover(
        identity_ready, mcp_bearer, uris=[uri], search_filters={"music.bpm_range": "fast"}
    )
    groups = result["offer_groups"]
    assert [g["uri"] for g in groups] == [uri], result
    assert groups[0].get("discovery_method") == _EXCHANGE, groups[0]
    _assert_three_alternatives(groups[0])


async def test_malformed_filter_is_a_tool_error(
    identity_ready: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,
) -> None:
    """A filter value the profile refuses is a tool error.

    fora_discover's input schema leaves search_filters open, so the profile check
    in the tool's handler is what refuses the value. The service sends the tool
    error as text alone, with no structured content, so the refusal is asserted as
    a tool error and its wording is not.
    """
    async with mcp_session(identity_ready, mcp_bearer, timeout=60) as mcp:
        # A number, where the profile requires a "MIN-MAX" string.
        await mcp.refused("fora_discover", {"search_filters": {"music.bpm_range": 90}})
