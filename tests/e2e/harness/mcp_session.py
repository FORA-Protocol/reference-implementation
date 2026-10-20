"""Reaching the identity service's MCP endpoint as a real agent.

Two suites drive that endpoint — the discover/execute flow and the account
tools — and everything they need to get a session is the same: the endpoint is
in-network only, the container carries no healthcheck, and the bearer has to be
minted with the credentials the compose file gave the service. Kept here because
the duplication budget is zero, and because a second copy of the credential
reading is the copy that goes stale.

THE ORDER IS PART OF IT. Skip first, then wait for identity to answer, then mint:
a test that minted before the service was up would fail as an unexplained
connection error rather than as a wait that timed out. The three are not called
one by one in a test body — conftest wraps them as the ``identity_ready``,
``mcp_agent`` and ``mcp_bearer`` fixtures (and, for the second login, as
``unregistered_agent`` and ``unregistered_bearer``), so the sequence is stated
once instead of being a contract seven test bodies each had to get right.

WHAT REPEATS AND WHAT DOES NOT. Readiness and sign-up are session-scoped; only
the bearer is minted per test, because only its ten-minute TTL forces that.

A BEARER THE SERVICE NEVER ISSUED. :func:`forged_bearer` mints a token that has
every claim a real one has and is signed by a key the service has never seen, for
the tests that show such a token is refused. A caller with no usable bearer never
gets an MCP session, so those refusals are read below the MCP client, in
``mcp_transport.py``.
"""

from __future__ import annotations

import base64
import os

import pytest

from .identity_signup import BOB_USER, CAROL_USER, mint_bearer, provision_agent
from .readiness import wait_healthy
from .stack_urls import StackURLs


def required_env(name: str) -> str:
    """Read ``name`` or fail naming what is supposed to set it.

    Read without a fallback on purpose: a default here would be a second copy of
    a credential, and the failure mode of a stale copy is an unexplained 401
    rather than anything that names the mismatch. Absent env means the stack was
    not brought up as the compose file defines it, which is worth failing on.

    Called inside a test, never at import: a module-level failure would be a
    COLLECTION error, and ``make test-e2e-collect`` enumerates these suites
    deliberately without a stack.
    """
    value = os.environ.get(name)
    if not value:
        pytest.fail(
            f"{name} is not set. docker-compose.e2e.yml sets it on the runner via the "
            f"x-identity-auth anchor, shared with the identity service; run this test "
            f"through `make test-e2e` (or `docker compose --profile test run --rm "
            f"runner`), not a bare pytest."
        )
    return value


def require_in_network(compose_stack: StackURLs) -> None:
    """The MCP endpoint is in-network only: identity publishes no host port."""
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1" or not compose_stack.identity:
        pytest.skip("identity MCP endpoint is reachable only in-network (FORA_E2E_IN_NETWORK=1)")


def await_identity(compose_stack: StackURLs) -> None:
    """Wait for identity to answer /healthz (distroless carries no healthcheck).

    Uses the harness's one readiness poller rather than another copy: that one
    also treats a refused connection (OSError) as "not up yet", which is exactly
    the condition here while the container is starting, and it reports the last
    error it saw instead of discarding every diagnostic across the whole wait.
    """
    wait_healthy(f"{compose_stack.identity}/healthz", timeout_seconds=60.0)


def mcp_session_agent(compose_stack: StackURLs) -> str:
    """Provision a real agent through Zitadel sign-up and return its subdomain.

    This is the expensive half and it runs ONCE per session. Sign-up is a dynamic
    client registration, /authorize, the Zitadel login form through two POSTs, a
    redirect chain, /callback and a consent-screen scrape — and it is idempotent
    for the agent, which is why the account-tools suite can treat its
    registrations as accumulating. What is NOT idempotent is the OAuth client the
    flow mints and persists: running this per test left one client row per test
    where one would do.
    """
    return provision_agent(compose_stack.identity)


def mcp_unregistered_agent(compose_stack: StackURLs) -> str:
    """Provision the agent that holds no billing account anywhere, and return it.

    The same sign-up as above, as the bootstrap's second local login. Registration
    is per agent and survives a stack left up, so the session agent above has an
    account at every exchange the account-tool suites registered it at, and
    nothing can make it a fresh sign-up again. This agent is the one no suite
    registers. That is a reservation, not a guarantee: a test that takes it and
    calls ``fora_register`` breaks every claim its consumers make, so they read
    ``fora_status`` before relying on it rather than assuming it.
    """
    return provision_agent(compose_stack.identity, login_name=BOB_USER)


def mcp_spending_agent(compose_stack: StackURLs) -> str:
    """Provision the agent only the balance suite registers and spends as.

    The same sign-up again, as the bootstrap's third local login. The balance
    suite measures how much one purchase takes off this agent's balance, so no
    other suite may register it or buy with it. The welcome credit it receives
    at its first registration is what it spends.
    """
    return provision_agent(compose_stack.identity, login_name=CAROL_USER)


def mcp_session_bearer(subdomain: str) -> str:
    """Mint a bearer for an already-provisioned agent.

    This is the half that has to repeat: the bearer's TTL is ten minutes, so a
    session-scoped one would expire under a long run. It makes no network call —
    it signs a token with the seed the compose file gave the service — so
    repeating it costs nothing.

    The issuer and seed are read from the shared compose env, never defaulted
    here: a default would let a suite pass against a service configured
    differently from the one deployed.
    """
    issuer = required_env("IDENTITY_AUTH_ISSUER")
    seed_b64 = required_env("IDENTITY_TOKEN_SIGNING_KEY")
    return mint_bearer(subdomain, issuer=issuer, audience=issuer, seed_b64=seed_b64)


def forged_bearer(subdomain: str) -> str:
    """A bearer naming ``subdomain`` with the right issuer and audience, signed by a stranger.

    Every claim is what :func:`mcp_session_bearer` would write. Only the signing
    key differs: a fresh random seed the identity service has never held, so the
    token is refused for its signature and for nothing else.
    """
    issuer = required_env("IDENTITY_AUTH_ISSUER")
    stranger = base64.b64encode(os.urandom(32)).decode()
    return mint_bearer(subdomain, issuer=issuer, audience=issuer, seed_b64=stranger)
