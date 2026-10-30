"""The Exchanges' e2e-only control service (ADR-028 D6), and the one client for it.

Each Exchange in this stack is built with the ``e2e`` Go build tag, which mounts a
control service on its admin listener. It sets state no admin RPC reaches:

* **per-tenant lifetimes**: how long the seat's offers and delivery URLs stay
  valid. A test shortens them (:func:`short_lifetimes`) and polls until what it
  bought or was offered has expired. The control can only shorten: the Exchange
  refuses an override that is not shorter than the configured lifetime, and
  ignores one that would be. A tenant id that names no seat is refused 404;
* **the tenant relay policy**: whether the seat accepts purchases a Broker relays
  (:func:`relay_policy`), which before this control a test could set only by
  writing the database;
* **the tenant agent-activation policy**: whether an agent's first Register opens
  an active account or one held for the operator's review
  (:func:`agent_activation`);
* **a seat's subscription records**: the issuer root keys a subscription-gated
  term's delegation chain must be rooted in (:func:`subscription_record`). No
  admin RPC writes one yet; both states are production states, and every chain
  is still verified in full against what the record holds;
* **an account's active flag** in the Exchange's reference System of Record, the
  operator's out-of-band approval of a held account (:func:`set_account_active`).
  It reads and writes the store, not the Exchange's read-through cache, so a
  purchase can still see the old flag for up to the cache's lifetime, as after
  the operator's own update.

Production binaries are built without the tag and carry none of these routes
(``internal/guards/e2e_control_build_tag_guard_test.go``,
``harness/test_guards_e2e_images.py``).

The admin listener's address allowlist admits only the runner, so like
:mod:`harness.admin_plane` every call here needs the runner inside the compose
network; :func:`harness.admin_plane.require_admin_plane` skips anywhere else. The
routes are plain JSON over HTTP, not a protocol RPC, so no SDK verb serves them;
they are read here and nowhere else, and only the fields a test asserts on are
taken out.

The seat controls act on a seat other tests share, so each is a context manager
that puts the seat back when the block ends, however it ends. The e2e runner runs
one test at a time, so nothing else reads the seat while a block holds it.
"""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import AbstractContextManager, contextmanager
from dataclasses import dataclass
from http import HTTPStatus
from typing import Any

import httpx

from .admin_plane import admin_url
from .exchanges import seat_tenant_id

#: The control routes, as the Exchange's e2econtrol package mounts them.
_LIFETIMES_PATH = "/e2e/v1/tenants/{tenant}/lifetimes"
_SUBSCRIPTIONS_PATH = "/e2e/v1/tenants/{tenant}/subscriptions"

_TIMEOUT_SEC = 10.0


@dataclass(frozen=True)
class _Flag:
    """One boolean the control service reads and writes: its route and its JSON member.

    ``key`` is the route's placeholder naming the subject: a seat's tenant id, or
    an account's billing reference.
    """

    path: str
    key: str
    member: str


_RELAY_POLICY = _Flag("/e2e/v1/tenants/{tenant}/relay-policy", "tenant", "allow_broker_relay")
_AGENT_ACTIVATION = _Flag(
    "/e2e/v1/tenants/{tenant}/agent-activation", "tenant", "activate_new_agents_by_default"
)
_ACCOUNT_ACTIVE = _Flag("/e2e/v1/accounts/{billing_ref}/active", "billing_ref", "active")

#: How long a purchase or a GetAccountStatus may still see an account's old active
#: flag after :func:`set_account_active` changed the store: the Exchange caches the
#: flag for 30 seconds (EXCHANGE_SOR_CACHE_TTL is unset in this stack), plus margin.
ACCOUNT_FLAG_CACHE_S = 35.0


@dataclass(frozen=True)
class Lifetimes:
    """What a seat's offers and delivery URLs are issued with now, in seconds."""

    offer_ttl_seconds: int
    url_ttl_seconds: int


