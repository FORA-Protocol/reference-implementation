"""Provision a real agent through the identity service's OIDC sign-up, in-network.

This is how the e2e gets a usable agent for the Go MCP endpoint: it drives the
SAME sign-up an SDK-less developer would — register an OAuth client at identity,
start the authorize flow, log in through the real Zitadel UI headlessly, and let
identity's /callback provision the agent. Provisioning mints the agent's Ed25519
key in identity's Vault and hosts its WBA directory at ``<subdomain>.foramcp.org``
by the time the callback lands, so the agent can already sign FORA requests —
which is all the discover/execute/report tools need.

The bearer is then minted directly with the deterministic e2e token key
(:func:`mint_bearer`) rather than by finishing the consent → token dance:
identity verifies that self-minted token with the very same key it would have
signed one with, so it is the same credential, and skipping the UI steps that add
nothing to a discover/execute test keeps this driver from scraping more of
Zitadel's and identity's HTML than it must. Provisioning is real; only the final
token issuance is short-circuited.

The sign-in itself is the subject of one obligation, so the whole of it is here
too. :func:`sign_in` approves the consent screen and redeems the code at the token
endpoint. :func:`authorize_with`, :func:`token_grant` and
:func:`authorization_server_metadata` read how the authorization server answers
requests it must refuse. Each returns a typed answer; the raw responses stay here.
"""

from __future__ import annotations

import base64
import hashlib
import re
import secrets
import time
import uuid
from dataclasses import dataclass
from http import HTTPStatus
from typing import Any
from urllib.parse import parse_qsl, urlsplit

import httpx
import jwt
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from fora_sdk.b64 import b64url_nopad

# The provider logins are re-exported: the suites that sign up import them from
# here, beside the sign-up that uses them. The `X as X` form marks the two this
# module does not use itself as deliberate re-exports.
from .provider_login import (
    ALICE_PASSWORD,
    ALICE_USER,
    BOB_USER as BOB_USER,
    CAROL_USER as CAROL_USER,
    Browser,
    Login,
    Registration,
    pass_provider,
)


# The harness's OAuth redirect — never actually dialed (the flow stops before the
# browser would follow it), but it must be registered and echoed consistently
# through the authorize flow.
CLIENT_REDIRECT = "http://127.0.0.1:5599/callback"

# The authorization server's metadata (RFC 8414), at a fixed path on its issuer.
_METADATA_PATH = "/.well-known/oauth-authorization-server"

_TIMEOUT_SEC = 15.0

# The consent page renders two spans with class "sub" — the subdomain and the
# redirect URI — so the id is what disambiguates them. The Go integration test
# asserts this same fragment, so a template edit that breaks this regex fails
# there first.
_SUB_RE = re.compile(r'<span class="sub" id="agent-subdomain">([^<]+)</span>')
# The consent form's one hidden input: the token the approval has to echo.
_CSRF_RE = re.compile(r'<input type="hidden" name="csrf_token" value="([^"]+)">')


def _body_excerpt(html: str, limit: int = 400) -> str:
    """Return an excerpt of a render starting at <body>, for a failure message.

    The consent page carries an inline stylesheet, so <body> does not start until
    roughly 700 characters in and the subdomain span not until roughly 880. An
    excerpt taken from the start of the document shows the doctype, the head and
    half the CSS — never the markup a regex mismatch is about.
    """
    start = html.find("<body")
    return html[start if start != -1 else 0 :][:limit]


class SignupError(RuntimeError):
    """A sign-up leg did not produce the expected result."""


