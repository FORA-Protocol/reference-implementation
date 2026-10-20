"""Obligations MCP-013, MCP-015 and MCP-018: what the fora_* tools do behind the endpoint.

The contract is the identity service overview of the pinned protocol module
``github.com/FORA-Protocol/protocol``
(``website/src/content/docs/components/identity/overview.mdx``), "The five
tools", and the authentication page beside it:

* registration reaches the named Exchange directly, because the Exchange binds
  the account to the agent's own inbound signature;
* offers are verified before the agent sees them, because an agent on this
  surface runs no verifier of its own;
* the SDK signing transports add an RFC 9421 ``nonce`` to every signature, so
  identical calls from one agent in the same second are not replays.

THE SURFACE. Every tool call goes through the MCP send seam (``mcp_client.py``),
which checks each result against the tool's outputSchema (ADR-028 D3). Every test
signs up agents of its own (ADR-028 D7). What the identity service sent an
Exchange on the agent's behalf is read off the stack's capture proxy
(``harness.capture_proxy``, ADR-028 D8 H12), scoped by the agent's subdomain.
"""

from __future__ import annotations

import asyncio
import uuid
from decimal import Decimal
from typing import Any

import pytest
from wire.models import GetAccountStatusRequest, RegisterRequest

from ..capture_proxy import BROKER_DOMAIN, CaptureProxy, Traffic
from ..e2e_catalog import DEMO_PHILOSOPHY_DOMAIN
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN
from ..mcp_agents import McpAgent, McpAgentFactory, key_directory, thumbprints
from ..mcp_client import McpSession, mcp_session
from ..mcp_helpers import first_discovered_offer
from ..obligation_registry import Obligation, obligation
from ..sdk_client import stack_offer_verifier
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: exchange-a publishes no registration schema and grants a welcome credit at an
#: agent's first registration, which is what the paid purchase below spends.
_HOME = EXCHANGE_A_DOMAIN
_HOME_FIELDS = {"company_name": "Stoa Press"}

#: Sold by exchange-a at a flat price in EUR.
_PAID_AT_HOME = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/aristotle.txt"

#: How many identical calls MCP-018 sends at once.
_CONCURRENT = 10


async def _home_entry(mcp: McpSession) -> dict[str, Any]:
    """The entry exchange-a itself answers fora_status with, for the session's agent."""
    status = (await mcp.call("fora_status", {"exchange": _HOME})).structured
    (entry,) = status["accounts"]
    assert entry["source"] == "exchange", f"not exchange-a's own answer: {entry}"
    return dict(entry)


def _eur(entry: dict[str, Any]) -> Decimal:
    amounts = [b["amount"] for b in entry["balances"] if b["currency"] == "EUR"]
    assert len(amounts) == 1, f"want one EUR balance, got {entry['balances']}"
    return Decimal(amounts[0])


@obligation(Obligation.MCP_013)
async def test_register_opens_the_account_the_agents_own_signature_is_charged_to(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seat and its catalog must exist before Register.
    capture_proxy: CaptureProxy,
) -> None:
    """fora_register reaches exchange-a directly, signed by the agent's own key, and binds the account.

    The Register the identity service sent is read off the stack's capture proxy:
    it went from the identity service to exchange-a itself, never through the
    Broker, it names exchange-a as its recipient, its Signature-Agent names the
    agent's own subdomain and its one signature is made by the key the agent's
    directory publishes.

    exchange-a then reports the account to the agent that registered, with the
    handle the registration returned, and reports none to a second agent signed up
    beside it. A paid purchase the agent then makes at exchange-a, whose acceptance
    the agent's own key signs, is charged to that account: the balance drops by the
    price.
    """
    agent, other = fresh_mcp_agent_factory(), fresh_mcp_agent_factory()
    traffic = capture_proxy.watch(agent.subdomain)

    async with mcp_session(identity_ready, agent.bearer) as mcp:
        registered = (
            await mcp.call("fora_register", {"exchange": _HOME, "fields": _HOME_FIELDS})
        ).structured
        _assert_registered_directly(traffic, agent)
        before = await _home_entry(mcp)
        offer = await first_discovered_offer(mcp, _PAID_AT_HOME)
        assert offer.get("exchange") == _HOME, f"the paid resource is sold elsewhere: {offer}"
        executed = (
            await mcp.call("fora_execute", {"offers": [offer], "embed_content": False})
        ).structured
        after = await _home_entry(mcp)
    async with mcp_session(identity_ready, other.bearer) as mcp:
        others = await _home_entry(mcp)

    assert before["registered"] is True, f"the registration is not reported: {before}"
    assert before["billing_ref"] == registered["billing_ref"], (registered, before)
    assert others["registered"] is False, f"a second agent sees the account: {others}"
    (item,) = executed["items"]
    assert not item.get("denial_reason"), f"the registered agent's purchase was denied: {item}"
    price = Decimal(item["cost"]["amount"])
    assert price > 0, f"the purchase was expected to be paid: {item}"
    assert _eur(before) - _eur(after) == price, (
        f"the account went from {_eur(before)} to {_eur(after)}; the purchase cost {price}"
    )


