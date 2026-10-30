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

- the test's own agent, as a third-party signer;
- a call the SDK builds, signs and decodes, re-signed by a third-party library
  over the same bytes in between, read back as a typed :class:`Answer`;
- a purchase through the Broker as that agent, optionally re-signed the same way;
- reading a captured request's signatures back and handing them to the two
  third-party verifiers;
- fetching a key directory over https under the stack's local CA, and reading
  the Lambda@Edge directory as served. These read the raw answer on purpose: the
  WG-00 directory check judges the status, the media type, the body and its
  response signature exactly as they were served.

The signing agent is the test's own (``fresh_agent``), whose private key the
harness holds. It signs with the https origin of its own host in the agent zone:
the stack's wildcard DNS sends every name in that zone to the capture proxy,
which answers https for it under the local CA and routes it to the
agent-directory service that serves the agent's directory. So the origin names a
directory a WBA verifier can actually fetch. The SDK stamps that origin's host as
``Requester.domain`` on every request built here, because a verifier refuses a
request whose requester is not the directory its signature resolved from.

Nothing here signs or verifies by itself. Third-party signing and verifying is
the oracles' job (``wba_oracle.py``, ``wba_oracle_ts.py``), and the oracles never
import FORA code. This module may, because it only drives the SDK, hands the
oracles what the SDK built, and reads the SDK's answers.
"""

from __future__ import annotations

import json
from collections.abc import Callable
from contextlib import suppress
from dataclasses import dataclass, replace
from typing import Any, Literal

import httpx
from fora_sdk.client import CallError, RawBody
from fora_sdk.core import DiscoveryResult
from http_message_signatures import http_sfv
from wire.models import TransactionResultItem, WBAFile

from .after_signing import Alteration, after_signing
from .broker_purchase import only_item, resolve_offer, resolve_request, resource_query
from .capture_proxy import CapturedRequest
from .constants import WBA_DIRECTORY_PATH
from .exchanges import EXCHANGE_A_DOMAIN
from .fresh_agent import FreshAgent
from .httpsig_signer import directory_origin
from .identity_requests import auth_reason
from .lambda_edge import PUBLISHER, invoke, viewer_request_event
from .local_ca import local_ca_context
from .protocol_documents import parse_document
from .signature_fields import DeclaredSignature, declared_signatures
from .stack_urls import StackURLs
from .wba_directory import DirectoryResult, check_directory
from .wba_oracle import VerifyResult, private_jwk, sign, verify
from .wba_oracle_ts import TSOracle, TSVerifyResult

# The directory the Broker signs as in this stack.
BROKER_ORIGIN = "https://broker"

# What the profile adds to the WG-00 minimum on a FORA RPC.
FORA_COMPONENTS = ("@method", "@target-uri", "content-digest", "authorization")

#: Which party a conformance call goes to: the Broker's Resolve, or exchange-a's
#: DiscoverResources.
Target = Literal["broker", "exchange"]

#: A third-party signer: the request, and options such as ``legacy`` or ``tag``.
Signer = Callable[..., dict[str, str]]

# The JWK alg every directory key must carry: the JOSE name, a deliberate FORA
# deviation from WG-00 Section 5.5.1 (the directory suite's docstring says why).
EXPECTED_JWK_ALG = "EdDSA"

_WBA_HEADERS = frozenset({"signature", "signature-input", "signature-agent"})
_TIMEOUT_SEC = 30.0


def agent_origin(agent: FreshAgent) -> str:
    """The https origin ``agent`` signs under: its own directory host."""
    return directory_origin(agent.host)


# ---- Calls a third-party library signs ------------------------------------------


def python_signer(agent: FreshAgent) -> Signer:
    """The Python oracle, signing as ``agent`` under its origin (an ``origin`` option overrides)."""

    def sign_as_agent(request: httpx.Request, **options: Any) -> dict[str, str]:
        return sign(request, agent.ed25519, options.pop("origin", agent_origin(agent)), **options)

    return sign_as_agent


def cloudflare_signer(oracle: TSOracle, agent: FreshAgent) -> Signer:
    """Cloudflare's library, signing as ``agent`` under its origin (an ``origin`` option overrides)."""
    key = private_jwk(agent.ed25519)

    def sign_as_agent(request: httpx.Request, **options: Any) -> dict[str, str]:
        return oracle.sign(request, key, options.pop("origin", agent_origin(agent)), **options)

    return sign_as_agent


