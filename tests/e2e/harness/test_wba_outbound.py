"""E2E: the signatures FORA produces pass third-party Web Bot Auth verifiers.

FORA's own signatures are read off the wire and handed to verifiers FORA did not
write: the Python oracle (pyauth ``http-message-signatures`` plus a
dictionary-member resolver, ``wba_oracle.py``) and Cloudflare's ``web-bot-auth``
library in the ``wba-oracle-ts`` sidecar. Each verifier is given the key set the
signer serves at its own Signature-Agent origin, fetched over https with the
stack's local CA, so a pass means a WBA verifier on the open web would accept
the same request.

Where the signatures come from, each read from the capture proxy's record of the
request as it was sent:

- one ``fora_discover`` MCP tool call by an agent this test signs up at the
  identity service. It causes two signed hops: the identity service's Resolve
  to the Broker, signed as the agent, and the DiscoverResources query the Broker
  originates to the Exchange. Both are scoped by the agent's own host, which the
  first names in its Signature-Agent and the second as its requester;
- one purchase through the Broker: agent-e2e resolves an offer and buys it with
  BrokerService.ExecuteTransaction through the FORA Python SDK, and the Broker
  re-packages the purchase into an ExecuteTransaction to the Exchange, which it
  signs alone. Both hops are scoped by an X-Request-ID the test sends, which the
  Broker copies onto every request it sends for the call;
- one Broker call made by the FORA Python SDK client, signed as agent-e2e with
  the https origin ``https://agent-e2e``.

The Broker never forwards an agent's request unchanged. It originates the
discovery call and re-packages the purchase, so the agent's transport signature
ends at the Broker. On a purchase the agent's binding to the Exchange is the
AgentAcceptance on each item of the body: a detached signature over the offer's
signature, the requester and the idempotency key.

What is proven, per the Web Bot Auth profile of the pinned protocol module
``github.com/FORA-Protocol/protocol`` (Authentication, "The Web Bot Auth
Profile", for draft-ietf-webbotauth-httpsig-protocol-00, "WG-00"):

- on both Broker paths, the first hop's signature (the identity service's, as the
  agent, on discovery; the agent's own, through the FORA Python SDK, on a
  purchase) and the Broker's signature on every request it sends an Exchange
  each pass both verifiers;
- on both Broker paths, the request the Broker sends carries one signature, its
  own: it covers the FORA components and exactly one Signature-Agent member, its
  own, naming the Broker's directory, and no Signature or Signature-Input member;
- on the purchase, every item's AgentAcceptance reaches the Exchange exactly as
  the agent sent it;
- every signature on both Broker paths carries tag="web-bot-auth",
  alg="ed25519" and a 64-byte nonce, one test per property so a failure names
  it;
- an SDK client request passes both verifiers.

A last test proves the verification path itself: a conformant signature made by
a third party and read back from the capture passes it, and corrupted copies
fail it. Without that a pass above could come from a path that checks nothing.

In-network only: the capture proxy and the sidecar publish no host port.
"""

from __future__ import annotations

import asyncio
import dataclasses
import os
import secrets
from contextlib import closing

import pytest
from wire.models import DiscoveryRequest, ResourceQuery, TransactionRequest

from .broker_purchase import resolve_request
from .capture_proxy import BROKER_DOMAIN, CaptureProxy, CapturedRequest
from .e2e_catalog import SeededFixture
from .mcp_agents import McpAgentFactory
from .mcp_client import mcp_session
from .sdk_client import sdk_broker_client
from .stack_urls import StackURLs
from .wba_checks import b64_any
from .wba_conformance import (
    AGENT_ORIGIN,
    FORA_COMPONENTS,
    RESOLVE_PATH,
    assert_passes,
    assert_signed_alone_by_the_broker,
    agent_sdk_signer,
    bodied_request,
    buy_as_agent,
    cloudflare_verdict,
    https_jwks,
    labels,
    python_sign,
    python_verdict,
    send,
    signature_params,
    with_header,
)
from .wba_oracle import Check
from .wba_oracle_ts import TSOracle

pytestmark = [
    pytest.mark.skipif(
        os.environ.get("FORA_E2E_IN_NETWORK") != "1",
        reason="the capture proxy and the oracle sidecar are reachable only in-network",
    ),
    pytest.mark.stack_isolation("shared-without-cleanup"),
]


@dataclasses.dataclass(frozen=True)
class Hops:
    """The signed requests one call through the Broker causes.

    ``sender`` names who signed the first hop: the identity service for a
    ``fora_discover`` call, the agent itself for a purchase. ``to_exchange``
    holds every request the Broker sent an Exchange for the call.
    """

    to_broker: CapturedRequest
    to_exchange: tuple[CapturedRequest, ...]
    sender: str

    def named(self) -> list[tuple[str, CapturedRequest]]:
        return [
            (f"{self.sender} to Broker", self.to_broker),
            *((f"Broker to {e.target}", e) for e in self.to_exchange),
        ]


