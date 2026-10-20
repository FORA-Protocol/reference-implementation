"""Obligation BROWSE-003: an unrecognised URI gets an empty offer list and a reason.

The PAID-005 test covers the same observable from the paid-access perspective;
this one grounds it in browsing without committing.
Driven against a never-seeded URI on the demo music domain.
"""

from __future__ import annotations

import uuid

import pytest

from ..broker_purchase import discover_group
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..seed import DEMO_MUSIC_DOMAIN
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"


@obligation(Obligation.BROWSE_003, negative=True)
def test_unknown_uri_browse_empty_with_explanation(fresh_agent: FreshAgent) -> None:
    """DiscoverResources for an unknown demo URI answers no offer and the reason."""
    unknown_uri = f"http://{DEMO_MUSIC_DOMAIN}/lyrics/never-seeded-{uuid.uuid4().hex}.txt"
    group = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN),
        EXCHANGE_A_DOMAIN,
        unknown_uri,
        "unknown-URI browse",
    )
    assert not group.result.verified, f"browse on an unknown URI must return no offers; got {group}"
    assert group.absence_reason == _NOT_IN_CATALOG, (
        f"absence_reason for an unknown URI must be {_NOT_IN_CATALOG!r}; got {group}"
    )
