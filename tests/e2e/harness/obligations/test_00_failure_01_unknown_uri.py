"""Obligation PAID-005: a URI unknown to the platform gets no offer and a reason.

Driven against a never-seeded URI on the demo philosophy domain (the demo
catalog the real ingester produced has no entry for it): a DiscoverResources
call answers with no offer for the URI AND a structured
``OFFER_ABSENCE_REASON_NOT_IN_CATALOG`` explanation (ADR-008 D2).
"""

from __future__ import annotations

import uuid

import pytest

from ..broker_purchase import discover_group
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"


@obligation(Obligation.PAID_005, negative=True)
def test_unknown_uri_browse_returns_no_offer_with_explanation(fresh_agent: FreshAgent) -> None:
    """DiscoverResources on a never-seeded demo URI answers no offer and the reason.

    The call goes out through the test's own agent's SDK client, which refuses
    anything but a successful answer that passes the strict response schema.
    """
    unknown_uri = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/never-seeded-{uuid.uuid4().hex}.txt"
    group = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN),
        EXCHANGE_A_DOMAIN,
        unknown_uri,
        "unknown-URI discovery",
    )
    assert not group.result.verified, (
        f"unknown-URI DiscoverResources must return no offers; got {group}"
    )
    assert group.absence_reason == _NOT_IN_CATALOG, (
        f"absence_reason for an unknown URI must be {_NOT_IN_CATALOG!r}; got {group}"
    )
