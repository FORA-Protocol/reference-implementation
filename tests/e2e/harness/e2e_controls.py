"""The Exchanges' e2e-only control service (ADR-028 D6), and the one client for it.

Each Exchange in this stack is built with the ``e2e`` Go build tag, which mounts a
control service on its admin listener. It sets state no admin RPC reaches:

* **per-tenant lifetimes**: how long the seat's offers and delivery URLs stay
  valid. A test shortens them (:func:`short_lifetimes`) and polls until what it
  bought or was offered has expired. The control can only shorten: the Exchange
  refuses an override that is not shorter than the configured lifetime, and
  ignores one that would be;
* **the tenant relay policy**: whether the seat accepts purchases a Broker relays
  (:func:`relay_policy`), which before this control a test could set only by
  writing the database.

Production binaries are built without the tag and carry neither route
(``internal/guards/e2e_control_build_tag_guard_test.go``,
``harness/test_guards_e2e_images.py``).

The admin listener's address allowlist admits only the runner, so like
:mod:`harness.admin_plane` every call here needs the runner inside the compose
network; :func:`harness.admin_plane.require_admin_plane` skips anywhere else. The
routes are plain JSON over HTTP, not a protocol RPC, so no SDK verb serves them;
they are read here and nowhere else, and only the fields a test asserts on are
taken out.

Both controls act on a seat other tests share, so each is a context manager that
puts the seat back when the block ends, however it ends. The e2e runner runs one
test at a time, so nothing else reads the seat while a block holds it.
"""

from __future__ import annotations

from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from http import HTTPStatus
from typing import Any

import httpx

from .admin_plane import admin_url
from .exchanges import seat_tenant_id

#: The control routes, as the Exchange's e2econtrol package mounts them.
_LIFETIMES_PATH = "/e2e/v1/tenants/{tenant}/lifetimes"
_RELAY_POLICY_PATH = "/e2e/v1/tenants/{tenant}/relay-policy"

_TIMEOUT_SEC = 10.0


@dataclass(frozen=True)
class Lifetimes:
    """What a seat's offers and delivery URLs are issued with now, in seconds."""

    offer_ttl_seconds: int
    url_ttl_seconds: int


def _url(exchange_domain: str, path: str, tenant: str | None = None) -> str:
    tenant = tenant if tenant is not None else seat_tenant_id(exchange_domain)
    return f"{admin_url(exchange_domain)}{path.format(tenant=tenant)}"


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


def lifetimes_put_status(exchange_domain: str, body: dict[str, Any]) -> HTTPStatus:
    """The status a lifetimes PUT of ``body`` is answered with, for the refusal checks."""
    return HTTPStatus(_call("PUT", _url(exchange_domain, _LIFETIMES_PATH), body).status_code)


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


def read_relay_policy(exchange_domain: str) -> bool:
    """Whether ``exchange_domain``'s seat accepts purchases a Broker relays."""
    body = _answered(_call("GET", _url(exchange_domain, _RELAY_POLICY_PATH)))
    return bool(body["allow_broker_relay"])


def set_relay_policy(exchange_domain: str, allow: bool) -> bool:
    """Store ``allow`` as the seat's relay policy; return the policy read back after the write."""
    url = _url(exchange_domain, _RELAY_POLICY_PATH)
    return bool(_answered(_call("PUT", url, {"allow_broker_relay": allow}))["allow_broker_relay"])


def relay_policy_put_status(
    exchange_domain: str, body: dict[str, Any], *, tenant: str | None = None
) -> HTTPStatus:
    """The status a relay-policy PUT of ``body`` is answered with, for the refusal checks.

    ``tenant`` replaces the seat's tenant id in the path, for a path naming no seat.
    """
    url = _url(exchange_domain, _RELAY_POLICY_PATH, tenant)
    return HTTPStatus(_call("PUT", url, body).status_code)


@contextmanager
def relay_policy(exchange_domain: str, allow: bool) -> Iterator[bool]:
    """Set ``exchange_domain``'s seat relay policy to ``allow`` for the block; yield it.

    The policy the seat held before is read first and written back on the way out,
    however the block ends. A relayed purchase reads the policy when it executes.
    """
    before = read_relay_policy(exchange_domain)
    stored = set_relay_policy(exchange_domain, allow)
    try:
        yield stored
    finally:
        set_relay_policy(exchange_domain, before)
