"""The requests the identity, key and fetch obligations send, and how they read a refusal.

Every negative in those suites alters one of a few control requests: a fresh
agent's discovery of a priced philosophy resource at exchange-a, the same
agent's Resolve at the Broker, and the same agent's purchase through the Broker.
They are built here once, through the agent's own SDK clients, so each test
module states only what it changes and what it expects.

A refusal is read through the SDK's typed error: the Connect code, and the
``request_auth_failure`` reason of its ErrorDetail. Nothing here reads message
text.
"""

from __future__ import annotations

import time
from collections.abc import Callable
from typing import Any

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk.client import CallError
from fora_sdk.core import DiscoveryResult, VerifiedOffer
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import BrokerClient, Client
from fora_sdk.wba import MAX_SIGNATURE_LIFETIME
from fora_sdk.window import clock_window
from wire.models import BrokerTransactionResponse

from ._multi_exchange_common import _MUSIC_URI, _PHILOSOPHY_URI, _SFX_URI
from .broker_purchase import (
    one_group_per_uri,
    only_verified_offer,
    resolve_request,
    resource_query,
)
from .e2e_catalog import DEMO_SFX_DOMAIN
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, EXCHANGE_C_DOMAIN
from .fresh_agent import FreshAgent
from .httpsig_signer import signing_transport
from .malformed import Request, SeamHTTP
from .sdk_client import BeforeSign

#: A resource exchange-a sells with a signed offer: the probe of every discovery.
PROBE_URI = _PHILOSOPHY_URI

#: One resource per Exchange the Broker routes to, for a Resolve that shows every
#: Exchange verifies the Broker's fan-out again.
ROUTED_URIS = {
    EXCHANGE_A_DOMAIN: _PHILOSOPHY_URI,
    EXCHANGE_B_DOMAIN: _MUSIC_URI,
    EXCHANGE_C_DOMAIN: _SFX_URI,
}

#: A resource exchange-c sells at 0.25 USD per access: a purchase of it moves money.
PRICED_URI = f"http://{DEMO_SFX_DOMAIN}/sfx/wooden-door-creak.json"

#: The signature validity the SDK signs with by default, in seconds: the Web Bot
#: Auth profile's ceiling.
SDK_SIGNING_TTL_S = MAX_SIGNATURE_LIFETIME

SIGNATURE_MISSING = "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_MISSING"
SIGNATURE_INVALID = "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID"
SIGNATURE_STALE = "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_STALE"


def probe_query(exchange: str = EXCHANGE_A_DOMAIN, uri: str = PROBE_URI) -> dict[str, Any]:
    """The discovery of ``uri`` addressed to ``exchange``; the SDK fills the requester."""
    return resource_query(exchange, [uri])


def discovery_by(
    agent: FreshAgent, *, exchange: str = EXCHANGE_A_DOMAIN, query: dict[str, Any] | None = None
) -> Request[Client, DiscoveryResult]:
    """``agent``'s discovery at ``exchange``: the control a discovery negative alters."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> Client:
        return agent.exchange_client(exchange, signer=signer, before_sign=before_sign, http=http)

    message = query if query is not None else probe_query(exchange)
    return Request(client=client, verb=lambda c: c.discover, message=message)


def resolve_by(agent: FreshAgent, uri: str = PROBE_URI) -> Request[BrokerClient, DiscoveryResult]:
    """``agent``'s Resolve of ``uri`` at the Broker: the control a Resolve negative alters."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> BrokerClient:
        return agent.broker_client(signer=signer, before_sign=before_sign, http=http)

    return Request(client=client, verb=lambda c: c.resolve, message=resolve_request([uri]))


def answered_with_offer(uri: str = PROBE_URI) -> Callable[[DiscoveryResult], None]:
    """A check that a discovery or Resolve came back with a verified offer for ``uri``."""

    def check(result: DiscoveryResult) -> None:
        group = one_group_per_uri(result, [uri], f"discovery of {uri}")[uri]
        only_verified_offer(group, f"discovery of {uri}")

    return check


def discover_outcome(
    agent: FreshAgent, *, signer: SigningTransport | None = None
) -> CallError | None:
    """``agent``'s probe discovery once: None when answered with an offer, else the refusal."""
    try:
        result = agent.exchange_client(EXCHANGE_A_DOMAIN, signer=signer).discover(probe_query())
    except CallError as refusal:
        return refusal
    answered_with_offer()(result)
    return None


def signer_for(
    agent: FreshAgent, key: Ed25519PrivateKey, *, created_offset_s: float = 0.0
) -> SigningTransport:
    """The SDK's signer naming ``agent``'s directory, signing with ``key``.

    ``created_offset_s`` shifts the signature's ``created`` from now; the window
    lasts the SDK's default validity from there.
    """
    window = clock_window(lambda: time.time() + created_offset_s, SDK_SIGNING_TTL_S)
    return signing_transport(agent.host, key, window=window)


def auth_reason(refusal: CallError) -> str | None:
    """The ``request_auth_failure`` reason a refusal carries, or None when it carries none."""
    block = refusal.detail.request_auth_failure if refusal.detail is not None else None
    return block.reason.value if block is not None else None


def assert_auth_refusal(refusal: CallError, reason: str) -> None:
    """``refusal`` is ``unauthenticated`` with ``request_auth_failure`` ``reason``."""
    assert refusal.code == "unauthenticated", f"refused as {refusal.code!r}: {refusal}"
    assert auth_reason(refusal) == reason, f"want {reason}, the refusal carries: {refusal}"


def offer_for(agent: FreshAgent, uri: str) -> VerifiedOffer:
    """The Broker's ranked, verified winner for ``uri``, resolved as ``agent``."""
    result = agent.broker.resolve(resolve_request([uri]))
    leg = f"broker resolve of {uri}"
    return only_verified_offer(one_group_per_uri(result, [uri], leg)[uri], leg)


def bought(answer: BrokerTransactionResponse | CallError) -> bool:
    """Whether a purchase through the Broker bought its one item: a delivery URL came back."""
    if isinstance(answer, CallError):
        return False
    items = answer.items or []
    return len(items) == 1 and items[0].refusal is None and bool(items[0].retrieval_endpoint)


def buy_through_broker(
    agent: FreshAgent, offer: VerifiedOffer
) -> BrokerTransactionResponse | CallError:
    """``agent``'s purchase of ``offer`` through the Broker: its answer, or its refusal."""
    try:
        return agent.broker.execute([offer])
    except CallError as refusal:
        return refusal


def every_exchange_verifies_the_broker(agent: FreshAgent) -> bool:
    """Whether one Resolve brings back a verified offer from each Exchange in ROUTED_URIS."""
    uris = list(ROUTED_URIS.values())
    try:
        result = agent.broker.resolve(resolve_request(uris))
    except CallError:
        return False
    groups: dict[str, Any] = {group.uri: group for group in result.groups}
    return all(uri in groups and groups[uri].result.verified for uri in uris)
