"""Obligations MCP-001 and MCP-009..012: the MCP endpoint's transport rules.

The contract is the identity service overview of the pinned protocol module
``github.com/FORA-Protocol/protocol``
(``website/src/content/docs/components/identity/overview.mdx``), its "Two-hop
authentication" and "MCP transport" sections:

* an unauthenticated request is refused before any outbound call is made;
* the endpoint accepts protocol versions ``2026-07-28``, ``2025-11-25``,
  ``2025-06-18``, ``2025-03-26`` and ``2024-11-05``, needs no ``initialize``,
  and refuses any other ``Mcp-Protocol-Version`` with ``400``;
* an authenticated ``GET`` or ``DELETE`` returns ``405`` with ``Allow: POST``,
  and a JSON-RPC request is answered with one ``application/json`` body;
* on ``2026-07-28`` a request carries ``Mcp-Method``, ``Mcp-Name`` on a tool
  call, and its version in ``_meta``, and one where any of these is missing or
  disagrees with the body is refused with ``400``;
* the endpoint issues no ``Mcp-Session-Id``.

THE SURFACE. These are rules about HTTP, which an MCP client hides, so they are
driven as raw JSON-RPC posts through ``mcp_transport.py``. A tool result read
there passes the same outputSchema check as one read through the MCP send seam.

EACH NEGATIVE IS A CHANGE TO A WORKING REQUEST. A refused request is the control
request with one thing changed (ADR-028 D5): the bearer, the version, or one
header. The refused request is a ``fora_register`` at exchange-a, which opens an
account if it runs at all, so "no tool ran and nothing was sent" is checked as an
account that is still not there, and as no request naming the agent in the
record of the stack's capture proxy, which sees every request the identity
service sends the Broker or an Exchange (``harness.capture_proxy``, ADR-028 D8
H12). The control request is sent afterwards, opens the account, and its
Register is in that record, which shows the refused one was refused for the
change and nothing else, and that the record sees this agent's calls.

THE IDENTITIES. Every test signs up an agent of its own, so an account another
test opened cannot hide a refusal that failed (ADR-028 D7).
"""

from __future__ import annotations

import re
from http import HTTPStatus
from urllib.parse import urlsplit

import pytest
from wire.models import RegisterRequest

from ..capture_proxy import CaptureProxy
from ..exchanges import EXCHANGE_A_DOMAIN
from ..mcp_agents import McpAgent, McpAgentFactory
from ..mcp_client import mcp_session
from ..mcp_session import forged_bearer
from ..mcp_transport import (
    LEGACY_VERSIONS,
    META_VERSION,
    METHOD_HEADER,
    NAME_HEADER,
    SESSION_HEADER,
    STATELESS_VERSION,
    SUPPORTED_VERSIONS,
    VERSION_HEADER,
    McpHttpRequest,
    bodyless,
    initialize,
    rpc,
    send,
    tool_call,
)
from ..obligation_registry import Obligation, obligation
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: exchange-a publishes no registration schema, so these fields are accepted and
#: the only thing that can refuse the registration is the change under test.
_EXCHANGE = EXCHANGE_A_DOMAIN
_REGISTRATION = {"exchange": _EXCHANGE, "fields": {"company_name": "Stoa Press"}}

#: A version the endpoint does not serve. It sorts after every real one, so an
#: implementation that admits "anything newer than it knows" accepts it.
_UNSUPPORTED = "2099-01-01"

#: Where the RFC 9728 challenge must point a client.
_METADATA_PATH = "/.well-known/oauth-protected-resource"

_INVENTED_SESSION = "e2e-invented-session-id"


async def _registered(stack: StackURLs, agent: McpAgent) -> bool:
    """Whether exchange-a itself reports an account for ``agent``, asked now."""
    async with mcp_session(stack, agent.bearer) as mcp:
        status = (await mcp.call("fora_status", {"exchange": _EXCHANGE})).structured
    (entry,) = status["accounts"]
    assert entry["source"] == "exchange", f"not exchange-a's own answer: {entry}"
    return bool(entry["registered"])


