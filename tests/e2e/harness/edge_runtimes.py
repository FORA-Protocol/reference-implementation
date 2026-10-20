"""Every edge runtime the e2e stack runs, behind one reader.

The edge function ships for three runtimes, and the stack runs each with the same
shared app (``src/edge``):

* **Cloudflare**, on Miniflare, fronting ``demo.fora-protocol.org``. Its delivery
  keys are exchange-a's, so the harness can mint a URL for it as well as buy one;
* **Fastly Compute**, on Viceroy, fronting ``music.demo.fora-protocol.org``. Its
  delivery keys are exchange-b's, which the harness does not hold, so every URL
  it presents there is one exchange-b issued for a purchase;
* **Lambda@Edge**, the bundle a CloudFront deployment uploads, on the AWS Lambda
  runtime emulator (``harness.lambda_edge``). CloudFront always presents an https
  URL and this stack's Exchanges mint http ones, so its URLs are minted with
  exchange-a's own delivery key.

The ``aws-edge`` service is not one of them. It is a stand-in for CloudFront's
native RSA check and runs none of the edge function's code, so an obligation of the
edge function proved there would prove the stand-in. CloudFront's supported
integration is the Lambda@Edge bundle above.

The two HTTP runtimes are read over HTTP through ``harness.edge_fetch``. Lambda@Edge
is read by invoking the function with the CloudFront viewer-request event, and its
answer is either a generated response or the request handed back to CloudFront for
the origin. :class:`EdgeReply` is the one shape a test reads from all three.

What the HTTP runtimes cannot show: whether the origin was asked. Their reader sees
only the answer, so "the origin was not reached" is asserted there as "the answer
is the edge's own refusal and carries none of the publisher's bytes". Lambda@Edge
shows it directly.

Proofs of possession are minted by the SDK's own helper
(``lambda_edge.proof_of_possession``). The altered proofs below change only what
their name says, after the SDK signed. A proof covering another set of components
than the SDK signs is made by a third-party Web Bot Auth library
(:func:`library_proof`).
"""

from __future__ import annotations

import json
import secrets
import time
from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from dataclasses import dataclass, field
from http import HTTPStatus
from urllib.parse import urlsplit, urlunsplit

import httpx
from fora_sdk.b64 import b64url_nopad
from fora_sdk.client.content import DEFAULT_PROOF_WINDOW_SEC
from fora_sdk.pop import AGENT_KEY_HEADER
from fora_sdk.resolvers.errors import DirectoryUnavailableError
from fora_sdk.sync import BrokerClient

from ._compose import restart_service, stopped_service
from .e2e_catalog import CANARY_MARKER, DEMO_MUSIC_DOMAIN, DEMO_PHILOSOPHY_DOMAIN
from .e2e_controls import short_lifetimes
from .edge_fetch import EdgeAnswer, browse, present, tamper_query_param
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN
from . import wba_oracle
from .fresh_agent import FreshAgent
from .httpsig_signer import directory_origin, load_keypair
from .lambda_edge import (
    ARTICLE_PATH,
    PUBLISHER,
    ExchangeDeliveryKey,
    ViewerResult,
    invoke,
    mint_delivery_url,
    proof_of_possession,
    viewer_request_event,
    wait_ready,
)
from .money_path import (
    ARISTOTLE,
    PAPER_SATELLITES,
    THALES,
    TITANS,
    assert_bought,
    discover_offer,
    discover_offers,
)
from .poll import poll_until
from .protocol_documents import wba_directory_at
from .seed import wait_until_broker_routes
from .stack_urls import StackURLs

#: How long an edge or an Exchange may take to answer again after a restart.
_RESTART_S = 120.0

#: How far a shifted proof clock moves: well past the proof window and the edge's
#: 300-second tolerance for a proof created ahead of its clock.
_PAST_PROOF_S = -600
_FUTURE_PROOF_S = 600