def provision_agent(
    identity_base: str,
    *,
    login_name: str = ALICE_USER,
    password: str = ALICE_PASSWORD,
    registration: Registration | None = None,
    browser: Browser | None = None,
) -> str:
    """Run a full sign-up and return the minted agent subdomain.

    The provider is passed as ``login_name`` with ``password``, or, given a
    ``registration``, by signing up a new account there. ``browser`` keeps the
    provider's session for the caller to inspect afterwards.

    identity_base is the in-network origin (http://identity); the provider's
    address is whatever identity's authorize redirect names. Cookies are kept
    per origin: one client for identity (the authorize→callback session) and a
    separate per-host browser inside the provider login. ``password`` is the
    bootstrap logins' shared one unless the caller minted a login of its own.
    """
    # A single identity client carries the flow cookie authorize sets and
    # callback reads. Redirects are followed manually so each Location is visible.
    with _identity_client(identity_base) as idc:
        client_id = _register_client(idc)
        _, challenge = _pkce()
        _through_callback(
            idc,
            identity_base,
            client_id,
            challenge,
            Login(login_name, password, registration),
            browser,
        )
        subdomain, _ = _consent(idc)
        return subdomain


@dataclass(frozen=True)
class SignIn:
    """A sign-in carried to its end: the agent, the authorization response, the token.

    ``response`` holds the parameters the consent approval redirected back to the
    client with (``code``, ``state`` and ``iss``), and ``access_token`` is what the
    token endpoint answered for that code.
    """

    subdomain: str
    response: dict[str, str]
    access_token: str


def sign_in(identity_base: str, *, login_name: str, password: str) -> SignIn:
    """Sign in as ``login_name`` the way an MCP client does, through to the access token.

    The same sign-up as :func:`provision_agent`, then the two steps it skips: the
    developer approves the consent screen, and the client redeems the code with its
    PKCE verifier at the token endpoint.
    """
    with _identity_client(identity_base) as idc:
        client_id = _register_client(idc)
        verifier, challenge = _pkce()
        _through_callback(idc, identity_base, client_id, challenge, Login(login_name, password))
        subdomain, csrf = _consent(idc)
        approved = idc.post("/consent", data={"csrf_token": csrf, "decision": "approve"})
        if approved.status_code != httpx.codes.FOUND:
            raise SignupError(f"consent POST: {approved.status_code} {approved.text[:256]}")
        target, response = _split_redirect(approved.headers.get("location", ""))
        if target != CLIENT_REDIRECT or not response.get("code"):
            raise SignupError(f"the approval redirected to {target!r} with {response}")
        redeemed = idc.post(
            "/token",
            data={
                "grant_type": "authorization_code",
                "code": response["code"],
                "code_verifier": verifier,
                "client_id": client_id,
                "redirect_uri": CLIENT_REDIRECT,
            },
        )
    if redeemed.status_code != httpx.codes.OK:
        raise SignupError(f"token: {redeemed.status_code} {redeemed.text[:256]}")
    token = str(redeemed.json().get("access_token", ""))
    if not token:
        raise SignupError(f"the token endpoint answered no access_token: {redeemed.text[:256]}")
    return SignIn(subdomain=subdomain, response=response, access_token=token)


@dataclass(frozen=True)
class AuthorizationAnswer:
    """How ``/authorize`` answered: its status, and where and with what it redirected."""

    status: HTTPStatus
    #: The redirect target without its query, empty when the answer did not redirect.
    target: str
    #: The query parameters of the redirect.
    params: dict[str, str]


def authorize_with(identity_base: str, *, challenge_method: str | None) -> AuthorizationAnswer:
    """Send an authorization request whose PKCE challenge uses ``challenge_method``.

    ``None`` sends no challenge at all. ``"plain"`` sends the verifier itself as the
    challenge, which is what the plain method means (RFC 7636 §4.2). The client is
    registered first, so the server trusts its redirect and answers through it.
    """
    with _identity_client(identity_base) as idc:
        client_id = _register_client(idc)
        verifier, _ = _pkce()
        params = {
            "response_type": "code",
            "client_id": client_id,
            "redirect_uri": CLIENT_REDIRECT,
            "state": "e2e-state",
            "scope": "openid",
        }
        if challenge_method is not None:
            params["code_challenge"] = verifier
            params["code_challenge_method"] = challenge_method
        resp = idc.get("/authorize", params=params)
    target, query = _split_redirect(resp.headers.get("location", ""))
    return AuthorizationAnswer(status=HTTPStatus(resp.status_code), target=target, params=query)


