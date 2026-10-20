"""A per-test MCP agent, and the documents the identity service publishes for it.

THE AGENT. The three MCP agents the session fixtures sign up are the bootstrap's
fixed logins, so each carries every registration and purchase any earlier test or
run made as it. A test that needs an agent nobody else has touched (ADR-028 D7)
takes one from :class:`McpAgentFactory`, which goes through the same steps a new
developer does:

* **a login** the developers' instance of the stack's login provider has never
  seen, imported through that instance's management API
  (:func:`publisher_accounts.import_login`, which imports a fresh publisher into
  the publishers' instance the same way). The identity service signs people in
  through the developers' instance, so the login works on its sign-in page;
* **a sign-up** at the identity service as that login
  (:func:`identity_signup.provision_agent`), which mints the agent's subdomain and
  custodied key and publishes its documents;
* **a bearer** for the new subdomain, minted the way every MCP suite mints one
  (:func:`mcp_session.mcp_session_bearer`).

:meth:`McpAgentFactory.sign_in` instead carries the sign-in through the consent
screen and the token endpoint, for the test whose subject the sign-in is.

THE DOCUMENTS. An agent's subdomain serves four documents. The key directory, the
role marker and the revocation list are read through the SDK's own readers, which
refuse a wrong media type and a body that fails the published strict schema. The
SDK has no reader for the Signature Agent Card, a document the protocol does not
define, so :func:`signature_agent_card` reads it here and returns a typed answer.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from http import HTTPStatus
from urllib.parse import parse_qs, urlsplit

import httpx
from fora_sdk.b64 import b64url_decode
from fora_sdk.resolvers import (
    Document,
    read_manifest,
    read_revocation_list,
    read_wba_directory,
)
from fora_sdk.thumbprint import thumbprint
from wire.models import KeyRevocationList, WBAFile, WellKnownManifest

from .identity_signup import SignIn, provision_agent, sign_in
from .mcp_session import mcp_session_bearer
from .publisher_accounts import DEVELOPERS_TOKEN_FILE, import_login
from .stack_urls import StackURLs

#: The scheme the identity service serves agent documents on in this stack
#: (``IDENTITY_WELLKNOWN_SCHEME`` in ``docker-compose.e2e.yml``).
WELLKNOWN_SCHEME = "http"

#: Where an agent's Signature Agent Card is served.
CARD_PATH = "/.well-known/signature-agent-card.json"

_TIMEOUT_SEC = 10.0


@dataclass(frozen=True)
class McpAgent:
    """One agent the identity service custodies, signed up for this test alone."""

    #: The agent's subdomain, a full host name. It is also its FORA identity.
    subdomain: str
    #: A bearer for the MCP endpoint, valid for ten minutes.
    bearer: str

    @property
    def origin(self) -> str:
        """The origin the agent's documents are served on."""
        return f"{WELLKNOWN_SCHEME}://{self.subdomain}"


@dataclass
class McpAgentFactory:
    """Signs up agents for one test: ``factory()`` once per agent needed."""

    stack: StackURLs

    def __call__(self) -> McpAgent:
        login = import_login(self.stack.zitadel, DEVELOPERS_TOKEN_FILE, verified=True)
        subdomain = provision_agent(
            self.stack.identity, login_name=login.name, password=login.password
        )
        return McpAgent(subdomain=subdomain, bearer=mcp_session_bearer(subdomain))

    def sign_in(self) -> SignIn:
        """Sign a new login in through consent and the token endpoint, as an MCP client does."""
        login = import_login(self.stack.zitadel, DEVELOPERS_TOKEN_FILE, verified=True)
        return sign_in(self.stack.identity, login_name=login.name, password=login.password)


def key_directory(agent: McpAgent) -> Document[WBAFile]:
    """The Web Bot Auth key directory at the agent's subdomain, read by the SDK."""
    return read_wba_directory(agent.subdomain, scheme=WELLKNOWN_SCHEME)


def role_marker(agent: McpAgent) -> Document[WellKnownManifest]:
    """The ``fora.json`` at the agent's subdomain, read by the SDK."""
    return read_manifest(agent.subdomain, scheme=WELLKNOWN_SCHEME)


def revocation_list(directory: WBAFile) -> Document[KeyRevocationList]:
    """The revocation list ``directory`` names, read by the SDK."""
    assert directory.revocation_url, f"the directory names no revocation list: {directory}"
    return read_revocation_list(directory.revocation_url)


def thumbprints(directory: WBAFile) -> set[str]:
    """The RFC 7638 thumbprint of every key ``directory`` publishes."""
    keys = directory.keys or []
    assert all(key.x for key in keys), f"a key in the directory carries no x: {directory}"
    return {thumbprint(b64url_decode(str(key.x))) for key in keys}


def bound_agent_id(delivery_url: str) -> str:
    """The ``agent_id`` a delivery URL is bound to: the thumbprint of the buyer's key."""
    values = parse_qs(urlsplit(delivery_url).query).get("agent_id", [])
    assert len(values) == 1, (
        f"the delivery URL carries {len(values)} agent_id values: {delivery_url}"
    )
    return values[0]


@dataclass(frozen=True)
class AgentCard:
    """How the agent's subdomain answered for its Signature Agent Card."""

    status: HTTPStatus
    #: The ``Content-Type`` essence, lowercased and without parameters.
    media_type: str
    #: The card's members, or None when the answer was not a JSON object.
    members: dict[str, object] | None


def signature_agent_card(agent: McpAgent) -> AgentCard:
    """Read the Signature Agent Card at the agent's subdomain."""
    resp = httpx.get(f"{agent.origin}{CARD_PATH}", timeout=_TIMEOUT_SEC)
    media_type = resp.headers.get("content-type", "").split(";", 1)[0].strip().lower()
    members: dict[str, object] | None = None
    if resp.status_code == httpx.codes.OK:
        parsed = json.loads(resp.content)
        members = parsed if isinstance(parsed, dict) else None
    return AgentCard(status=HTTPStatus(resp.status_code), media_type=media_type, members=members)
