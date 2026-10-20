"""E2E: FORA accepts and refuses third-party Web Bot Auth signatures per the profile.

A third-party signer calls FORA here. Every transport signature comes from code
FORA did not write: the Python oracle (pyauth ``http-message-signatures`` plus a
dictionary-member resolver, ``wba_oracle.py``) or Cloudflare's ``web-bot-auth``
library in the ``wba-oracle-ts`` sidecar. Each signs as agent-e2e, a registered
static agent, under the https origin ``https://agent-e2e``; the capture proxy
serves that agent's key directory there.

The profile under test is the Web Bot Auth profile of the pinned protocol module
``github.com/FORA-Protocol/protocol`` (Authentication, "The Web Bot Auth
Profile"), for draft-ietf-webbotauth-httpsig-protocol-00 ("WG-00"):

- a WG-00 signature carrying the four FORA components (@method, @target-uri,
  content-digest, authorization) is accepted by the Broker and by an Exchange;
- the legacy sf-string Signature-Agent is accepted on a request carrying one
  signature only: a direct call to an Exchange, and a purchase sent to the
  Broker, which never forwards the agent's request but re-packages it and signs
  the sub-request it sends the Exchange alone;
- a FORA RPC signed with the bare WG-00 minimum is refused with a 401 whose
  Accept-Signature lists the four FORA components;
- a signature with no tag, one naming an http origin, and the bare unquoted
  Signature-Agent of the v1.0.8 SDK are refused. Each of these is a refusal the
  signer can fix by signing again (no tag, a member that is not an https origin,
  the bare unquoted value), so each is answered with an Accept-Signature asking
  for the tagged dictionary form.

Every refusal is read as a 401 carrying the Connect code unauthenticated, never
by its message text (ADR-028 D4). Every refusal test first sends the same request
signed with the one property corrected, and that control is accepted, so the
mutation is the only difference between the accepted call and the refused one
(ADR-028 D5).

Surfaces: every request goes to the public Connect routes (Broker Resolve,
Broker ExecuteTransaction, Exchange DiscoverResources) on the URLs the stack
serves them on, and every assertion reads the answer. The legacy-form purchase
also reads the capture proxy's record of the sub-request the Broker sent the
Exchange, which no answer shows.

The calls go to the services' plain-http URLs. The services verify @target-uri
against the URL they are served on, so a call through the proxy's https leg
would be refused for a signature mismatch, which is not the property under test
here and would mask every one that is.
"""

from __future__ import annotations

import os
import secrets
from collections.abc import Callable

import pytest
from wire.models import TransactionRequest

from .capture_proxy import BROKER_DOMAIN, CaptureProxy
from .e2e_catalog import SeededFixture
from .stack_urls import StackURLs
from .wba_conformance import (
    AGENT_ORIGIN,
    FORA_COMPONENTS,
    Answer,
    assert_signed_alone_by_the_broker,
    buy_as_agent,
    cloudflare_sign,
    discover_at,
    python_sign,
    resolve_to_broker,
    send,
)
from .wba_oracle import Message
from .wba_oracle_ts import TSOracle

pytestmark = [
    pytest.mark.skipif(
        os.environ.get("FORA_E2E_IN_NETWORK") != "1",
        reason="the oracle sidecar and the capture proxy are reachable only in-network",
    ),
    # Reads the seeded catalog and buys a free resource; nothing another suite reads.
    pytest.mark.stack_isolation("shared-without-cleanup"),
]

Signer = Callable[..., dict[str, str]]


@pytest.fixture(params=["python", "cloudflare"])
def signer(request: pytest.FixtureRequest, ts_oracle: TSOracle) -> Signer:
    """Each third-party signer, as one callable: ``signer(request, **options)``."""
    if request.param == "python":
        return python_sign
    return lambda message, **options: cloudflare_sign(ts_oracle, message, **options)


def _discover(stack: StackURLs, seeded: SeededFixture) -> Message:
    """A DiscoverResources for the free philosophy article, at the Exchange that sells it."""
    return discover_at(stack.exchange, seeded.free.uri)


def _assert_accepted_with_offers(answer: Answer, what: str) -> None:
    assert answer.status == 200, f"{what} was refused: {answer.status} {answer.text[:300]}"
    assert answer.offers, f"{what} was accepted with no offers: {answer.text[:300]}"


