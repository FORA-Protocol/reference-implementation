"""E2E: drive the Go MCP endpoint over the MCP protocol, as an agent.

The replacement for the retired Python shim's e2e: the MCP send seam
(mcp_client.py), a real MCP client, speaks Streamable HTTP to the identity
service's /mcp endpoint and makes genuine tools/call requests. The agent is
provisioned through a REAL Zitadel sign-up (identity_signup.provision_agent), so
its signing key is custodied in identity's Vault and its WBA directory is hosted
on its subdomain — which the Broker and Exchange resolve (via the compose
wildcard-DNS) to verify the signatures identity makes on the agent's behalf.

Flow: fora_discover a FREE demo resource → the agent picks the offer →
fora_execute → the real bytes come back INSIDE the tool result, as an MCP
embedded resource. A free offer is used deliberately: a freshly-signed-up
registry agent has no billing account, and free access needs none.

THAT PREMISE IS MADE TRUE, NOT ASSUMED. The session agent every other MCP test
acts as (``mcp_bearer``) holds an account at exchange-a: the register test below
opens one, so does the multi-exchange account suite, and a registration survives
a stack left up. Nothing can turn it back into a fresh sign-up. The two flow tests
therefore act as a SECOND agent (``unregistered_bearer``) that no suite registers
anywhere, and each first reads ``fora_status`` and fails if that agent turns out
to hold an account — a suite that registers it would otherwise quietly turn "free
access needs no account" into a claim these tests no longer check.

The edges run with delivery-URL binding ENFORCED. A custodied agent holds no key
and never fetches: the identity service fetches on its behalf, presenting the key
it signed the offer acceptance with, which is the key the Exchange bound the URL
to (ADR-023). That the bytes arrive at all is therefore the proof that the whole
custody chain lines up — and the companion negative below shows the same URL is
worthless to anyone who cannot present that key.
"""

from __future__ import annotations

from http import HTTPStatus

import pytest

from .conftest import StackURLs
from .edge_fetch import edge_fetch_target, fetch_without_proof
from .exchanges import EXCHANGE_A_DOMAIN
from .mcp_client import McpSession, ToolResult, mcp_session
from .mcp_helpers import first_discovered_offer
from .obligation_registry import Obligation, obligation
from .seed import SeededFixture

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


# socrates lyric/article marker — the real delivered body, not a non-empty stub.
_CONTENT_MARKER = "Socrates"

# The discovery method the MCP projection reports. Kept file-local rather than
# shared with the Exchange-facing obligation suites: this one reads the value
# through internal/forareason.EnumPtrName in the MCP adapter, they read it off
# the Exchange wire through protojson, and one shared name would hide two
# separate contracts.
_EXCHANGE_METHOD = "DISCOVERY_METHOD_EXCHANGE"


def _assert_text_block_carries(executed: ToolResult, item: dict[str, object]) -> None:
    """Fail unless content[0] is the structured text block and carries ``item``.

    The seam reads ``structured`` from the structuredContent field, so it says
    nothing about the text block, which is what a client reading content[0].text
    relies on for the delivery URL.
    """
    assert executed.block_count, "the tool result has no content blocks"
    assert isinstance(executed.text_copy, dict), (
        f"content[0] is not a text block carrying a JSON object: {executed.text_copy!r}"
    )
    items = executed.text_copy["items"]
    assert items[0].get("retrieval_endpoint") == item.get("retrieval_endpoint"), items


async def _assert_no_account(mcp: McpSession, exchange: str) -> None:
    """Fail unless ``exchange`` reports this agent as unregistered.

    Asked of the exchange by name, so the answer is its own rather than the
    registry's local note of where it registered the agent.
    """
    status = (await mcp.call("fora_status", {"exchange": exchange})).structured
    entries = [e for e in status["accounts"] if e.get("exchange") == exchange]
    assert entries and entries[0]["registered"] is False, (
        f"the flow agent holds an account at {exchange}, so a free delivery here "
        f"no longer shows that free access needs none. Some test registered the "
        f"agent this module reserves as never-registered: {status}"
    )


