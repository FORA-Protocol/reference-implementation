"""Obligation MCP-019: an MCP tool error's structured content is ``{"refusal": UpstreamRefusal}``.

The pinned module ``github.com/FORA-Protocol/protocol`` specifies the shape. Its
AI-agent page: "When a call is refused, the tool's error result carries
`{"refusal": UpstreamRefusal}` as its structured content: the party that refused
(the Broker, an Exchange or the Identity Service), the Connect code it answered
with, and its FORA `ErrorDetail` unchanged." Its Identity Service page says what
each member holds:

- "`party` is the bare host of the party that refused: the Broker or the Exchange
  the tool called, or the Identity Service itself when it refused the tool call."
- "`code` is the Connect code that party answered with, in its wire form, or the
  code the call failed with when no answer came (`unavailable`,
  `deadline_exceeded`)."
- "`detail` is the FORA `ErrorDetail` the party attached, unchanged, for example
  `request_auth_failure`, `registration_failure` or `transaction_denial`. It is
  absent when the party attached none."

The test reads exactly that shape. The structured content is an object with the
one member ``refusal``, and that member passes the SDK's strict check as a
``fora.v1.UpstreamRefusal``, so an ErrorDetail placed at the top level, or a
refusal with a member the message does not have, is not read as one.

THE REFUSAL. ``fora_report`` reaches the Exchange its report names directly,
with no Broker between them, so the Exchange is the refusing party and its host
is the one the report names. A report for a transaction the Exchange never
issued is refused. The test files that report twice at exchange-a: once as an
SDK agent through the SDK send seam, which reads the Connect code and the
ErrorDetail the Exchange answers, and once as an MCP agent through
``fora_report``. Both agents are this test's own and hold no account, so the
Exchange has the same answer for each.

No refusal the Exchange types today reaches an MCP tool error. The identity
service checks a registration against the Exchange's published schema and terms
before it signs, so the Exchange's two typed registration refusals are not
reached; a purchase goes through the Broker, which reports an Exchange's refusal
inside its own successful answer; and a report refusal carries no typed reason
yet (``USAGE_REPORT_REFUSAL_UNTYPED``). So the test compares the two answers
rather than naming a reason: whatever code and ErrorDetail the Exchange answers,
the tool error must carry them. The message is left out of the comparison,
because message text is never asserted (ADR-028 D4).
"""

from __future__ import annotations

import uuid
from typing import Any

import pytest
from fora_sdk import StrictViolationError, check_strict
from fora_sdk.client import CallError
from wire.models import ErrorDetail, UpstreamRefusal

from .defects import MCP_TOOL_ERROR_DETAIL_DROPPED
from .exchanges import EXCHANGE_A_DOMAIN
from .fresh_agent import FreshAgent
from .mcp_agents import McpAgentFactory
from .mcp_client import mcp_session
from .obligation_registry import Obligation, obligation
from .reporting import report_body
from .stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _comparable(detail: ErrorDetail | None) -> dict[str, Any] | None:
    """The parts of ``detail`` a pass-through keeps: everything but the message."""
    if detail is None:
        return None
    return detail.model_dump(mode="json", exclude={"message"}, exclude_none=True)


def _refusal_in(structured: dict[str, Any] | None) -> UpstreamRefusal | None:
    """The tool error's structured content read as ``{"refusal": UpstreamRefusal}``, or None.

    None means the structured content is absent, holds any member other than
    ``refusal`` or more than that one, or holds a ``refusal`` the SDK's strict
    check refuses as a ``fora.v1.UpstreamRefusal``.
    """
    if structured is None or set(structured) != {"refusal"}:
        return None
    try:
        check_strict("fora.v1.UpstreamRefusal", structured["refusal"])
    except StrictViolationError:
        return None
    return UpstreamRefusal.model_validate(structured["refusal"])


@obligation(Obligation.MCP_019)
@MCP_TOOL_ERROR_DETAIL_DROPPED.mark
async def test_a_tool_error_carries_the_exchanges_refusal_unchanged(
    fresh_agent: FreshAgent, fresh_mcp_agent_factory: McpAgentFactory, identity_ready: StackURLs
) -> None:
    """fora_report for an unknown transaction: the tool error is exchange-a's UpstreamRefusal."""
    unknown = str(uuid.uuid4())
    with pytest.raises(CallError) as direct:
        fresh_agent.exchange(EXCHANGE_A_DOMAIN).report_usage(
            report_body(exchange=EXCHANGE_A_DOMAIN, transaction_id=unknown)
        )
    want = direct.value

    agent = fresh_mcp_agent_factory()
    async with mcp_session(identity_ready, agent.bearer) as mcp:
        tool_error = await mcp.refused(
            "fora_report",
            {
                "exchange": EXCHANGE_A_DOMAIN,
                "transaction_id": unknown,
                "idempotency_key": f"report-{uuid.uuid4().hex}",
                "function": ["ai_input"],
            },
        )

    got = _refusal_in(tool_error.structured)
    MCP_TOOL_ERROR_DETAIL_DROPPED.check(
        got is not None, f"the tool error's structured content is {tool_error.structured!r}"
    )
    assert got is not None
    assert got.party == EXCHANGE_A_DOMAIN, f"the refusal names {got.party!r}, not exchange-a"
    assert got.code == want.code, (
        f"the refusal carries {got.code!r}, exchange-a answered {want.code!r}"
    )
    assert _comparable(got.detail) == _comparable(want.detail), (
        f"the refusal carries {got.detail}, exchange-a answered {want.detail}"
    )