def _assert_refused_without_data(answer: Answer, what: str) -> None:
    """A 401 with the Connect code unauthenticated that leaks no offer."""
    assert answer.status == 401, (
        f"{what} was not refused with 401: {answer.status} {answer.text[:300]}"
    )
    assert answer.code == "unauthenticated", (
        f"{what} was refused {answer.code!r}, not unauthenticated: {answer.text[:300]}"
    )
    assert "offers" not in answer.text, f"{what} leaked offers: {answer.text[:300]}"


def _assert_asks_for_the_tagged_dictionary_form(answer: Answer, what: str) -> None:
    """The refusal's Accept-Signature asks for a keyed member and tag="web-bot-auth"."""
    assert answer.requests_dictionary_member(), (
        f"{what}: Accept-Signature {answer.requested} asks for no keyed Signature-Agent member"
    )
    assert answer.requests_tag("web-bot-auth"), (
        f"{what}: Accept-Signature {answer.requested} asks for no tag=web-bot-auth"
    )


# ---- Accepted: a conformant signature, from either library --------------------


def test_a_broker_resolve_signed_by_a_wba_library_is_accepted(
    compose_stack: StackURLs, seeded: SeededFixture, signer: Signer
) -> None:
    """A WG-00 signature plus the four FORA components gets offers from the Broker."""
    request = resolve_to_broker(compose_stack, seeded.free.uri)
    answer = send(request, signer(request, extra_components=FORA_COMPONENTS))
    _assert_accepted_with_offers(answer, "a WG-00 signed Broker Resolve")


def test_an_exchange_discovery_signed_by_a_wba_library_is_accepted(
    compose_stack: StackURLs, seeded: SeededFixture, signer: Signer
) -> None:
    """The same, sent straight to the Exchange that sells the resource."""
    request = _discover(compose_stack, seeded)
    answer = send(request, signer(request, extra_components=FORA_COMPONENTS))
    _assert_accepted_with_offers(answer, "a WG-00 signed DiscoverResources")


def test_the_legacy_string_form_is_accepted_on_a_direct_call(
    compose_stack: StackURLs, seeded: SeededFixture, signer: Signer
) -> None:
    """The profile accepts the sf-string on a request carrying one signature only."""
    request = _discover(compose_stack, seeded)
    signature = signer(request, extra_components=FORA_COMPONENTS, legacy=True)
    assert signature["Signature-Agent"] == f'"{AGENT_ORIGIN}"', signature
    _assert_accepted_with_offers(send(request, signature), "a legacy-form DiscoverResources")


def test_a_broker_purchase_signed_in_the_legacy_form_is_accepted_and_re_signed_by_the_broker(
    compose_stack: StackURLs, seeded: SeededFixture, capture_proxy: CaptureProxy
) -> None:
    """A purchase whose one signature uses the legacy form is bought, and signed anew.

    The agent's ExecuteTransaction carries one signature, so the profile accepts
    its legacy sf-string Signature-Agent. The Broker never forwards the agent's
    request: it re-packages the purchase into a new request for the Exchange and
    signs that alone. So the sub-request carries the Broker's own signature and
    nothing of the agent's transport signature: one label, one Signature-Agent
    member naming https://broker, and the agent's legacy value nowhere.

    Every leg is read through the surface that carries it. The purchase is
    answered through the Broker's ExecuteTransaction. The two requests, the
    agent's to the Broker and the Broker's to the Exchange, are read from the
    capture proxy, scoped by an X-Request-ID only this test sends; the Broker
    copies it onto every request it sends for the call.
    """
    request_id = f"wba-legacy-purchase-{secrets.token_hex(8)}"
    traffic = capture_proxy.watch(request_id)
    item = buy_as_agent(
        compose_stack, seeded.free.uri, request_id=request_id, third_party_legacy=True
    )
    assert item.retrieval_endpoint, f"the legacy-form purchase delivered nothing: {item}"

    (sent,) = traffic.carrying(TransactionRequest, target=BROKER_DOMAIN)
    assert sent.header("signature-agent") == f'"{AGENT_ORIGIN}"', (
        f"the purchase reached the Broker without the legacy form: "
        f"{sent.header('signature-agent')!r}"
    )
    forwarded = traffic.carrying(TransactionRequest, caller="broker")
    assert forwarded, "the Broker sent the Exchange no sub-request for the purchase it answered"
    for sub in forwarded:
        assert_signed_alone_by_the_broker(sub, f"the sub-request to {sub.target}")
        assert AGENT_ORIGIN not in sub.header("signature-agent"), sub.header("signature-agent")


