"""The Exchanges' e2e-only control service (ADR-028 D6), driven against the running stack.

The obligation suites use the controls to make time pass and to set a seat's relay
policy; this module checks the controls themselves, through the one client
(``harness.e2e_controls``), on the admin listener of a running Exchange:

* a lifetime can be shortened and is put back, and a request that would make an
  offer or a delivery URL live longer than the configured five minutes is refused
  and changes nothing. A control that exercises an expiry check must never be able
  to weaken it. A lifetime for a tenant that names no seat is refused 404, and the
  Exchange's own seat keeps its lifetimes;
* the relay policy is stored and read back through the tenant repository, a write
  for a seat that does not exist is refused 404, and a malformed one 400, neither
  changing the seat;
* the agent-activation policy decides how an account opens: inside a block that
  holds new agents for review, a fresh agent's Register opens an inactive account,
  and after the block a fresh agent's opens active. A write for a seat that does
  not exist is refused 404 and a malformed one 400, neither changing the seat;
* an account's active flag is the operator's approval: a held account switched on
  through the control is active for the Exchange too, and holds no money, because
  an account that opens inactive is granted no welcome credit. A write for an
  account that does not exist is refused 404 and a malformed one 400, neither
  changing the account.

The seat policies and the account flag are read back through the same control
routes that write them, because no other surface reads them: the protocol has no
RPC for them, and the admin plane has no read of them. Where a policy has an
effect a public surface shows, that effect is asserted too, through the SDK's
Register and GetAccountStatus.
"""

from __future__ import annotations

import uuid
from http import HTTPStatus
from typing import Any

import pytest

from .admin_plane import require_admin_plane
from .e2e_controls import (
    ACCOUNT_FLAG_CACHE_S,
    account_active_put_status,
    agent_activation,
    agent_activation_put_status,
    lifetimes_put_status,
    read_account_active,
    read_agent_activation,
    read_lifetimes,
    read_relay_policy,
    relay_policy,
    relay_policy_put_status,
    set_account_active,
    short_lifetimes,
)
from .exchanges import EXCHANGE_C_DOMAIN, EXCHANGE_D_DOMAIN
from .fresh_agent import AgentFactory, FreshAgent
from .ledger import balances
from .poll import poll_until
from .seed_accounts import account_status_request, register_account

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The lifetime every Exchange in this stack issues offers and URLs with.
_CONFIGURED_S = 300

#: Bodies a boolean control refuses: no value, an unknown member, a value of the
#: wrong type.
_MALFORMED_FLAG_BODIES: list[dict[str, Any]] = [{}, {"enabled": True}, {"value": "yes"}]


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


def test_a_lifetime_for_no_seat_is_refused_and_changes_nothing() -> None:
    """No such tenant: 404, and the Exchange's own seat keeps its lifetimes."""
    before = read_lifetimes(EXCHANGE_D_DOMAIN)
    missing = lifetimes_put_status(
        EXCHANGE_D_DOMAIN, {"offer_ttl_seconds": 4}, tenant=str(uuid.uuid4())
    )
    assert missing == HTTPStatus.NOT_FOUND
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


def _register_at_d(agent: FreshAgent) -> tuple[str, bool]:
    """Register ``agent`` at exchange-d; return its billing reference and its active flag."""
    return register_account(agent.exchange(EXCHANGE_D_DOMAIN), EXCHANGE_D_DOMAIN, agent.host)


def test_an_account_opened_while_the_seat_holds_new_agents_is_inactive(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Inside the block a fresh agent's account opens held; after it, a fresh one opens active."""
    own = read_agent_activation(EXCHANGE_D_DOMAIN)
    assert own is True, "every seat activates new agents once the seed has run"
    with agent_activation(EXCHANGE_D_DOMAIN, False) as stored:
        assert stored is False
        assert read_agent_activation(EXCHANGE_D_DOMAIN) is False
        _held_ref, held_active = _register_at_d(fresh_agent_factory())
    assert held_active is False, "an account opened while the seat holds new agents is inactive"
    assert read_agent_activation(EXCHANGE_D_DOMAIN) is own
    _open_ref, open_active = _register_at_d(fresh_agent_factory())
    assert open_active is True, "an account opened after the block is active"


@pytest.mark.parametrize("body", _MALFORMED_FLAG_BODIES, ids=["empty", "unknown-member", "string"])
def test_an_activation_policy_without_a_boolean_value_is_refused(body: dict[str, object]) -> None:
    """A body that is not exactly {activate_new_agents_by_default: bool} is a 400."""
    own = read_agent_activation(EXCHANGE_D_DOMAIN)
    assert agent_activation_put_status(EXCHANGE_D_DOMAIN, body) == HTTPStatus.BAD_REQUEST
    assert read_agent_activation(EXCHANGE_D_DOMAIN) is own


def test_an_activation_policy_for_no_seat_is_refused() -> None:
    """No such tenant: 404, and the Exchange's own seat is unchanged."""
    own = read_agent_activation(EXCHANGE_D_DOMAIN)
    missing = agent_activation_put_status(
        EXCHANGE_D_DOMAIN, {"activate_new_agents_by_default": not own}, tenant=str(uuid.uuid4())
    )
    assert missing == HTTPStatus.NOT_FOUND
    assert read_agent_activation(EXCHANGE_D_DOMAIN) is own


def test_a_held_account_switched_on_is_active_and_holds_nothing(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The operator's approval turns a held account on, with no welcome credit in it."""
    agent = fresh_agent_factory()
    with agent_activation(EXCHANGE_D_DOMAIN, False):
        billing_ref, active = _register_at_d(agent)
    assert active is False
    assert read_account_active(EXCHANGE_D_DOMAIN, billing_ref) is False

    assert set_account_active(EXCHANGE_D_DOMAIN, billing_ref, True) is True
    assert read_account_active(EXCHANGE_D_DOMAIN, billing_ref) is True
    client = agent.exchange(EXCHANGE_D_DOMAIN)
    poll_until(
        lambda: client.get_account_status(account_status_request(EXCHANGE_D_DOMAIN)).active,
        ACCOUNT_FLAG_CACHE_S,
        failure=lambda: f"exchange-d still reports {billing_ref} inactive after the approval",
    )
    held = balances(client, EXCHANGE_D_DOMAIN)
    assert all(amount == 0 for amount in held.values()), held


def test_an_account_switch_for_no_account_or_without_a_boolean_value_is_refused(
    fresh_agent: FreshAgent,
) -> None:
    """No such account: 404. A body other than {active: bool}: 400. The account is unchanged."""
    billing_ref, active = _register_at_d(fresh_agent)
    assert active is True
    missing = account_active_put_status(EXCHANGE_D_DOMAIN, str(uuid.uuid4()), {"active": False})
    assert missing == HTTPStatus.NOT_FOUND
    for body in _MALFORMED_FLAG_BODIES:
        assert account_active_put_status(EXCHANGE_D_DOMAIN, billing_ref, body) == (
            HTTPStatus.BAD_REQUEST
        ), body
    assert read_account_active(EXCHANGE_D_DOMAIN, billing_ref) is True