async def test_account_register_status_and_repeat_for_named_exchange(
    compose_stack: StackURLs,
    mcp_bearer: str,
    seeded: SeededFixture,  # Default tenant must exist before Register.
) -> None:
    """Register and authoritative status agree for the explicitly named Exchange."""
    registration = {
        "exchange": EXCHANGE_A_DOMAIN,
        "fields": {
            "legal_entity": "Example Research",
            "address": "1 Example Street",
            "jurisdiction_country": "DE",
        },
    }
    async with mcp_session(compose_stack, mcp_bearer) as mcp:
        registered = (await mcp.call("fora_register", registration)).structured
        assert registered["exchange"] == EXCHANGE_A_DOMAIN, registered
        assert registered["billing_ref"], registered
        result = (await mcp.call("fora_status", {"exchange": EXCHANGE_A_DOMAIN})).structured
        assert len(result["accounts"]) == 1, result
        status = result["accounts"][0]
        assert status["exchange"] == EXCHANGE_A_DOMAIN, status
        assert status["source"] == "exchange", status
        assert status["registered"] is True, status
        assert status["billing_ref"] == registered["billing_ref"], status
        assert status["active"] == registered["active"], status
        repeated = (await mcp.call("fora_register", registration)).structured
        assert repeated["exchange"] == EXCHANGE_A_DOMAIN, repeated
        assert repeated["billing_ref"] == registered["billing_ref"], repeated


@obligation(Obligation.MCP_016)
async def test_agent_discovers_executes_and_receives_content_over_mcp(
    compose_stack: StackURLs,
    unregistered_bearer: str,
    seeded: SeededFixture,
) -> None:
    """Two-call MCP flow against the Go endpoint: discover → execute → real bytes.

    Nothing is fetched from outside the MCP session: that is the point of the
    change this test covers. The content arrives on the execute result, fetched by
    the identity service across an edge that enforces the key binding, so the
    service presented the custodied key as the proof.
    """
    res = seeded.free  # socrates: FREE EUR, no billing account required

    async with mcp_session(compose_stack, unregistered_bearer) as mcp:
        await _assert_no_account(mcp, EXCHANGE_A_DOMAIN)
        # Phase 1 — discovery. The agent asks for offers; nothing is charged and no
        # delivery URL is minted yet.
        disc = (await mcp.call("fora_discover", {"uris": [res.uri]})).structured
        groups = disc["offer_groups"]
        assert groups and groups[0]["offers"], f"no offers discovered for {res.uri!r}: {disc}"
        # How the URL was found reaches the agent through the MCP projection, not
        # only over the wire between the services. This test names the URL in the
        # tool call, so the Broker reports EXCHANGE — the agent asked for it
        # directly.
        assert groups[0].get("discovery_method") == _EXCHANGE_METHOD, (
            f"the discovery method must reach the agent; got {groups[0]}"
        )
        offer = groups[0]["offers"][0]
        assert offer["offer_id"], offer
        assert offer["signature"], offer

        # Phase 2 — execute the chosen offer. The endpoint signs the acceptance
        # with the agent's custodied key, relays through the Broker, then fetches
        # the content itself at the edge with that SAME key and returns both the
        # bytes and the signed delivery URL.
        executed = await mcp.call("fora_execute", {"offers": [offer]})
    result = executed.structured

    items = result["items"]
    assert len(items) == 1, result
    item = items[0]
    assert not item.get("denial_reason"), f"execute denied: {item}"
    # The item must answer for the offer the agent CHOSE, not a substitute. Without
    # this the adapter could relay a different offer from the group — or mismatch
    # results to submissions in a >1 batch — and every other assertion would pass.
    assert item.get("offer_id") == offer["offer_id"], (
        f"execute answered for offer {item.get('offer_id')!r}, "
        f"but the agent chose {offer['offer_id']!r}"
    )
    signed_url = item.get("retrieval_endpoint")
    assert signed_url, f"execute returned no retrieval_endpoint: {item}"
    _assert_text_block_carries(executed, item)
    assert not result.get("delivery_failures"), (
        f"the service could not fetch the content it licensed: {result['delivery_failures']}"
    )

    # The bytes came back through MCP, having crossed a binding-enforcing edge.
    delivered = executed.embedded
    assert len(delivered) == 1, f"want one embedded resource, got {len(delivered)}: {delivered}"
    body = delivered[0].body
    assert _CONTENT_MARKER in body.decode("utf-8", errors="replace"), (
        f"delivered body does not carry {_CONTENT_MARKER!r}: {body[:200]!r}"
    )
    # The media type the publisher actually served, with parameters stripped —
    # socrates is a .txt, so text/plain. Asserted concretely for the same reason
    # the body marker above is: application/octet-stream is what the service falls
    # back to when the edge sends no usable Content-Type, so a truthiness check
    # would pass on precisely the outcome worth catching.
    assert delivered[0].mime_type == "text/plain", (
        f"embedded resource media type is {delivered[0].mime_type!r}, want the served text/plain"
    )