@obligation(Obligation.MCP_013, negative=True)
async def test_register_refuses_an_exchange_given_as_a_url(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seat must exist before Register.
    capture_proxy: CaptureProxy,
) -> None:
    """An exchange written as a URL is refused, sends no Register, opens no account, leaves no note.

    The fields are the ones exchange-c's schema requires, so the URL form is the
    only thing wrong with the call. Afterwards exchange-c reports no account for
    the agent and the agent's local note list names nothing. The stack's capture
    proxy recorded no Register naming the agent; it did record the status query
    that followed, so the record sees this agent's calls.
    """
    agent = fresh_mcp_agent_factory()
    fields = {"company_name": "Stoa Press", "billing_email": "billing@stoa.example"}
    traffic = capture_proxy.watch(agent.subdomain)

    async with mcp_session(identity_ready, agent.bearer) as mcp:
        await mcp.refused(
            "fora_register", {"exchange": f"https://{EXCHANGE_C_DOMAIN}/", "fields": fields}
        )
        hints = (await mcp.call("fora_status", {})).structured
        at_seller = (await mcp.call("fora_status", {"exchange": EXCHANGE_C_DOMAIN})).structured

    assert hints["accounts"] == [], f"a refused registration left a note: {hints}"
    (entry,) = at_seller["accounts"]
    assert entry["registered"] is False, f"the refused call opened an account: {entry}"
    traffic.await_carrying(GetAccountStatusRequest, caller="identity", target=EXCHANGE_C_DOMAIN)
    sent = traffic.carrying(RegisterRequest)
    assert not sent, f"the refused call sent a Register: {[(r.caller, r.target) for r in sent]}"


def _assert_registered_directly(traffic: Traffic, agent: McpAgent) -> None:
    """The one Register naming ``agent``: identity to exchange-a, signed by the agent's own key."""
    (register,) = traffic.await_carrying(RegisterRequest, target=_HOME)
    assert register.caller == "identity", f"the Register was sent by {register.caller!r}"
    assert not traffic.carrying(RegisterRequest, target=BROKER_DOMAIN), (
        "a Register naming the agent went to the Broker"
    )
    assert register.message_as(RegisterRequest).exchange == _HOME, register.message
    assert agent.subdomain in register.header("signature-agent"), (
        f"the Register's Signature-Agent {register.header('signature-agent')!r} does not "
        f"name the agent's subdomain {agent.subdomain}"
    )
    published = thumbprints(key_directory(agent).message)
    assert register.keyids and set(register.keyids) <= published, (
        f"the Register is signed by {register.keyids}; the agent publishes {sorted(published)}"
    )


@obligation(Obligation.MCP_015)
async def test_discover_returns_offers_the_exchange_signature_verifies(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
) -> None:
    """Every offer fora_discover hands the agent verifies under its Exchange's published key.

    The offers are checked again here, independently of the service, by the
    SDK's strict verifier over each Exchange's Web Bot Auth directory. None is
    rejected, and none is listed by the service as rejected either.
    """
    agent = fresh_mcp_agent_factory()
    # The verifier reads the directories with its own event loop, so it is built
    # off this one.
    verifier = await asyncio.to_thread(stack_offer_verifier)

    async with mcp_session(identity_ready, agent.bearer) as mcp:
        discovered = (
            await mcp.call("fora_discover", {"uris": [seeded.free.uri, _PAID_AT_HOME]})
        ).structured

    groups = discovered["offer_groups"]
    offers = [offer for group in groups for offer in group["offers"] or []]
    assert len(offers) >= len(groups) == 2, f"want offers for both resources: {discovered}"
    assert not [g["rejected"] for g in groups if g.get("rejected")], discovered
    checked = verifier.sort(offers)
    assert not checked.rejected, f"offers the agent was given do not verify: {checked.rejected}"
    assert len(checked.verified) == len(offers), checked


@obligation(Obligation.MCP_018)
async def test_identical_concurrent_calls_from_one_agent_all_succeed(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
) -> None:
    """Ten identical fora_status calls, and ten fora_execute calls on one free offer, all succeed.

    Sent at once from one agent, so the outbound requests share a second and a
    key: without a nonce in each signature, every one after the first would be
    refused as a replay. Each fora_status reaches exchange-a; each fora_execute
    reaches the Broker, the Exchange and the edge, with its own idempotency key.
    """
    agent = fresh_mcp_agent_factory()

    async with mcp_session(identity_ready, agent.bearer) as mcp:
        offer = await first_discovered_offer(mcp, seeded.free.uri)
        statuses = await asyncio.gather(
            *(mcp.call("fora_status", {"exchange": _HOME}) for _ in range(_CONCURRENT))
        )
        executions = await asyncio.gather(
            *(
                mcp.call(
                    "fora_execute",
                    {"offers": [offer], "idempotency_key": f"concurrent-{uuid.uuid4().hex}"},
                )
                for _ in range(_CONCURRENT)
            )
        )

    for status in statuses:
        (entry,) = status.structured["accounts"]
        assert entry["source"] == "exchange", entry
    transactions = set()
    for executed in executions:
        (item,) = executed.structured["items"]
        assert not item.get("denial_reason"), f"a concurrent purchase was denied: {item}"
        assert not executed.structured.get("delivery_failures"), executed.structured
        assert len(executed.embedded) == 1, f"a concurrent purchase delivered {executed.embedded}"
        transactions.add(item["transaction_id"])
    assert len(transactions) == _CONCURRENT, (
        f"ten purchases answered {len(transactions)} transactions"
    )