#: A short delivery-URL lifetime, for the runtime whose URLs only a purchase makes.
_SHORT_URL_S = 10


@dataclass(frozen=True)
class EdgeRuntime:
    """One edge runtime: the publisher it fronts, two licensed pages and its services."""

    name: str
    scheme: str
    publisher: str
    #: Two licensed pages of this publisher. A purchase runtime sells both.
    pages: tuple[str, str]
    #: Text the first page's bytes carry, or None where no origin answers (Lambda@Edge).
    marker: str | None
    #: The compose service the edge runs in.
    edge_service: str
    #: The compose service serving the key directory this edge resolves URL keys from.
    directory_service: str
    #: The Exchange that issues this runtime's delivery URLs.
    issuer: str
    #: True when this runtime is read over HTTP; False for the Lambda invoke API.
    http: bool
    #: True when the harness can sign a URL this edge accepts (it holds the key).
    mintable: bool

    @property
    def article(self) -> str:
        return self.pages[0]

    @property
    def manifest_url(self) -> str:
        """The URL X-Content-Rules must carry for a refusal on this publisher."""
        return f"{self.scheme}://{self.publisher}/.well-known/fora.json"

    def page(self, path: str) -> str:
        """``path`` on this runtime's publisher."""
        return f"{self.scheme}://{self.publisher}{path}"

    def directory_origin(self, stack: StackURLs) -> str:
        return stack.exchange if self.directory_service == "exchange" else stack.exchange_b

    def __str__(self) -> str:
        return self.name


CLOUDFLARE = EdgeRuntime(
    name="cloudflare",
    scheme="http",
    publisher=DEMO_PHILOSOPHY_DOMAIN,
    pages=(THALES, ARISTOTLE),
    marker=CANARY_MARKER,
    edge_service="edge",
    directory_service="exchange",
    issuer=EXCHANGE_A_DOMAIN,
    http=True,
    mintable=True,
)
FASTLY = EdgeRuntime(
    name="fastly",
    scheme="http",
    publisher=DEMO_MUSIC_DOMAIN,
    pages=(PAPER_SATELLITES, TITANS),
    marker="Paper Satellites",
    edge_service="fastly-edge",
    directory_service="exchange-b",
    issuer=EXCHANGE_B_DOMAIN,
    http=True,
    mintable=False,
)
LAMBDA = EdgeRuntime(
    name="lambda",
    scheme="https",
    publisher=PUBLISHER,
    pages=(f"https://{PUBLISHER}{ARTICLE_PATH}", f"https://{PUBLISHER}/articles/on-friendship"),
    marker=None,
    edge_service="lambda-edge",
    directory_service="exchange",
    issuer=EXCHANGE_A_DOMAIN,
    http=False,
    mintable=True,
)

RUNTIMES = (CLOUDFLARE, FASTLY, LAMBDA)


@dataclass(frozen=True)
class EdgeReply:
    """One edge answer, from any runtime.

    ``forwarded`` is True when the edge handed the request on to the origin and
    False when it answered itself; None on an HTTP runtime, whose reader cannot
    tell. ``status`` is None only for a Lambda@Edge pass-through, which has none
    until CloudFront fetches the origin. ``headers`` is keyed by lowercased name.
    """

    status: int | None
    forwarded: bool | None
    body: str
    headers: Mapping[str, str] = field(default_factory=dict)

    def header(self, name: str) -> str | None:
        return self.headers.get(name.lower())

    @property
    def reason(self) -> str:
        """The edge's refusal token, or a marker saying why there is none."""
        try:
            payload = json.loads(self.body)
        except ValueError:
            return f"<non-JSON body: {self.body[:120]!r}>"
        if not isinstance(payload, dict):
            return f"<JSON is {type(payload).__name__}, not an object>"
        return str(payload.get("reason", "<no reason field>"))