@obligation(Obligation.DELIVERY_004, Obligation.MCP_016, negative=True)
async def test_a_leaked_delivery_url_is_useless_without_the_key(
    compose_stack: StackURLs,
    unregistered_bearer: str,
    seeded: SeededFixture,
) -> None:
    """The security property, driven end to end: holding the URL is not enough.

    Everything above would still pass if the edges quietly served bound URLs to
    anyone, so this is what shows the check is live rather than that the MCP path
    got lucky. The URL is a real one the service just used successfully; the only
    difference here is that the caller cannot prove possession of the key it names.
    """
    res = seeded.free

    async with mcp_session(compose_stack, unregistered_bearer) as mcp:
        await _assert_no_account(mcp, EXCHANGE_A_DOMAIN)
        discovered = (await mcp.call("fora_discover", {"uris": [res.uri]})).structured
        offer = discovered["offer_groups"][0]["offers"][0]
        executed = (await mcp.call("fora_execute", {"offers": [offer]})).structured

    signed_url = str(executed["items"][0]["retrieval_endpoint"])
    assert "agent_id=" in signed_url, f"the URL carries no binding to defeat: {signed_url}"
    # The service's OWN fetch of this URL succeeded. Without this the 403 below
    # would be just as consistent with an edge that refuses everyone, which would
    # make this test pass at the exact moment registry-side delivery broke.
    assert not executed.get("delivery_failures"), (
        f"the service could not fetch the URL it is about to prove is bound: "
        f"{executed['delivery_failures']}"
    )

    # A fetch with no proof of possession: the whole point here is the refusal.
    # The MCP session is closed by now, so this blocking read holds up no stream.
    url, _headers = edge_fetch_target(signed_url, compose_stack)
    leaked = fetch_without_proof(url)
    assert leaked.status == HTTPStatus.FORBIDDEN, (
        f"a bound URL was served to a caller with no key: {leaked.status} {leaked.text[:200]}"
    )
    assert leaked.reason == "missing_agent_key", f"refused for the wrong reason: {leaked.reason}"
    assert _CONTENT_MARKER not in leaked.text, "the refusal carried the publisher's content"


@pytest.mark.parametrize("embed", [True, False], ids=["embed_true", "embed_false"])
async def test_embed_content_chooses_whether_the_content_is_returned(
    compose_stack: StackURLs,
    unregistered_bearer: str,
    seeded: SeededFixture,
    embed: bool,
) -> None:
    """embed_content true returns the bytes; false returns only the delivery URL.

    The absent case is the flow test above, which sends no embed_content and
    receives the content. With false the purchase still happens: the item carries
    its delivery URL and transaction id, and no content block comes back.
    """
    async with mcp_session(compose_stack, unregistered_bearer) as mcp:
        await _assert_no_account(mcp, EXCHANGE_A_DOMAIN)
        offer = await first_discovered_offer(mcp, seeded.free.uri)
        executed = await mcp.call("fora_execute", {"offers": [offer], "embed_content": embed})

    result = executed.structured
    item = result["items"][0]
    assert not item.get("denial_reason"), f"execute denied: {item}"
    assert item.get("retrieval_endpoint"), f"no retrieval_endpoint: {item}"
    assert item.get("transaction_id"), f"no transaction_id: {item}"
    assert not result.get("delivery_failures"), result["delivery_failures"]
    _assert_text_block_carries(executed, item)

    if embed:
        assert len(executed.embedded) == 1, f"want one embedded resource, got {executed.embedded}"
        assert _CONTENT_MARKER in executed.embedded[0].body.decode("utf-8", errors="replace")
    else:
        assert executed.embedded == (), (
            f"embed_content false still returned content: {executed.embedded}"
        )
        assert executed.block_count == 1, (
            f"want only the structured text block, got {executed.block_count} blocks"
        )


@pytest.mark.parametrize("value", [None, "false", 0], ids=["null", "string", "number"])
async def test_embed_content_refuses_a_non_boolean(
    compose_stack: StackURLs,
    unregistered_bearer: str,
    seeded: SeededFixture,
    value: object,
) -> None:
    """A non-boolean embed_content is refused, and the published schema says why.

    The seam checks that fora_execute's input schema refuses the value before
    the call is sent, and the service answers it with a tool error. The service
    sends that error as text alone, so the test asserts the refusal and not which
    check made it.
    """
    async with mcp_session(compose_stack, unregistered_bearer) as mcp:
        offer = await first_discovered_offer(mcp, seeded.free.uri)
        await mcp.refused(
            "fora_execute",
            {"offers": [offer], "embed_content": value},
            input_invalid="embed_content is declared boolean",
        )