async def _discover(stack: StackURLs, bearer: str, uri: str) -> None:
    async with mcp_session(stack, bearer) as mcp:
        result = await mcp.call("fora_discover", {"uris": [uri]})
    groups = result.structured.get("offer_groups") or []
    assert groups and groups[0].get("offers"), f"fora_discover found no offer: {result}"


@pytest.fixture
def hops(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
    capture_proxy: CaptureProxy,
) -> Hops:
    """Drive ``fora_discover`` once as an agent of this test's own; read both signed hops."""
    agent = fresh_mcp_agent_factory()
    traffic = capture_proxy.watch(agent.subdomain)
    asyncio.run(_discover(identity_ready, agent.bearer, seeded.free.uri))
    to_broker = traffic.await_carrying(DiscoveryRequest, caller="identity", target=BROKER_DOMAIN)
    to_exchange = traffic.await_carrying(ResourceQuery, caller="broker")
    assert len(to_broker) == 1, f"want one identity-to-Broker Resolve: {to_broker}"
    return Hops(to_broker=to_broker[0], to_exchange=tuple(to_exchange), sender="identity")


@pytest.fixture
def purchase_hops(
    compose_stack: StackURLs, seeded: SeededFixture, capture_proxy: CaptureProxy
) -> Hops:
    """Buy the free philosophy article through the Broker as agent-e2e; read both signed hops."""
    request_id = f"wba-purchase-{secrets.token_hex(8)}"
    traffic = capture_proxy.watch(request_id)
    item = buy_as_agent(compose_stack, seeded.free.uri, request_id=request_id)
    assert item.retrieval_endpoint, f"the purchase delivered nothing: {item}"
    to_broker = traffic.carrying(TransactionRequest, target=BROKER_DOMAIN)
    to_exchange = traffic.carrying(TransactionRequest, caller="broker")
    assert len(to_broker) == 1, f"want the agent's one ExecuteTransaction: {to_broker}"
    assert to_exchange, "the Broker answered the purchase and sent the Exchange nothing"
    return Hops(to_broker=to_broker[0], to_exchange=tuple(to_exchange), sender="agent")


@pytest.fixture(
    params=[
        pytest.param("hops", id="fora_discover-to-resolve"),
        pytest.param("purchase_hops", id="broker-purchase"),
    ]
)
def broker_path(request: pytest.FixtureRequest) -> Hops:
    """The hops of each Broker path: the discovery it originates, the purchase it re-packages."""
    hops: Hops = request.getfixturevalue(request.param)
    return hops


def _only_label(entry: CapturedRequest, what: str) -> str:
    found = labels(entry)
    assert len(found) == 1, f"{what} carries {found}, want one signature"
    return found[0]


def test_each_hop_signature_passes_the_third_party_verifiers(
    broker_path: Hops, ts_oracle: TSOracle
) -> None:
    """Every hop's one signature passes both verifiers, on both Broker paths.

    The first hop is signed as the agent: by the identity service on discovery,
    by the agent itself on a purchase. Its signature covers the Broker's
    @target-uri and the body it carried, so it verifies against the request it
    was made for. The Broker sends each Exchange a request of its own, a query or
    a re-packaged purchase, so the first hop's signature cannot verify there; the
    Broker signs that request alone, and its signature is the one checked.
    """
    for what, entry in broker_path.named():
        assert_passes(entry, _only_label(entry, what), what, ts_oracle)


def test_the_broker_hop_signature_covers_its_own_request_and_no_other_signature(
    broker_path: Hops,
) -> None:
    """The Broker signs each request it sends an Exchange alone, on its own terms, on both paths.

    The Broker originates the discovery query and re-packages the purchase, so
    the request it sends is its own and so is the one signature that binds it.
    The request carries no agent signature headers. The signature covers the
    FORA components of an RPC and exactly one Signature-Agent member, keyed to its
    own label and naming the Broker's directory. It covers no Signature or
    Signature-Input member: under WG-00 Section 5.2.2 a signer that changed
    values an earlier signature covers MUST NOT cover that signature.
    """
    for entry in broker_path.to_exchange:
        assert_signed_alone_by_the_broker(entry, f"the Broker's request to {entry.target}")


def test_a_broker_purchase_carries_each_agent_acceptance_to_the_exchange_unchanged(
    purchase_hops: Hops,
) -> None:
    """Each item's AgentAcceptance reaches the Exchange exactly as the agent sent it.

    The agent's transport signature ends at the Broker, so the acceptance is
    the agent's binding to the purchase at the Exchange. Each body is read
    through the generated TransactionRequest, whatever encoding it travelled in,
    and the (signature, algorithm) pairs are compared item by item.
    """
    sent = _acceptances(purchase_hops.to_broker)
    carried = [pair for entry in purchase_hops.to_exchange for pair in _acceptances(entry)]
    assert sent and all(signature for signature, _ in sent), (
        f"the agent's request carries no acceptance to compare: {sent}"
    )
    assert carried == sent, (
        f"the Broker changed the acceptances: the agent sent {sent}, the Exchange got {carried}"
    )