def _from_http(answer: EdgeAnswer) -> EdgeReply:
    return EdgeReply(
        status=int(answer.status), forwarded=None, body=answer.text, headers=answer.headers
    )


def _from_lambda(result: ViewerResult) -> EdgeReply:
    if result.pass_through:
        return EdgeReply(status=None, forwarded=True, body="")
    headers = {
        name: ", ".join(str(entry.get("value", "")) for entry in entries)
        for name, entries in result.headers.items()
    }
    return EdgeReply(
        status=int(result.status or 0), forwarded=False, body=result.body or "", headers=headers
    )


def read(
    stack: StackURLs,
    runtime: EdgeRuntime,
    url: str,
    *,
    method: str = "GET",
    headers: Mapping[str, str] | None = None,
    body: bytes | None = None,
) -> EdgeReply:
    """Send one request to ``runtime``'s edge and return its answer.

    On Lambda@Edge the viewer-request event carries no body: CloudFront passes one
    to a viewer-request function only when the distribution is configured to, and
    the edge's decision never reads it.
    """
    if runtime.http:
        return _from_http(present(url, headers=headers or {}, method=method, body=body))
    event = viewer_request_event(url, method=method, headers=headers)
    return _from_lambda(invoke(stack.lambda_edge, event))


def _without_query(url: str) -> str:
    parts = urlsplit(url)
    return urlunsplit((parts.scheme, parts.netloc, parts.path, "", ""))


def assert_served(runtime: EdgeRuntime, reply: EdgeReply, url: str) -> None:
    """The edge let the read through to the origin, and the origin's bytes came back.

    On Lambda@Edge the request is handed back to CloudFront. Over HTTP the answer is
    200, and for the runtime's first page it carries that page's text, so a 200
    error stub from the edge cannot pass.
    """
    if not runtime.http:
        assert reply.forwarded, f"{runtime}: the read was not handed to the origin: {reply}"
        return
    assert reply.status == HTTPStatus.OK, f"{runtime}: answered {reply.status}: {reply.body[:200]}"
    if runtime.marker is not None and _without_query(url) == runtime.article:
        assert runtime.marker in reply.body, f"{runtime}: not the page's bytes: {reply.body[:200]}"


def assert_refused(runtime: EdgeRuntime, reply: EdgeReply, status: int, reason: str | None) -> None:
    """The edge answered ``status`` itself, and no content went out.

    ``reason`` is the refusal token the protocol names for this refusal. None is for
    a refusal whose token the protocol does not name, such as the AI-bot 403, whose
    headers are the protocol and whose body is informative only.
    """
    assert reply.forwarded is not True, f"{runtime}: the request was handed to the origin"
    assert reply.status == status, f"{runtime}: answered {reply.status}: {reply.body[:200]}"
    if reason is not None:
        assert reply.reason == reason, f"{runtime}: refused {reply.reason!r}, want {reason!r}"
    if runtime.marker is not None:
        assert runtime.marker not in reply.body, f"{runtime}: the refusal carried the content"


def _bought(agent: FreshAgent, runtime: EdgeRuntime, count: int) -> list[str]:
    """``count`` delivery URLs ``runtime``'s issuer sells ``agent``, bought in one purchase."""
    agent.fund(runtime.issuer)
    offers = [discover_offer(agent, runtime.pages[0])]
    if count > 1:
        offers.append(discover_offers(agent, runtime.pages[1])[0])
    items = agent.exchange(runtime.issuer).execute(offers).items or []
    assert len(items) == count, f"{runtime}: want {count} items, got {items}"
    urls = []
    for item in items:
        assert_bought(item, f"{runtime}: the purchase")
        url = str(item.retrieval_endpoint)
        assert f"agent_id={agent.thumbprint}" in url, f"the URL is not bound to the buyer: {url}"
        urls.append(url)
    return urls