def _url(exchange_domain: str, path: str, key: str = "tenant", subject: str | None = None) -> str:
    """``path`` on ``exchange_domain``'s admin listener, with its ``key`` placeholder filled.

    A route on a seat names the Exchange's own seat unless ``subject`` says otherwise.
    """
    if subject is None:
        subject = seat_tenant_id(exchange_domain)
    return f"{admin_url(exchange_domain)}{path.format(**{key: subject})}"


def _call(method: str, url: str, body: dict[str, Any] | None = None) -> httpx.Response:
    return httpx.request(method, url, json=body, timeout=_TIMEOUT_SEC)


def _answered(response: httpx.Response) -> dict[str, Any]:
    assert response.status_code == HTTPStatus.OK, (
        f"the control service answered {response.request.method} {response.request.url} with "
        f"{response.status_code}: {response.text[:256]}"
    )
    body: dict[str, Any] = response.json()
    return body


def _lifetimes(body: dict[str, Any]) -> Lifetimes:
    return Lifetimes(
        offer_ttl_seconds=int(body["offer_ttl_seconds"]),
        url_ttl_seconds=int(body["url_ttl_seconds"]),
    )


def read_lifetimes(exchange_domain: str) -> Lifetimes:
    """What ``exchange_domain``'s seat issues offers and delivery URLs with now."""
    return _lifetimes(_answered(_call("GET", _url(exchange_domain, _LIFETIMES_PATH))))


def lifetimes_put_status(
    exchange_domain: str, body: dict[str, Any], *, tenant: str | None = None
) -> HTTPStatus:
    """The status a lifetimes PUT of ``body`` is answered with, for the refusal checks.

    ``tenant`` replaces the seat's tenant id in the path, for a path naming no seat.
    """
    url = _url(exchange_domain, _LIFETIMES_PATH, subject=tenant)
    return HTTPStatus(_call("PUT", url, body).status_code)


@contextmanager
def short_lifetimes(
    exchange_domain: str, *, offer_seconds: int = 0, url_seconds: int = 0
) -> Iterator[Lifetimes]:
    """Shorten ``exchange_domain``'s seat lifetimes for the block; yield what is now in force.

    Zero leaves that lifetime as configured. Only offers and URLs issued inside the
    block get the short lifetime; each carries its expiry signed into it. On the
    way out the overrides are cleared, however the block ends.
    """
    url = _url(exchange_domain, _LIFETIMES_PATH)
    body = {"offer_ttl_seconds": offer_seconds, "url_ttl_seconds": url_seconds}
    in_force = _lifetimes(_answered(_call("PUT", url, body)))
    try:
        yield in_force
    finally:
        _answered(_call("DELETE", url))


def _read_flag(flag: _Flag, exchange_domain: str, subject: str | None = None) -> bool:
    body = _answered(_call("GET", _url(exchange_domain, flag.path, flag.key, subject)))
    return bool(body[flag.member])


def _set_flag(flag: _Flag, exchange_domain: str, value: bool, subject: str | None = None) -> bool:
    """Store ``value``; return the value read back after the write."""
    url = _url(exchange_domain, flag.path, flag.key, subject)
    return bool(_answered(_call("PUT", url, {flag.member: value}))[flag.member])


def _flag_put_status(
    flag: _Flag, exchange_domain: str, body: dict[str, Any], subject: str | None = None
) -> HTTPStatus:
    url = _url(exchange_domain, flag.path, flag.key, subject)
    return HTTPStatus(_call("PUT", url, body).status_code)


@contextmanager
def _seat_flag_held(flag: _Flag, exchange_domain: str, value: bool) -> Iterator[bool]:
    """Set the seat's ``flag`` to ``value`` for the block; yield the value stored.

    The value the seat held before is read first and written back on the way out,
    however the block ends.
    """
    before = _read_flag(flag, exchange_domain)
    stored = _set_flag(flag, exchange_domain, value)
    try:
        yield stored
    finally:
        _set_flag(flag, exchange_domain, before)


def read_relay_policy(exchange_domain: str) -> bool:
    """Whether ``exchange_domain``'s seat accepts purchases a Broker relays."""
    return _read_flag(_RELAY_POLICY, exchange_domain)