@dataclass(frozen=True)
class TokenAnswer:
    """How the token endpoint answered: its status, the OAuth error, and any token."""

    status: HTTPStatus
    #: The RFC 6749 ``error`` code, empty when the answer named none.
    error: str
    #: The access token, empty when none was issued.
    access_token: str


def token_grant(identity_base: str, *, grant_type: str) -> TokenAnswer:
    """POST a token request of ``grant_type`` as a freshly registered client."""
    with _identity_client(identity_base) as idc:
        client_id = _register_client(idc)
        resp = idc.post("/token", data={"grant_type": grant_type, "client_id": client_id})
    is_json = resp.headers.get("content-type", "").startswith("application/json")
    body = resp.json() if is_json else {}
    return TokenAnswer(
        status=HTTPStatus(resp.status_code),
        error=str(body.get("error", "")),
        access_token=str(body.get("access_token", "")),
    )


def authorization_server_metadata(identity_base: str) -> dict[str, Any]:
    """The authorization server's RFC 8414 metadata document."""
    resp = httpx.get(f"{identity_base}{_METADATA_PATH}", timeout=_TIMEOUT_SEC)
    if resp.status_code != httpx.codes.OK:
        raise SignupError(f"metadata: {resp.status_code} {resp.text[:256]}")
    document: dict[str, Any] = resp.json()
    return document


def provider_login_page(identity_base: str, browser: Browser) -> httpx.Response:
    """The provider's login page, reached the way identity sends a developer there."""
    with _identity_client(identity_base) as idc:
        return browser.get(_upstream_authorize_url(idc))


def _upstream_authorize_url(idc: httpx.Client) -> str:
    """Register the harness at identity and start its authorize flow; return
    the provider's authorize address identity sends the browser to."""
    _, challenge = _pkce()
    return _authorize(idc, _register_client(idc), challenge)


def mint_bearer(
    subdomain: str, *, issuer: str, audience: str, seed_b64: str, ttl_s: int = 600
) -> str:
    """Mint the EdDSA bearer identity's token verifier accepts for subdomain.

    Signed with the deterministic e2e token seed (IDENTITY_TOKEN_SIGNING_KEY), so
    it verifies under the same key identity mints with. sub is the subdomain — the
    durable agent identity every tool signs as.
    """
    key = Ed25519PrivateKey.from_private_bytes(base64.b64decode(seed_b64))
    now = int(time.time())
    claims = {
        "iss": issuer,
        "sub": subdomain,
        "aud": audience,
        "iat": now,
        "nbf": now,
        "exp": now + ttl_s,
        "jti": uuid.uuid4().hex,
    }
    return jwt.encode(claims, key, algorithm="EdDSA", headers={"typ": "JWT"})


def _identity_client(identity_base: str) -> httpx.Client:
    """A client for identity that keeps its cookies and shows every redirect."""
    return httpx.Client(base_url=identity_base, timeout=_TIMEOUT_SEC, follow_redirects=False)


def _split_redirect(location: str) -> tuple[str, dict[str, str]]:
    """A redirect's target without its query, and the query as a mapping."""
    if not location:
        return "", {}
    parts = urlsplit(location)
    return f"{parts.scheme}://{parts.netloc}{parts.path}", dict(parse_qsl(parts.query))


def _through_callback(
    idc: httpx.Client,
    identity_base: str,
    client_id: str,
    challenge: str,
    login: Login,
    browser: Browser | None = None,
) -> None:
    """Authorize, get past the provider as ``login`` says (in ``browser`` when
    given), and deliver its code to identity's /callback."""
    upstream_url = _authorize(idc, client_id, challenge)
    code, state = _zitadel_login(upstream_url, identity_base, login, browser)
    _callback(idc, code, state)


