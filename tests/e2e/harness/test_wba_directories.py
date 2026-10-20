"""E2E: every participant's key directory, and the edge's proof of possession, as WBA.

Two halves, both judged by code FORA did not write.

Key directories. Every participant that signs serves a Web Bot Auth key
directory (draft-ietf-webbotauth-httpsig-protocol-00, "WG-00", Section 5.5, as
the Web Bot Auth profile of the pinned protocol module
``github.com/FORA-Protocol/protocol`` adopts it): the Broker, the four
Exchanges, the console, an agent subdomain the identity service hosts, the three
publisher edges, the harness's own static agent agent-e2e, and the Lambda@Edge
function, whose directory is checked on the response it generates. Each is
fetched over https with the stack's local CA and checked by the Python oracle's
directory check (``wba_directory.py``): 200 with no redirect followed, the
profile's media type, a JWKS of public keys whose kid, when present, is the
thumbprint, and a valid response signature tagged
http-message-signatures-directory, which the check is told to require. Each key
set is also handed to Cloudflare's web-bot-auth library, which must import every
key.

The JWK ``alg`` the directories must carry is "EdDSA". That departs from WG-00
Section 5.5.1, which restricts the member to HTTP Message Signatures algorithm
names ("ed25519"), in favour of RFC 7517 Section 4.4, which defines a JWK's alg
as a JOSE algorithm name. It is one of the three places where the profile
departs from WG-00, all in the key directory: the other two are that the
directory response must be signed, and that every key carries both not_before
and not_after. Cloudflare's library reads alg the JOSE way: it refuses an
Ed25519 key whose alg is present and is anything but "EdDSA". The oracle is told
to expect "EdDSA" explicitly, rather than its WG-00 default.

Proof of possession at the edge. A delivery URL the Exchange binds to an agent
(its agent_id is the agent key's thumbprint) is fetched with a full WG-00
signature that also covers @method and @target-uri, signed by the Python
oracle, plus the agent's public key in X-FORA-Agent-Key. The edge verifies
offline and takes that key only when its thumbprint equals both the keyid and
the URL's agent_id. This runs on each runtime that enforces delivery binding:
the Cloudflare and Fastly runtimes, through a URL a real purchase through the
Broker mints, and Lambda@Edge, through a URL signed with exchange-a's own key
(the reason is in ``lambda_edge.py``: CloudFront always rebuilds the URL as
https, and the Exchange mints http URLs in this stack). The AWS runtime fronted
by CloudFront signed URLs is not among them: its RSA URLs carry no agent_id and
bind nothing.

Surfaces: the directories' public https URLs; the Broker's Resolve and
ExecuteTransaction for the arrange step; the edges' content URLs, and the Lambda
runtime's invoke endpoint, for the fetch.

In-network only: the capture proxy's https leg and the oracle sidecar publish no
host port.
"""

from __future__ import annotations

import json
import os
import secrets
from dataclasses import dataclass

import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk.b64 import b64url_nopad

from .e2e_catalog import SeededFixture
from .edge_fetch import present
from .lambda_edge import (
    ARTICLE_PATH,
    PUBLISHER,
    invoke,
    load_exchange_delivery_key,
    mint_delivery_url,
    viewer_request_event,
)
from .stack_urls import StackURLs
from .wba_conformance import (
    AGENT_ORIGIN,
    EXPECTED_JWK_ALG,
    agent_key,
    buy_as_agent,
    check_participant_directory,
    https_jwks,
    lambda_edge_directory,
)
from .wba_oracle import check_directory_response, fresh_key, key_thumbprint, message, public_jwk
from .wba_oracle import sign
from .wba_oracle_ts import TSOracle

pytestmark = [
    pytest.mark.skipif(
        os.environ.get("FORA_E2E_IN_NETWORK") != "1",
        reason="the capture proxy's https leg and the oracle sidecar are reachable only in-network",
    ),
    pytest.mark.stack_isolation("shared-without-cleanup"),
]

