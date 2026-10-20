"""Open the seeded buyers' accounts the way an agent does: a signed Register.

Each Exchange in the stack bills on its own TigerBeetle ledger, and a ledger
holds money only for an account that Register opened. Register opens the
account in the Exchange's System of Record, opens one ledger account per billed
currency, grants the Exchange's welcome credit in each currency when the
account opens active, and saves the account's billing reference on the agent's
row. Each buyer signs its own Register with its own key, through the SDK
client, at every Exchange it buys on. That first signed call is also how each
Exchange learns the buyer: it fetches the buyer's own Web Bot Auth directory and
records the identity it publishes (lazy registration, ADR-009 D2). Nothing here
writes an identity, an account, a balance or a billing reference, and nothing
here opens a database connection.

The welcome credit itself is Exchange configuration: each Exchange in compose
sets EXCHANGE_DEFAULT_AGENT_CREDIT and grants that map at an agent's first
Register on an active account. Nothing here writes or copies it.

Two kinds of buyer, two meanings:

* The FUNDED buyers (``agent-e2e`` and ``agent-demo-eur``) register at seats
  that activate new agents, so each holds the welcome credit in USD and in EUR
  at every Exchange.
* The CREDIT-LESS buyer (``agent-nobilling-e2e``) registers FIRST, while every
  seat still holds new agents for review. Its account opens inactive, and an
  inactive account is granted no welcome credit. The seed then approves the
  account in each Exchange's account registry — the update an operator runs to
  approve a held account, which no admin call performs yet, so it is one of the
  writes in ``operator_writes`` — and it is active and holds nothing. A paid
  purchase therefore reaches the billing gate and is refused for its balance,
  which is the obligation this buyer exists for. The welcome credit is granted
  only at the first registration, so approving the account later never
  funds it.

Register is idempotent: an agent whose row already carries a billing reference
gets the same account back and nothing changes. So a second seed on a stack that
was left up opens nothing new and grants nothing twice.

``lazy-agent-e2e`` is deliberately absent here. Its suite needs an agent that
was never registered.

A test's own agent (``fresh_agent.py``) opens its accounts through
:func:`open_active_account` too, after the seed has switched every seat to
activating new agents, so its Register grants it the welcome credit.
"""

from __future__ import annotations

from collections.abc import Mapping
from pathlib import Path
from typing import Any

from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from fora_sdk.sync import Client

from .e2e_catalog import (
    EUR_AGENT_ID,
    EUR_AGENT_KEY_PATH,
    NOBILLING_AGENT_ID,
    NOBILLING_AGENT_KEY_PATH,
    USD_AGENT_ID,
    USD_AGENT_KEY_PATH,
)
from .sdk_client import sdk_account_client

# The buyers whose accounts carry the welcome credit, as (agent id, key file).
FUNDED_BUYERS: tuple[tuple[str, Path], ...] = (
    (USD_AGENT_ID, USD_AGENT_KEY_PATH),
    (EUR_AGENT_ID, EUR_AGENT_KEY_PATH),
)
# The buyer whose account is opened held for review, then approved unfunded.
CREDIT_LESS_BUYER: tuple[str, Path] = (NOBILLING_AGENT_ID, NOBILLING_AGENT_KEY_PATH)

# The registration details every buyer sends. exchange-b and exchange-c publish a
# registration schema that requires these two members, and the SDK checks the
# payload against the schema each Exchange publishes before it signs, so a
# payload that stops matching fails here by name. exchange-a and exchange-d
# publish no schema and store the payload as given.
REGISTRATION_DATA: dict[str, str] = {
    "company_name": "Example Buyer",
    "billing_email": "billing@buyer.example",
}


class SeedRegistrationError(RuntimeError):
    """A buyer's Register did not leave it with the account the suites need."""


def registration_request(exchange: str) -> dict[str, Any]:
    """The Register body every buyer sends to ``exchange``: the shared registration details."""
    return {"ver": ProtocolVersion, "exchange": exchange, "registration_data": REGISTRATION_DATA}


