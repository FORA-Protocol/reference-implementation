"""The edge discovery headers X-Content-Rules and X-FORA-Exchange, on every edge runtime.

EDGEHINT-001 to EDGEHINT-006, from the pinned protocol module
``github.com/FORA-Protocol/protocol`` (``fora.proto``, the discovery headers an
edge sends on a 403 to a request for licensed content without a valid signed
URL).

Each case runs on Cloudflare, Fastly and Lambda@Edge through
``harness.edge_runtimes``, which says how each is reached and why the CloudFront
RSA stand-in is not one of them. Every reader returns the answer's headers.

X-Content-Rules is compared with the manifest URL of the refused host, under the
scheme the request used: fora.proto admits "http://" on a deployment that serves
plaintext, which is what this stack's Cloudflare and Fastly publishers do, while
Lambda@Edge sees the https request CloudFront terminated. Each header's shape is
judged by the SDK's own discovery-hint reader (``fora_sdk.parse_discovery_hint``),
and the hinted Exchange is checked against the manifest by the SDK's
``reconcile_discovery_hint``.

The two defects here are in the edge app all three runtimes share, so each keeps
one Defect and marks every runtime's case.
"""

from __future__ import annotations

import pytest
from fora_sdk import parse_discovery_hint, reconcile_discovery_hint
from wire.models import WellKnownManifest

from ..admin_plane import require_admin_plane
from ..constants import AI_BOT_UA, BROWSER_UA
from ..defects import EDGE_EXCHANGE_HINT_NOT_BARE, EDGE_REFUSAL_WITHOUT_HINT
from ..edge_runtimes import (
    RUNTIMES,
    EdgeReply,
    EdgeRuntime,
    assert_served,
    expired_url,
    read,
    tampered_url,
)
from ..fresh_agent import FreshAgent
from ..lambda_edge import ExchangeDeliveryKey, load_exchange_delivery_key
from ..obligation_registry import Obligation, obligation
from ..protocol_documents import parse_document
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

CONTENT_RULES = "X-Content-Rules"
EXCHANGE_HINT = "X-FORA-Exchange"

_EACH_RUNTIME = pytest.mark.parametrize("runtime", RUNTIMES, ids=str)


def _each_runtime_marked(defect_marker: pytest.MarkDecorator) -> pytest.MarkDecorator:
    """Every runtime, each case marked with the shared edge app's defect."""
    return pytest.mark.parametrize(
        "runtime",
        [pytest.param(runtime, id=str(runtime), marks=defect_marker) for runtime in RUNTIMES],
    )


@pytest.fixture(scope="module")
def exchange_key(compose_stack: StackURLs) -> ExchangeDeliveryKey:
    """exchange-a's delivery-URL key, confirmed to be the one it publishes."""
    return load_exchange_delivery_key(compose_stack.exchange)


def _bot_refusal(stack: StackURLs, runtime: EdgeRuntime, query: str = "") -> EdgeReply:
    reply = read(stack, runtime, runtime.article + query, headers={"User-Agent": AI_BOT_UA})
    assert reply.status == 403, f"{runtime}: the AI bot was answered {reply.status}"
    return reply


def _manifest(stack: StackURLs, runtime: EdgeRuntime) -> tuple[EdgeReply, WellKnownManifest]:
    reply = read(stack, runtime, runtime.manifest_url)
    assert reply.status == 200, f"{runtime}: the manifest was answered {reply.status}"
    return reply, parse_document(WellKnownManifest, reply.body)


@obligation(Obligation.EDGEHINT_001, Obligation.EDGEHINT_002, Obligation.EDGEHINT_005)
@_EACH_RUNTIME
def test_the_bot_refusal_points_at_the_refused_hosts_manifest(
    compose_stack: StackURLs, runtime: EdgeRuntime
) -> None:
    """An AI bot's 403 for a page with a query: X-Content-Rules is exactly the host's manifest URL.

    The query and path of the refused request do not leak into the header, the
    SDK's reader judges it well formed, and the Exchange hint, when sent, travels
    with X-Content-Rules.
    """
    reply = _bot_refusal(compose_stack, runtime, "?page=2")
    assert reply.header(CONTENT_RULES) == runtime.manifest_url, reply.headers
    assert parse_discovery_hint(403, reply.headers).content_rules_state == "valid", reply.headers
    if reply.header(EXCHANGE_HINT) is not None:
        assert reply.header(CONTENT_RULES) is not None, reply.headers


