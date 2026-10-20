"""The FORA SDK's own client, wired for this stack.

Two callers still send a body dict built by hand to a path constant
(:func:`post_signed_json`, signed by the SDK's own signer): the console's
transactions-list read, a console route the SDK has no client for, and the staging
registration script's Register. A hand-built protocol body is the re-derivation the
SDK exists to remove; every test sends through an SDK verb instead, over the SDK's
Connect-unary JSON client.

This module is the wiring such a client needs and does not supply itself. Three
pieces are load-bearing, and each is a thing the client refuses to guess:

* **the signer** — an RFC 9421 signing transport over the agent's own key. The
  SDK holds one agent key: the one the request is signed with.
* **the verifier** — fail-closed by default over a resolver that resolves
  nothing, so a client given no keys rejects EVERY offer, with a reason. That is
  the correct default and it looks exactly like a broken stack on first run.
  Real keys come out of each Exchange's Web Bot Auth directory, which is the only
  place an exchange's offer-signing key is published.
* **the endpoint resolver** — turns the exchange domain inside a signed offer
  into the origin that Exchange advertises for itself. A usage report goes where
  the signed offer says, never where configuration says, so this is not an
  optional convenience.

The blocking face (``fora_sdk.sync``) is used rather than the async core,
because everything around it here is synchronous. Prefetching the offer keys is
the one async step, and it runs once during setup rather than inside a call.

Scheme note: the services in this stack serve plain http on a private network;
only the capture proxy answers https, for the names it fronts. The resolvers are
told so; the client's own scheme gate reads ``ALLOW_INSECURE``, which the runner
service sets for the same reason the five Go FORA services that dial out do.
"""

from __future__ import annotations

import asyncio
import json
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from typing import Any

import httpx
from fora_sdk.client import DEFAULT_CALL_TIMEOUT_SEC, ClientConfig
from fora_sdk.core import Mode, StaticOfferKeyResolver, Verifier
from fora_sdk.hosts import host_of
from pydantic import ValidationError
from fora_sdk.resolvers import (
    CachedOfferKeyResolver,
    SsrfError,
    WellKnownEndpointResolver,
    WellKnownRequirementsReader,
    guarded_async_client,
    guarded_client,
    wba_directory_url,
)
from fora_sdk.sync import AdminClient, BrokerClient, CatalogClient, Client
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.window import Window
from wire.models import WBAFile

from .constants import requester
from .e2e_catalog import USD_AGENT_ID, USD_AGENT_KEY_PATH
from .exchanges import SERVICE_IDENTITIES, exchange_url
from .httpsig_signer import load_keypair, signing_transport
from .stack_urls import StackURLs

#: The scheme every service in the compose network answers on.
STACK_SCHEME = "http"

#: The scheme a deployed stack answers on.
_DEPLOYED_SCHEME = "https"

_FETCH_TIMEOUT_SEC = 10.0

#: Called with every request just before the SDK signs it; what it returns is
#: signed and sent. The SDK's own pre-signing hook (``ClientConfig.before_sign``),
#: for a test that must send a deliberately altered message that still carries a
#: valid signature over the altered bytes.
BeforeSign = Callable[["httpx.Request"], "httpx.Request"]

#: Mints the X-Request-ID value each call carries: the SDK's own correlation hook
#: (``ClientConfig.request_id``), for a test that reads the id back where the
#: request arrived.
RequestID = Callable[[], str]


@dataclass(frozen=True)
class _Resolvers:
    """The read-only resolvers every client addressed in one scheme shares."""

    endpoints: WellKnownEndpointResolver
    requirements: WellKnownRequirementsReader


