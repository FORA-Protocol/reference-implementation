"""A per-test agent identity: ``fresh_agent`` and ``fresh_agent_factory`` (ADR-028 D7).

A buyer shared across tests carries state from one test into the next and from
one run into the next: its balance, the reporting obligations of every
purchase it never reported (they go REPORTING_OVERDUE after the reporting
window), and its idempotency keys. A test that buys as a shared buyer is
therefore testing whatever the tests before it left behind. ADR-028 D7 gives
each test its own agent instead, and requires the suite to pass a second time
against a stack it already ran on.

A fresh agent is the same three things every agent in this stack is, made at
run time:

* **a key**: an Ed25519 key minted in this process by the SDK's
  ``fora_sdk.identity.generate_key``;
* **a key directory**: the Web Bot Auth document publishing that key, built by
  the SDK's ``fora_sdk.identity.directory_document`` and registered over HTTP at
  the stack's ``agent-directory`` service under a random host in
  :data:`AGENT_ZONE`. The stack's wildcard DNS resolves every name in that zone
  to the service, so the Broker and every Exchange fetch the directory from the
  agent's own host, the way they fetch any agent's;
* **a requester identity**: the host is the agent's id and its
  ``requester.domain``, and it is the ``Signature-Agent`` every request is signed
  under.

No Exchange knows the agent before its first signed call. Each one resolves the
key from the directory and registers the agent's identity row itself (lazy
registration, ADR-009 D2). An agent that is to pay opens an account with
:meth:`FreshAgent.fund`, a signed Register at each Exchange it names, which
grants that Exchange's welcome credit: every seat activates new agents by
default once the seed has run.

**One identity currency.** Every signing seam in this harness takes the keyfile
``scripts/gen-e2e-keys.sh`` writes (``load_keypair`` reads it). A fresh agent
writes its key to such a file under the test's temporary directory, so it signs
through exactly the seams the fixed identities sign through instead of a second
copy of each.

**Fixed identities stay** where a test needs a party that was set up before it
ran: the credit-less buyer, whose account is opened held and switched on unfunded;
the lazy-registration agent; and the subjects of the identity-list guard.
"""

from __future__ import annotations

import json
import secrets
import time
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from datetime import UTC, datetime
from decimal import Decimal
from pathlib import Path

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk.b64 import b64url_nopad
from fora_sdk.directory_signature import sign_directory_response
from fora_sdk.identity import directory_document, generate_key
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import BrokerClient, Client
from fora_sdk.thumbprint import thumbprint as raw_thumbprint
from fora_sdk.wire import to_wire
from wire.models import KeyRevocationList, WBAFile

from .constants import REVOCATION_PATH
from .httpsig_signer import signing_transport
from .ledger import Balances, balances
from .sdk_client import (
    BeforeSign,
    RequestID,
    sdk_broker_client,
    sdk_exchange_client,
    stack_offer_verifier,
)
from .seed_accounts import open_active_account
from .stack_urls import StackURLs

#: The DNS zone every per-test agent's host is minted in. The stack's
#: wildcard-DNS sidecar resolves the whole zone to the ``agent-directory``
#: service; ``test_guards_identity_lists.py`` fails when the compose file and
#: this value disagree. ``.example`` is reserved (RFC 6761), so no agent host
#: can ever name a real party.
AGENT_ZONE = "agents.example"

#: The path under which ``agent-directory`` registers a host's directory.
_REGISTER_PATH = "/hosts/"

#: Where every agent directory is served, on the agent's own host.
DIRECTORY_PATH = "/.well-known/http-message-signatures-directory"

_REGISTER_TIMEOUT_SEC = 10.0

#: A directory's response signature opens a minute in the past, so a verifier's
#: clock a little ahead of the runner's still accepts it, and lasts a day, longer
#: than any run: a verifier caches a directory, and the signature has to outlive
#: the copy it holds.
_DIRECTORY_SIGNATURE_BACKDATE_SEC = 60
_DIRECTORY_SIGNATURE_LIFETIME_SEC = 24 * 60 * 60


def thumbprint_of(key: Ed25519PrivateKey) -> str:
    """The RFC 7638 thumbprint of ``key``'s public half: its keyid, and how a list names it."""
    return raw_thumbprint(key.public_key().public_bytes_raw())