@dataclass(frozen=True)
class Answer:
    """A FORA answer to a third-party-signed call, read through the SDK's types.

    ``status`` and ``code`` are a refusal's HTTP status and Connect code, such as
    401 and ``unauthenticated``; an answered call reads as 200 and an empty code.
    ``offers`` counts the verified offers an answered Resolve or DiscoverResources
    carries; a refusal carries none. ``requested`` is the refusal's
    Accept-Signature header (WG-00 Section 5.3), read from the SDK's
    ``CallError.headers`` as a Dictionary: each label it names, with the
    components it asks that signature to cover, written as in Signature-Input,
    and its parameters. A missing header reads as empty. ``sent`` is the request
    as it went on the wire, for a test that reads the signature the library made.
    ``reason`` is the refusal's typed ``request_auth_failure`` reason as the SDK
    decodes it, and None for an answered call or a refusal that carries none.
    A refusal is read by its code and its typed reason, never by its message text
    (ADR-028 D4).
    """

    status: int
    code: str
    offers: int
    requested: dict[str, DeclaredSignature]
    sent: httpx.Request
    reason: str | None = None

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


def _requested(raw: str | None) -> dict[str, DeclaredSignature]:
    return declared_signatures([raw] if raw else [], field="Accept-Signature", where="the refusal")


def resigned_by(signer: Signer, **options: Any) -> Alteration:
    """The SDK's signed request, its signature replaced by one ``signer`` makes over the same bytes.

    The SDK's Signature, Signature-Input and Signature-Agent are dropped and the
    library's take their place. The body, its Content-Digest and the empty
    Authorization the SDK sent all stay as they were, so the library signs exactly
    the request the SDK built, and the FORA components it covers are all present.
    """

    def alter(request: httpx.Request) -> httpx.Request:
        signature = signer(request, **options)
        kept = [
            (k, v)
            for k, v in request.headers.multi_items()
            if k.lower() not in _WBA_HEADERS | {"content-length"}
        ]
        return httpx.Request(
            request.method,
            request.url,
            headers=[*kept, *signature.items()],
            content=request.content,
        )

    return alter


def wba_call(agent: FreshAgent, target: Target, uri: str, signer: Signer, **options: Any) -> Answer:
    """Discover ``uri`` at ``target`` as ``agent``, the transport signature made by ``signer``.

    The SDK builds the request (a Resolve at the Broker; a DiscoverResources at
    exchange-a, which sells the seeded catalog), stamps the agent's requester and
    signs it. :func:`resigned_by` then swaps in the library's signature with
    ``options``, and the SDK decodes the answer or the refusal. The library's
    signature is the one part of the call the SDK did not make.
    """
    sent: list[httpx.Request] = []
    with after_signing(resigned_by(signer, **options), observe=sent.append) as http:
        try:
            result = _discovered(agent, target, uri, http)
        except CallError as refusal:
            assert refusal.status is not None and sent, f"the call was not answered: {refusal}"
            return Answer(
                status=refusal.status,
                code=refusal.code or "",
                offers=0,
                requested=_requested(refusal.headers.get("accept-signature")),
                sent=sent[-1],
                reason=auth_reason(refusal),
            )
    return Answer(status=200, code="", offers=len(result.verified()), requested={}, sent=sent[-1])


def _discovered(agent: FreshAgent, target: Target, uri: str, http: httpx.Client) -> DiscoveryResult:
    """The agent's discovery through a client sending on ``http``; the agent closes it."""
    if target == "broker":
        return agent.broker_client(http=http).resolve(resolve_request([uri]))
    exchange = agent.exchange_client(EXCHANGE_A_DOMAIN, http=http)
    return exchange.discover(resource_query(EXCHANGE_A_DOMAIN, [uri]))


