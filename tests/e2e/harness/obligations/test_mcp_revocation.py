"""Obligations MCP-004 and MCP-008: an operator revokes a custodial agent's key.

The contract is the identity service overview of the pinned protocol module
``github.com/FORA-Protocol/protocol``
(``website/src/content/docs/components/identity/overview.mdx``):

* "The signing key is resolved per request and never cached." So the call after a
  revocation never signs with the revoked key (MCP-004);
* revocation "publishes the compromised key to the subdomain's revocation list and
  destroys the material." (MCP-008).

THE LEVER. Revocation is the operator's break-glass CLI, ``identity-operator``,
run inside the identity container the way an operator runs it
(``harness.identity_operator``); it is deliberately not an HTTP endpoint. The CLI
writes to the store the running service reads, so the service serves the new
revocation list once its cached copy expires, within its document cache lifetime.

THE SURFACE. Every tool call goes through a real MCP client speaking Streamable
HTTP to the identity service's ``/mcp`` endpoint, as a custodial agent would
(ADR-028 D2); the client checks each result's ``structuredContent`` against the
tool's declared ``outputSchema``. The agent's documents are read through the SDK's
readers (``harness.mcp_agents``). Each test signs up agents of its own.

NOT DRIVEN. MCP-004's positive half, that the first call after a rotation is bound
to the new key, and all of MCP-007, need the identity service to rotate one
agent's key on demand. That needs an e2e-only key-rotation control, which ADR-028
D6 does not list, so both stay untested until it does.

THE WIRE. MCP-004's "no captured outbound signature names it as keyid" is read off
the stack's capture proxy, which records every request the identity service sends
the Broker or an Exchange (``harness.capture_proxy``, ADR-028 D8 H12), scoped by
the agent's subdomain. The delivery URL a call returns is the other observable
binding, and both are asserted.
"""

from __future__ import annotations

import pytest
from wire.models import TransactionRequest

from ..capture_proxy import CaptureProxy
from ..identity_operator import document_ttl_seconds, require_operator_cli, revoke_agent_key
from ..mcp_client import McpSession, McpToolError, mcp_session
from ..mcp_agents import (
    McpAgent,
    McpAgentFactory,
    bound_agent_id,
    key_directory,
    revocation_list,
    thumbprints,
)
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: Beyond the document cache lifetime: the time a revocation list takes to rebuild
#: and be served once its cached copy has expired.
_PROPAGATION_MARGIN_S = 30.0


@pytest.fixture(autouse=True)
def _operator_cli() -> None:
    require_operator_cli()


def _only_key(agent: McpAgent) -> str:
    """The thumbprint of the one key a newly signed-up agent holds."""
    keys = thumbprints(key_directory(agent).message)
    assert len(keys) == 1, f"a new agent publishes {len(keys)} keys: {keys}"
    return next(iter(keys))


async def _execute(mcp: McpSession, uri: str) -> tuple[bool, list[str]]:
    """Discover ``uri`` and license its first offer without fetching it.

    Returns whether the tool call failed, and the ``agent_id`` of every delivery URL
    the call returned. A call that fails returns none.
    """
    try:
        discovered = await mcp.call("fora_discover", {"uris": [uri]})
    except McpToolError:
        return True, []
    groups = discovered.structured.get("offer_groups") or []
    if not groups or not groups[0].get("offers"):
        return True, []
    try:
        executed = await mcp.call(
            "fora_execute", {"offers": [groups[0]["offers"][0]], "embed_content": False}
        )
    except McpToolError:
        return True, []
    items = executed.structured.get("items") or []
    return False, [
        bound_agent_id(item["retrieval_endpoint"])
        for item in items
        if item.get("retrieval_endpoint")
    ]


@obligation(Obligation.MCP_004)
async def test_the_call_after_a_revocation_never_signs_with_the_revoked_key(
    compose_stack: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
    capture_proxy: CaptureProxy,
) -> None:
    """Before the revocation the agent's calls are signed by its key; after it, never.

    The control is a purchase of a free offer, bound to the agent's one key, whose
    outbound requests the capture proxy recorded signed with that key's keyid. The
    operator revokes that key. The next purchase, on the same open MCP session, is
    made at once, before any cache could expire: it either fails or returns a
    delivery URL bound to some other key, never one bound to the revoked
    thumbprint, and no request it sent carries a signature whose keyid is the
    revoked thumbprint.
    """
    agent = fresh_mcp_agent_factory()
    revoked = _only_key(agent)
    before = capture_proxy.watch(agent.subdomain)
    async with mcp_session(compose_stack, agent.bearer) as client:
        failed, bound = await _execute(client, seeded.free.uri)
        assert not failed and bound == [revoked], (
            f"control: the purchase before the revocation is not bound to {revoked}: {bound}"
        )
        signed = before.await_carrying(TransactionRequest, caller="identity")
        assert any(revoked in request.keyids for request in signed), (
            f"control: no recorded purchase is signed by {revoked}: "
            f"{[request.keyids for request in signed]}"
        )
        revoke_agent_key(agent.subdomain, revoked)
        after_revocation = capture_proxy.watch(agent.subdomain)
        _failed, after = await _execute(client, seeded.free.uri)

    assert revoked not in after, (
        f"a delivery URL was bound to the revoked key after revocation: {after}"
    )
    signed_after = [
        (request.target, request.path)
        for request in after_revocation.requests()
        if request.caller == "identity" and revoked in request.keyids
    ]
    assert not signed_after, f"requests sent after the revocation are signed by it: {signed_after}"


@obligation(Obligation.MCP_008)
async def test_a_revoked_key_is_listed_and_never_signs_again(
    compose_stack: StackURLs, fresh_mcp_agent_factory: McpAgentFactory, seeded: SeededFixture
) -> None:
    """Revoked: the agent's revocation list names the key within its cache lifetime.

    The control is the agent's revocation list before the revocation, which does
    not name the key, and a purchase bound to it. After the operator revokes the
    key, the list is read again until it names the thumbprint, for at most the
    service's document cache lifetime plus a margin. A purchase made after the
    revocation returns no delivery URL bound to the revoked thumbprint.
    """
    agent = fresh_mcp_agent_factory()
    revoked = _only_key(agent)
    directory = key_directory(agent).message
    listed_before = revocation_list(directory).message.revoked or []
    assert revoked not in listed_before, f"control: {revoked} was listed before its revocation"
    async with mcp_session(compose_stack, agent.bearer) as client:
        failed, bound = await _execute(client, seeded.free.uri)
        assert not failed and bound == [revoked], (
            f"control: the purchase is not bound to {revoked}: {bound}"
        )

        revoke_agent_key(agent.subdomain, revoked)
        _failed, after = await _execute(client, seeded.free.uri)
    assert revoked not in after, f"a delivery URL was bound to the revoked key: {after}"

    deadline = document_ttl_seconds() + _PROPAGATION_MARGIN_S
    seen: list[list[str]] = []

    def listed() -> bool:
        seen.append(list(revocation_list(directory).message.revoked or []))
        return revoked in seen[-1]

    poll_until(
        listed,
        deadline,
        failure=lambda: (
            f"the revocation list still did not name {revoked} after {deadline:.0f}s: {seen[-1]}"
        ),
        interval_s=5.0,
    )
