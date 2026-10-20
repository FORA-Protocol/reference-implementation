"""Obligations MONEY-005 and DELIVERY-008: an expired offer and an expired delivery URL.

The contract is the pinned module ``github.com/FORA-Protocol/protocol``:

* ``fora.proto``: "``expires_at`` is signed so the offer's validity window is
  integrity-protected: a relaying Broker cannot extend (or shorten) the TTL of a
  signed offer to replay it outside the window the Exchange intended."
  (MONEY-005). An offer presented after its expiry is denied, and one presented
  before it is honoured;
* the edge function's signed-URL verification page: "The edge rejects a URL whose
  ``exp`` has passed, using its own clock." (DELIVERY-008).

THE TIME. Nothing in the stack runs on a test clock (ADR-028 D6). An Exchange
issues offers and delivery URLs that live five minutes, so each test shortens the
lifetimes of one seat through the Exchange's e2e-only control service
(``harness.e2e_controls``), takes what that seat issues while the lifetimes are
short, and polls until the expiry signed into it has passed. The control only
shortens a lifetime, and the expiry is signed into the offer or the URL, so the
test exercises the same expiry checks production runs.

THE SURFACE. Discovery and purchase are the SDK's verbs at the issuing Exchange, as
the test's own funded agent. A delivery URL is fetched through the SDK's ``fetch``,
which presents the agent's proof of possession, at the Cloudflare and Fastly edges.
On Lambda@Edge the URL is minted by ``harness.lambda_edge`` with exchange-a's own
delivery key, because CloudFront always presents an https URL and this stack's
Exchanges mint http ones (``test_lambda_edge.py`` gives the full reason); it is
signed exactly as the Exchange signs, with a short expiry, and presented in the
CloudFront viewer-request event.
"""

from __future__ import annotations

import time
from datetime import datetime
from http import HTTPStatus
from urllib.parse import parse_qs, urlsplit

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import VerifiedOffer

from ..admin_plane import require_admin_plane
from ..e2e_catalog import CANARY_MARKER
from ..e2e_controls import short_lifetimes
from ..edge_fetch import fetch_signed
from ..fresh_agent import FreshAgent
from ..lambda_edge import (
    ARTICLE_PATH,
    PUBLISHER,
    ExchangeDeliveryKey,
    invoke,
    load_exchange_delivery_key,
    mint_delivery_url,
    proof_of_possession,
    viewer_request_event,
)
from ..money_path import (
    ISSUER,
    PAPER_SATELLITES,
    THALES,
    assert_bought,
    assert_denied,
    buy_at_issuer,
    charged,
    discover_offer,
    only_item,
)
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The lifetimes a test gives one seat: long enough to buy or fetch once before the
#: expiry, short enough to wait out.
_OFFER_TTL_S = 8
_URL_TTL_S = 10

#: The edge compares whole seconds; waiting one past the expiry leaves no doubt.
_PAST_EXPIRY_S = 1.0

_OFFER_EXPIRED = "DENIAL_REASON_OFFER_EXPIRED"
_EDGE_EXPIRED = "expired"


@pytest.fixture(autouse=True)
def _control_plane() -> None:
    require_admin_plane()


@pytest.fixture(scope="module")
def exchange_key(compose_stack: StackURLs) -> ExchangeDeliveryKey:
    """exchange-a's delivery-URL key, confirmed to be the one it publishes."""
    return load_exchange_delivery_key(compose_stack.exchange)


def _wait_past(expiry: float, what: str) -> None:
    """Return once this clock, which the stack's containers share, is past ``expiry``."""
    deadline = max(expiry - time.time(), 0.0) + 10.0
    poll_until(
        lambda: time.time() >= expiry + _PAST_EXPIRY_S,
        deadline,
        failure=lambda: f"{what} at {expiry} had not passed",
        interval_s=0.5,
    )


def _url_expiry(url: str) -> float:
    values = parse_qs(urlsplit(url).query).get("exp", [])
    assert len(values) == 1 and values[0].isdigit(), f"the URL carries no single exp: {url}"
    return float(values[0])


def _offer_expiry(offer: VerifiedOffer) -> float:
    raw = offer.offer.get("expires_at")
    assert isinstance(raw, str), f"the offer carries no expires_at: {offer.offer}"
    return datetime.fromisoformat(raw).timestamp()


