"""E2E: FORA accepts and refuses third-party Web Bot Auth signatures per the profile.

A third-party signer calls FORA here. Every transport signature comes from code
FORA did not write: the Python oracle (pyauth ``http-message-signatures`` plus a
dictionary-member resolver, ``wba_oracle.py``) or Cloudflare's ``web-bot-auth``
library in the ``wba-oracle-ts`` sidecar. Each signs as the test's own agent
(``fresh_agent``), under the https origin of its own host in the agent zone; the
capture proxy serves that agent's key directory there.

The profile under test is the Web Bot Auth profile of the pinned protocol module
``github.com/FORA-Protocol/protocol`` (Authentication, "The Web Bot Auth
Profile"), for draft-ietf-webbotauth-httpsig-protocol-00 ("WG-00"):

- a WG-00 signature carrying the four FORA components (@method, @target-uri,
  content-digest, authorization) is accepted by the Broker and by an Exchange;
- the legacy sf-string Signature-Agent is accepted on a request carrying one
  signature only: a direct call to an Exchange, and a purchase sent to the
  Broker, which never forwards the agent's request but re-packages it and signs
  the sub-request it sends the Exchange alone;
- a FORA RPC signed with the bare WG-00 minimum, or with any one of the four
  FORA components or the Signature-Agent member left out of an otherwise
  complete signature, is refused;
- a signature with no nonce is accepted, because WG-00 sets no nonce
  requirement, and the same signed bytes sent again are refused as STALE;
- a signature with no tag, one naming an http origin, and the bare unquoted
  Signature-Agent of the v1.0.8 SDK are refused.

Each refusal is one the signer can fix by signing again, and each is answered
the same way: a 401 with the Connect code unauthenticated and the typed reason
SIGNATURE_INVALID, no offer, and an Accept-Signature asking for the four FORA
components, the keyed Signature-Agent member and the tag.

Every refusal is read as a 401 carrying the Connect code unauthenticated, never
by its message text (ADR-028 D4). Every refusal test first sends the same request
signed with the one property corrected, and that control is accepted, so the
mutation is the only difference between the accepted call and the refused one
(ADR-028 D5). These negatives do not go through ``malformed()``: its control is
the SDK's own signature, so a library's signature would differ from it in two
ways, the signer and the defect. Here the control is signed by the same library,
with the one property corrected.

Surfaces: every request goes to the public Connect routes (Broker Resolve,
Broker ExecuteTransaction, Exchange DiscoverResources) on the URLs the stack
serves them on, and every assertion reads the answer. The SDK builds each
request and decodes its answer or refusal; the library's signature replaces the
SDK's over the same bytes after the SDK signs (``wba_conformance.wba_call``), and
a refusal's Accept-Signature is read from the SDK's ``CallError.headers``. The legacy-form purchase
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
from functools import partial

import pytest
from wire.models import TransactionRequest

from .capture_proxy import BROKER_DOMAIN, CaptureProxy
from .e2e_catalog import SeededFixture
from .fresh_agent import FreshAgent
from .identity_requests import (
    SIGNATURE_INVALID,
    SIGNATURE_STALE,
    assert_auth_refusal,
    resent_discovery,
)
from .obligation_registry import Obligation, obligation
from .signature_fields import declared_signatures
from .wba_conformance import (
    FORA_COMPONENTS,
    Answer,
    Signer,
    Target,
    agent_origin,
    assert_signed_alone_by_the_broker,
    buy_as_agent,
    cloudflare_signer,
    python_signer,
    wba_call,
)
from .wba_oracle_ts import TSOracle

pytestmark = [
    pytest.mark.skipif(
        os.environ.get("FORA_E2E_IN_NETWORK") != "1",
        reason="the oracle sidecar and the capture proxy are reachable only in-network",
    ),
    # Reads the seeded catalog and buys a free resource; nothing another suite reads.
    pytest.mark.stack_isolation("shared-without-cleanup"),
]


@pytest.fixture(params=["python", "cloudflare"])
def signer(request: pytest.FixtureRequest, ts_oracle: TSOracle, fresh_agent: FreshAgent) -> Signer:
    """Each third-party signer, signing as the test's own agent: ``signer(request, **options)``."""
    if request.param == "python":
        return python_signer(fresh_agent)
    return cloudflare_signer(ts_oracle, fresh_agent)


def _assert_accepted_with_offers(answer: Answer, what: str) -> None:
    assert answer.status == 200, f"{what} was refused: {answer.status} {answer.code!r}"
    assert answer.offers, f"{what} was accepted with no verified offers"


def _assert_refused_without_data(answer: Answer, what: str) -> None:
    """A 401 with the Connect code unauthenticated, which carries no offer."""
    assert answer.status == 401, f"{what} was not refused with 401: {answer.status}"
    assert answer.code == "unauthenticated", (
        f"{what} was refused {answer.code!r}, not unauthenticated"
    )
    assert not answer.offers, f"{what} leaked offers"


