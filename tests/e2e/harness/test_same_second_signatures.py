"""E2E: identical signed requests sent in one burst are each accepted.

WHY IDENTICAL REQUESTS ARE NORMAL. An agent whose purchase timed out retries it
with the SAME ``idempotency_key``, and the Exchange answers with the original
result instead of charging again. The retry is byte-identical to the original:
same offer, same requester, same key, and so the same deterministic Ed25519
acceptance. Polling ``fora_status`` or repeating a discovery is identical too.

WHY THEY USED TO COLLIDE. Ed25519 signatures are deterministic, and the RFC 9421
``created``/``expires`` parameters have one-second resolution. Two identical
requests signed in the same second therefore carry the same signature, unless
the signer adds a fresh ``nonce`` parameter (RFC 9421 section 2.3). The Broker
and every Exchange keep a replay store and refuse the second request as a
replay. So the signer must put a fresh nonce on every signature; the
``created``/``expires`` window is the plain clock window and plays no part in
keeping signatures distinct.

WHAT THIS MODULE PROVES, leg by leg, through the agent's own surfaces:

- the identity service's SDK legs (``fora_status``, ``fora_discover``) accept a
  burst of identical concurrent MCP calls;
- the purchase leg (``fora_execute``) accepts a burst of identical concurrent
  retries, and every retry returns the original transaction;
- the replay store is still live: the exact same signed bytes, sent twice to the
  Broker's ExecuteTransaction, are refused the second time. Without this
  negative, the two positives would also pass on a Broker that had stopped
  checking replays.

Every MCP call goes through the MCP send seam (``mcp_client.py``), and each test
signs up an agent of its own (ADR-028 D7). The replay is the agent's own purchase
through its SDK Broker client, recorded as it was sent and sent again unchanged
through ``after_signing``; its refusal is read as the SDK's typed error.

WHAT THIS MODULE DOES NOT PROVE. The burst runs against the stack's wall clock,
and how many seconds it spans depends on the machine. A signer that only bumped
``expires`` would collide inside a fast burst and might not collide inside a
slow one, so the bursts alone cannot guarantee that such a signer is caught.
That guarantee is deterministic: the fixed-window test in
``internal/forahttpsig/nonce_test.go`` signs identical requests with the same
timestamps through both the local signer and the SDK transport, and checks that
the signatures differ and that each one is refused when sent again;
``test_httpsig_signer.py`` holds the harness's own signer to the same rule.
"""

from __future__ import annotations

import asyncio
import time
import uuid
from typing import Any

import pytest
from fora_sdk.client import CallError

from .after_signing import Capture, after_signing, resend
from .exchanges import EXCHANGE_A_DOMAIN
from .fresh_agent import FreshAgent
from .identity_requests import SIGNATURE_STALE, assert_auth_refusal, offer_for
from .mcp_agents import McpAgentFactory
from .mcp_client import McpSession, ToolResult, mcp_session
from .mcp_helpers import first_discovered_offer
from .money_path import assert_bought, only_item
from .seed import SeededFixture
from .stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

# Enough identical calls in flight at once that several are signed within the
# same wall-clock second on any machine. The count is not a collision proof;
# the module docstring says where that proof lives.
_BURST = 64

# What a replay refusal says, searched for only to count refusals in a failure
# message, never asserted on.
_REPLAY_TEXT = "replayed"


def _describe(failures: list[BaseException], elapsed: float) -> str:
    """A failure message that counts replay refusals separately from other errors."""
    replays = sum(1 for failure in failures if _REPLAY_TEXT in str(failure))
    return (
        f"{len(failures)} of {_BURST} identical calls failed in {elapsed:.1f}s, "
        f"{replays} of them refused as replays. First failures: "
        f"{[str(failure)[:200] for failure in failures[:3]]}"
    )