# Every participant directory the capture proxy serves over https. "agent"
# stands for the agent subdomain this session signed up at the identity service,
# known only at run time.
_DIRECTORY_HOSTS = [
    "broker",
    "exchange",
    "exchange-b",
    "exchange-c",
    "exchange-d",
    "console",
    "agent",
    "demo.fora-protocol.org",
    "music.demo.fora-protocol.org",
    "sfx.demo.fora-protocol.org",
    "agent-e2e",
]


def _origin(host: str, request: pytest.FixtureRequest) -> str:
    if host == "agent":
        host = str(request.getfixturevalue("mcp_agent"))
    return f"https://{host}"


@pytest.mark.parametrize("host", _DIRECTORY_HOSTS)
def test_each_participant_directory_passes_the_wba_directory_check(
    host: str, request: pytest.FixtureRequest
) -> None:
    origin = _origin(host, request)
    result = check_participant_directory(origin)
    assert result.ok, f"{origin}: {result.failed}: {result.detail}"


@pytest.mark.parametrize("host", _DIRECTORY_HOSTS)
def test_every_directory_key_imports_into_the_cloudflare_verifier(
    host: str, request: pytest.FixtureRequest, ts_oracle: TSOracle
) -> None:
    """The library imports every key, each under the thumbprint the Python oracle computes."""
    origin = _origin(host, request)
    jwks = https_jwks(origin)
    imported = ts_oracle.import_keys(jwks)
    assert imported.refused == (), f"{origin}: the library refused {imported.refused}"
    expected = sorted(key_thumbprint(key) for key in jwks["keys"])
    assert sorted(imported.keyids) == expected, f"{origin}: {imported}"


def test_the_import_check_refuses_a_registry_named_alg(ts_oracle: TSOracle) -> None:
    """The check above can fail: the library refuses an Ed25519 key whose alg is "ed25519"."""
    jwk = public_jwk(fresh_key())
    assert ts_oracle.import_keys({"keys": [{**jwk, "alg": EXPECTED_JWK_ALG}]}).refused == ()
    refused = ts_oracle.import_keys({"keys": [{**jwk, "alg": "ed25519"}]})
    assert refused.keyids == () and len(refused.refused) == 1, refused
    assert "EdDSA" in refused.refused[0], refused


@pytest.mark.xdist_group("lambda-edge")
def test_the_lambda_edge_directory_passes_the_wba_directory_check(compose_stack: StackURLs) -> None:
    """The AWS edge's directory is held to the same WG-00 rules as every other one."""
    result = check_directory_response(
        lambda_edge_directory(compose_stack), require_signed=True, expected_alg=EXPECTED_JWK_ALG
    )
    assert result.ok, f"lambda-edge: {result.failed}: {result.detail}"


@pytest.mark.xdist_group("lambda-edge")
def test_every_lambda_edge_directory_key_imports_into_the_cloudflare_verifier(
    compose_stack: StackURLs, ts_oracle: TSOracle
) -> None:
    """The library imports every key the Lambda@Edge directory publishes."""
    response = lambda_edge_directory(compose_stack)
    assert response.status_code == 200, response.text[:200]
    jwks = json.loads(response.content)
    imported = ts_oracle.import_keys(jwks)
    assert imported.refused == (), f"lambda-edge: the library refused {imported.refused}"
    assert sorted(imported.keyids) == sorted(key_thumbprint(k) for k in jwks["keys"]), imported


# ---- Retrieval proof of possession as a WBA signature ------------------------


def _wba_possession_headers(url: str, key: Ed25519PrivateKey) -> dict[str, str]:
    """A WG-00 signature over a GET, on @target-uri, plus @method: the profile's proof.

    The protocol's retrieval proof of possession is the WG-00 base (its
    ``@authority`` or ``@target-uri``, the covered Signature-Agent member, the tag
    and a 64-byte nonce) plus ``@method`` and ``@target-uri``. The base here takes
    ``@target-uri``, so the proof covers exactly those three components, the set
    the edge checks. Signed by the Python oracle with ``key``, and
    X-FORA-Agent-Key presenting that key's public half, as the edge's offline
    check needs.
    """
    signature = sign(
        message("GET", url), key, AGENT_ORIGIN, target="@target-uri", extra_components=("@method",)
    )
    public = key.public_key().public_bytes_raw()
    return {**signature, "X-FORA-Agent-Key": b64url_nopad(public)}


