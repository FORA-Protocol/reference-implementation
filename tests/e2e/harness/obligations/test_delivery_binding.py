"""Obligations DELIVERY-003, -005 to -007, -010, -014 and PAID-008: how the edge judges a URL.

The contract is the edge function's signed-URL verification in the documentation
of the pinned module ``github.com/FORA-Protocol/protocol``:

* step 1 verifies the Exchange's signature over the whole URL, so a URL whose
  ``agent_id`` is rewritten to another key's thumbprint is refused
  ``signature_mismatch``, whatever proof of possession comes with it;
* steps 3 and 4: the proof must present the buyer's public key and be signed by
  its private key, so a proof that presents the buyer's key but is signed by
  another is refused ``pop_sig_invalid``;
* a signature that is not 64 bytes, in the URL's ``sig`` or in the proof, is a
  signature that does not verify like any other: refused ``signature_mismatch``
  or ``pop_sig_invalid``, never answered with an error from the edge's Ed25519
  primitive;
* the proof covers ``@method`` and ``@target-uri`` inside a short window, so a
  proof made for another URL is refused ``pop_sig_invalid``, an expired one
  ``pop_expired``, and one created more than 300 seconds ahead of the edge's clock
  ``pop_future_created``;
* those components are a minimum: a proof that also covers ``@authority`` is
  served, and one that covers ``@authority`` instead of ``@target-uri`` is refused
  401 ``bad_covered_components`` with the Accept-Signature naming the minimum;
* the URL signature covers the URL and not the method, so a signed request with a
  method other than GET or HEAD is refused ``405 method_not_bound`` unless the
  proof-of-possession check runs, and that check covers ``@method``;
* the edge strips ``agent_id``, ``exp``, ``kid`` and ``sig`` before forwarding to
  the origin, on the signed and the unsigned path.

The edge's refusal contract is its HTTP status and its reason token, so each
negative asserts both, and that no content went out.

Each case runs on Cloudflare, Fastly and Lambda@Edge through
``harness.edge_runtimes``, which says how each is reached and where its URLs come
from. Every proof is the SDK's own, altered after signing only where the case says
so. Two cases cannot run everywhere, and say why on themselves: the unbound POST
needs a bearer URL, which only a runtime whose key the harness holds can be handed,
and the stripping case needs a reader of what the origin received, which only
Lambda@Edge has.
"""

from __future__ import annotations

import base64
import re
from http import HTTPStatus
from urllib.parse import parse_qs

import pytest
from fora_sdk.b64 import b64url_nopad

from ..constants import BROWSER_UA
from ..edge_fetch import replace_query_param
from ..edge_runtimes import (
    AUTHORITY,
    CLOUDFLARE,
    LAMBDA,
    RUNTIMES,
    TARGET_URI,
    EdgeRuntime,
    assert_refused,
    assert_served,
    bound_urls,
    expired_proof,
    future_proof,
    library_proof,
    proof,
    proof_signed_by_another,
    read,
    unbound_url,
)
from ..fresh_agent import AgentFactory, FreshAgent
from ..lambda_edge import (
    ARTICLE_PATH,
    PUBLISHER,
    ExchangeDeliveryKey,
    invoke,
    load_exchange_delivery_key,
    viewer_request_event,
)
from ..obligation_registry import Obligation, obligation
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_EACH_RUNTIME = pytest.mark.parametrize("runtime", RUNTIMES, ids=str)

_SIGNATURE_MISMATCH = "signature_mismatch"
_METHOD_NOT_BOUND = "method_not_bound"
_POP_SIG_INVALID = "pop_sig_invalid"
_POP_EXPIRED = "pop_expired"
_POP_FUTURE_CREATED = "pop_future_created"
_BAD_COVERED_COMPONENTS = "bad_covered_components"

#: The reserved query parameters the edge strips before the origin sees a request.
_SIGNATURE_PARAMS = ("agent_id", "exp", "kid", "sig")

#: A body a replayed write would carry. Its content is irrelevant: it must never
#: reach the origin.
_WRITE_BODY = b'{"overwrite": true}'

#: A signature length no Ed25519 key can make. The requester chooses the bytes of
#: the URL's ``sig`` and of the proof's Signature, so the edge sees any length.
_SHORT_SIGNATURE_BYTES = 10

#: The one byte sequence in a Signature field value: ``sig1=:<base64>:``.
_BYTE_SEQUENCE = re.compile(r":[A-Za-z0-9+/=]*:")


def _with_signature_bytes(headers: dict[str, str], length: int) -> dict[str, str]:
    """``headers`` with the proof's signature replaced by ``length`` zero bytes.

    Everything else stays the SDK's own proof, so every identity check still agrees
    with the bound URL and only the signature check can refuse it.
    """
    zeros = base64.b64encode(bytes(length)).decode()
    replaced, count = _BYTE_SEQUENCE.subn(f":{zeros}:", headers["signature"])
    assert count == 1, f"the proof carries {count} byte sequences: {headers['signature']}"
    return {**headers, "signature": replaced}


