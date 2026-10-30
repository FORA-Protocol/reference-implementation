"""Open accounts the way an agent does: a signed Register.

Each Exchange in the stack bills on the stack's one TigerBeetle cluster, under
its own id namespace, and holds money only for an account that Register opened. Register opens the
account in the Exchange's System of Record, opens one ledger account per billed
currency, grants the Exchange's welcome credit in each currency when the
account opens active, and saves the account's billing reference on the agent's
row. Each agent signs its own Register with its own key, through the SDK
client, at every Exchange it buys on. That first signed call is also how each
Exchange learns the agent: it fetches the agent's own Web Bot Auth directory and
records the identity it publishes (lazy registration, ADR-009 D2). Nothing here
writes an identity, an account, a balance or a billing reference, and nothing
here opens a database connection.

The welcome credit itself is Exchange configuration: each Exchange in compose
sets EXCHANGE_DEFAULT_AGENT_CREDIT and grants that map at an agent's first
Register on an active account. Nothing here writes or copies it.

The seed sets up one buyer of its own, the CREDIT-LESS buyer
(``agent-nobilling-e2e``), in :func:`open_credit_less_accounts`. It registers
while every seat holds new agents for review, so its account opens inactive, and
an inactive account is granted no welcome credit. The seed then approves the
account at each Exchange, the operator's out-of-band approval of a held account,
and it is active and holds nothing. A paid purchase therefore reaches the billing
gate and is refused for its balance, which is the obligation this buyer exists
for. The welcome credit is granted only at the first registration, so approving
the account later never funds it. The hold and the approval go through each
Exchange's e2e-only controls (``e2e_controls``, ADR-028 D6), which only the runner
inside the compose network reaches.

The buyer itself, as a :class:`SeededBuyer` value, is in ``seed_agents``; the
seed hands it to :func:`open_credit_less_accounts`, and nothing else here names
it.

Register is idempotent: an agent whose row already carries a billing reference
gets the same account back and nothing changes. So a second seed on a stack that
was left up opens nothing new and grants nothing twice.

A test's own agent (``fresh_agent.py``) opens its accounts through
:func:`open_active_account`. Every seat activates new agents, as its boot fixture
declares, except while the seed holds it, so the agent's Register grants it the
welcome credit.
"""

from __future__ import annotations

from collections.abc import Iterator, Mapping
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

from fora_sdk.client import CallError
from fora_sdk.sync import Client
from wire.models import GetAccountStatusRequest, RegisterRequest

from .e2e_controls import ACCOUNT_FLAG_CACHE_S, set_account_active, set_agent_activation
from .poll import poll_until
from .sdk_client import sdk_account_client


@dataclass(frozen=True)
class SeededBuyer:
    """A buyer the seed registers before any test runs: its agent id and its keyfile."""

    agent_id: str
    key_path: Path


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


def registration_request(
    exchange: str, data: Mapping[str, str] = REGISTRATION_DATA
) -> RegisterRequest:
    """The Register an agent sends to ``exchange``, carrying ``data`` as its registration details.

    ``data`` defaults to the details every seeded buyer sends. The SDK stamps ``ver``,
    and, since the request names no ``terms_digest``, reads the Exchange's terms
    digest and checks ``data`` against the registration schema it publishes before
    it signs.
    """
    return RegisterRequest(exchange=exchange, registration_data=dict(data))


def account_status_request(exchange: str) -> GetAccountStatusRequest:
    """The GetAccountStatus for the calling agent's own account at ``exchange``."""
    return GetAccountStatusRequest(exchange=exchange)


def has_account(client: Client, exchange: str) -> bool:
    """Whether GetAccountStatus at ``exchange`` reports an account for the agent ``client`` signs as.

    An agent that holds no account at the Exchange is answered with the Connect code
    not_found (MONEY-038), so that refusal is the one that means "no account". Any
    other refusal does not answer the question, and raises.
    """
    try:
        status = client.get_account_status(account_status_request(exchange))
    except CallError as refused:
        if refused.code == "not_found":
            return False
        raise
    return bool(status.billing_ref)