async def _burst(
    mcp: McpSession, tool: str, arguments: dict[str, Any]
) -> tuple[list[ToolResult], list[BaseException], float]:
    """Call ``tool`` with ``arguments`` ``_BURST`` times at once.

    Returns the results, the failures, and how long the burst took. A tool error
    is raised by the seam, so it is collected here as a failure.
    """
    started = time.monotonic()
    answers = await asyncio.gather(
        *(mcp.call(tool, arguments) for _ in range(_BURST)), return_exceptions=True
    )
    elapsed = time.monotonic() - started
    results = [answer for answer in answers if isinstance(answer, ToolResult)]
    failures = [answer for answer in answers if isinstance(answer, BaseException)]
    return results, failures, elapsed


@pytest.mark.parametrize("tool", ["fora_status", "fora_discover"])
async def test_identical_sdk_calls_in_one_burst_are_all_accepted(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
    tool: str,
) -> None:
    """A burst of identical status or discovery calls: every call succeeds.

    Both tools sign through the protocol SDK's transport, which adds a fresh
    nonce to each signature, so identical requests carry distinct signatures.
    """
    arguments: dict[str, Any] = (
        {"exchange": EXCHANGE_A_DOMAIN} if tool == "fora_status" else {"uris": [seeded.free.uri]}
    )
    agent = fresh_mcp_agent_factory()
    async with mcp_session(identity_ready, agent.bearer) as mcp:
        _, failures, elapsed = await _burst(mcp, tool, arguments)

    assert not failures, _describe(failures, elapsed)


async def test_identical_purchase_retries_in_one_burst_return_the_original(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
) -> None:
    """A burst of identical purchase retries: each returns the original transaction.

    The purchase is made once and allowed to complete, so every retry in the
    burst is a plain idempotent replay at the Exchange. A retry can then fail
    only on the way in, which is the leg under test. The free resource keeps the
    agent from needing an account, and ``embed_content`` false keeps the edge
    fetch out of the measurement.
    """
    idempotency_key = f"idem-burst-{uuid.uuid4().hex}"
    agent = fresh_mcp_agent_factory()
    async with mcp_session(identity_ready, agent.bearer) as mcp:
        offer = await first_discovered_offer(mcp, seeded.free.uri)
        arguments = {"offers": [offer], "idempotency_key": idempotency_key, "embed_content": False}
        original = await mcp.call("fora_execute", arguments)
        results, failures, elapsed = await _burst(mcp, "fora_execute", arguments)

    first = original.structured["items"][0]
    assert not first.get("denial_reason"), f"the original purchase was denied: {first}"
    transaction_id = first.get("transaction_id")
    assert transaction_id, f"the original purchase returned no transaction_id: {first}"

    assert not failures, _describe(failures, elapsed)
    answered = {result.structured["items"][0].get("transaction_id") for result in results}
    assert answered == {transaction_id}, (
        f"identical retries must all return the original transaction {transaction_id!r}, "
        f"got {sorted(str(t) for t in answered)}"
    )


def test_the_same_signed_bytes_sent_twice_are_refused_as_a_replay(
    fresh_agent: FreshAgent,
    seeded: SeededFixture,
) -> None:
    """One signed purchase, sent twice: bought once, then refused as a replay.

    This is the guarantee the bursts above must not weaken. Distinct signatures
    are accepted; the SAME signature is not. The agent buys the free resource
    through its own SDK Broker client, which records the request exactly as it
    went on the wire, and a second client sends those bytes again in place of
    the request it signed. The Broker owns the replay store on this route, and
    it refuses the second send as a stale signature.
    """
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    offer = offer_for(fresh_agent, seeded.free.uri)
    capture = Capture()
    bought = fresh_agent.broker_client(http=capture.client()).execute([offer])
    assert_bought(only_item(bought, "the first send"), "the first send")
    original = capture.last()

    replay = fresh_agent.broker_client(http=after_signing(resend(original)))
    with pytest.raises(CallError) as refused:
        replay.execute([offer])
    assert_auth_refusal(refused.value, SIGNATURE_STALE)