def _assert_asks_for_the_tagged_dictionary_form(answer: Answer, what: str) -> None:
    """The refusal's Accept-Signature asks for a keyed member and tag="web-bot-auth"."""
    assert answer.requests_dictionary_member(), (
        f"{what}: Accept-Signature {answer.requested} asks for no keyed Signature-Agent member"
    )
    assert answer.requests_tag("web-bot-auth"), (
        f"{what}: Accept-Signature {answer.requested} asks for no tag=web-bot-auth"
    )


# ---- Accepted: a conformant signature, from either library --------------------


@obligation(Obligation.IDENTITY_013)
def test_a_broker_resolve_signed_by_a_wba_library_is_accepted(
    seeded: SeededFixture, signer: Signer, fresh_agent: FreshAgent
) -> None:
    """A WG-00 signature plus the four FORA components gets offers from the Broker."""
    answer = wba_call(
        fresh_agent, "broker", seeded.free.uri, signer, extra_components=FORA_COMPONENTS
    )
    _assert_accepted_with_offers(answer, "a WG-00 signed Broker Resolve")


@obligation(Obligation.IDENTITY_001)
def test_an_exchange_discovery_signed_by_a_wba_library_is_accepted(
    seeded: SeededFixture, signer: Signer, fresh_agent: FreshAgent
) -> None:
    """The same, sent straight to the Exchange that sells the resource."""
    answer = wba_call(
        fresh_agent, "exchange", seeded.free.uri, signer, extra_components=FORA_COMPONENTS
    )
    _assert_accepted_with_offers(answer, "a WG-00 signed DiscoverResources")


@obligation(Obligation.IDENTITY_014, negative=True)
def test_a_signature_without_a_nonce_is_accepted_and_the_same_bytes_again_are_not(
    seeded: SeededFixture, signer: Signer, fresh_agent: FreshAgent
) -> None:
    """A library's signature with no nonce is answered; its exact bytes sent again are STALE.

    WG-00 sets no nonce requirement and WBA signers omit it, so a FORA verifier
    accepts the signature. The replay store still knows those signed bytes: a
    second send is refused, and no offer comes back.
    """
    answer = wba_call(
        fresh_agent,
        "exchange",
        seeded.free.uri,
        signer,
        extra_components=FORA_COMPONENTS,
        include_nonce=False,
    )
    _assert_accepted_with_offers(answer, "a WG-00 signature with no nonce")
    signatures = declared_signatures(answer.sent.headers.get_list("signature-input"))
    assert signatures and not any("nonce" in s.params for s in signatures.values()), signatures

    refusal = resent_discovery(fresh_agent, answer.sent)
    assert refusal is not None, "the same nonce-less signed bytes, sent again, were answered"
    assert_auth_refusal(refusal, SIGNATURE_STALE)


def test_the_legacy_string_form_is_accepted_on_a_direct_call(
    seeded: SeededFixture, signer: Signer, fresh_agent: FreshAgent
) -> None:
    """The profile accepts the sf-string on a request carrying one signature only."""
    answer = wba_call(
        fresh_agent,
        "exchange",
        seeded.free.uri,
        signer,
        extra_components=FORA_COMPONENTS,
        legacy=True,
    )
    sent = answer.sent.headers.get("signature-agent")
    assert sent == f'"{agent_origin(fresh_agent)}"', f"the call carried Signature-Agent {sent!r}"
    _assert_accepted_with_offers(answer, "a legacy-form DiscoverResources")


def test_a_broker_purchase_signed_in_the_legacy_form_is_accepted_and_re_signed_by_the_broker(
    seeded: SeededFixture, capture_proxy: CaptureProxy, fresh_agent: FreshAgent
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
        fresh_agent, seeded.free.uri, request_id=request_id, third_party_legacy=True
    )
    assert item.retrieval_endpoint, f"the legacy-form purchase delivered nothing: {item}"

    (sent,) = traffic.carrying(TransactionRequest, target=BROKER_DOMAIN)
    assert sent.header("signature-agent") == f'"{agent_origin(fresh_agent)}"', (
        f"the purchase reached the Broker without the legacy form: "
        f"{sent.header('signature-agent')!r}"
    )
    forwarded = traffic.carrying(TransactionRequest, caller="broker")
    assert forwarded, "the Broker sent the Exchange no sub-request for the purchase it answered"
    for sub in forwarded:
        assert_signed_alone_by_the_broker(sub, f"the sub-request to {sub.target}")
        assert agent_origin(fresh_agent) not in sub.header("signature-agent"), sub.header(
            "signature-agent"
        )


# ---- Refused, with the refusal a WBA client can act on ------------------------

#: One library-signed call: the agent's Resolve or DiscoverResources, signed with options.
Call = Callable[..., Answer]