def register_account(
    client: Client, exchange: str, agent_id: str, *, data: Mapping[str, str] = REGISTRATION_DATA
) -> tuple[str, bool]:
    """Register the agent ``client`` signs as at ``exchange``; return its billing reference and active flag.

    ``data`` is the registration details sent. ``agent_id`` names the agent in the
    error, nothing more: the agent an Exchange opens the account for is the one the
    request's signature resolves to.
    """
    try:
        response = client.register(registration_request(exchange, data))
    except CallError as exc:
        msg = f"Register of {agent_id} at {exchange} was refused: {exc}"
        raise SeedRegistrationError(msg) from exc
    if not response.billing_ref:
        msg = f"Register of {agent_id} at {exchange} answered no billing_ref"
        raise SeedRegistrationError(msg)
    return response.billing_ref, bool(response.active)


@contextmanager
def _signed_as(base_url: str, buyer: SeededBuyer) -> Iterator[Client]:
    """An SDK client at ``base_url`` that signs as ``buyer``, closed when the block ends."""
    # Closed by hand rather than by the client's own ``with``: the SDK's __enter__
    # is typed as its shared base class, which does not carry ``register``.
    client = sdk_account_client(base_url, agent_id=buyer.agent_id, key_path=buyer.key_path)
    try:
        yield client
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
    billing_ref, active = register_account(client, exchange, agent_id)
    if not active:
        msg = (
            f"Register of {agent_id} at {exchange} answered billing_ref={billing_ref!r} "
            f"active=False; a funded agent needs an active account"
        )
        raise SeedRegistrationError(msg)
    return billing_ref


def _register_held(url_by_exchange: Mapping[str, str], buyer: SeededBuyer) -> dict[str, str]:
    """Register the credit-less ``buyer`` at each Exchange; return its billing reference at each.

    Every seat must still hold new agents for review when this runs, so the first
    Register opens an inactive account and grants nothing; the seed then approves
    each account. On a stack left up the buyer is already registered and
    approved, and Register returns the same account.
    """
    held: dict[str, str] = {}
    for exchange, base_url in url_by_exchange.items():
        with _signed_as(base_url, buyer) as client:
            held[exchange], _active = register_account(client, exchange, buyer.agent_id)
    return held


#: Whether a seat activates new agents when the seed is not holding it: what every
#: selling seat's boot fixture declares, and what a test's own agent needs to be
#: granted the welcome credit at its Register. The boot-fixture guard holds every
#: seat's fixture to it.
SEATS_ACTIVATE_NEW_AGENTS = True


def open_credit_less_accounts(url_by_exchange: Mapping[str, str], buyer: SeededBuyer) -> None:
    """Open ``buyer``'s accounts held for review, approve each, and wait until each is active.

    Every seat holds new agents for review while the buyer registers, so each
    account opens inactive and is granted nothing. Each account is then approved,
    and the seed waits until its Exchange answers the buyer that the account is
    active: the Exchange caches the flag, so an account a Register on a stack left
    up already read as inactive shows the approval only when that entry expires.

    The seats are switched back to activating new agents however this ends, to
    the value the boot fixtures declare rather than to the value read before the
    hold. A seed stopped inside the hold leaves the seats holding, and a later seed
    would then read the hold as each seat's own policy. Nothing else would put the
    policy back: the Exchange never rewrites a stored seat from its fixture.
    """
    try:
        for exchange in url_by_exchange:
            if set_agent_activation(exchange, False):
                # Checked before the buyer registers: its first Register is the
                # only one that grants credit, so a hold that did not take would
                # fund it for the life of the stack.
                msg = f"{exchange} still activates new agents after the seed held its seat"
                raise SeedRegistrationError(msg)
        held = _register_held(url_by_exchange, buyer)
        for exchange, billing_ref in held.items():
            if not set_account_active(exchange, billing_ref, True):
                msg = f"{exchange} kept {buyer.agent_id}'s account {billing_ref} inactive"
                raise SeedRegistrationError(msg)
    finally:
        for exchange in url_by_exchange:
            set_agent_activation(exchange, SEATS_ACTIVATE_NEW_AGENTS)
    for exchange, base_url in url_by_exchange.items():
        with _signed_as(base_url, buyer) as client:
            poll_until(
                lambda: client.get_account_status(account_status_request(exchange)).active,
                ACCOUNT_FLAG_CACHE_S,
                failure=lambda: f"{exchange} still answers {buyer.agent_id}'s account inactive",
            )
