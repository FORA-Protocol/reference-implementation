"""Obligation BROWSE-001: discovery for one URI returns its offers with prices and terms.

R1 scope: subscription offers and bundles are deferred; "all applicable
offers" means the per-request Offer the Exchange materialises for the
matching term. Driven against the demo ``epicurus`` resource (single PER_UNIT
USD term). The test asserts the offer surfaces with non-empty pricing and a
non-empty Exchange-minted signature — the data agents need to evaluate cost
and commit later via ExecuteTransaction.
"""

from __future__ import annotations


from typing import Any, cast

import pytest

from ..broker_purchase import discover_group
from ..constants import requester
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"


@obligation(Obligation.BROWSE_001)
def test_single_uri_browse_returns_offers_with_prices_and_terms(fresh_agent: FreshAgent) -> None:
    """DiscoverResources on a single demo URI returns its offer with full terms.

    (a) is the SDK client's own guarantee: it returns only a successful answer that
    passed the strict response schema, and every offer it returns as verified
    carries a signature that checked out against the Exchange's published key.
    """
    group = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN),
        EXCHANGE_A_DOMAIN,
        _RESOURCE_URI,
        "single-URI browse",
        requester=requester(fresh_agent.agent_id, user_type="individual", geography="US"),
    )
    offers = [verified.offer for verified in group.result.verified]
    # (b)
    assert offers, f"DiscoverResources must return at least one offer; got {group}"

    # (c) each surfaced Offer carries pricing.rate (number), pricing.currency
    # (ISO 4217), and a non-empty Exchange-minted signature.
    for offer in offers:
        offer = cast(dict[str, Any], offer)
        offer_id = offer.get("offer_id")
        pricing = cast(dict[str, Any], offer.get("pricing") or {})
        rate = pricing.get("rate")
        currency = pricing.get("currency")
        signature = offer.get("signature")
        # Money-as-string: pricing.rate rides as a decimal STRING on the
        # wire (discover.go FormatMoney). Assert it is present and parses to a
        # number (the "plain number" invariant), not that it is a JSON number.
        assert isinstance(rate, (int, float, str)) and not isinstance(rate, bool), (
            f"offer {offer_id!r} must carry a numeric pricing.rate; got {rate!r}"
        )
        try:
            float(rate)
        except (TypeError, ValueError) as exc:
            msg = f"offer {offer_id!r} pricing.rate {rate!r} is not a plain number"
            raise AssertionError(msg) from exc
        assert (
            isinstance(currency, str)
            and len(currency) == 3
            and currency.isalpha()
            and currency.isupper()
        ), f"offer {offer_id!r} pricing.currency must be 3-letter ISO 4217; got {currency!r}"
        assert isinstance(signature, str) and signature, (
            f"offer {offer_id!r} must carry a non-empty Exchange-minted signature; got {signature!r}"
        )