@lru_cache(maxsize=None)
def _resolvers(scheme: str) -> _Resolvers:
    """One set of resolvers per scheme, built once for the process.

    **The endpoint resolver** turns an offer's exchange domain into that
    Exchange's own origin, for a usage report or a dispute. It is shared because
    ``Client.close()`` closes only the transports the client itself built, so a
    resolver per client would leave a keep-alive pool per client to object
    finalization. It also caches each Exchange's manifest behind its own locks,
    which is what makes sharing correct rather than merely cheaper: five clients
    asking the same Exchange for its endpoint ask it once.

    **The registration-requirements reader** is read by Register for the
    Exchange's terms digest and registration schema before it signs. A client
    given no reader builds one with the SDK's default scheme, https; this one is
    built for the scheme the services themselves serve, as the endpoint resolver
    is. The reader caches nothing, so one shared instance answers exactly what a
    reader per client would.

    Both dial through ``guarded_client``. It sets the ``follow_redirects``,
    ``max_redirects`` and ``timeout`` the SDK's ``default_client`` chooses, and
    adds ``trust_env=False``, so a set ``HTTP(S)_PROXY`` cannot tunnel these legs
    somewhere else. The runner sets ``SKIP_SSRF`` and ``ALLOW_INSECURE``, so the
    dial-time address guard and the https-only check are both off in this stack;
    the point is that each leg dials through the factory the SDK marks for a
    third-party-influenceable fetch. The resolvers expose no way to close their
    clients, so those pools close at interpreter exit.
    """
    return _Resolvers(
        endpoints=WellKnownEndpointResolver(
            scheme=scheme, http=guarded_client(timeout=_FETCH_TIMEOUT_SEC)
        ),
        requirements=WellKnownRequirementsReader(
            scheme=scheme, http=guarded_client(timeout=_FETCH_TIMEOUT_SEC)
        ),
    )


async def _fetch_wba_directory(url: str) -> WBAFile | None:
    """Read one exchange's Web Bot Auth directory.

    The offer-signing key lives ONLY here — not in ``fora.json`` — so this is
    what makes an offer verifiable at all.

    **Returns None for every failure, and raises for none of them.** That is the
    seam's contract, not a preference: ``CachedOfferKeyResolver.prefetch`` gathers
    these calls without ``return_exceptions``, so one raised error abandons the
    whole prefetch instead of leaving a single exchange unresolved. Its own
    docstring states the intent — an unresolvable exchange is simply absent from
    the map, and the Verifier then rejects that exchange's offers fail-closed.
    A dropped connection would otherwise take down the verifier setup for every
    exchange, which surfaces as a crash in module setup rather than as the one
    exchange whose offers could not be checked.

    Dials through the SDK's guarded client rather than a bare httpx one. That is
    what reads ``SKIP_SSRF`` and ``ALLOW_INSECURE`` — the two flags the runner
    sets — and what keeps this leg off the ambient proxy environment.

    Three outbound legs run under a client built here, and they are not guarded
    alike:

    1. This fetch. Its host comes from the domain list the caller hands
       :func:`offer_verifier`, and every call site fills that from the stack's
       own exchange constants or its own configuration — so it is addressed by
       configuration, not by anything read off the wire. Guarded, as above.
    2. The SDK's ``WellKnownEndpointResolver``, reading an Exchange's manifest
       before a usage report or a dispute. Its host is the report's ``exchange``
       field — data, and the leg a signed offer actually steers. The shared
       endpoint resolver (``_resolvers``) is handed ``guarded_client``, so this
       leg dials through the same factory as leg 1 rather than the unguarded
       default.

       ``vet_exchange_endpoint`` runs in front of it, and it is worth being exact
       about what that does and does not do. It calls ``is_bare_host``, which
       answers whether the value is EXACTLY a host — it refuses a scheme,
       userinfo, a path, a query or a fragment, so a URL smuggled into the
       ``exchange`` field cannot choose what gets fetched. That is a real check
       and it is a check about SHAPE. It is not an address check: ``localhost``,
       ``127.0.0.1``, ``169.254.169.254`` and ``10.0.0.5:8080`` are all bare
       hosts and all pass it. The address class an SSRF guard exists for is the
       dial-time guard's job, which is why this leg dials guarded rather than
       relying on the vet alone.
    3. The RPC send that follows the resolve. The SDK marks that one guarded,
       and keeps the plain transport for discover and execute, whose address is
       the operator's own configuration rather than an offer's.
    """
    try:
        async with guarded_async_client(timeout=_FETCH_TIMEOUT_SEC) as http:
            resp = await http.get(url)
        if resp.status_code != httpx.codes.OK:
            return None
        return WBAFile.model_validate(resp.json())
    except (httpx.HTTPError, SsrfError, ValidationError, ValueError):
        # ValueError covers a body that is not JSON; ValidationError, a body that
        # is JSON and not a directory.
        return None