def _control_registers(stack: StackURLs, control: McpHttpRequest) -> None:
    """Send ``control`` and fail unless it is answered with a new account."""
    version = dict(control.headers)[VERSION_HEADER]
    contracts = send(stack.identity, rpc("tools/list", version=version, bearer=control.bearer))
    registered = send(stack.identity, control).tool_result(contracts.contracts()["fora_register"])
    assert registered["billing_ref"], f"the control registration opened no account: {registered}"


async def _assert_refused_then_control(
    stack: StackURLs,
    capture_proxy: CaptureProxy,
    agent: McpAgent,
    control: McpHttpRequest,
    refused: dict[str, McpHttpRequest],
    *,
    status: HTTPStatus,
) -> dict[str, str]:
    """Send each refused request, then the control, and check the account at each step.

    "Nothing was sent" is read two ways. exchange-a still reports no account after
    the refused requests. And the capture proxy recorded no request naming the
    agent while they ran: every request the identity service signs as the agent
    names the agent's host. The control's Register is then waited for in the same
    record, so the record is shown to see this agent's outbound calls.

    Returns the ``WWW-Authenticate`` challenge of each refused answer, by label.
    """
    traffic = capture_proxy.watch(agent.subdomain)
    challenges = {}
    for label, request in refused.items():
        answer = send(stack.identity, request)
        assert answer.status == status, (
            f"{label}: answered {answer.status}, want {status}: {answer.excerpt}"
        )
        challenges[label] = answer.www_authenticate
    sent_while_refused = traffic.requests()
    assert not await _registered(stack, agent), (
        f"a refused fora_register opened an account at {_EXCHANGE}: one of {sorted(refused)} ran"
    )
    _control_registers(stack, control)
    assert await _registered(stack, agent), "the control registration left no account"
    traffic.await_carrying(RegisterRequest, caller="identity", target=_EXCHANGE)
    assert not sent_while_refused, (
        f"the refused requests were followed by outbound calls: "
        f"{[(r.caller, r.target, r.path) for r in sent_while_refused]}"
    )
    return challenges


@obligation(Obligation.MCP_001, negative=True)
async def test_a_call_without_a_valid_bearer_is_refused_before_any_outbound_call(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seat must exist before Register.
    capture_proxy: CaptureProxy,
) -> None:
    """No bearer, or one signed by a key the service never held, gets 401 and runs nothing.

    The forged bearer carries the agent's own subdomain, issuer and audience; only
    its signature is wrong. Both answers carry the RFC 9728 challenge naming the
    protected-resource metadata. The same call with the agent's real bearer is
    answered and opens the account.
    """
    agent = fresh_mcp_agent_factory()
    control = tool_call(
        "fora_register", _REGISTRATION, version=LEGACY_VERSIONS[0], bearer=agent.bearer
    )

    challenges = await _assert_refused_then_control(
        identity_ready,
        capture_proxy,
        agent,
        control,
        {
            "no bearer": control.with_bearer(None),
            "forged bearer": control.with_bearer(forged_bearer(agent.subdomain)),
        },
        status=HTTPStatus.UNAUTHORIZED,
    )

    for label, challenge in challenges.items():
        pointer = re.search(r'resource_metadata="([^"]+)"', challenge)
        assert pointer, f"{label}: the 401 names no resource_metadata URL: {challenge!r}"
        assert urlsplit(pointer.group(1)).path == _METADATA_PATH, (
            f"{label}: the challenge points at {pointer.group(1)}, not the {_METADATA_PATH} document"
        )