@pytest.fixture(scope="module")
def exchange_key(compose_stack: StackURLs) -> ExchangeDeliveryKey:
    """exchange-a's delivery-URL key, confirmed to be the one it publishes."""
    return load_exchange_delivery_key(compose_stack.exchange)


def _served_to_buyer(stack: StackURLs, runtime: EdgeRuntime, url: str, buyer: FreshAgent) -> None:
    """Control: ``url`` presented with its buyer's own fresh proof is served."""
    assert_served(runtime, read(stack, runtime, url, headers=proof(url, buyer)), url)


@obligation(Obligation.DELIVERY_003, negative=True)
@_EACH_RUNTIME
def test_a_url_rewritten_to_another_agent_is_refused_despite_that_agents_proof(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent_factory: AgentFactory,
    runtime: EdgeRuntime,
) -> None:
    """The buyer's URL serves the buyer; rewritten to another agent, it serves nobody.

    A second agent changes the URL's ``agent_id`` to its own thumbprint and presents
    a valid proof of possession of its own key. The keyid, the presented key and
    the ``agent_id`` all agree, so only the Exchange's signature can refuse it:
    403 ``signature_mismatch``, and no content.
    """
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    url = bound_urls(runtime, buyer, exchange_key)[0]
    _served_to_buyer(compose_stack, runtime, url, buyer)

    rewritten = replace_query_param(url, "agent_id", other.thumbprint)
    reply = read(compose_stack, runtime, rewritten, headers=proof(rewritten, other))
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, _SIGNATURE_MISMATCH)


@obligation(Obligation.DELIVERY_005, negative=True)
@_EACH_RUNTIME
def test_a_proof_presenting_the_buyers_key_but_signed_by_another_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent_factory: AgentFactory,
    runtime: EdgeRuntime,
) -> None:
    """Holding the buyer's public key is not enough: the proof must be signed by its private key.

    The proof is a second agent's own, with its X-FORA-Agent-Key and its keyid
    replaced by the buyer's. The URL's ``agent_id``, the keyid and the thumbprint of
    the presented key agree, so the signature check is what refuses it: 403
    ``pop_sig_invalid``.
    """
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    url = bound_urls(runtime, buyer, exchange_key)[0]
    _served_to_buyer(compose_stack, runtime, url, buyer)

    forged = proof_signed_by_another(url, presented=buyer, signer=other)
    reply = read(compose_stack, runtime, url, headers=forged)
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, _POP_SIG_INVALID)


@obligation(Obligation.DELIVERY_005, negative=True)
@_EACH_RUNTIME
def test_a_proof_whose_signature_is_not_64_bytes_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A proof whose signature is not 64 bytes is refused ``pop_sig_invalid``, not answered 500.

    The control is the URL served with the buyer's fresh proof. Then the same proof's
    signature is replaced by ten zero bytes, a length no Ed25519 key can make. The
    presented key, the keyid and the URL's ``agent_id`` still agree, so the
    signature check refuses it: 403 ``pop_sig_invalid``.
    """
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    _served_to_buyer(compose_stack, runtime, url, fresh_agent)

    short = _with_signature_bytes(proof(url, fresh_agent), _SHORT_SIGNATURE_BYTES)
    reply = read(compose_stack, runtime, url, headers=short)
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, _POP_SIG_INVALID)


@obligation(Obligation.PAID_008, negative=True)
@_EACH_RUNTIME
def test_a_url_whose_signature_is_not_64_bytes_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A URL whose ``sig`` is not 64 bytes is refused ``signature_mismatch``, not answered 503.

    The control is the URL served to its buyer. Then its ``sig`` is replaced by ten
    zero bytes, and the changed URL is presented with the buyer's fresh proof for
    it. The Exchange's signature no longer verifies, so the edge refuses it: 403
    ``signature_mismatch``, and no content.
    """
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    _served_to_buyer(compose_stack, runtime, url, fresh_agent)

    short = replace_query_param(url, "sig", b64url_nopad(bytes(_SHORT_SIGNATURE_BYTES)))
    reply = read(compose_stack, runtime, short, headers=proof(short, fresh_agent))
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, _SIGNATURE_MISMATCH)


