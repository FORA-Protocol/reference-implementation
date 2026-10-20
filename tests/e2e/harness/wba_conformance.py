"""What the three Web Bot Auth conformance suites share.

The suites (``test_wba_inbound.py``, ``test_wba_outbound.py`` and
``test_wba_directories.py``) check FORA against the Web Bot Auth profile of the
pinned protocol module ``github.com/FORA-Protocol/protocol`` (its Authentication
page, "The Web Bot Auth Profile"), which targets
draft-ietf-webbotauth-httpsig-protocol-00 ("WG-00"): every FORA request
signature is a WG-00 signature, a FORA RPC signature also covers the four FORA
components, and every participant serves a WG-00 key directory. This module
holds what more than one suite needs, and everything that touches HTTP, so the
test modules read typed answers and never a raw response:

- the agent the runner signs as, as a third-party signer and as an SDK signer;
- building a bodied request the way any WBA library would, sending it with a
  third-party signature, and reading the answer (:class:`Answer`);
- a purchase through the Broker as that agent, optionally re-signed by a
  third-party library after the SDK built it;
- reading a captured request's signatures back and handing them to the two
  third-party verifiers;
- fetching a key directory over https under the stack's local CA.

The signing agent is agent-e2e, a registered static agent whose private key the
harness holds. It signs with the https origin ``https://agent-e2e``: the capture
proxy answers https for that name under the local CA (it is in the compose
file's x-through-capture-proxy list), so the origin names a directory a WBA
verifier can actually fetch. ``Requester.domain`` is that origin's host on every
body built here, because a verifier refuses a request whose requester is not the
directory its signature resolved from.

Nothing here signs or verifies by itself. Third-party signing and verifying is
the oracles' job (``wba_oracle.py``, ``wba_oracle_ts.py``), and the oracles never
import FORA code. This module may, because it only builds requests, drives the
SDK and reads answers.
"""

from __future__ import annotations

import json
import uuid
from collections.abc import Mapping
from contextlib import closing
from dataclasses import dataclass, replace
from typing import Any, cast

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk import ProtocolVersion
from fora_sdk.hosts import host_of
from fora_sdk.httpsig import content_digest
from fora_sdk.signing_transport import SigningTransport
from http_message_signatures import http_sfv
from wire.models import TransactionResultItem

from .after_signing import after_signing
from .broker_purchase import only_item, resolve_offer, resolve_request
from .capture_proxy import CapturedRequest
from .constants import WBA_DIRECTORY_PATH, requester
from .e2e_catalog import USD_AGENT_ID, USD_AGENT_KEY_PATH
from .exchanges import recipient_of
from .httpsig_signer import load_keypair, signing_transport
from .lambda_edge import PUBLISHER, invoke, viewer_request_event
from .local_ca import local_ca_context
from .sdk_client import sdk_broker_client
from .stack_urls import StackURLs
from .wba_directory import DirectoryResult, check_directory
from .wba_oracle import VerifyResult, private_jwk, sign, verify
from .wba_oracle_ts import TSOracle, TSVerifyResult

# The registered static agent the runner signs as, the https origin it names,
# and that origin's host, which every body names as Requester.domain.
AGENT_ID = USD_AGENT_ID
AGENT_ORIGIN = "https://agent-e2e"
AGENT_HOST = host_of(AGENT_ORIGIN)

# The directory the Broker signs as in this stack.
BROKER_ORIGIN = "https://broker"

# What the profile adds to the WG-00 minimum on a FORA RPC.
FORA_COMPONENTS = ("@method", "@target-uri", "content-digest", "authorization")

RESOLVE_PATH = "/fora.v1.BrokerService/Resolve"
DISCOVER_PATH = "/fora.v1.ExchangeService/DiscoverResources"

# The JWK alg every directory key must carry: the JOSE name, a deliberate FORA
# deviation from WG-00 Section 5.5.1 (the directory suite's docstring says why).
EXPECTED_JWK_ALG = "EdDSA"

