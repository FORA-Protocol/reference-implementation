"""E2E: the edge worker on the AWS Lambda@Edge runtime.

What runs here is the production artifact. The container is built by
``src/edge/scripts/build-lambda-edge.mjs`` — the same script
``deploy/terraform/scripts/build-lambda-edge.sh`` runs for a deployment. It
writes the release bundle and an ``index.mjs`` beside it that carries the
per-deployment config, because Lambda@Edge has no environment variables; the
console hands publishers the same two files. It executes inside the AWS Lambda
runtime image, so the invocations below go through the real Lambda runtime
rather than a Node process pretending to be one. The other two runtimes (Cloudflare via Miniflare,
Fastly Compute via Viceroy) already have that; this closes the third.

Two shapes are asserted throughout, and telling them apart is what the
Lambda@Edge entry exists for: a denial or the key directory comes back as a
GENERATED RESPONSE (status/headers/body) that CloudFront returns without
touching the origin, while an authorized read, and a read of the publisher's
own discovery document or RSL file, comes back as the REQUEST OBJECT, which is
how the function tells CloudFront to go and fetch the origin itself.
This deployment bakes no ORIGIN_URL, the CloudFront-native posture where the CDN
owns that fetch.


Why the delivery URLs here are minted by the harness rather than bought through
Broker and Exchange, as the other runtimes' suites do:

* CloudFront terminates TLS, so a Lambda@Edge function always sees an https
  request, and the adapter rebuilds the URL that way. The services here serve
  plain http, and the Exchange mints its delivery URLs on http demo domains. The
  signature covers the scheme, so a URL minted in this stack can never verify
  through this runtime — it would only ever prove a mismatch.
* The signing key is not a test key. It is exchange-a's own Ed25519
  delivery-URL key, and ``lambda_edge.load_exchange_delivery_key`` refuses to
  go on unless the Exchange is publishing that exact key in its Web Bot Auth
  directory. The minting and the proof of possession both live in
  ``harness/lambda_edge.py`` and go through the SDK's own signers.
* Verification stays entirely on the production path: the worker resolves the
  key by thumbprint by fetching the Exchange's directory over the network, with
  no pre-provisioned key list baked in.

IMPORTANT: Lambda Runtime Interface Emulator (RIE) serves ONE invocation at a time.
All tests in this file MUST be serialized using @pytest.mark.xdist_group("lambda-edge")
to prevent concurrent invocations that would crash the emulator.
"""

from __future__ import annotations

import pytest
from fora_sdk.resolvers import WBA_DIRECTORY_MEDIA_TYPE
from wire.models import WBAFile

from .conftest import StackURLs
from .constants import AI_BOT_UA, BROWSER_UA, PUBLISHER_DOCUMENT_PATHS, WBA_DIRECTORY_PATH
from .edge_fetch import tamper_query_param
from .fresh_agent import FreshAgent
from .lambda_edge import (
    ARTICLE_PATH,
    PUBLISHER,
    ExchangeDeliveryKey,
    invoke,
    load_exchange_delivery_key,
    mint_delivery_url,
    proof_of_possession,
    viewer_request_event,
)
from .obligation_registry import Obligation, obligation
from .protocol_documents import parse_document

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-without-cleanup")

# Query the publisher's own site puts on its URLs. It must survive a
# pass-through untouched, unlike the signature parameters.
SITE_QUERY = "page=2&utm_source=e2e"

# The reserved query parameters the Exchange's signer appends. The function
# strips exactly these before handing the request back to CloudFront, so the
# origin never sees the spoofable attribution namespace and the cache key stays
# clean.
SIGNATURE_PARAMS = ("exp", "sig", "kid", "agent_id")


@pytest.fixture(scope="session")
def exchange_key(compose_stack: StackURLs) -> ExchangeDeliveryKey:
    """exchange-a's delivery-URL key, confirmed to be the one it publishes."""
    return load_exchange_delivery_key(compose_stack.exchange)


def signed_url(
    key: ExchangeDeliveryKey,
    *,
    path: str = ARTICLE_PATH,
    query: str = "",
    agent_id: str = "",
    ttl_seconds: int = 300,
) -> str:
    """A delivery URL for this publisher, signed with the Exchange's key.

    https, because that is what CloudFront hands a Lambda@Edge function. An
    empty ``agent_id`` yields a bearer URL; a non-empty one binds the URL to
    that agent's key. A negative ``ttl_seconds`` produces an already-expired URL.
    """
    base = f"https://{PUBLISHER}{path}"
    if query:
        base = f"{base}?{query}"
    return mint_delivery_url(key, base, agent_id=agent_id, ttl_seconds=ttl_seconds)