def _acceptances(entry: CapturedRequest) -> list[tuple[str, str]]:
    """Each item's AgentAcceptance (signature, algorithm); ("", "") for an item with none."""
    message = entry.message_as(TransactionRequest)
    return [
        (
            item.agent_acceptance.signature if item.agent_acceptance else "",
            (item.agent_acceptance.signature_algorithm or "") if item.agent_acceptance else "",
        )
        for item in message.items or []
    ]


def _tag_problem(params: dict[str, object]) -> str | None:
    tag = params.get("tag")
    return None if tag == "web-bot-auth" else f"tag is {tag!r}, not 'web-bot-auth'"


def _alg_problem(params: dict[str, object]) -> str | None:
    alg = params.get("alg")
    return None if alg == "ed25519" else f"alg is {alg!r}, not 'ed25519'"


def _nonce_problem(params: dict[str, object]) -> str | None:
    nonce = params.get("nonce")
    decoded = b64_any(nonce) if isinstance(nonce, str) else None
    if decoded is not None and len(decoded) == 64:
        return None
    size = "no nonce" if nonce is None else f"{len(decoded or b'')} bytes"
    return f"nonce {nonce!r} decodes to {size}, not 64 bytes"


_PROPERTIES = {"tag": _tag_problem, "alg": _alg_problem, "nonce": _nonce_problem}


@pytest.mark.parametrize("prop", ["tag", "alg", "nonce"])
def test_every_fora_signature_carries_the_wba_parameter(broker_path: Hops, prop: str) -> None:
    """tag="web-bot-auth", alg="ed25519" and a 64-byte nonce, on every label of every hop.

    Both Broker paths: discovery, whose first hop the identity service signs, and a
    purchase, whose first hop the FORA Python SDK signs as the agent.
    """
    problems = [
        f"{what} {label}: {problem}"
        for what, entry in broker_path.named()
        for label in labels(entry)
        if (problem := _PROPERTIES[prop](signature_params(entry, label))) is not None
    ]
    assert not problems, "\n".join(problems)


def test_an_sdk_client_request_passes_both_verifiers(
    compose_stack: StackURLs,
    seeded: SeededFixture,
    capture_proxy: CaptureProxy,
    ts_oracle: TSOracle,
) -> None:
    """The SDK's Broker client, signing as agent-e2e under https://agent-e2e."""
    request_id = f"wba-sdk-{secrets.token_hex(8)}"
    traffic = capture_proxy.watch(request_id)
    client = sdk_broker_client(
        compose_stack, signer=agent_sdk_signer(), request_id=lambda: request_id
    )
    with closing(client):
        client.resolve(resolve_request([seeded.free.uri]))
    found = traffic.carrying(DiscoveryRequest, target=BROKER_DOMAIN)
    assert len(found) == 1, f"want the SDK's one Resolve in the capture: {found}"
    entry = found[0]
    assert_passes(entry, _only_label(entry, "the SDK request"), "SDK to Broker", ts_oracle)


def test_the_verification_path_tells_a_conformant_signature_from_a_corrupted_one(
    compose_stack: StackURLs, capture_proxy: CaptureProxy, ts_oracle: TSOracle
) -> None:
    """The path the tests above use accepts a conformant signature and rejects copies of it.

    A WG-00 signature from the Python oracle, plus the FORA components, goes to
    the Broker and is read back from the capture, exactly as the FORA hops are.
    Whether the Broker accepts it does not matter here. It passes both verifiers
    with the key fetched from https://agent-e2e. Then one character of the
    signature is changed, and separately the request is moved to another
    authority, and both verifiers reject each copy.
    """
    request_id = f"wba-path-{secrets.token_hex(8)}"
    traffic = capture_proxy.watch(request_id)
    request = bodied_request(f"{compose_stack.broker}{RESOLVE_PATH}", {"probe": request_id})
    send(request, python_sign(request, extra_components=FORA_COMPONENTS), request_id=request_id)
    (entry,) = traffic.requests()
    assert_passes(entry, "sig1", "the conformant probe", ts_oracle)

    jwks = https_jwks(AGENT_ORIGIN)
    value = entry.header("signature")
    flipped = value[:10] + ("A" if value[10] != "A" else "B") + value[11:]
    for corrupted in (
        with_header(entry, "Signature", flipped),
        dataclasses.replace(entry, target="exchange:8081"),
    ):
        python = python_verdict(corrupted, "sig1", jwks)
        assert python.failed is Check.SIGNATURE, python
        cloudflare = cloudflare_verdict(ts_oracle, corrupted, "sig1", jwks)
        assert not cloudflare.ok and cloudflare.code == "VerificationFailed", cloudflare
