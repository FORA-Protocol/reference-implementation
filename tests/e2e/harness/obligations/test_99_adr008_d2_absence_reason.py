"""ADR-008 D2 — every empty-offers DiscoverResources/Discover response carries
a structured ``OfferAbsenceReason`` enum at the request level.

Anchor docs:
* ``docs/architecture/adr-008-testing-surface-isolation.md`` D2 —
  "every empty result carries a structured reason; tests assert on the
  enum, not on length-zero or truthiness."
* ``docs/architecture/adr-004-protocol-layers.md`` — refusal is the
  product; a refusal without a reason is a half-implementation.
* ``github.com/FORA-Protocol/protocol/fora/v1/fora.proto`` — ``OfferAbsenceReason`` (request-level
  vocabulary) and ``ResourceResponse.absence_reason_oneof``.

This module exercises the wire surface for the request-level vocabulary
the absence-reason task added (UNKNOWN_RESOURCE, NO_AGENT_ENTITLEMENT,
GRANTS_DO_NOT_COVER, etc.). It deliberately sticks to the easiest
asserted-on-enum case — UNKNOWN_RESOURCE for a never-seeded URI —
because the harder causes (BILLING_BLOCK, OUTSTANDING_OBLIGATIONS,
SUBSCRIPTION_EXPIRED, INTERNAL_ERROR) are exhaustively unit-tested at
the helper boundary in
``src/exchange/internal/service/absence_reason_test.go`` and
``src/broker/internal/transport/resolve_gate_test.go``.

The unit tests at the helper boundary are what guard the per-cause
mapping; this E2E test guards that the wired call site stamps the
enum on the wire response when the path is exercised end-to-end.
"""

from __future__ import annotations

import uuid

import pytest

from ..broker_purchase import discover_group
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..seed import DEMO_PHILOSOPHY_DOMAIN

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def test_unknown_uri_discover_resources_sets_not_in_catalog_enum(fresh_agent: FreshAgent) -> None:
    """DiscoverResources on a never-seeded URI MUST stamp NOT_IN_CATALOG.

    Asserts on the SPECIFIC enum value (NOT len(offers)==0 / truthiness),
    per ADR-008 D2: "tests assert on the enum, not on length-zero or
    truthiness." A regression that drops the absence_reason field, or
    collapses every cause to one value, fails this test at the wire boundary.
    One that emits ``OFFER_ABSENCE_REASON_UNSPECIFIED`` fails it inside the SDK
    client: the generated enum has no such member, so the strict response check
    refuses the answer before the test reads it.

    (W1 of the proto-rename wave renamed the never-seeded-URI cause from the
    project-specific ``OFFER_ABSENCE_REASON_UNKNOWN_RESOURCE`` to the
    canonical FORA-protocol ``OFFER_ABSENCE_REASON_NOT_IN_CATALOG``.)
    """
    never_seeded = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/never-seeded-{uuid.uuid4().hex}.txt"
    group = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN),
        EXCHANGE_A_DOMAIN,
        never_seeded,
        "never-seeded discovery",
    )

    want = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"
    assert not group.result.verified, f"a never-seeded URI carried offers: {group}"
    assert group.absence_reason == want, (
        f"DiscoverResources on never-seeded URI did not carry absence_reason={want!r}; "
        f"observed {group.absence_reason!r} — obligation: 'every empty result carries a "
        f"structured reason'"
    )
