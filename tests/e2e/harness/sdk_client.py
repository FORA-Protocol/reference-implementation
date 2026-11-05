"""The FORA SDK's own client, wired for this stack.

This module builds every SDK client the harness and its scripts send through, over
the SDK's Connect-unary JSON client, and offers no way to put a body built by hand
on the wire: a request goes out through an SDK verb.

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
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path

import httpx
from fora_sdk.client import DEFAULT_CALL_TIMEOUT_SEC, ClientConfig
from fora_sdk.core import Mode, StaticOfferKeyResolver, Verifier
from fora_sdk.hosts import host_of
from fora_sdk.resolvers import (
    CachedOfferKeyResolver,
    create_wba_offer_directory_fetch,
    WellKnownEndpointResolver,
    WellKnownRequirementsReader,
    guarded_client,
)
from fora_sdk.sync import AdminClient, BrokerClient, CatalogClient, Client
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.window import Window
from wire.models import WBAFile

from .constants import requester
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

    Each directory is read by the SDK's own ``create_wba_offer_directory_fetch``:
    no redirect, the directory media type, and the response signatures checked,
    keeping only the keys that signed it, so the verifier holds no key the
    directory did not sign for. Every failure is contained as None, so an
    exchange whose directory cannot be read is absent from the map and its offers
    arrive rejected, fail-closed. The read dials through the SDK's guarded client,
    which is what reads the ``SKIP_SSRF`` and ``ALLOW_INSECURE`` flags the runner
    sets and keeps the leg off the ambient proxy environment.

    Each directory is read at ``scheme://<domain>`` unless ``dial`` maps the
    domain to another host to read it from; the key is still filed under the
    domain, which is what an offer's ``exchange`` field names. The response
    signatures are checked against the host actually read, so a dial to another
    exchange reads that exchange's own signed directory, and a dial to an address
    the directory was not signed for keeps no key at all.
    """
    routes = dict(dial or {})
    with guarded_client(timeout=_FETCH_TIMEOUT_SEC) as http:
        read = create_wba_offer_directory_fetch(http=http, scheme=scheme)

        async def fetch(domain: str) -> WBAFile | None:
            return await read(routes.get(domain, domain))

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
    * ``strict_requests=True``: a verb takes only its own generated request model,
      or a ``RawBody``; a plain dict, or a model of another message, is refused as a
      ``MALFORMED`` ``CallError`` before anything is read, signed or sent. The
      message the SDK stamps (``ver``, the configured requester, a purchase's
      reflected offers) must then pass the same strict schema before it is signed,
      so a dictionary cannot reach the wire and a member the protocol does not
      define is refused by name (ADR-028 H1). There is no parameter for it either.

    A deliberately altered request goes through :func:`harness.malformed.malformed`,
    which builds its clients here too: its pre-signing hook runs after that check,
    and its raw mode sends a ``RawBody``, which the SDK never checks.
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
        strict_requests=True,
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


def _signer(key_path: Path | None, signer: SigningTransport | None) -> SigningTransport:
    """What an agent client signs with: ``signer`` when given, else the key at ``key_path``.

    A client signs only as an agent its caller names. One built with neither is
    refused, so no client falls back to an identity the caller did not choose.
    """
    if signer is not None:
        return signer
    if key_path is None:
        raise TypeError("an SDK client signs as an agent: pass its key_path or a signer")
    return agent_signer(key_path)


def sdk_exchange_client(
    stack: StackURLs,
    exchange_domain: str,
    verifier: Verifier,
    *,
    agent_id: str,
    key_path: Path | None = None,
    signer: SigningTransport | None = None,
    before_sign: BeforeSign | None = None,
    http: httpx.Client | None = None,
    base_url: str | None = None,
) -> Client:
    """The SDK client for one Exchange, addressed at the URL this stack reaches it on.

    The client signs as the agent the caller names: ``agent_id`` is the free label
    in ``requester.id``, and the agent signs with the key at ``key_path``. There is
    no default agent.

    ``signer`` replaces the agent's own signer, ``before_sign`` alters each request
    before the SDK signs it, and ``http`` replaces the client the SDK sends
    through, so a transport can see or alter a request after it was signed.
    ``base_url`` addresses a party that is not one of the stack's seeded Exchanges,
    which :func:`exchange_url` does not know: the Exchange impostor, say.
    """
    config = _agent_config(
        base_url if base_url is not None else exchange_url(stack, exchange_domain),
        agent_id,
        _signer(key_path, signer),
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


def deployed_account_client(exchange_url: str, *, agent_id: str, key_path: Path) -> Client:
    """The SDK client for one agent's account calls at a deployed Exchange on https.

    The deployed counterpart of :func:`sdk_account_client`. Its resolvers read the
    Exchange's manifest over https, which the SDK's https-only guard requires of
    every leg outside this stack. Register and GetAccountStatus go to the endpoint
    that manifest names, so ``exchange_url`` is where the client is addressed and
    not what decides where the call goes.
    """
    return Client(
        _agent_config(
            exchange_url,
            agent_id,
            agent_signer(key_path),
            no_offer_verifier(),
            scheme=_DEPLOYED_SCHEME,
        )
    )


def sdk_broker_client(
    stack: StackURLs,
    verifier: Verifier | None = None,
    *,
    agent_id: str,
    key_path: Path | None = None,
    signer: SigningTransport | None = None,
    before_sign: BeforeSign | None = None,
    http: httpx.Client | None = None,
    request_id: RequestID | None = None,
    broker_url: str | None = None,
) -> BrokerClient:
    """The SDK client for the Broker: Resolve to discover, ExecuteTransaction to buy.

    The client signs as the agent the caller names (``agent_id``, ``key_path``), as
    :func:`sdk_exchange_client` does; there is no default agent. ``verifier``
    defaults to one holding every stack exchange's offer key.
    ``signer`` replaces the agent's own signer. ``before_sign`` alters each request
    before the SDK signs it, so the altered bytes still carry a valid signature;
    ``http`` replaces the client the SDK sends through, so a transport can alter a
    request after it was signed (see ``after_signing``). ``request_id`` mints the
    X-Request-ID every call carries; without it the SDK sends none. ``broker_url``
    addresses another Broker of the stack than ``stack.broker``: ``None`` means
    ``stack.broker``, and any other value, an empty one included, is used as given.
    """
    config = _agent_config(
        broker_url if broker_url is not None else stack.broker,
        agent_id,
        _signer(key_path, signer),
        verifier if verifier is not None else stack_offer_verifier(),
        before_sign=before_sign,
        request_id=request_id,
    )
    return BrokerClient(config, http=http)


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