def offer_verifier(
    exchange_domains: Sequence[str],
    *,
    scheme: str = STACK_SCHEME,
    dial: Mapping[str, str] | None = None,
) -> Verifier:
    """A fail-closed verifier holding the offer-signing key of each named exchange.

    The Python SDK's cached resolver is a batch prefetch rather than a lookup
    per offer, so the caller names the exchanges it expects to hear from and the
    keys are read once. An exchange left out of that list is not an error here —
    its offers simply arrive rejected, which is what the caller asserts on.

    Each directory is read at ``scheme://<domain>`` unless ``dial`` maps the
    domain to another host to read it from. Inside the compose network the
    domain IS the address; a script on the host passes the mapped port, because
    from there an exchange's domain resolves to nothing. The key is still filed
    under the domain, which is what an offer's ``exchange`` field names.
    """
    routes = dict(dial or {})

    async def fetch(domain: str) -> WBAFile | None:
        return await _fetch_wba_directory(wba_directory_url(scheme, routes.get(domain, domain)))

    keys = asyncio.run(CachedOfferKeyResolver(fetch=fetch).prefetch(exchange_domains))
    return Verifier(
        mode=Mode.STRICT,
        resolver=StaticOfferKeyResolver(keys),
        now=lambda: int(time.time()),
    )


@lru_cache(maxsize=1)
def stack_offer_verifier() -> Verifier:
    """A verifier holding the offer key of every exchange this stack runs.

    Built once per process, the first time a suite asks for it, which is after
    the stack is up. A Web Bot Auth directory is per-Exchange configuration that
    no test changes, so one verifier is correct for the whole session.
    """
    return offer_verifier(list(SERVICE_IDENTITIES.values()))


def agent_signer(key_path: Path, window: Window | None = None) -> SigningTransport:
    """The request signer over one agent keyfile.

    The keyfile's kid is the signer's DIRECTORY, not its keyid; the helper applies
    that split (its docstring says why). The default window is the SDK's own
    clock window; identical requests stay distinct through the fresh nonce the
    transport puts on every signature (see ``httpsig_signer``).
    """
    directory, priv = load_keypair(key_path)
    return signing_transport(directory, priv, window=window)


def _config(
    base_url: str,
    *,
    signer: SigningTransport | None,
    requester: dict[str, object] | None = None,
    verifier: Verifier | None = None,
    before_sign: BeforeSign | None = None,
    request_id: RequestID | None = None,
    scheme: str = STACK_SCHEME,
    call_timeout_sec: float = DEFAULT_CALL_TIMEOUT_SEC,
) -> ClientConfig:
    """The ONE place this harness builds an SDK ``ClientConfig``.

    Every client the harness sends through is built from what this returns, so
    two settings hold for every call without a test being able to opt out:

    * ``strict=True``: every answer is checked against the published strict JSON
      Schema of its response message and the proto's cross-field rules before the
      test sees it. An unknown field, a field-level rule broken or a cross-field
      rule broken fails the call as a ``MALFORMED`` ``CallError`` (ADR-028 D3,
      the universal shape check). There is no parameter for it.

    A deliberately altered request goes through :func:`harness.malformed.malformed`,
    which builds its clients here too.
    """
    resolvers = _resolvers(scheme)
    return ClientConfig(
        base_url=base_url,
        signer=signer,
        requester=requester,
        verifier=verifier,
        endpoint_resolver=resolvers.endpoints,
        registration_requirements=resolvers.requirements,
        before_sign=before_sign,
        request_id=request_id,
        strict=True,
        call_timeout_sec=call_timeout_sec,
    )