_WBA_HEADERS = frozenset({"signature", "signature-input", "signature-agent"})
_TIMEOUT_SEC = 30.0


def agent_key() -> Ed25519PrivateKey:
    """agent-e2e's private key, from the fixture its directory host serves."""
    return load_keypair(USD_AGENT_KEY_PATH)[1]


def agent_sdk_signer() -> SigningTransport:
    """The SDK's request signer for agent-e2e, naming the same origin the oracles sign as."""
    return signing_transport(AGENT_ORIGIN, agent_key())


# ---- Requests a third-party library signs --------------------------------------


def bodied_request(url: str, body: Mapping[str, Any] | bytes) -> httpx.Request:
    """An unsigned Connect-unary POST carrying everything a FORA signature covers.

    ``body`` is a message, serialized here, or bytes already serialized by the
    caller. The empty Authorization header is what an agent-direct FORA call
    sends: the profile requires ``authorization`` to be covered, and RFC 9421
    covers only a header that is present.
    """
    payload = (
        body if isinstance(body, bytes) else json.dumps(dict(body), separators=(",", ":")).encode()
    )
    headers = {
        "Content-Type": "application/json",
        "Connect-Protocol-Version": "1",
        "Content-Digest": content_digest(payload),
        "Authorization": "",
    }
    return httpx.Request("POST", url, headers=headers, content=payload)


def resolve_to_broker(stack: StackURLs, uri: str) -> httpx.Request:
    """An unsigned Broker Resolve for ``uri``, asking as agent-e2e."""
    body = {**resolve_request([uri]), "requester": requester(AGENT_ID, AGENT_HOST)}
    return bodied_request(f"{stack.broker}{RESOLVE_PATH}", body)


def discover_at(exchange_url: str, uri: str) -> httpx.Request:
    """An unsigned DiscoverResources for ``uri`` at the Exchange at ``exchange_url``.

    The query names the Exchange it is addressed to by identity, not by the URL it
    is posted to, and stamps ``ver`` from the SDK's constant, as the harness's
    ver-wire guard requires of every body. Its ``id`` is fresh on every call.
    """
    url = f"{exchange_url}{DISCOVER_PATH}"
    body = {
        "ver": ProtocolVersion,
        "id": f"q-{uuid.uuid4().hex}",
        "exchange": recipient_of(url),
        "requester": requester(AGENT_ID, AGENT_HOST),
        "uris": [uri],
    }
    return bodied_request(url, body)


def python_sign(request: httpx.Request, **options: Any) -> dict[str, str]:
    """Sign ``request`` as agent-e2e through the Python oracle (``origin`` overrides)."""
    return sign(request, agent_key(), options.pop("origin", AGENT_ORIGIN), **options)


def cloudflare_sign(oracle: TSOracle, request: httpx.Request, **options: Any) -> dict[str, str]:
    """Sign ``request`` as agent-e2e through Cloudflare's library (``origin`` overrides)."""
    origin = options.pop("origin", AGENT_ORIGIN)
    return oracle.sign(request, private_jwk(agent_key()), origin, **options)


@dataclass(frozen=True)
class Requested:
    """One signature an Accept-Signature header asks for: its components and parameters."""

    components: tuple[str, ...]
    params: dict[str, object]


@dataclass(frozen=True)
class Answer:
    """A FORA answer as a WBA client reads it.

    ``offers`` are the offers an accepted Resolve or DiscoverResources carries.
    ``requested`` is the Accept-Signature header (WG-00 Section 5.3) read as a
    Dictionary: each label it names, with the components it asks that signature
    to cover, written as in Signature-Input, and its parameters. A missing header
    reads as empty. ``code`` is the Connect error code of a refusal, such as
    ``unauthenticated``, and empty when the answer carries none. A refusal is read
    by its code, never by its message text (ADR-028 D4).
    """

    status: int
    offers: tuple[dict[str, Any], ...]
    requested: dict[str, Requested]
    code: str
    text: str

    def requests_component(self, component: str) -> bool:
        return any(component in r.components for r in self.requested.values())

    def requests_dictionary_member(self) -> bool:
        """Whether some requested signature covers a keyed Signature-Agent member."""
        return any(
            c.startswith('"signature-agent";key=')
            for r in self.requested.values()
            for c in r.components
        )

    def requests_tag(self, tag: str) -> bool:
        return any(r.params.get("tag") == tag for r in self.requested.values())


