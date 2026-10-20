"""The Exchanges' e2e-only control service (ADR-028 D6), driven against the running stack.

The obligation suites use the controls to make time pass and to set a seat's relay
policy; this module checks the controls themselves, through the one client
(``harness.e2e_controls``), on the admin listener of a running Exchange:

* a lifetime can be shortened and is put back, and a request that would make an
  offer or a delivery URL live longer than the configured five minutes is refused
  and changes nothing. A control that exercises an expiry check must never be able
  to weaken it;
* the relay policy is stored and read back through the tenant repository, a write
  for a seat that does not exist is refused 404, and a malformed one 400, neither
  changing the seat.

The relay policy is read back through the same control route that writes it,
because no other surface reads it: the protocol has no relay-policy RPC, and the
admin plane has no read of it.
"""

from __future__ import annotations

import uuid
from http import HTTPStatus

import pytest

from .admin_plane import require_admin_plane
from .e2e_controls import (
    lifetimes_put_status,
    read_lifetimes,
    read_relay_policy,
    relay_policy,
    relay_policy_put_status,
    short_lifetimes,
)
from .exchanges import EXCHANGE_C_DOMAIN, EXCHANGE_D_DOMAIN

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The lifetime every Exchange in this stack issues offers and URLs with.
_CONFIGURED_S = 300


@pytest.fixture(autouse=True)
def _in_network() -> None:
    require_admin_plane()


def test_a_shortened_lifetime_is_in_force_inside_the_block_and_cleared_after() -> None:
    """Inside the block the seat issues with the short lifetimes; after it, the configured ones."""
    assert read_lifetimes(EXCHANGE_D_DOMAIN).offer_ttl_seconds == _CONFIGURED_S
    with short_lifetimes(EXCHANGE_D_DOMAIN, offer_seconds=4, url_seconds=6) as in_force:
        assert (in_force.offer_ttl_seconds, in_force.url_ttl_seconds) == (4, 6), in_force
        assert read_lifetimes(EXCHANGE_D_DOMAIN) == in_force
    after = read_lifetimes(EXCHANGE_D_DOMAIN)
    assert (after.offer_ttl_seconds, after.url_ttl_seconds) == (_CONFIGURED_S, _CONFIGURED_S), after


@pytest.mark.parametrize(
    "body",
    [
        {"offer_ttl_seconds": 3600},
        {"url_ttl_seconds": _CONFIGURED_S},
        {"offer_ttl_seconds": -1},
        {"offer_ttl_seconds": 4, "disable_expiry_check": True},
    ],
    ids=["longer-offer", "equal-url", "negative", "unknown-member"],
)
def test_a_lifetime_that_would_not_shorten_is_refused_and_changes_nothing(
    body: dict[str, object],
) -> None:
    """A lifetime as long as or longer than the configured one, or a malformed body, is a 400."""
    before = read_lifetimes(EXCHANGE_D_DOMAIN)
    assert lifetimes_put_status(EXCHANGE_D_DOMAIN, body) == HTTPStatus.BAD_REQUEST
    assert read_lifetimes(EXCHANGE_D_DOMAIN) == before


def test_the_relay_policy_is_stored_and_put_back() -> None:
    """The opposite policy is read back inside the block; the seat's own is read back after it."""
    own = read_relay_policy(EXCHANGE_C_DOMAIN)
    with relay_policy(EXCHANGE_C_DOMAIN, not own) as stored:
        assert stored is (not own)
        assert read_relay_policy(EXCHANGE_C_DOMAIN) is (not own)
    assert read_relay_policy(EXCHANGE_C_DOMAIN) is own


def test_a_relay_policy_for_no_seat_or_without_a_value_is_refused() -> None:
    """No such tenant: 404. A body without allow_broker_relay: 400. The seat is unchanged."""
    own = read_relay_policy(EXCHANGE_C_DOMAIN)
    missing = relay_policy_put_status(
        EXCHANGE_C_DOMAIN, {"allow_broker_relay": not own}, tenant=str(uuid.uuid4())
    )
    assert missing == HTTPStatus.NOT_FOUND
    assert relay_policy_put_status(EXCHANGE_C_DOMAIN, {}) == HTTPStatus.BAD_REQUEST
    assert read_relay_policy(EXCHANGE_C_DOMAIN) is own