def _register(
    stack: StackURLs,
    host: str,
    kind: str,
    document: dict[str, object],
    *,
    signed_by: Sequence[Ed25519PrivateKey] = (),
) -> None:
    """PUT ``document`` as ``host``'s ``kind`` ("" for the directory) at agent-directory.

    A directory is registered with the response signature the Web Bot Auth
    profile requires of every key it lists (WG-00 Section 5.5): ``signed_by`` signs
    the exact body through the SDK's ``sign_directory_response``, for ``host`` as the
    authority a verifier fetches it under, and agent-directory serves the three
    headers with it. With no ``signed_by`` the document is served unsigned.
    """
    assert stack.agent_directory, "this stack runs no agent-directory service to mint agents at"
    suffix = f"/{kind}" if kind else ""
    body = json.dumps(document, separators=(",", ":")).encode()
    headers = {"Content-Type": "application/json"}
    if signed_by:
        now = int(time.time())
        signature = sign_directory_response(
            host,
            body,
            [key.private_bytes_raw() for key in signed_by],
            now - _DIRECTORY_SIGNATURE_BACKDATE_SEC,
            now + _DIRECTORY_SIGNATURE_LIFETIME_SEC,
        )
        headers.update(signature.headers())
    response = httpx.put(
        f"{stack.agent_directory}{_REGISTER_PATH}{host}{suffix}",
        content=body,
        headers=headers,
        timeout=_REGISTER_TIMEOUT_SEC,
    )
    assert response.status_code == httpx.codes.NO_CONTENT, (
        f"agent-directory refused {kind or 'the directory'} for {host}: "
        f"{response.status_code} {response.text}"
    )


@dataclass(frozen=True)
class KeyWindow:
    """A published key's half-open validity window, ``[not_before, not_after)``."""

    not_before: datetime
    not_after: datetime


def _directory(
    keys: Sequence[Ed25519PrivateKey],
    revocations: str | None,
    windows: Mapping[str, KeyWindow] | None = None,
) -> dict[str, object]:
    """The SDK's directory document for ``keys``, naming the revocation URL when given.

    The SDK helper leaves revocation_url unset and gives every key the same window,
    so the revocation URL, and the window of each key ``windows`` names by
    thumbprint, are set through the generated ``WBAFile`` model rather than by
    editing the JSON by hand.
    """
    document = WBAFile.model_validate(directory_document([key.public_key() for key in keys]))
    published = list(document.keys or [])
    for index, key in enumerate(keys):
        window = (windows or {}).get(thumbprint_of(key))
        if window is not None:
            published[index] = published[index].model_copy(
                update={
                    "not_before": window.not_before.isoformat(),
                    "not_after": window.not_after.isoformat(),
                }
            )
    update: dict[str, object] = {"keys": published}
    if revocations is not None:
        update["revocation_url"] = revocations
    built: dict[str, object] = to_wire(document.model_copy(update=update))
    return built


def _register_directory(stack: StackURLs, host: str, key: Ed25519PrivateKey) -> None:
    """Publish ``key``'s directory at ``host`` through the stack's agent-directory service."""
    _register(stack, host, "", _directory([key], None), signed_by=[key])


def revocation_url(host: str) -> str:
    """The revocation list URL on ``host``, where agent-directory serves it."""
    return f"https://{host}{REVOCATION_PATH}"


def publish_revocations(stack: StackURLs, host: str, thumbprints: Sequence[str]) -> None:
    """Serve a KeyRevocationList naming ``thumbprints`` at ``host``'s revocation path.

    ``as_of`` is now. A verifier keeps the snapshot with the newest ``as_of``, and
    each host here is minted for one test and published once, so the list is the
    newest the verifier sees for that host.
    """
    as_of = datetime.fromtimestamp(int(time.time()), tz=UTC)
    document = KeyRevocationList(as_of=as_of, revoked=list(thumbprints))
    _register(stack, host, "revocations", to_wire(document))


def redirect(stack: StackURLs, host: str, path: str, location: str) -> None:
    """Make ``host`` answer ``path`` with a 302 to ``location``, a URL under AGENT_ZONE."""
    _register(stack, host, "redirects", {path: location})


def new_host() -> str:
    """A fresh host in :data:`AGENT_ZONE`, registered nowhere yet."""
    return f"{secrets.token_hex(8)}.{AGENT_ZONE}"


