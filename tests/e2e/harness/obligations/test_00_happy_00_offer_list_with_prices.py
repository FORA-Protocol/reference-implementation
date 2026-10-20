"""Obligation PAID-001: discovery returns the ways to pay, with prices and currencies.

Driven against the demo ``epicurus`` resource — a single PER_UNIT USD term
(0.0001/characters, individual, US/GB) the real ingester produced. A single
DiscoverResources call surfaces the per-request offer with its term-derived
price and ISO-4217 currency as plain numbers.

Assertions:
(a) one DiscoverResources call for the URI is answered successfully.
(b) the per-request offer for the URI surfaces.
(c) the offer carries ``pricing.rate`` (double) and ``pricing.currency``
    (3-letter ISO 4217).
(d) ``pricing.rate`` is a plain number, not locale- or currency-formatted.
Scope boundary: no SUBSCRIPTION offer surfaces; subscription access is not part
of this obligation.
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

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


# epicurus: PER_UNIT 0.0001/characters USD; individual; geo[US,GB].
_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"
_EXPECTED_RATE = 0.0001
_CURRENCY = "USD"


def _coerce_rate(raw: object) -> float:
    """Parse canonical ``pricing.rate`` enforcing the 'plain numbers' invariant."""
    if isinstance(raw, bool):
        msg = f"pricing.rate must not be a JSON boolean: {raw!r}"
        raise AssertionError(msg)
    if isinstance(raw, (int, float)):
        return float(raw)
    if isinstance(raw, str):
        try:
            return float(raw)
        except ValueError as exc:
            msg = f"pricing.rate {raw!r} is not a plain number"
            raise AssertionError(msg) from exc
    msg = f"pricing.rate must be a JSON number or proto-JSON double string, got {raw!r}"
    raise AssertionError(msg)


@obligation(Obligation.PAID_001)
def test_paid_resource_offer_list_with_plain_prices_and_currencies(
    fresh_agent: FreshAgent,
) -> None:
    """DiscoverResources on a paid demo URI returns the per-request offer with plain prices."""
    # (a): the SDK client returns only a successful answer that passed the strict
    # response schema.
    group = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN),
        EXCHANGE_A_DOMAIN,
        _RESOURCE_URI,
        "paid-URI discovery",
        requester=requester(fresh_agent.agent_id, user_type="individual", geography="US"),
    )
    offers = [verified.offer for verified in group.result.verified]

    # (b)
    assert offers, f"expected a per-request offer for {_RESOURCE_URI!r}; got {group}"

    # scope boundary — no SUBSCRIPTION offers
    subscription_offers = [o for o in offers if o.get("subscription_id")]
    assert not subscription_offers, f"PAID-001 scopes subscription out; got {subscription_offers}"

    spot = cast(dict[str, Any], offers[0])
    pricing = cast(dict[str, Any], spot.get("pricing") or {})
    # (c) + (d)
    rate = _coerce_rate(pricing.get("rate"))
    assert rate == pytest.approx(_EXPECTED_RATE), (
        f"per-request offer should echo the term rate {_EXPECTED_RATE}, got {rate} from {spot}"
    )
    currency = pricing.get("currency")
    assert (
        isinstance(currency, str)
        and len(currency) == 3
        and currency.isalpha()
        and currency.isupper()
    ), f"currency must be a 3-letter uppercase ISO 4217 code, got {currency!r}"
    assert currency == _CURRENCY, f"currency should echo {_CURRENCY!r}, got {currency!r}"
    assert isinstance(rate, float)