def _offers(payload: object) -> tuple[dict[str, Any], ...]:
    """The offers a Resolve or DiscoverResources answer carries."""
    if not isinstance(payload, dict):
        return ()
    groups = cast(list[dict[str, Any]], payload.get("offer_groups") or [])
    offers = [o for g in groups for o in cast(list[dict[str, Any]], g.get("offers") or [])]
    return tuple(offers + cast(list[dict[str, Any]], payload.get("offers") or []))


def _requested(raw: str | None) -> dict[str, Requested]:
    if not raw:
        return {}
    parsed = http_sfv.Dictionary()
    parsed.parse(raw.encode())
    return {
        label: Requested(tuple(str(c) for c in member), dict(member.params))
        for label, member in parsed.items()
        if isinstance(member, http_sfv.InnerList)
    }


def send(
    request: httpx.Request, signature: Mapping[str, str], *, request_id: str | None = None
) -> Answer:
    """Send ``request`` with the signature headers a signer returned, and read the answer."""
    headers = {**dict(request.headers), **signature}
    if request_id is not None:
        headers["X-Request-ID"] = request_id
    response = httpx.post(
        str(request.url), content=request.content, headers=headers, timeout=_TIMEOUT_SEC
    )
    try:
        payload: object = response.json()
    except ValueError:
        payload = None
    code = str(payload.get("code", "")) if isinstance(payload, dict) else ""
    return Answer(
        status=response.status_code,
        offers=_offers(payload) if response.status_code == httpx.codes.OK else (),
        requested=_requested(response.headers.get("Accept-Signature")),
        code=code,
        text=response.text,
    )


# ---- A purchase through the Broker as agent-e2e ----------------------------------


def _resigned_in_the_legacy_form(request: httpx.Request) -> httpx.Request:
    """The SDK's signed request, re-signed by the Python oracle in the legacy form.

    The SDK's Signature, Signature-Input and Signature-Agent are dropped and the
    oracle's take their place, over the same bytes: the body, its Content-Digest
    and the empty Authorization the SDK sent all stay as they were.
    """
    signature = python_sign(request, extra_components=FORA_COMPONENTS, legacy=True)
    kept = [
        (k, v)
        for k, v in request.headers.multi_items()
        if k.lower() not in _WBA_HEADERS | {"content-length"}
    ]
    return httpx.Request(
        request.method, request.url, headers=[*kept, *signature.items()], content=request.content
    )


def buy_as_agent(
    stack: StackURLs, uri: str, *, request_id: str, third_party_legacy: bool = False
) -> TransactionResultItem:
    """Resolve ``uri`` at the Broker and buy the winner through ExecuteTransaction, as agent-e2e.

    Both calls carry ``request_id`` as X-Request-ID, which the Broker copies onto
    every request it sends for them. The SDK builds the purchase: the request,
    each item's AgentAcceptance and the request acceptance, all signed with
    agent-e2e's key. With ``third_party_legacy`` the ExecuteTransaction's
    transport signature is then replaced, over the same bytes, by one the Python
    oracle makes in the legacy sf-string Signature-Agent form, which no FORA
    signer sends. The Resolve is always the SDK's own, so a refusal of the legacy
    form shows up on the purchase alone.
    """

    def minted() -> str:
        return request_id

    with closing(sdk_broker_client(stack, signer=agent_sdk_signer(), request_id=minted)) as c:
        offer = resolve_offer(c, uri)
    http = after_signing(_resigned_in_the_legacy_form) if third_party_legacy else None
    buyer = sdk_broker_client(stack, signer=agent_sdk_signer(), http=http, request_id=minted)
    with closing(buyer):
        response = buyer.execute([offer])
    return only_item(response, f"broker purchase of {uri}")