@dataclass(frozen=True)
class Fetched:
    """One fetch of a bound URL, on whichever runtime: delivered or refused."""

    delivered: bool
    status: str
    reason: str | None
    text: str


def _reason(body: str) -> str | None:
    """The ``reason`` of an edge's JSON refusal, if the body is one."""
    try:
        payload = json.loads(body)
    except ValueError:
        return None
    reason = payload.get("reason") if isinstance(payload, dict) else None
    return str(reason) if reason is not None else None


@dataclass(frozen=True)
class BoundURL:
    """A delivery URL bound to agent-e2e, and how to fetch it on its runtime."""

    url: str
    marker: str
    lambda_base: str | None = None

    def fetch(self, headers: dict[str, str]) -> Fetched:
        if self.lambda_base is not None:
            result = invoke(self.lambda_base, viewer_request_event(self.url, headers=headers))
            if result.pass_through:
                return Fetched(result.uri == self.marker, "pass-through", None, str(result))
            body = result.body or ""
            return Fetched(False, str(result.status), _reason(body), body)
        answer = present(self.url, headers=headers)
        delivered = answer.status == 200 and self.marker in answer.text
        return Fetched(delivered, str(int(answer.status)), _reason(answer.text), answer.text)


def _purchased_bound_url(stack: StackURLs, uri: str, marker: str) -> BoundURL:
    """Buy ``uri`` through the Broker as agent-e2e and keep the delivery URL it is bound to."""
    item = buy_as_agent(stack, uri, request_id=f"wba-pop-{secrets.token_hex(8)}")
    bound = item.retrieval_endpoint or ""
    assert "agent_id=" in bound, f"the purchase minted no bound URL: {item}"
    return BoundURL(url=bound, marker=marker)


@pytest.fixture(params=["edge", "fastly-edge", "lambda-edge"])
def bound_url(
    request: pytest.FixtureRequest, compose_stack: StackURLs, seeded: SeededFixture
) -> BoundURL:
    """A URL bound to agent-e2e's thumbprint, on each runtime that enforces the binding."""
    if request.param == "edge":
        return _purchased_bound_url(compose_stack, seeded.cloudflare.uri, "Socrates")
    if request.param == "fastly-edge":
        return _purchased_bound_url(compose_stack, seeded.fastly.uri, "Paper Satellites")
    exchange_key = load_exchange_delivery_key(compose_stack.exchange)
    url = mint_delivery_url(
        exchange_key, f"https://{PUBLISHER}{ARTICLE_PATH}", agent_id=key_thumbprint(agent_key())
    )
    return BoundURL(url=url, marker=ARTICLE_PATH, lambda_base=compose_stack.lambda_edge)


@pytest.mark.xdist_group("lambda-edge")
def test_a_bound_url_fetched_with_a_wba_signature_is_delivered(bound_url: BoundURL) -> None:
    fetched = bound_url.fetch(_wba_possession_headers(bound_url.url, agent_key()))
    assert fetched.delivered, (
        f"{bound_url.url}: {fetched.status} {fetched.reason}: {fetched.text[:200]}"
    )


@pytest.mark.xdist_group("lambda-edge")
def test_a_wba_signature_by_another_key_is_refused_without_content(bound_url: BoundURL) -> None:
    """Same request, signed and presented by a key that is not the one the URL names."""
    fetched = bound_url.fetch(_wba_possession_headers(bound_url.url, fresh_key()))
    assert fetched.status == "403", f"{bound_url.url}: {fetched.status}: {fetched.text[:200]}"
    assert not fetched.delivered and bound_url.marker not in fetched.text, fetched.text[:200]
    assert fetched.reason in ("keyid_mismatch", "thumbprint_mismatch"), (
        f"the refusal names {fetched.reason!r}, not the agent binding"
    )