@obligation(Obligation.MONEY_005, negative=True)
def test_an_offer_bought_after_its_expiry_is_denied_and_one_bought_before_is_charged(
    fresh_agent: FreshAgent,
) -> None:
    """Two offers from a seat issuing eight-second offers: one bought in time, one too late.

    Both are discovered while exchange-b issues offers that expire eight seconds
    later. The first is bought at once: it is bought, and the balance moves by its
    cost. Then the test waits until the second offer's signed expires_at has
    passed and buys it: the item is denied DENIAL_REASON_OFFER_EXPIRED in the body,
    carries no delivery URL, and the balance does not move.
    """
    uri = PAPER_SATELLITES
    issuer = ISSUER[uri]
    fresh_agent.fund(issuer)
    with short_lifetimes(issuer, offer_seconds=_OFFER_TTL_S) as in_force:
        assert in_force.offer_ttl_seconds == _OFFER_TTL_S, in_force
        in_time = discover_offer(fresh_agent, uri)
        too_late = discover_offer(fresh_agent, uri)
    expiry = _offer_expiry(too_late)
    assert expiry <= time.time() + _OFFER_TTL_S, (
        f"the offer expires at {expiry}, not within seconds"
    )

    before = fresh_agent.balances(issuer)[issuer]
    item = only_item(fresh_agent.exchange(issuer).execute(in_time), "the purchase in time")
    assert_bought(item, "the purchase before expires_at")
    currency, cost = charged(item)
    paid = fresh_agent.balances(issuer)[issuer]
    assert paid[currency] == before[currency] - cost, (
        f"the purchase in time moved {currency} from {before[currency]} to {paid[currency]}"
    )

    _wait_past(expiry, "the offer's expires_at")
    late = only_item(fresh_agent.exchange(issuer).execute(too_late), "the purchase too late")

    assert_denied(late, _OFFER_EXPIRED, "the purchase after expires_at")
    assert fresh_agent.balances(issuer)[issuer] == paid, "the denied purchase moved money"


@obligation(Obligation.DELIVERY_008, negative=True)
@pytest.mark.parametrize(
    ("uri", "marker"),
    [(THALES, CANARY_MARKER), (PAPER_SATELLITES, "Paper Satellites")],
    ids=["cloudflare", "fastly"],
)
def test_an_exchange_issued_url_fetched_after_its_exp_is_refused_expired(
    fresh_agent: FreshAgent, uri: str, marker: str
) -> None:
    """The buyer's URL serves the buyer before its exp, and nobody after it.

    The URL is bought while the issuing seat mints ten-second delivery URLs. The
    control is the buyer's own fetch, with its proof of possession, right away: the
    publisher's bytes are delivered. Once the URL's exp has passed, the same fetch
    with a fresh valid proof is refused 403 ``expired``, with no content.
    """
    issuer = ISSUER[uri]
    with short_lifetimes(issuer, url_seconds=_URL_TTL_S) as in_force:
        assert in_force.url_ttl_seconds == _URL_TTL_S, in_force
        url = str(buy_at_issuer(fresh_agent, uri).retrieval_endpoint)
    expiry = _url_expiry(url)
    assert expiry <= time.time() + _URL_TTL_S, f"the URL expires at {expiry}, not within seconds"

    fetch_signed(url, fresh_agent.stack, key_path=fresh_agent.key_path, expect_marker=marker)
    _wait_past(expiry, "the URL's exp")
    with pytest.raises(CallError) as refused:
        fetch_signed(url, fresh_agent.stack, key_path=fresh_agent.key_path)

    assert refused.value.status == HTTPStatus.FORBIDDEN, refused.value
    assert refused.value.reason == _EDGE_EXPIRED, refused.value


@obligation(Obligation.DELIVERY_008, negative=True)
@pytest.mark.xdist_group("lambda-edge")
def test_a_url_presented_at_lambda_edge_after_its_exp_is_refused_expired(
    compose_stack: StackURLs, exchange_key: ExchangeDeliveryKey, fresh_agent: FreshAgent
) -> None:
    """The same expiry on Lambda@Edge: served before exp, a generated 403 ``expired`` after.

    The URL is bound to the agent and expires ten seconds after it is minted. The
    control, presented with the agent's proof right away, is handed back to
    CloudFront for the origin. Presented again with a fresh proof once its exp has
    passed, it comes back as a generated refusal, and the origin is never asked.
    """
    url = mint_delivery_url(
        exchange_key,
        f"https://{PUBLISHER}{ARTICLE_PATH}",
        agent_id=fresh_agent.thumbprint,
        ttl_seconds=_URL_TTL_S,
    )
    served = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(url, headers=proof_of_possession(url, fresh_agent.key_path)),
    )
    assert served.pass_through, f"the URL was not served before its exp: {served}"

    _wait_past(_url_expiry(url), "the URL's exp")
    refused = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(url, headers=proof_of_possession(url, fresh_agent.key_path)),
    )

    assert not refused.pass_through, f"an expired URL was handed to the origin: {refused}"
    assert refused.status == str(HTTPStatus.FORBIDDEN.value), refused
    assert refused.json_body().get("reason") == _EDGE_EXPIRED, refused