def redirect_chain(stack: StackURLs, first: str, path: str, location: str, hops: int) -> None:
    """Make ``first`` answer ``path`` through a chain of ``hops`` redirects ending at ``location``.

    ``first`` answers with the first redirect; each further hop is a fresh host of
    :data:`AGENT_ZONE` answering the same path with the next one, and the last hop
    points at ``location``. A fetch of ``first`` that follows the whole chain
    therefore follows exactly ``hops`` redirects.
    """
    assert hops >= 1, "a chain has at least one redirect"
    hosts = [first, *(new_host() for _ in range(hops - 1))]
    for here, there in zip(hosts, hosts[1:], strict=False):
        redirect(stack, here, path, f"https://{there}{path}")
    redirect(stack, hosts[-1], path, location)


def _write_keyfile(path: Path, host: str, key: Ed25519PrivateKey) -> Path:
    """Write ``key`` in the harness keyfile format, with ``host`` as its kid (its directory)."""
    path.write_text(
        json.dumps(
            {
                "kid": host,
                "private_key": b64url_nopad(key.private_bytes_raw()),
                "public_key": b64url_nopad(key.public_key().public_bytes_raw()),
            }
        )
    )
    return path


@dataclass
class FreshAgent:
    """One test's own agent: its key, its directory host, and SDK clients signed as it.

    ``host`` is the agent's identity in every sense the stack has: its id, its
    ``requester.domain``, the host its key directory is served at and the
    ``Signature-Agent`` its requests carry. ``ed25519`` is the agent's private
    key, and ``key_path`` the same key in the harness keyfile format, for the
    signing seams that take one.

    Clients built here are closed when the test ends; a test does not close them.
    """

    host: str
    ed25519: Ed25519PrivateKey
    thumbprint: str
    key_path: Path
    stack: StackURLs
    _exchanges: dict[str, Client] = field(default_factory=dict, repr=False)
    _owned: list[Client | BrokerClient] = field(default_factory=list, repr=False)
    _broker: BrokerClient | None = field(default=None, repr=False)
    _views: list[FreshAgent] = field(default_factory=list, repr=False)

    @property
    def agent_id(self) -> str:
        """The agent's id: its directory host."""
        return self.host

    @property
    def broker(self) -> BrokerClient:
        """The agent's Broker client, verifying offers from every Exchange in the stack."""
        if self._broker is None:
            self._broker = self.broker_client()
        return self._broker

    def exchange(self, domain: str) -> Client:
        """The agent's client for the Exchange ``domain``, built once per test."""
        if domain not in self._exchanges:
            self._exchanges[domain] = self.exchange_client(domain)
        return self._exchanges[domain]

    def broker_client(
        self,
        *,
        agent_id: str | None = None,
        directory: str | None = None,
        signer: SigningTransport | None = None,
        before_sign: BeforeSign | None = None,
        http: httpx.Client | None = None,
        request_id: RequestID | None = None,
    ) -> BrokerClient:
        """A further Broker client signed as this agent.

        ``agent_id`` replaces the free label in ``requester.id`` only; the
        signature and ``requester.domain`` still name this agent. ``directory``
        replaces how ``Signature-Agent`` writes the agent's directory (a schemed
        URL, say); the key is still this agent's. ``signer``, ``before_sign`` and
        ``http`` are the seams :func:`harness.malformed.malformed` alters a request
        through; an explicit ``signer`` wins over ``directory``. ``request_id``
        mints the X-Request-ID each call carries.
        """
        client = sdk_broker_client(
            self.stack,
            agent_id=agent_id or self.host,
            key_path=self.key_path,
            signer=signer or (signing_transport(directory, self.ed25519) if directory else None),
            before_sign=before_sign,
            http=http,
            request_id=request_id,
        )
        self._owned.append(client)
        return client

    def exchange_client(
        self,
        domain: str,
        *,
        agent_id: str | None = None,
        signer: SigningTransport | None = None,
        before_sign: BeforeSign | None = None,
        http: httpx.Client | None = None,
    ) -> Client:
        """A further client for the Exchange ``domain``, with the seams ``broker_client`` takes.

        ``agent_id`` replaces the free label in ``requester.id`` only, as for
        :meth:`broker_client`.
        """
        client = sdk_exchange_client(
            self.stack,
            domain,
            stack_offer_verifier(),
            agent_id=agent_id or self.host,
            key_path=self.key_path,
            signer=signer,
            before_sign=before_sign,
            http=http,
        )
        self._owned.append(client)
        return client

    def fund(self, *exchanges: str) -> None:
        """Open an active account at each of ``exchanges``, holding its welcome credit."""
        assert exchanges, "fund() needs at least one Exchange to open an account at"
        for domain in exchanges:
            open_active_account(self.exchange(domain), domain, self.host)

    def balances(self, *exchanges: str) -> dict[str, Balances]:
        """The agent's balance per currency at each of ``exchanges``, through GetAccountStatus."""
        return {domain: balances(self.exchange(domain), domain) for domain in exchanges}

    def balance(self, exchange: str, currency: str) -> Decimal:
        """The agent's balance in ``currency`` at ``exchange``."""
        return self.balances(exchange)[exchange][currency]

    def publish(
        self,
        keys: Sequence[Ed25519PrivateKey],
        *,
        revocations: bool = False,
        windows: Mapping[str, KeyWindow] | None = None,
    ) -> None:
        """Republish this agent's directory with ``keys``, in order.

        With ``revocations`` the directory names a revocation_url on the agent's own
        host, which :func:`publish_revocations` fills. A verifier reads a host's list
        when it first fetches the host's directory, so a test publishes both before
        the agent's first signed call.

        ``windows`` gives a key, named by its thumbprint, a validity window of its
        own: one that opens in the future, or one that has already closed. Every
        other key gets the SDK's default window, which covers now.
        """
        url = revocation_url(self.host) if revocations else None
        _register(self.stack, self.host, "", _directory(keys, url, windows), signed_by=keys)

    def signing_as(self, key: Ed25519PrivateKey) -> FreshAgent:
        """This same agent, signing every request and acceptance with ``key`` instead.

        The host, the account and the requester are this agent's; only the key
        differs. ``key`` must be one this agent's directory publishes for a
        signature by it to verify. The view's clients close with this agent's.
        """
        view = _agent(self.stack, self.key_path.parent, self.host, key, thumbprint_of(key)[:12])
        self._views.append(view)
        return view

    def close(self) -> None:
        """Close every client built for this agent and for its other-key views."""
        for client in self._owned:
            client.close()
        for view in self._views:
            view.close()
        self._owned.clear()
        self._views.clear()
        self._exchanges.clear()
        self._broker = None