def _agent_config(
    base_url: str,
    agent_id: str,
    signer: SigningTransport,
    verifier: Verifier,
    *,
    before_sign: BeforeSign | None = None,
    request_id: RequestID | None = None,
    scheme: str = STACK_SCHEME,
) -> ClientConfig:
    """One agent client's configuration.

    ``requester.domain`` is the host of the directory the signer signs as. That
    is what the field means — where a verifier fetches this agent's keys — and a
    Broker refuses a purchase that names any other host, because every Exchange
    it relays to resolves the agent's acceptance keys from it. ``agent_id`` is
    only the free label carried in ``requester.id``.
    """
    return _config(
        base_url,
        signer=signer,
        requester=requester(agent_id, host_of(signer.signature_agent)),
        verifier=verifier,
        before_sign=before_sign,
        request_id=request_id,
        scheme=scheme,
    )


def sdk_exchange_client(
    stack: StackURLs,
    exchange_domain: str,
    verifier: Verifier,
    *,
    agent_id: str = USD_AGENT_ID,
    key_path: Path = USD_AGENT_KEY_PATH,
    signer: SigningTransport | None = None,
    before_sign: BeforeSign | None = None,
    http: httpx.Client | None = None,
    base_url: str | None = None,
) -> Client:
    """The SDK client for one Exchange, addressed at the URL this stack reaches it on.

    ``signer`` replaces the agent's own signer, ``before_sign`` alters each request
    before the SDK signs it, and ``http`` replaces the client the SDK sends
    through, so a transport can see or alter a request after it was signed.
    ``base_url`` addresses a party that is not one of the stack's seeded Exchanges,
    which :func:`exchange_url` does not know: the Exchange impostor, say.
    """
    config = _agent_config(
        base_url if base_url is not None else exchange_url(stack, exchange_domain),
        agent_id,
        signer if signer is not None else agent_signer(key_path),
        verifier,
        before_sign=before_sign,
    )
    return Client(config, http=http)


def sdk_content_client(stack: StackURLs, key_path: Path) -> Client:
    """The SDK client a test fetches delivered content through, as the agent at ``key_path``.

    The ``fetch`` verb needs only the agent's signer: it mints the proof of
    possession of that key inside the SDK. A delivery URL names its own edge, so a
    fetch never dials the client's base URL (the stack's primary Exchange); it is
    set because every client is addressed somewhere.
    """
    return Client(_config(stack.exchange, signer=agent_signer(key_path)))


def post_signed_json(
    url: str, body: dict[str, Any], *, signer: SigningTransport, timeout: float = 30.0
) -> httpx.Response:
    """POST ``body`` as JSON to ``url``, signed by the SDK's ``signer``.

    For the two callers that still send a hand-built body rather than an SDK verb:
    the console's transactions-list read (``console_client``), a console route the
    SDK has no client for, and the staging registration script's Register. The
    SDK's signer signs the exact bytes sent (RFC 9421 over the Content-Digest of
    this serialization), so nothing changes the body after it is built here.
    Prefer an SDK verb wherever one exists.
    """
    payload = json.dumps(body, separators=(",", ":")).encode()
    signed = signer.sign_outbound(method="POST", url=url, body=payload, authorization="")
    headers = {**signed.headers, "Content-Type": "application/json"}
    return httpx.post(url, content=payload, headers=headers, timeout=timeout)


def no_offer_verifier() -> Verifier:
    """A fail-closed verifier that holds no offer-signing keys at all.

    For a client that never receives an offer: the account calls (Register and
    GetAccountStatus) carry none, so there is nothing to resolve a key for. Any
    offer such a client did receive would be rejected, which is the right answer
    for a client that was never meant to buy.
    """
    return Verifier(
        mode=Mode.STRICT,
        resolver=StaticOfferKeyResolver(),
        now=lambda: int(time.time()),
    )


def sdk_account_client(base_url: str, *, agent_id: str, key_path: Path) -> Client:
    """The SDK client for one agent's account calls at the Exchange at ``base_url``.

    Built from a URL rather than from ``StackURLs`` because the seed calls it
    with the URLs it was handed, before any suite holds a stack. Register and
    GetAccountStatus are routed by the ``exchange`` named in the request, through
    that Exchange's own manifest, so ``base_url`` is where the client is
    addressed and not what decides where the call goes.

    It is also the one builder addressed by a URL rather than by a stack
    Exchange, so a test that needs a client pointed at an address no service
    answers on (a transient-failure scenario) builds it here. Such a client
    receives no offer, which is why the verifier holding no keys is no loss.
    """
    return Client(_agent_config(base_url, agent_id, agent_signer(key_path), no_offer_verifier()))


