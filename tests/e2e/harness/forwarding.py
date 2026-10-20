"""A request forwarded unchanged: the agent's signature, then one appended per forwarding party.

The protocol lets a party that passes a request on unchanged append its own RFC 9421
signature beside the earlier ones, under its own label and its own ``Signature-Agent``
member, and optionally cover the signature before it (WG-00 §5.2.2). The Exchange
resolves each signature's key in the directory that signature's own member names,
verifies every signature, and counts them against its published
``max_intermediary_hops``.

Every signature here is the SDK's. A forwarding party is a
:class:`fora_sdk.signing_transport.SigningTransport` built with ``append_only=True``,
so ``sign_outbound`` appends to the signatures the request already carries instead of
signing it fresh; ``cover_previous`` makes the appended signature cover the last
earlier one completely. The agent's own signature is the one its SDK client makes.

* :func:`forwarder` is a forwarding party's signer: a fresh agent's key and directory,
  or any key under any directory for a negative; :func:`unpublished_forwarder` signs
  under a party's directory with a key that directory does not publish.
* :func:`hop` is an after-signing alteration (:mod:`harness.after_signing`) that
  appends one forwarding party's signature to the signed request.
* :func:`forwarded_discovery` is the control a forwarding negative alters: the agent's
  discovery at an Exchange with the hops appended in order, built so that the
  declared alteration of :func:`harness.malformed.malformed` still applies on top of
  them: a different signer replaces the agent's own signature, and an after-signing
  alteration sees the request with every hop already appended.
* :func:`send` sends such a control once, unaltered, for a positive.

Infrastructure, outside the scope of the harness bans: a test module never builds a
signer from raw key material; it names the parties and the seam builds the signers.
"""

from __future__ import annotations

from collections.abc import Sequence
from contextlib import closing
from typing import Any, Protocol, TypeVar

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk.core import DiscoveryResult
from fora_sdk.identity import generate_key
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import Client
from fora_sdk.thumbprint import thumbprint

from .after_signing import Alteration as AfterSigningAlteration
from .after_signing import after_signing
from .exchanges import EXCHANGE_A_DOMAIN
from .fresh_agent import FreshAgent
from .httpsig_signer import directory_origin
from .identity_requests import probe_query
from .malformed import Request, SeamHTTP
from .sdk_client import BeforeSign


class _Closable(Protocol):
    def close(self) -> None: ...


C = TypeVar("C", bound=_Closable)
T = TypeVar("T")

#: Response headers that describe the bytes on the wire rather than the decoded body:
#: an answer handed back from the inner client has already been decoded.
_WIRE_ONLY_HEADERS = frozenset({"content-encoding", "content-length", "transfer-encoding"})


def forwarder(
    party: FreshAgent,
    *,
    cover_previous: bool = True,
    using: Ed25519PrivateKey | None = None,
    directory: str | None = None,
) -> SigningTransport:
    """The signer of a forwarding party that appends its signature to a signed request.

    By default it signs with ``party``'s own key under ``party``'s own directory, and
    covers the signature before it, as a party forwarding a request unchanged may.
    ``using`` and ``directory`` replace either half for a negative: a key the directory
    does not publish, or a directory other than the key's.
    """
    signing_key = using if using is not None else party.ed25519
    return SigningTransport(
        signer_seed=signing_key.private_bytes_raw(),
        keyid=thumbprint(signing_key.public_key().public_bytes_raw()),
        signature_agent=directory_origin(directory if directory is not None else party.host),
        append_only=True,
        cover_previous=cover_previous,
    )


def unpublished_forwarder(party: FreshAgent) -> SigningTransport:
    """A forwarding party's signer under ``party``'s directory, over a key it does not publish."""
    key, _thumbprint = generate_key()
    return forwarder(party, using=key)


def _append(request: httpx.Request, signer: SigningTransport) -> httpx.Request:
    """``request`` with ``signer``'s signature appended beside the ones it carries."""
    request.read()
    signed = signer.sign_outbound(
        method=request.method,
        url=str(request.url),
        body=request.content,
        authorization=request.headers.get("authorization", ""),
        headers=dict(request.headers),
    )
    replaced = {name.lower() for name in signed.headers}
    headers = [
        (name, value)
        for name, value in request.headers.multi_items()
        if name.lower() not in replaced and name.lower() != "content-length"
    ]
    headers.extend(signed.headers.items())
    return httpx.Request(request.method, request.url, headers=headers, content=request.content)


def hop(signer: SigningTransport) -> AfterSigningAlteration:
    """An after-signing alteration appending ``signer``'s signature: one more forwarding party."""
    return lambda request: _append(request, signer)


class _Forwarding(httpx.BaseTransport):
    """Appends each hop's signature in order, then sends through the seam's own client.

    The seam's client records what is sent and applies the declared alteration, so it
    sees the request exactly as the last forwarding party sends it on.
    """

    def __init__(self, hops: Sequence[SigningTransport], inner: httpx.Client) -> None:
        self._hops = list(hops)
        self._inner = inner

    def handle_request(self, request: httpx.Request) -> httpx.Response:
        for signer in self._hops:
            request = _append(request, signer)
        answer = self._inner.send(request)
        headers = [
            (name, value)
            for name, value in answer.headers.multi_items()
            if name.lower() not in _WIRE_ONLY_HEADERS
        ]
        return httpx.Response(
            answer.status_code, headers=headers, content=answer.content, request=request
        )


def forwarded_discovery(
    agent: FreshAgent,
    hops: Sequence[SigningTransport],
    *,
    exchange: str = EXCHANGE_A_DOMAIN,
    query: dict[str, Any] | None = None,
) -> Request[Client, DiscoveryResult]:
    """``agent``'s discovery at ``exchange``, forwarded unchanged through ``hops`` in order."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> Client:
        forwarding = httpx.Client(transport=_Forwarding(hops, http))
        return agent.exchange_client(
            exchange, signer=signer, before_sign=before_sign, http=forwarding
        )

    message = query if query is not None else probe_query(exchange)
    return Request(client=client, verb=lambda c: c.discover, message=message)


def send(request: Request[C, T]) -> T:
    """Send ``request`` once, unaltered, through the same seams a declared negative uses."""
    with after_signing() as http:
        client = request.client(signer=None, before_sign=None, http=http)
        with closing(client):
            return request.verb(client)(request.message)