def _agent(
    stack: StackURLs, directory: Path, host: str, key: Ed25519PrivateKey, label: str = ""
) -> FreshAgent:
    """The agent signing as ``key`` under ``host``, its keyfile written under ``directory``."""
    name = f"{host}.{label}.json" if label else f"{host}.json"
    return FreshAgent(
        host=host,
        ed25519=key,
        thumbprint=thumbprint_of(key),
        key_path=_write_keyfile(directory / name, host, key),
        stack=stack,
    )


def mint_agent(stack: StackURLs, directory: Path) -> FreshAgent:
    """Mint an agent: a new key, published under a new host, its keyfile written under ``directory``."""
    key, _thumbprint = generate_key()
    host = new_host()
    _register_directory(stack, host, key)
    return _agent(stack, directory, host, key)


@dataclass
class AgentFactory:
    """Mints agents for one test (``factory()``) and closes their clients when it ends.

    The ``fresh_agent_factory`` fixture is one of these, for a test that needs
    several agents: a buyer and a foreign party, say.
    """

    stack: StackURLs
    directory: Path
    _minted: list[FreshAgent] = field(default_factory=list, repr=False)

    def __call__(self) -> FreshAgent:
        agent = mint_agent(self.stack, self.directory)
        self._minted.append(agent)
        return agent

    def redirected_to(self, agent: FreshAgent, *, hops: int = 1) -> FreshAgent:
        """An agent on a new host whose directory path redirects to ``agent``'s directory.

        The path answers through a chain of ``hops`` redirects (:func:`redirect_chain`).
        It signs with ``agent``'s key under its own host, so the only directory that
        publishes the key is the one at the end of the chain: a verifier resolves the
        key only by following every redirect.
        """
        host = new_host()
        redirect_chain(
            self.stack, host, DIRECTORY_PATH, f"https://{agent.host}{DIRECTORY_PATH}", hops
        )
        alias = _agent(self.stack, self.directory, host, agent.ed25519)
        self._minted.append(alias)
        return alias

    def close(self) -> None:
        """Close every minted agent's clients."""
        for agent in self._minted:
            agent.close()