@obligation(Obligation.EDGEHINT_001)
@_each_runtime_marked(EDGE_REFUSAL_WITHOUT_HINT.marker)
def test_the_expired_and_tampered_refusals_carry_x_content_rules(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A 403 to an expired signed URL, and one to a tampered signed URL, each carry the header.

    On Fastly the expired URL is one exchange-b issued with a ten-second lifetime,
    which needs the Exchange's e2e-only lifetime control.
    """
    if not runtime.mintable:
        require_admin_plane()
    urls = {
        "expired": expired_url(runtime, fresh_agent, exchange_key),
        "tampered": tampered_url(runtime, fresh_agent, exchange_key),
    }
    refusals = {}
    for name, url in urls.items():
        reply = read(compose_stack, runtime, url)
        assert reply.status == 403, f"{runtime} {name}: answered {reply.status}"
        refusals[name] = reply.header(CONTENT_RULES)
    EDGE_REFUSAL_WITHOUT_HINT.check(
        all(value is not None for value in refusals.values()),
        f"{runtime}: X-Content-Rules {refusals}",
    )
    assert set(refusals.values()) == {runtime.manifest_url}, refusals


@obligation(Obligation.EDGEHINT_003)
@_each_runtime_marked(EDGE_EXCHANGE_HINT_NOT_BARE.marker)
def test_the_exchange_hint_is_a_bare_domain(compose_stack: StackURLs, runtime: EdgeRuntime) -> None:
    """X-FORA-Exchange, when sent, has the Request-recipient shape: no scheme, no path."""
    reply = _bot_refusal(compose_stack, runtime)
    raw = reply.header(EXCHANGE_HINT)
    assert raw is not None, f"{runtime}: this deployment is configured to send the Exchange hint"
    hint = parse_discovery_hint(403, reply.headers)
    EDGE_EXCHANGE_HINT_NOT_BARE.check(
        hint.exchange_state == "valid", f"{runtime}: X-FORA-Exchange is {raw!r}"
    )


@obligation(Obligation.EDGEHINT_004)
@_each_runtime_marked(EDGE_EXCHANGE_HINT_NOT_BARE.marker)
def test_the_exchange_hint_is_listed_in_the_publishers_manifest(
    compose_stack: StackURLs, runtime: EdgeRuntime
) -> None:
    """The hinted Exchange is the domain of an exchanges[] entry of the manifest the edge serves."""
    reply = _bot_refusal(compose_stack, runtime)
    raw = reply.header(EXCHANGE_HINT)
    assert raw is not None, f"{runtime}: this deployment is configured to send the Exchange hint"
    _, manifest = _manifest(compose_stack, runtime)
    listed = [exchange.domain for exchange in manifest.exchanges or []]
    agreement = reconcile_discovery_hint(parse_discovery_hint(403, reply.headers), listed)
    EDGE_EXCHANGE_HINT_NOT_BARE.check(
        agreement == "listed", f"{runtime}: X-FORA-Exchange {raw!r}, listed {listed}"
    )


@obligation(Obligation.EDGEHINT_006)
@_EACH_RUNTIME
def test_the_discovery_headers_are_only_on_the_refusal(
    compose_stack: StackURLs, runtime: EdgeRuntime
) -> None:
    """The manifest answer (200) and a browser read the edge lets through carry neither header."""
    manifest, _ = _manifest(compose_stack, runtime)
    browser = read(compose_stack, runtime, runtime.article, headers={"User-Agent": BROWSER_UA})
    assert_served(runtime, browser, runtime.article)
    for name, reply in (("manifest", manifest), ("browser read", browser)):
        assert reply.header(CONTENT_RULES) is None, f"{runtime} {name}: {reply.headers}"
        assert reply.header(EXCHANGE_HINT) is None, f"{runtime} {name}: {reply.headers}"