@obligation(Obligation.DELIVERY_006, negative=True)
@_EACH_RUNTIME
def test_a_proof_is_bound_to_its_url_and_its_window(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A proof serves the URL it was made for, inside its window, and nothing else.

    Two URLs are bound to the same buyer, and each is served with its own fresh
    proof. Then the proof made for the first is presented with the second: 403
    ``pop_sig_invalid``. The first URL with a proof whose window has passed: 403
    ``pop_expired``. With a proof created ten minutes ahead of the edge's clock:
    403 ``pop_future_created``.
    """
    first, second = bound_urls(runtime, fresh_agent, exchange_key, count=2)
    _served_to_buyer(compose_stack, runtime, first, fresh_agent)
    _served_to_buyer(compose_stack, runtime, second, fresh_agent)

    cases = (
        (second, proof(first, fresh_agent), _POP_SIG_INVALID),
        (first, expired_proof(first, fresh_agent), _POP_EXPIRED),
        (first, future_proof(first, fresh_agent), _POP_FUTURE_CREATED),
    )
    for url, headers, reason in cases:
        reply = read(compose_stack, runtime, url, headers=headers)
        assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, reason)


@obligation(Obligation.DELIVERY_014)
@_EACH_RUNTIME
def test_a_proof_that_also_covers_the_authority_is_accepted(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A WBA library's proof covering @authority besides the minimum serves the URL."""
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    headers = library_proof(url, fresh_agent, (TARGET_URI, AUTHORITY))
    assert_served(runtime, read(compose_stack, runtime, url, headers=headers), url)


@obligation(Obligation.DELIVERY_014, negative=True)
@_EACH_RUNTIME
def test_a_proof_covering_the_authority_but_not_the_url_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """The same library's proof without @target-uri: 401 ``bad_covered_components``.

    The extra component does not stand in for a required one. The refusal is one
    the fetcher fixes by signing again, so it carries the Accept-Signature naming
    the minimum, @target-uri included. The control is the full proof, served.
    """
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    full = library_proof(url, fresh_agent, (TARGET_URI, AUTHORITY))
    assert_served(runtime, read(compose_stack, runtime, url, headers=full), url)

    partial = library_proof(url, fresh_agent, (AUTHORITY,))
    reply = read(compose_stack, runtime, url, headers=partial)
    assert_refused(runtime, reply, HTTPStatus.UNAUTHORIZED, _BAD_COVERED_COMPONENTS)
    asked = reply.header("accept-signature") or ""
    assert f'"{TARGET_URI}"' in asked, f"{runtime}: Accept-Signature {asked!r} asks for no URL"


@obligation(Obligation.DELIVERY_007, negative=True)
@pytest.mark.parametrize("runtime", [CLOUDFLARE, LAMBDA], ids=str)
def test_an_unbound_url_posted_to_is_refused(
    compose_stack: StackURLs, exchange_key: ExchangeDeliveryKey, runtime: EdgeRuntime
) -> None:
    """A bearer URL serves a GET; a POST with a body to it is refused ``405 method_not_bound``.

    The URL is unbound, so no proof of possession runs and nothing would bind the
    method. It is minted with exchange-a's key, because every URL an Exchange
    issues for a purchase is bound to its buyer. Not driven on Fastly: its edge
    resolves exchange-b's key, which the harness does not hold.
    """
    url = unbound_url(runtime, exchange_key)
    assert_served(runtime, read(compose_stack, runtime, url), url)

    reply = read(compose_stack, runtime, url, method="POST", body=_WRITE_BODY)
    assert_refused(runtime, reply, HTTPStatus.METHOD_NOT_ALLOWED, _METHOD_NOT_BOUND)


@obligation(Obligation.DELIVERY_007, negative=True)
@_EACH_RUNTIME
def test_a_bound_url_posted_to_with_a_proof_made_for_get_is_refused(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A proof signed over GET authorizes the GET, and not a POST of the same URL.

    The proof is the SDK's own, minted for a GET of the URL. With the GET it is
    served. With a POST the proof check runs, so the method gate lets the request
    through to it, and the check finds ``@method`` signed as GET: 403
    ``pop_sig_invalid``.
    """
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    _served_to_buyer(compose_stack, runtime, url, fresh_agent)

    reply = read(
        compose_stack,
        runtime,
        url,
        method="POST",
        headers=proof(url, fresh_agent),
        body=_WRITE_BODY,
    )
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, _POP_SIG_INVALID)


@obligation(Obligation.DELIVERY_010)
@pytest.mark.xdist_group("lambda-edge")
def test_an_unsigned_read_carrying_attribution_parameters_reaches_the_origin_without_them(
    compose_stack: StackURLs,
) -> None:
    """A browser read with no signature passes through, stripped of agent_id and kid.

    Lambda@Edge only: it hands back the very request CloudFront will send the
    origin, which is what this case reads. The Cloudflare and Fastly readers see
    only the edge's answer, not what the origin received.
    """
    url = f"https://{PUBLISHER}{ARTICLE_PATH}?page=2&agent_id=spoofed-agent&kid=spoofed-kid"
    result = invoke(
        compose_stack.lambda_edge, viewer_request_event(url, headers={"User-Agent": BROWSER_UA})
    )
    assert result.pass_through, f"an unsigned browser read was not passed through: {result}"
    forwarded = parse_qs(result.querystring or "", keep_blank_values=True)
    assert not set(forwarded) & set(_SIGNATURE_PARAMS), (
        f"reserved parameters reached the origin: {result.querystring}"
    )
    assert forwarded.get("page") == ["2"], f"the site's own query was lost: {result.querystring}"