def _refused_after_its_control(
    call: Call, what: str, *, control: dict[str, object], defect: dict[str, object]
) -> Answer:
    """The call signed with ``control`` is answered; signed with ``defect``, it is refused.

    The control differs from the refused call only in the one property ``defect``
    sets, so that property is what the refusal is for. The refusal is the one the
    profile gives every signature its signer can fix by signing again: a 401 with
    the Connect code unauthenticated and the typed reason SIGNATURE_INVALID, no
    offer, and an Accept-Signature asking for the four FORA components, the keyed
    Signature-Agent member and the tag. Accept-Signature lists the whole required
    set, so it says what to sign, not which part was wrong; the message that names
    the part is text, which is never read (ADR-028 D4).
    """
    _assert_accepted_with_offers(call(**control), f"the control for {what}")
    answer = call(**defect)
    _assert_refused_without_data(answer, what)
    assert answer.reason == SIGNATURE_INVALID, f"{what} was refused with {answer.reason!r}"
    missing = [c for c in FORA_COMPONENTS if not answer.requests_component(f'"{c}"')]
    assert not missing, f"{what}: Accept-Signature {answer.requested} does not request {missing}"
    _assert_asks_for_the_tagged_dictionary_form(answer, what)
    return answer


def _all_but(component: str) -> tuple[str, ...]:
    """The four FORA components without ``component``."""
    return tuple(c for c in FORA_COMPONENTS if c != component)


#: Signatures that leave out what a FORA RPC requires, each otherwise valid: the WG-00
#: minimum a WBA library signs by default (@authority plus the Signature-Agent member),
#: and one complete signature less one component each.
_LEFT_OUT: dict[str, tuple[str, dict[str, object]]] = {
    "bare-minimum": ("the bare WG-00 minimum", {}),
    "method": ("a signature without @method", {"extra_components": _all_but("@method")}),
    "target-uri": (
        "a signature without @target-uri",
        {"extra_components": _all_but("@target-uri")},
    ),
    "content-digest": (
        "a signature without content-digest",
        {"extra_components": _all_but("content-digest")},
    ),
    "authorization": (
        "a signature without authorization",
        {"extra_components": _all_but("authorization")},
    ),
    "signature-agent": (
        "a signature without its Signature-Agent member",
        {"extra_components": FORA_COMPONENTS, "cover_agent": False},
    ),
}


@obligation(Obligation.IDENTITY_001, negative=True)
@pytest.mark.parametrize("left_out", list(_LEFT_OUT))
def test_a_signature_leaving_out_what_a_fora_rpc_requires_is_refused(
    seeded: SeededFixture, fresh_agent: FreshAgent, left_out: str
) -> None:
    """A signature that leaves out what a FORA RPC requires is refused.

    The control is the same request signed with the four FORA components and the
    Signature-Agent member.
    """
    what, defect = _LEFT_OUT[left_out]
    call = partial(wba_call, fresh_agent, "exchange", seeded.free.uri, python_signer(fresh_agent))
    _refused_after_its_control(
        call, what, control={"extra_components": FORA_COMPONENTS}, defect=defect
    )


def test_a_signature_without_the_wba_tag_is_refused(
    seeded: SeededFixture, fresh_agent: FreshAgent
) -> None:
    """The profile refuses a signature with no tag.

    Signed in the legacy form, which a one-signature call accepts; the control is
    the same legacy-form request with its tag.
    """
    call = partial(wba_call, fresh_agent, "exchange", seeded.free.uri, python_signer(fresh_agent))
    legacy: dict[str, object] = {"extra_components": FORA_COMPONENTS, "legacy": True}
    _refused_after_its_control(
        call, "an untagged signature", control=legacy, defect={**legacy, "tag": None}
    )


def test_an_http_signature_agent_origin_is_refused(
    seeded: SeededFixture, fresh_agent: FreshAgent
) -> None:
    """The profile requires the member to name an https origin.

    Legacy form on a one-signature call again; the control is the same request
    naming the https origin.
    """
    call = partial(wba_call, fresh_agent, "exchange", seeded.free.uri, python_signer(fresh_agent))
    legacy: dict[str, object] = {"extra_components": FORA_COMPONENTS, "legacy": True}
    _refused_after_its_control(
        call,
        "an http Signature-Agent",
        control=legacy,
        defect={**legacy, "origin": f"http://{fresh_agent.host}"},
    )


@pytest.mark.parametrize("service", ["broker", "exchange"])
def test_the_bare_unquoted_signature_agent_is_refused(
    seeded: SeededFixture, service: Target, fresh_agent: FreshAgent
) -> None:
    """The v1.0.8 form, the bare ``Signature-Agent: https://<host>``, is no form WG-00 knows.

    The control is the same call signed in the dictionary form.
    """
    call = partial(wba_call, fresh_agent, service, seeded.free.uri, python_signer(fresh_agent))
    dictionary: dict[str, object] = {"extra_components": FORA_COMPONENTS}
    answer = _refused_after_its_control(
        call,
        f"a bare-form call to the {service}",
        control=dictionary,
        defect={**dictionary, "unquoted": True},
    )
    sent = answer.sent.headers.get("signature-agent")
    assert sent == agent_origin(fresh_agent), f"the call carried Signature-Agent {sent!r}"