@pytest.mark.parametrize("user_agent", [BROWSER_UA, AI_BOT_UA], ids=["browser", "ai-bot"])
@pytest.mark.parametrize("path", PUBLISHER_DOCUMENT_PATHS)
@pytest.mark.xdist_group("lambda-edge")
def test_publisher_documents_are_handed_back_to_cloudfront(
    compose_stack: StackURLs, path: str, user_agent: str
) -> None:
    """The publisher's discovery document and RSL file go to the origin, for any client.

    This is the document the whole discovery chain starts from: an agent that
    was turned away reads it to learn which exchange sells access. It is the
    publisher's own file, so the function must hand the request back for
    CloudFront to fetch from the origin, never answer it itself, and never gate
    it, the AI bot's read included. Viewer-request is where the function stops;
    what the origin then answers is the publisher's, which this runtime's
    container does not reach.
    """
    result = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(
            f"https://{PUBLISHER}{path}?{SITE_QUERY}", headers={"User-Agent": user_agent}
        ),
    )
    assert result.pass_through, result
    assert result.uri == path, result
    assert result.querystring == SITE_QUERY, result


@pytest.mark.parametrize(
    ("service", "expected_status"),
    [("publishing", "200"), ("not-publishing", "404")],
)
@pytest.mark.xdist_group("lambda-edge")
def test_wba_directory_matches_the_deployment_key_posture(
    compose_stack: StackURLs,
    service: str,
    expected_status: str,
) -> None:
    """The Web Bot Auth directory is served only where the publisher has a key.

    Two containers, because a Lambda function serves exactly one package
    and the two answers come from two build-time configurations of the same
    worker. The publishing one serves its signing key as a JWK Set, which is how
    the Exchange learns a catalog writer's key without a database pre-seed; the
    other answers 404, which is what a publisher that issues no key must do
    rather than serve an empty directory a verifier would treat as a key set.
    """
    base_url = (
        compose_stack.lambda_edge if service == "publishing" else compose_stack.lambda_edge_no_wba
    )
    result = invoke(base_url, viewer_request_event(f"https://{PUBLISHER}{WBA_DIRECTORY_PATH}"))
    assert result.status == expected_status, result
    if expected_status != "200":
        return
    assert result.header("content-type") == WBA_DIRECTORY_MEDIA_TYPE, result
    keys = parse_document(WBAFile, result.body or "").keys or []
    assert len(keys) >= 1, keys
    assert all(k.kty == "OKP" and k.crv == "Ed25519" for k in keys), keys


@obligation(Obligation.DELIVERY_010)
@pytest.mark.xdist_group("lambda-edge")
def test_valid_delivery_url_passes_through_with_signature_params_stripped(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
) -> None:
    """An authorized read is handed back to CloudFront, carrying only the site's query.

    Two properties in one invocation, because they are one decision. The result
    is the request object, so CloudFront fetches the origin — the function never
    serves the bytes itself, which at viewer-request it could not do anyway (a
    generated response is capped at about 40 KB). And the signature parameters
    are gone from the query, so the origin cannot be handed spoofable
    attribution data that looks like it survived verification, while the site's
    own parameters arrive untouched.
    """
    url = signed_url(exchange_key, query=SITE_QUERY)
    result = invoke(compose_stack.lambda_edge, viewer_request_event(url))
    assert result.pass_through, result
    assert result.uri == ARTICLE_PATH, result
    querystring = str(result.querystring)
    for param in SIGNATURE_PARAMS:
        assert f"{param}=" not in querystring, (
            f"{param} survived into the origin request: {querystring}"
        )
    assert "page=2" in querystring and "utm_source=e2e" in querystring, querystring


@pytest.mark.xdist_group("lambda-edge")
def test_tampered_signature_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
) -> None:
    """One flipped character in the signature turns the read into a 403.

    The key still resolves — this invocation fetches the Exchange's directory
    like the successful one above — so what is being proved is that the
    signature is actually checked against the URL, not merely that some key was
    found.
    """
    url = tamper_query_param(signed_url(exchange_key))
    result = invoke(compose_stack.lambda_edge, viewer_request_event(url))
    assert result.status == "403", result
    assert result.reason == "signature_mismatch", result


@pytest.mark.xdist_group("lambda-edge")
def test_expired_delivery_url_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
) -> None:
    """A correctly signed URL whose expiry has passed is refused as expired.

    The distinct reason matters operationally: an agent that comes back with a
    stale URL should buy a new one, while an invalid signature means something
    is wrong with the URL it was given.
    """
    url = signed_url(exchange_key, ttl_seconds=-60)
    result = invoke(compose_stack.lambda_edge, viewer_request_event(url))
    assert result.status == "403", result
    assert result.reason == "expired", result