# ---- Reading a captured request's signatures --------------------------------------


def target_uri(entry: CapturedRequest) -> str:
    """The URL ``entry`` was sent to, as its signer signed it."""
    return f"{entry.scheme}://{entry.target}{entry.path}"


def _signature_inputs(entry: CapturedRequest) -> dict[str, Any]:
    parsed = http_sfv.Dictionary()
    parsed.parse(entry.header("signature-input").encode())
    return dict(parsed.items())


def labels(entry: CapturedRequest) -> list[str]:
    """The label of every signature ``entry`` carries, in header order."""
    return list(_signature_inputs(entry))


def covered(entry: CapturedRequest, label: str) -> tuple[str, ...]:
    """The components ``label`` covers, written as in Signature-Input."""
    return tuple(str(c) for c in _signature_inputs(entry)[label])


def signature_params(entry: CapturedRequest, label: str) -> dict[str, object]:
    """The parameters of the signature ``label`` (created, keyid, alg, nonce, tag...)."""
    return dict(_signature_inputs(entry)[label].params)


def signature_agent_members(entry: CapturedRequest) -> dict[str, str] | None:
    """Signature-Agent as member key -> String value; None when it is not a Dictionary."""
    members = http_sfv.Dictionary()
    try:
        members.parse(entry.header("signature-agent").encode())
    except ValueError:
        return None
    return {
        name: member.value
        for name, member in members.items()
        if isinstance(member, http_sfv.Item) and isinstance(member.value, str)
    }


def signer_origin(entry: CapturedRequest, label: str) -> str:
    """The origin the Signature-Agent member keyed ``label`` names.

    Fails, naming the form it found, when the header is not the WG-00
    dictionary or has no String member for ``label``: a WBA verifier resolves the
    signer's directory from that member and from nothing else.
    """
    members = signature_agent_members(entry) or {}
    header = entry.header("signature-agent")
    assert label in members, (
        f"Signature-Agent {header!r} is not the WG-00 dictionary form with a String "
        f"member keyed {label!r}"
    )
    origin = members[label]
    assert origin.startswith("https://"), f"Signature-Agent member {label!r} is {origin!r}"
    return origin


def with_header(entry: CapturedRequest, name: str, value: str) -> CapturedRequest:
    """``entry`` with header ``name`` set to ``value``, for a corrupted copy."""
    kept = tuple((k, v) for k, v in entry.headers if k.lower() != name.lower())
    return replace(entry, headers=(*kept, (name, value)))


def https_jwks(origin: str) -> dict[str, Any]:
    """The key set served at ``origin``'s directory over https, trusting the local CA.

    No redirect is followed. Only the keys matter here; the directory's own
    conformance is ``test_wba_directories.py``'s subject.
    """
    with httpx.Client(
        verify=local_ca_context(), follow_redirects=False, timeout=_TIMEOUT_SEC
    ) as client:
        response = client.get(f"{origin}{WBA_DIRECTORY_PATH}")
    assert response.status_code == httpx.codes.OK, (
        f"{origin}{WBA_DIRECTORY_PATH}: {response.status_code} {response.text[:200]}"
    )
    jwks: dict[str, Any] = response.json()
    return jwks


def python_verdict(entry: CapturedRequest, label: str, jwks: dict[str, Any]) -> VerifyResult:
    """The Python oracle's verdict on the signature ``label`` of ``entry``."""
    headers = httpx.Headers(list(entry.headers))
    return verify(headers, entry.method, target_uri(entry), entry.body, jwks, label=label)


def cloudflare_verdict(
    oracle: TSOracle, entry: CapturedRequest, label: str, jwks: dict[str, Any]
) -> TSVerifyResult:
    """Cloudflare's library's verdict on the signature ``label`` of ``entry``."""
    headers = httpx.Headers(list(entry.headers))
    return oracle.verify(headers, entry.method, target_uri(entry), jwks, label=label)