def has_account(client: Client, exchange: str) -> bool:
    """Whether GetAccountStatus at ``exchange`` reports an account for the agent ``client`` signs as.

    An agent that holds no account at the Exchange is answered with the Connect code
    not_found (MONEY-038), so that refusal is the one that means "no account". Any
    other refusal does not answer the question, and raises.
    """
    try:
        status = client.get_account_status({"ver": ProtocolVersion, "exchange": exchange})
    except CallError as refused:
        if refused.code == "not_found":
            return False
        raise
    return bool(status.billing_ref)


def _register_with(client: Client, exchange: str, agent_id: str) -> tuple[str, bool]:
    """Register the agent ``client`` signs as at ``exchange``; return its billing reference and active flag.

    ``agent_id`` names the agent in the error, nothing more: the agent an
    Exchange opens the account for is the one the request's signature resolves to.
    """
    try:
        response = client.register(registration_request(exchange))
    except CallError as exc:
        msg = f"Register of {agent_id} at {exchange} was refused: {exc}"
        raise SeedRegistrationError(msg) from exc
    if not response.billing_ref:
        msg = f"Register of {agent_id} at {exchange} answered no billing_ref"
        raise SeedRegistrationError(msg)
    return response.billing_ref, bool(response.active)


def _register(base_url: str, exchange: str, *, agent_id: str, key_path: Path) -> tuple[str, bool]:
    """Register ``agent_id`` at ``exchange``; return its billing reference and active flag."""
    # Closed by hand rather than by ``with``: the SDK's __enter__ is typed as its
    # shared base class, which does not carry ``register``.
    client = sdk_account_client(base_url, agent_id=agent_id, key_path=key_path)
    try:
        return _register_with(client, exchange, agent_id)
    finally:
        client.close()


def open_active_account(client: Client, exchange: str, agent_id: str) -> str:
    """Register the agent ``client`` signs as at ``exchange``; return its billing reference.

    The account must come back active. An inactive account would be refused
    ACCOUNT_INACTIVE at every paid purchase, which the suites would report far
    from this cause, so it fails here instead. Every seat activates new agents
    once the seed has run, so an account opened after it holds the Exchange's
    welcome credit.
    """
    billing_ref, active = _register_with(client, exchange, agent_id)
    if not active:
        msg = (
            f"Register of {agent_id} at {exchange} answered billing_ref={billing_ref!r} "
            f"active=False; every funded buyer needs an active account"
        )
        raise SeedRegistrationError(msg)
    return billing_ref


def register_buyer(base_url: str, exchange: str, *, agent_id: str, key_path: Path) -> str:
    """Register the seeded buyer ``agent_id`` at ``exchange``; return its billing reference."""
    # Closed by hand rather than by ``with``, for the reason ``_register`` gives.
    client = sdk_account_client(base_url, agent_id=agent_id, key_path=key_path)
    try:
        return open_active_account(client, exchange, agent_id)
    finally:
        client.close()


def register_buyers(
    url_by_exchange: Mapping[str, str], buyers: tuple[tuple[str, Path], ...]
) -> None:
    """Register each buyer at each Exchange."""
    for exchange, base_url in url_by_exchange.items():
        for agent_id, key_path in buyers:
            register_buyer(base_url, exchange, agent_id=agent_id, key_path=key_path)


def register_credit_less_buyer(url_by_exchange: Mapping[str, str]) -> dict[str, str]:
    """Register the credit-less buyer at each Exchange; return its billing reference at each.

    Every seat must still hold new agents for review when this runs, so the first
    Register opens an inactive account and grants nothing; the seed then approves
    each account. On a stack left up the buyer is already registered and
    approved, and Register returns the same account.
    """
    agent_id, key_path = CREDIT_LESS_BUYER
    held: dict[str, str] = {}
    for exchange, base_url in url_by_exchange.items():
        billing_ref, _active = _register(base_url, exchange, agent_id=agent_id, key_path=key_path)
        held[exchange] = billing_ref
    return held