@pytest.mark.xdist_group("lambda-edge")
def test_unsigned_ai_bot_is_sent_to_negotiate(compose_stack: StackURLs) -> None:
    """An unsigned AI bot gets a 403 that tells it where to buy access.

    The headers are the machine-readable half of that answer: X-Content-Rules
    points at this publisher's manifest, and X-FORA-Exchange names the exchange
    that sells the license. A 403 without them is a dead end for the bot.
    """
    result = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(
            f"https://{PUBLISHER}{ARTICLE_PATH}", headers={"User-Agent": AI_BOT_UA}
        ),
    )
    assert result.status == "403", result
    assert result.header("X-Content-Rules") == (f"https://{PUBLISHER}/.well-known/fora.json"), (
        result
    )
    # X-FORA-Exchange is held to the protocol's shape (a bare domain the manifest
    # lists) in obligations/test_edge_hints.py.
    assert result.reason == "ai_bot", result


@pytest.mark.xdist_group("lambda-edge")
def test_unsigned_human_read_passes_through(compose_stack: StackURLs) -> None:
    """A browser reads the site normally: no signature, no license, no denial.

    The bot gate must not turn the publisher's site off for its ordinary
    readers, so this is the case that keeps the gate from being a blanket block.
    """
    result = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(
            f"https://{PUBLISHER}{ARTICLE_PATH}", headers={"User-Agent": BROWSER_UA}
        ),
    )
    assert result.pass_through, result
    assert result.uri == ARTICLE_PATH, result


@pytest.mark.xdist_group("lambda-edge")
def test_unsigned_browser_at_site_root_passes_through(compose_stack: StackURLs) -> None:
    """The site root passes through to the origin like any other page.

    The origin serves the human-facing landing page at /, so the function must
    hand the root request back to CloudFront untouched. A stakeholder demo
    starts by opening the demo hostname in a browser; if this pass-through
    breaks, that first impression is an error page.
    """
    result = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(f"https://{PUBLISHER}/", headers={"User-Agent": BROWSER_UA}),
    )
    assert result.pass_through, result
    assert result.uri == "/", result


@pytest.mark.xdist_group("lambda-edge")
def test_unsigned_ai_bot_at_site_root_is_sent_to_negotiate(compose_stack: StackURLs) -> None:
    """The landing page is for people: an AI bot at / still gets the 403 payload.

    The root must not become a hole in the bot gate — the same negotiation
    pointers apply there as on any content path.
    """
    result = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(f"https://{PUBLISHER}/", headers={"User-Agent": AI_BOT_UA}),
    )
    assert result.status == "403", result
    assert result.reason == "ai_bot", result


@obligation(Obligation.DELIVERY_004, negative=True)
@pytest.mark.xdist_group("lambda-edge")
def test_agent_bound_url_needs_the_key_it_names(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
) -> None:
    """A bound delivery URL is worthless to anyone who cannot present its key.

    The same URL is driven twice and only the proof of possession changes, so
    the difference between the two answers is the check itself and not something
    about the URL. Without the proof the read is refused; with an RFC 9421
    signature made by the agent key the URL is bound to, it passes through. This
    is what stops a leaked URL from being a bearer token. The agent is the test's
    own, so the URL is bound to a key no other test holds.
    """
    url = signed_url(exchange_key, agent_id=fresh_agent.thumbprint)

    refused = invoke(compose_stack.lambda_edge, viewer_request_event(url))
    assert refused.status == "403", refused
    assert refused.reason == "missing_agent_key", refused

    proven = invoke(
        compose_stack.lambda_edge,
        viewer_request_event(url, headers=proof_of_possession(url, fresh_agent.key_path)),
    )
    assert proven.pass_through, proven
    assert proven.uri == ARTICLE_PATH, proven


@obligation(Obligation.DELIVERY_007, negative=True)
@pytest.mark.xdist_group("lambda-edge")
def test_signed_read_url_replayed_as_a_write_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
) -> None:
    """A valid signed URL does not authorize a POST to the origin.

    The URL signature covers the URL and nothing else — not the method, not the
    body. Only the proof-of-possession check binds the method, and this URL is
    unbound, so nothing would check it. Without this refusal one leaked read URL
    could be replayed as a write with an arbitrary body against the origin.
    """
    url = signed_url(exchange_key)
    result = invoke(compose_stack.lambda_edge, viewer_request_event(url, method="POST"))
    assert result.status == "405", result
    assert result.header("Allow") == "GET, HEAD", result
    assert result.reason == "method_not_bound", result