def assert_passes(
    entry: CapturedRequest, label: str, what: str, oracle: TSOracle | None = None
) -> None:
    """``label`` on ``entry`` passes the Python verifier and, given ``oracle``, Cloudflare's.

    The key set comes from the origin the signature's own Signature-Agent member
    names, over https: the only place a WBA verifier may take it from.
    """
    jwks = https_jwks(signer_origin(entry, label))
    python = python_verdict(entry, label, jwks)
    assert python.ok, (
        f"{what}: the Python verifier refused {label}: {python.failed}: {python.detail}"
    )
    if oracle is not None:
        cloudflare = cloudflare_verdict(oracle, entry, label, jwks)
        assert cloudflare.ok, (
            f"{what}: Cloudflare's verifier refused {label}: {cloudflare.code}: "
            f"{cloudflare.error} ({cloudflare.raw.get('cause')})"
        )


def assert_signed_alone_by_the_broker(entry: CapturedRequest, what: str) -> str:
    """The Broker signed ``entry`` alone, on its own terms; returns its label.

    The profile: the Broker never forwards an agent's request unchanged, so each
    request it sends an Exchange carries one signature, its own, and no agent
    signature headers. That signature covers the FORA components and exactly one
    Signature-Agent member, keyed to its own label and naming the Broker's
    directory, and no Signature or Signature-Input member of any label.
    """
    found = labels(entry)
    assert len(found) == 1, f"{what} carries the signatures {found}; the Broker signs alone"
    label = found[0]
    covers = covered(entry, label)
    missing = [f'"{c}"' for c in FORA_COMPONENTS if f'"{c}"' not in covers]
    assert not missing, f"{what}: the Broker's signature {covers} does not cover {missing}"
    inner = [c for c in covers if c.split(";")[0] in ('"signature"', '"signature-input"')]
    assert not inner, f"{what}: the Broker's signature covers other signatures: {inner}"
    agents = [c for c in covers if c.split(";")[0] == '"signature-agent"']
    assert agents == [f'"signature-agent";key="{label}"'], (
        f"{what}: the Broker's signature covers the Signature-Agent components {agents}, "
        f"want exactly its own member"
    )
    members = signature_agent_members(entry)
    assert members == {label: BROKER_ORIGIN}, (
        f"{what}: Signature-Agent is {entry.header('signature-agent')!r}, want the one "
        f"member {label}={BROKER_ORIGIN!r}"
    )
    return label


# ---- Key directories ----------------------------------------------------------------


def check_participant_directory(origin: str) -> DirectoryResult:
    """The WG-00 directory check on ``origin``'s live directory, over https under the local CA.

    A signed response is required, and the JWK alg is the JOSE name.
    """
    with httpx.Client(verify=local_ca_context(), timeout=_TIMEOUT_SEC) as client:
        return check_directory(origin, client, require_signed=True, expected_alg=EXPECTED_JWK_ALG)


def lambda_edge_directory(stack: StackURLs) -> httpx.Response:
    """The Lambda@Edge directory, as the response CloudFront would serve.

    The function answers the directory path itself at viewer-request, so its
    generated response IS what a client receives: CloudFront only adds the https
    transport. That transport is not the function's to get right, which is why
    the check runs on the generated response rather than over a TLS connection.
    The status, the headers, the body and its response signature are the
    function's, and those are what the WG-00 directory check reads.
    """
    url = f"https://{PUBLISHER}{WBA_DIRECTORY_PATH}"
    result = invoke(stack.lambda_edge, viewer_request_event(url))
    assert not result.pass_through, f"the function passed {url} through to the origin"
    headers = [
        (str(entry.get("key") or name), str(entry["value"]))
        for name, entries in result.headers.items()
        for entry in entries
    ]
    return httpx.Response(
        int(result.status or 0),
        headers=headers,
        content=(result.body or "").encode(),
        request=httpx.Request("GET", url),
    )