def bound_urls(
    runtime: EdgeRuntime, agent: FreshAgent, key: ExchangeDeliveryKey, count: int = 1
) -> list[str]:
    """``count`` delivery URLs for ``runtime``'s pages, bound to ``agent``.

    Bought from the issuing Exchange on an HTTP runtime, so the URL is the one
    production hands an agent; minted with exchange-a's key on Lambda@Edge, for the
    https reason the module docstring gives.
    """
    assert count in (1, 2), count
    if runtime.http:
        return _bought(agent, runtime, count)
    return [
        mint_delivery_url(key, page, agent_id=agent.thumbprint) for page in runtime.pages[:count]
    ]


def unbound_url(runtime: EdgeRuntime, key: ExchangeDeliveryKey) -> str:
    """A bearer delivery URL for ``runtime``'s first page; only a mintable runtime has one.

    Every URL an Exchange issues for a purchase is bound to its buyer, so a bearer
    URL exists only where the harness holds the key the edge resolves.
    """
    assert runtime.mintable, f"{runtime}: the harness holds no key this edge accepts"
    return mint_delivery_url(key, runtime.article)


def tampered_url(runtime: EdgeRuntime, agent: FreshAgent, key: ExchangeDeliveryKey) -> str:
    """A bound delivery URL of ``runtime`` with one character of its signature changed."""
    return tamper_query_param(bound_urls(runtime, agent, key)[0])


def _url_expiry(url: str) -> float:
    values = [p.split("=", 1)[1] for p in urlsplit(url).query.split("&") if p.startswith("exp=")]
    assert len(values) == 1 and values[0].isdigit(), f"the URL carries no single exp: {url}"
    return float(values[0])


def expired_url(runtime: EdgeRuntime, agent: FreshAgent, key: ExchangeDeliveryKey) -> str:
    """A delivery URL of ``runtime`` whose exp has passed.

    Minted already expired where the harness holds the key. On Fastly it is bought
    while exchange-b issues ten-second URLs (the Exchange's e2e-only lifetime
    control), and the call returns once its signed exp has passed.
    """
    if runtime.mintable:
        return mint_delivery_url(key, runtime.article, ttl_seconds=-60)
    with short_lifetimes(runtime.issuer, url_seconds=_SHORT_URL_S) as in_force:
        assert in_force.url_ttl_seconds == _SHORT_URL_S, in_force
        url = _bought(agent, runtime, 1)[0]
    expiry = _url_expiry(url)
    poll_until(
        lambda: time.time() >= expiry + 1.0,
        max(expiry - time.time(), 0.0) + 10.0,
        failure=lambda: f"the URL's exp {expiry} had not passed",
        interval_s=0.5,
    )
    return url


def proof(url: str, agent: FreshAgent, *, clock_offset_s: int = 0) -> dict[str, str]:
    """The SDK's proof of possession of ``agent``'s key for a GET of ``url``."""
    return proof_of_possession(url, agent.key_path, clock_offset_s=clock_offset_s)


def expired_proof(url: str, agent: FreshAgent) -> dict[str, str]:
    """``agent``'s proof for ``url``, minted on a clock ten minutes behind: its expires has passed."""
    return proof(url, agent, clock_offset_s=_PAST_PROOF_S)


def future_proof(url: str, agent: FreshAgent) -> dict[str, str]:
    """``agent``'s proof for ``url``, created ten minutes ahead of the edge's clock."""
    return proof(url, agent, clock_offset_s=_FUTURE_PROOF_S)


#: A component a Web Bot Auth library may cover beyond the profile's minimum.
AUTHORITY = "@authority"
#: The required component that binds a proof to the URL it fetches.
TARGET_URI = "@target-uri"