def _register_client(idc: httpx.Client) -> str:
    """Dynamically register the harness as an OAuth client; return its client_id."""
    resp = idc.post(
        "/register",
        json={"redirect_uris": [CLIENT_REDIRECT], "client_name": "fora-e2e"},
        headers={"Content-Type": "application/json"},
    )
    if resp.status_code != httpx.codes.CREATED:
        raise SignupError(f"register: {resp.status_code} {resp.text[:256]}")
    client_id = resp.json().get("client_id")
    if not client_id:
        raise SignupError(f"register returned no client_id: {resp.text[:256]}")
    return client_id


def _pkce() -> tuple[str, str]:
    """Return an (verifier, S256 challenge) PKCE pair, base64url-no-pad."""
    verifier = b64url_nopad(secrets.token_bytes(32))
    challenge = b64url_nopad(hashlib.sha256(verifier.encode()).digest())
    return verifier, challenge


def _authorize(idc: httpx.Client, client_id: str, challenge: str) -> str:
    """GET /authorize; return the upstream (Zitadel) authorize URL it redirects to."""
    resp = idc.get(
        "/authorize",
        params={
            "response_type": "code",
            "client_id": client_id,
            "redirect_uri": CLIENT_REDIRECT,
            "state": "e2e-state",
            "code_challenge": challenge,
            "code_challenge_method": "S256",
            "scope": "openid",
        },
    )
    if resp.status_code != httpx.codes.FOUND:
        raise SignupError(f"authorize: {resp.status_code} {resp.text[:256]}")
    loc = resp.headers.get("location", "")
    if not loc:
        raise SignupError("authorize returned no upstream redirect")
    return loc


def _callback(idc: httpx.Client, code: str, state: str) -> None:
    """GET /callback with the upstream code+state; expect a 302 onward."""
    resp = idc.get("/callback", params={"code": code, "state": state})
    if resp.status_code != httpx.codes.FOUND:
        raise SignupError(f"callback: {resp.status_code} {resp.text[:256]}")


def _consent(idc: httpx.Client) -> tuple[str, str]:
    """GET /consent; return the minted subdomain the render shows, and its form token.

    The consent screen names the identity it is about to grant a client access
    to, which is the same subdomain /callback just provisioned. It renders from
    the sealed pending cookie alone, so it is readable at this point in the flow
    without approving anything.
    """
    resp = idc.get("/consent")
    if resp.status_code != httpx.codes.OK:
        raise SignupError(f"consent GET: {resp.status_code} {resp.text[:256]}")
    m = _SUB_RE.search(resp.text)
    if not m:
        raise SignupError(f"consent render carried no subdomain: {_body_excerpt(resp.text)}")
    csrf = _CSRF_RE.search(resp.text)
    if not csrf:
        raise SignupError(f"consent render carried no form token: {_body_excerpt(resp.text)}")
    return m.group(1).strip(), csrf.group(1)


def _zitadel_login(
    authorize_url: str, redirect_prefix: str, login: Login, browser: Browser | None
) -> tuple[str, str]:
    """Pass the provider's login UI in browser (a fresh one when None) and
    return the callback's code+state.

    The login itself is the shared provider driver's. What is particular here
    is where it stops: at identity's /callback, which is NOT followed, because
    delivering it is identity's own client's job -- that client carries the
    flow cookie authorize set.
    """
    callback_prefix = redirect_prefix.rstrip("/") + "/callback"
    own = browser or Browser()
    try:
        _, matched = pass_provider(own, own.get(authorize_url), login, stop_prefix=callback_prefix)
    finally:
        if browser is None:
            own.close()
    if not matched:
        raise SignupError(f"the login did not reach {callback_prefix}")
    return _code_state(matched)


def _code_state(callback_url: str) -> tuple[str, str]:
    """Pull the OAuth code + state off the callback URL the login chain reached."""
    q = httpx.URL(callback_url).params
    if q.get("error"):
        raise SignupError(f"zitadel login error: {q.get('error')} {q.get('error_description')}")
    code = q.get("code")
    if not code:
        raise SignupError(f"reached callback without code: {callback_url}")
    return code, q.get("state", "")