def relay_policy_put_status(
    exchange_domain: str, body: dict[str, Any], *, tenant: str | None = None
) -> HTTPStatus:
    """The status a relay-policy PUT of ``body`` is answered with, for the refusal checks.

    ``tenant`` replaces the seat's tenant id in the path, for a path naming no seat.
    """
    return _flag_put_status(_RELAY_POLICY, exchange_domain, body, tenant)


def relay_policy(exchange_domain: str, allow: bool) -> AbstractContextManager[bool]:
    """Set ``exchange_domain``'s seat relay policy to ``allow`` for the block; yield it.

    A relayed purchase reads the policy when it executes.
    """
    return _seat_flag_held(_RELAY_POLICY, exchange_domain, allow)


def read_agent_activation(exchange_domain: str) -> bool:
    """Whether an agent's first Register at ``exchange_domain`` opens an active account."""
    return _read_flag(_AGENT_ACTIVATION, exchange_domain)


def agent_activation_put_status(
    exchange_domain: str, body: dict[str, Any], *, tenant: str | None = None
) -> HTTPStatus:
    """The status an agent-activation PUT of ``body`` is answered with, for the refusal checks.

    ``tenant`` replaces the seat's tenant id in the path, for a path naming no seat.
    """
    return _flag_put_status(_AGENT_ACTIVATION, exchange_domain, body, tenant)


def agent_activation(exchange_domain: str, activate: bool) -> AbstractContextManager[bool]:
    """Set whether ``exchange_domain``'s seat activates new agents, for the block; yield it.

    Register reads the policy when an agent's account opens, so only the accounts
    opened inside the block start the way it says.
    """
    return _seat_flag_held(_AGENT_ACTIVATION, exchange_domain, activate)


def set_agent_activation(exchange_domain: str, activate: bool) -> bool:
    """Store whether ``exchange_domain``'s seat activates new agents; return the value read back.

    For the seed, which holds every seat while it opens the credit-less buyer's
    accounts and then states the policy each seat's boot fixture declares. A test
    changes the policy for one block with :func:`agent_activation`, which puts
    back what the seat held before.
    """
    return _set_flag(_AGENT_ACTIVATION, exchange_domain, activate)


def read_account_active(exchange_domain: str, billing_ref: str) -> bool:
    """The active flag ``exchange_domain``'s System of Record stores for ``billing_ref``."""
    return _read_flag(_ACCOUNT_ACTIVE, exchange_domain, billing_ref)


def set_account_active(exchange_domain: str, billing_ref: str, active: bool) -> bool:
    """Store ``active`` as the account's flag, the operator's out-of-band switch.

    Returns the flag read back from the store after the write.
    """
    return _set_flag(_ACCOUNT_ACTIVE, exchange_domain, active, billing_ref)


def account_active_put_status(
    exchange_domain: str, billing_ref: str, body: dict[str, Any]
) -> HTTPStatus:
    """The status an account-active PUT of ``body`` is answered with, for the refusal checks."""
    return _flag_put_status(_ACCOUNT_ACTIVE, exchange_domain, body, billing_ref)


def subscription_record(
    exchange_domain: str, scope: str, issuer_identity: str, issuer_keys: list[str]
) -> str:
    """Make ``exchange_domain``'s seat trust ``issuer_keys`` for ``scope``; return the record id.

    ``issuer_keys`` are raw Ed25519 public keys in base64url. The seat's record for
    the scope is created, or its issuer identity and keys replaced when one exists,
    in one write; a test that withdraws a key calls this again with the list that
    no longer carries it, and a test that rotates the issuer passes the new identity.
    """
    body = _answered(
        _call(
            "PUT",
            _url(exchange_domain, _SUBSCRIPTIONS_PATH),
            {"scope": scope, "issuer_identity": issuer_identity, "issuer_keys": issuer_keys},
        )
    )
    assert (body["issuer_identity"], body["issuer_keys"]) == (issuer_identity, issuer_keys), (
        f"the record for {scope} stores {body['issuer_identity']!r} {body['issuer_keys']}, "
        f"want {issuer_identity!r} {issuer_keys}"
    )
    return str(body["record_id"])