def library_proof(url: str, agent: FreshAgent, components: tuple[str, ...]) -> dict[str, str]:
    """``agent``'s proof for a GET of ``url``, made by a third-party Web Bot Auth library.

    The SDK's own helper signs exactly the profile's minimum, so a proof that covers
    anything else is made by the pyauth signer behind ``harness.wba_oracle``. It
    covers ``@method``, the Signature-Agent member and then ``components`` in
    order, tagged ``web-bot-auth``, with a 64-byte nonce and the SDK's proof
    window. Its keyid is the thumbprint of ``agent``'s key and its member names
    ``agent``'s directory, so every identity check agrees with the bound URL, and
    the covered set is the only thing that differs from the SDK's proof.
    """
    directory, key = load_keypair(agent.key_path)
    created = int(time.time())
    headers = wba_oracle.sign(
        wba_oracle.message("GET", url),
        key,
        directory_origin(directory),
        target="@method",
        extra_components=components,
        nonce=b64url_nopad(secrets.token_bytes(wba_oracle.NONCE_BYTES)),
        created=created,
        expires=created + DEFAULT_PROOF_WINDOW_SEC,
    )
    covered = headers["Signature-Input"]
    assert all(f'"{c}"' in covered for c in components), covered
    return {**headers, AGENT_KEY_HEADER: b64url_nopad(key.public_key().public_bytes_raw())}


def proof_signed_by_another(
    url: str, *, presented: FreshAgent, signer: FreshAgent
) -> dict[str, str]:
    """A proof that presents ``presented``'s public key and keyid but is signed by ``signer``.

    The SDK mints ``signer``'s own proof; then its X-FORA-Agent-Key is replaced by
    ``presented``'s public key and the keyid in Signature-Input by ``presented``'s
    thumbprint. Every identity check agrees with the bound URL, so only the
    signature itself can refuse it.
    """
    headers = proof(url, signer)
    headers[AGENT_KEY_HEADER] = proof(url, presented)[AGENT_KEY_HEADER]
    old = f'keyid="{signer.thumbprint}"'
    assert headers["signature-input"].count(old) == 1, headers["signature-input"]
    headers["signature-input"] = headers["signature-input"].replace(
        old, f'keyid="{presented.thumbprint}"'
    )
    return headers


def _edge_answers(stack: StackURLs, runtime: EdgeRuntime) -> None:
    """Return once ``runtime``'s edge answers its health route again."""
    if not runtime.http:
        wait_ready(stack.lambda_edge, _RESTART_S)
        return

    def healthy() -> bool:
        try:
            return browse(runtime.page("/healthz"), user_agent="e2e-readiness").status == 200
        except httpx.HTTPError:
            return False

    poll_until(healthy, _RESTART_S, failure=lambda: f"{runtime}: the edge did not come back")


def _directory_answers(stack: StackURLs, runtime: EdgeRuntime) -> None:
    """Return once the key directory ``runtime``'s edge resolves keys from is served again."""

    def served() -> bool:
        try:
            wba_directory_at(runtime.directory_origin(stack))
        except DirectoryUnavailableError:
            return False
        return True

    poll_until(served, _RESTART_S, failure=lambda: f"{runtime.directory_service} did not come back")


@contextmanager
def key_directory_unreachable(
    stack: StackURLs, runtime: EdgeRuntime, broker: BrokerClient
) -> Iterator[None]:
    """For the block, ``runtime``'s edge holds no cached keys and cannot reach its directory.

    An edge keeps the directory it fetched for an hour, so stopping the directory's
    host alone proves nothing while that copy is warm. The edge is restarted first,
    which empties its cache and leaves its configuration as it was; then the
    service that serves the directory, an Exchange, is stopped. On exit that
    Exchange is started again, and the call returns once the directory is served
    AND the Broker routes to every seeded Exchange again (a health probe that ran
    while it was down holds it unroutable until the next pass), resolving as the
    test's own agent through ``broker``. In-network only.
    """
    restart_service(runtime.edge_service)
    _edge_answers(stack, runtime)
    try:
        with stopped_service(runtime.directory_service):
            yield
    finally:
        _directory_answers(stack, runtime)
        wait_until_broker_routes(stack, broker)