def sdk_broker_client(
    stack: StackURLs,
    verifier: Verifier | None = None,
    *,
    agent_id: str = USD_AGENT_ID,
    key_path: Path = USD_AGENT_KEY_PATH,
    signer: SigningTransport | None = None,
    before_sign: BeforeSign | None = None,
    http: httpx.Client | None = None,
    request_id: RequestID | None = None,
) -> BrokerClient:
    """The SDK client for the Broker: Resolve to discover, ExecuteTransaction to buy.

    ``verifier`` defaults to one holding every stack exchange's offer key.
    ``signer`` replaces the agent's own signer. ``before_sign`` alters each request
    before the SDK signs it, so the altered bytes still carry a valid signature;
    ``http`` replaces the client the SDK sends through, so a transport can alter a
    request after it was signed (see ``after_signing``). ``request_id`` mints the
    X-Request-ID every call carries; without it the SDK sends none.
    """
    config = _agent_config(
        stack.broker,
        agent_id,
        signer if signer is not None else agent_signer(key_path),
        verifier if verifier is not None else stack_offer_verifier(),
        before_sign=before_sign,
        request_id=request_id,
    )
    return BrokerClient(config, http=http)


def deployed_broker_client(
    broker_url: str, *, agent_id: str, key_path: Path, exchange: str
) -> BrokerClient:
    """The SDK Broker client for a deployed stack that answers on https.

    The verifier holds the selling Exchange's offer key, read from its directory
    over https.
    """
    signer = agent_signer(key_path)
    return BrokerClient(
        _agent_config(
            broker_url,
            agent_id,
            signer,
            offer_verifier([exchange], scheme=_DEPLOYED_SCHEME),
            scheme=_DEPLOYED_SCHEME,
        )
    )


def sdk_catalog_client(
    base_url: str,
    signer: SigningTransport,
    *,
    call_timeout_sec: float,
    before_sign: BeforeSign | None = None,
    http: httpx.Client | None = None,
) -> CatalogClient:
    """The SDK Catalog client for a publisher or contributor pushing to ``base_url``.

    ``before_sign`` alters each request before the SDK signs it, and ``http``
    replaces the client the SDK sends through: the two seams
    :func:`harness.malformed.malformed` alters a catalog call through.
    """
    config = _config(
        base_url, signer=signer, before_sign=before_sign, call_timeout_sec=call_timeout_sec
    )
    return CatalogClient(config, http=http)


def sdk_admin_client(
    admin_url: str,
    *,
    before_sign: BeforeSign | None = None,
    http: httpx.Client | None = None,
) -> AdminClient:
    """The SDK Admin client for one Exchange's admin listener at ``admin_url``.

    ``fora.admin.v1.AdminService`` authenticates no caller: it takes no request
    signature and has no operator identity, and the listener's address allowlist
    is its only gate (ADR-022). So the client carries no signer, and nothing here
    provisions an operator key. What lets a call through is the address it comes
    from: in this stack every Exchange's listener admits the runner's address and
    no other (``x-runner-address`` in ``docker-compose.e2e.yml``).

    ``before_sign`` and ``http`` are the two seams :func:`harness.malformed.malformed`
    alters a call through; an admin call is not signed, so ``before_sign`` only
    patches the request.
    """
    return AdminClient(_config(admin_url, signer=None, before_sign=before_sign), http=http)


def sdk_domain_verification_client(
    stack: StackURLs, exchange_domain: str, *, key_path: Path
) -> AdminClient:
    """The SDK client for the two domain-verification RPCs, at one Exchange's public listener.

    The SDK serves ``RequestDomainVerification`` and ``ConfirmDomainVerification``
    from its operator client, but they are ``fora.v1.ExchangeService`` methods on
    the listener every signed call reaches, so the client is addressed there and
    signs as the agent whose keyfile is ``key_path``: the Exchange verifies the
    signature before any handler answers.
    """
    return AdminClient(_config(exchange_url(stack, exchange_domain), signer=agent_signer(key_path)))