def probe_broker(agent: FreshAgent, request_id: str) -> None:
    """Send the Broker a Resolve whose body is ``{"probe": request_id}``, signed by the Python oracle.

    The body is deliberately no DiscoveryRequest, so the Broker sends nothing on
    and the one request the capture holds for ``request_id`` is this one. It goes
    as a ``RawBody``, the SDK's way to send bytes exactly as given; the SDK signs
    it and the oracle re-signs it with the four FORA components. The answer does
    not matter here, so a refusal is not raised.
    """
    alteration = resigned_by(python_signer(agent), extra_components=FORA_COMPONENTS)
    with after_signing(alteration) as http:
        client = agent.broker_client(http=http, request_id=lambda: request_id)
        with suppress(CallError):
            client.resolve(RawBody({"probe": request_id}))


# ---- A purchase through the Broker as the test's own agent ------------------------


def buy_as_agent(
    agent: FreshAgent, uri: str, *, request_id: str, third_party_legacy: bool = False
) -> TransactionResultItem:
    """Resolve ``uri`` at the Broker and buy the winner through ExecuteTransaction, as ``agent``.

    Both calls carry ``request_id`` as X-Request-ID, which the Broker copies onto
    every request it sends for them. The SDK builds the purchase: the request,
    each item's AgentAcceptance and the request acceptance, all signed with
    the agent's key. With ``third_party_legacy`` the ExecuteTransaction's
    transport signature is then replaced, over the same bytes, by one the Python
    oracle makes in the legacy sf-string Signature-Agent form, which no FORA
    signer sends. The Resolve is always the SDK's own, so a refusal of the legacy
    form shows up on the purchase alone.
    """

    def minted() -> str:
        return request_id

    offer = resolve_offer(agent.broker_client(request_id=minted), uri)
    legacy = resigned_by(python_signer(agent), extra_components=FORA_COMPONENTS, legacy=True)
    http = after_signing(legacy) if third_party_legacy else None
    buyer = agent.broker_client(http=http, request_id=minted)
    return only_item(buyer.execute([offer]), f"broker purchase of {uri}")


# ---- Reading a captured request's signatures --------------------------------------


def target_uri(entry: CapturedRequest) -> str:
    """The URL ``entry`` was sent to, as its signer signed it."""
    return f"{entry.scheme}://{entry.target}{entry.path}"


def _declared(entry: CapturedRequest) -> dict[str, DeclaredSignature]:
    return declared_signatures(entry.header_values("signature-input"), where=f"request {entry.seq}")


def labels(entry: CapturedRequest) -> list[str]:
    """The label of every signature ``entry`` carries, in header order."""
    return list(_declared(entry))


def covered(entry: CapturedRequest, label: str) -> tuple[str, ...]:
    """The components ``label`` covers, written as in Signature-Input."""
    return _declared(entry)[label].components


def signature_params(entry: CapturedRequest, label: str) -> dict[str, object]:
    """The parameters of the signature ``label`` (created, keyid, alg, nonce, tag...)."""
    return dict(_declared(entry)[label].params)


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


def lambda_edge_directory_keys(stack: StackURLs) -> list[dict[str, Any]]:
    """The keys the Lambda@Edge directory serves, as served, for a third-party library to import.

    The answer is first read as the SDK reads a document handed over as bytes
    (``parse_document``: the published strict schema, then the generated model), so
    an answer that is not a directory fails here. The keys are then handed on as
    the served JSON rather than as the SDK's reading of it, because the library
    judges what the edge sent.
    """
    response = lambda_edge_directory(stack)
    assert response.status_code == httpx.codes.OK, response.text[:200]
    parse_document(WBAFile, response.content)
    served = json.loads(response.content)
    keys = served.get("keys") if isinstance(served, dict) else None
    assert isinstance(keys, list) and keys, f"the Lambda@Edge directory lists no key: {served}"
    return keys