# ---- Refused, with the refusal a WBA client can act on ------------------------


def test_the_bare_wba_minimum_on_a_bodied_rpc_is_refused_with_accept_signature(
    compose_stack: StackURLs, seeded: SeededFixture
) -> None:
    """@authority plus the Signature-Agent member: what a WBA library signs by default.

    The control is the same request signed with the four FORA components too.
    """
    request = _discover(compose_stack, seeded)
    control = send(request, python_sign(request, extra_components=FORA_COMPONENTS))
    _assert_accepted_with_offers(control, "the same request signed with the FORA components")

    answer = send(request, python_sign(request))
    _assert_refused_without_data(answer, "a bare WG-00 minimum signature")
    missing = [c for c in FORA_COMPONENTS if not answer.requests_component(f'"{c}"')]
    assert not missing, f"Accept-Signature {answer.requested} does not request {missing}"


def test_a_signature_without_the_wba_tag_is_refused(
    compose_stack: StackURLs, seeded: SeededFixture
) -> None:
    """The profile refuses a signature with no tag, and asks for the tagged form.

    Signed in the legacy form, which a one-signature call accepts. The control,
    the same legacy-form request with its tag, is accepted, so the missing tag is
    the only thing wrong with the refused one.
    """
    request = _discover(compose_stack, seeded)
    tagged = python_sign(request, extra_components=FORA_COMPONENTS, legacy=True)
    _assert_accepted_with_offers(send(request, tagged), "the same legacy-form request, tagged")

    signature = python_sign(request, extra_components=FORA_COMPONENTS, legacy=True, tag=None)
    answer = send(request, signature)
    _assert_refused_without_data(answer, "an untagged signature")
    _assert_asks_for_the_tagged_dictionary_form(answer, "an untagged signature")


def test_an_http_signature_agent_origin_is_refused(
    compose_stack: StackURLs, seeded: SeededFixture
) -> None:
    """The profile requires the member to name an https origin, and asks for one.

    Legacy form on a one-signature call again. The control, the same request
    naming the https origin, is accepted, so the scheme is the only defect. The
    signer can fix it by signing again, so the refusal carries the
    Accept-Signature asking for the tagged dictionary form.
    """
    request = _discover(compose_stack, seeded)
    https = python_sign(request, extra_components=FORA_COMPONENTS, legacy=True)
    _assert_accepted_with_offers(send(request, https), "the same request naming the https origin")

    signature = python_sign(
        request, extra_components=FORA_COMPONENTS, legacy=True, origin="http://agent-e2e"
    )
    answer = send(request, signature)
    _assert_refused_without_data(answer, "an http Signature-Agent")
    _assert_asks_for_the_tagged_dictionary_form(answer, "an http Signature-Agent")


@pytest.mark.parametrize("service", ["broker", "exchange"])
def test_the_bare_unquoted_signature_agent_is_refused(
    compose_stack: StackURLs, seeded: SeededFixture, service: str
) -> None:
    """The v1.0.8 form, ``Signature-Agent: https://agent-e2e``, is no form WG-00 knows.

    The control is the same call signed in the dictionary form.
    """
    request = (
        resolve_to_broker(compose_stack, seeded.free.uri)
        if service == "broker"
        else _discover(compose_stack, seeded)
    )
    dictionary = python_sign(request, extra_components=FORA_COMPONENTS)
    _assert_accepted_with_offers(
        send(request, dictionary), f"the same call to the {service} in the dictionary form"
    )

    signature = python_sign(request, extra_components=FORA_COMPONENTS, unquoted=True)
    assert signature["Signature-Agent"] == AGENT_ORIGIN, signature
    what = f"a bare-form call to the {service}"
    answer = send(request, signature)
    _assert_refused_without_data(answer, what)
    _assert_asks_for_the_tagged_dictionary_form(answer, what)