@obligation(Obligation.MCP_009)
async def test_every_supported_version_is_served_with_or_without_a_handshake(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """tools/list and fora_status answer on each listed version, handshake or none.

    A legacy version is driven twice: once opening with ``initialize``, which must
    agree to the version asked for, and once with no handshake at all. The
    stateless version is driven with no handshake, and its ``server/discover``
    advertises all five versions. fora_status with no exchange makes no outbound
    call, so this is about the endpoint and nothing behind it.
    """
    agent = fresh_mcp_agent_factory()
    identity = identity_ready.identity

    for version in SUPPORTED_VERSIONS:
        handshakes = (False,) if version == STATELESS_VERSION else (True, False)
        for handshake in handshakes:
            where = f"{version} {'after initialize' if handshake else 'with no handshake'}"
            if handshake:
                agreed = send(identity, initialize(version=version, bearer=agent.bearer)).result()
                assert agreed.get("protocolVersion") == version, f"{where}: {agreed}"
            contracts = send(identity, rpc("tools/list", version=version, bearer=agent.bearer))
            status = send(
                identity, tool_call("fora_status", {}, version=version, bearer=agent.bearer)
            )
            answered = status.tool_result(contracts.contracts()["fora_status"])
            assert answered["accounts"] == [], f"{where}: a new agent reports accounts: {answered}"

    discovered = send(
        identity, rpc("server/discover", version=STATELESS_VERSION, bearer=agent.bearer)
    )
    advertised = discovered.result().get("supportedVersions", [])
    assert set(SUPPORTED_VERSIONS) <= set(advertised), (
        f"server/discover advertises {advertised}, not every version the endpoint accepts"
    )


@obligation(Obligation.MCP_009, negative=True)
async def test_an_unsupported_protocol_version_is_refused_with_400(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seat must exist before Register.
    capture_proxy: CaptureProxy,
) -> None:
    """A tool call naming version 2099-01-01 is refused 400, and the tool does not run.

    Two working requests are changed to name the unsupported version: a legacy
    one, in its header, and a stateless one, in its header and its ``_meta``, so
    neither is refused for a missing routing header. The legacy request, unchanged,
    is answered and opens the account.
    """
    agent = fresh_mcp_agent_factory()
    legacy = tool_call(
        "fora_register", _REGISTRATION, version=LEGACY_VERSIONS[0], bearer=agent.bearer
    )
    stateless = tool_call(
        "fora_register", _REGISTRATION, version=STATELESS_VERSION, bearer=agent.bearer
    )

    await _assert_refused_then_control(
        identity_ready,
        capture_proxy,
        agent,
        legacy,
        {
            "legacy request at 2099-01-01": legacy.with_version(_UNSUPPORTED),
            "stateless request at 2099-01-01": stateless.with_version(_UNSUPPORTED),
        },
        status=HTTPStatus.BAD_REQUEST,
    )


@obligation(Obligation.MCP_010)
async def test_a_json_rpc_request_is_answered_with_one_json_body(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """An authenticated POST of tools/list is answered 200 with one application/json body."""
    agent = fresh_mcp_agent_factory()
    for version in (STATELESS_VERSION, LEGACY_VERSIONS[0]):
        answer = send(
            identity_ready.identity, rpc("tools/list", version=version, bearer=agent.bearer)
        )
        assert answer.contracts(), f"{version}: tools/list named no tools"


@obligation(Obligation.MCP_010, negative=True)
async def test_get_and_delete_are_refused_405_with_allow_post(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """An authenticated GET asking for an event stream, and a DELETE, get 405 and Allow: POST.

    The GET would have opened the server-to-client stream and the DELETE would have
    closed a session, which a stateless endpoint has neither of. The DELETE names a
    session id, so it is refused without one being looked up; the GET's answer is
    not an event stream. tools/list is answered afterwards, so the refusals were
    about the method and nothing else.
    """
    agent = fresh_mcp_agent_factory()
    identity = identity_ready.identity
    for label, request in {
        "GET": bodyless("GET", bearer=agent.bearer).with_header("Accept", "text/event-stream"),
        "DELETE": bodyless("DELETE", bearer=agent.bearer).with_header(
            SESSION_HEADER, _INVENTED_SESSION
        ),
    }.items():
        answer = send(identity, request)
        assert answer.status == HTTPStatus.METHOD_NOT_ALLOWED, (
            f"{label}: answered {answer.status}, want 405: {answer.excerpt}"
        )
        allowed = {method.strip().upper() for method in answer.allow.split(",") if method.strip()}
        assert allowed == {"POST"}, f"{label}: Allow is {answer.allow!r}, want POST"
        assert answer.media_type != "text/event-stream", f"{label} opened an event stream"

    after = send(identity, rpc("tools/list", version=LEGACY_VERSIONS[0], bearer=agent.bearer))
    assert after.contracts(), "tools/list named no tools after the refused methods"


@obligation(Obligation.MCP_011, negative=True)
async def test_stateless_headers_that_are_missing_or_disagree_are_refused_with_400(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seat must exist before Register.
    capture_proxy: CaptureProxy,
) -> None:
    """On 2026-07-28 a tool call whose routing headers or _meta version are wrong runs nothing.

    The control is a 2026-07-28 tools/call of fora_register carrying ``Mcp-Method``,
    ``Mcp-Name`` and the ``_meta`` version, all agreeing with the body. Each
    refused request changes one of them. The control, sent last, is answered and
    opens the account.
    """
    agent = fresh_mcp_agent_factory()
    control = tool_call(
        "fora_register", _REGISTRATION, version=STATELESS_VERSION, bearer=agent.bearer
    )

    await _assert_refused_then_control(
        identity_ready,
        capture_proxy,
        agent,
        control,
        {
            "Mcp-Name names another tool": control.with_header(NAME_HEADER, "fora_status"),
            "Mcp-Method names another method": control.with_header(METHOD_HEADER, "tools/list"),
            "Mcp-Method missing": control.without_header(METHOD_HEADER),
            "Mcp-Name missing": control.without_header(NAME_HEADER),
            "_meta version missing": control.without_meta(META_VERSION),
        },
        status=HTTPStatus.BAD_REQUEST,
    )


@obligation(Obligation.MCP_012)
async def test_no_answer_issues_a_session_id_and_an_invented_one_changes_nothing(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """initialize, tools/list and tools/call carry no Mcp-Session-Id; sending one changes nothing.

    Each request is sent twice, once as built and once naming a session id the
    endpoint never issued. Neither answer issues a session id, and the two answers
    agree: the same tool list, and the same accounts from fora_status.
    """
    agent = fresh_mcp_agent_factory()
    identity = identity_ready.identity

    for version in (LEGACY_VERSIONS[0], STATELESS_VERSION):
        requests = {
            "tools/list": rpc("tools/list", version=version, bearer=agent.bearer),
            "tools/call": tool_call("fora_status", {}, version=version, bearer=agent.bearer),
        }
        if version != STATELESS_VERSION:
            requests["initialize"] = initialize(version=version, bearer=agent.bearer)
        contracts = send(identity, requests["tools/list"]).contracts()
        for label, request in requests.items():
            plain = send(identity, request)
            invented = send(identity, request.with_header(SESSION_HEADER, _INVENTED_SESSION))
            for answer, how in ((plain, "as built"), (invented, "naming a session id")):
                assert answer.session_id is None, (
                    f"{version} {label} {how}: the answer issued Mcp-Session-Id {answer.session_id!r}"
                )
            if label == "tools/call":
                plain_accounts = plain.tool_result(contracts["fora_status"])["accounts"]
                invented_accounts = invented.tool_result(contracts["fora_status"])["accounts"]
                assert plain_accounts == invented_accounts, (
                    f"{version}: a session id changed fora_status from {plain_accounts} "
                    f"to {invented_accounts}"
                )
            else:
                assert plain.result() == invented.result(), (
                    f"{version} {label}: a session id changed the answer"
                )
