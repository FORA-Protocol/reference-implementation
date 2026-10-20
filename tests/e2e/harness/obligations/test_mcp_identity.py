"""Obligations MCP-002, -003, -005, -006, -014 and -017: the identity service's custody wiring.

The contract is the identity service overview of the pinned protocol module
``github.com/FORA-Protocol/protocol``
(``website/src/content/docs/components/identity/overview.mdx``):

* one developer, one subdomain, one key, so two MCP users are two distinct agents
  to the Broker and the Exchange;
* the agent signed for comes from the authenticated identity on the request, never
  from a field the caller filled in;
* an agent's subdomain serves its key directory as
  ``application/http-message-signatures-directory+json``,
  its card and its revocation list as ``application/json``, and a ``fora.json``
  carrying only ``ver``, ``role: ROLE_AGENT`` and ``domain``;
* ``fora_report`` takes no endpoint argument, and reaches the Exchange that sold;
* sign-in codes are PKCE-protected with ``S256``, authorization responses carry
  ``iss``, and ``authorization_code`` is the only grant type.

THE SURFACE. Every tool call goes through the MCP send seam (``mcp_client.py``),
a real MCP client speaking Streamable HTTP to the identity service's ``/mcp``
endpoint, as a custodial agent would (ADR-028 D2). The seam checks each tool
result's ``structuredContent`` against the tool's declared ``outputSchema`` before
returning it, so no assertion here reads an answer that failed its contract (D3).
The agent's documents are read
through the SDK's document readers; the sign-in and the reporting obligation are
read through the harness readers in ``identity_signup.py``, ``mcp_agents.py`` and
``admin_plane.py``.

THE IDENTITIES. Every test signs up agents of its own through
``fresh_mcp_agent_factory``, each a login the provider has never seen, so nothing
another test or run did as an agent reaches these assertions (ADR-028 D7).

AN ARGUMENT REFUSED BY THE INPUT SCHEMA. Each tool's input schema refuses members
it does not declare. A call with such a member is sent through the seam's
``refused(..., input_invalid=...)``, which checks that the schema the service
publishes in ``tools/list`` does refuse the arguments, and that the service
answers a tool error. The identity service sends a tool error as text alone, with
no structured content, so which check refused the call cannot be read from the
answer without reading its wording, which a test does not do (ADR-028 D4). What
proves that nothing was signed and sent is the side effect: each refused call is
one that would have left a trace at an Exchange, and the test checks the trace is
absent.
"""

from __future__ import annotations

import json
import uuid
from typing import Any
from urllib.parse import urlsplit

import pytest
from fora_sdk.resolvers import (
    MANIFEST_MEDIA_TYPE,
    WBA_DIRECTORY_MEDIA_TYPE,
    WBA_DIRECTORY_PATH,
)
from wire.models import GetAccountStatusRequest, Role

from ..admin_plane import ObligationEvidence, require_admin_plane, transaction_evidence
from ..capture_proxy import CaptureProxy
from ..exchanges import EXCHANGE_C_DOMAIN
from ..identity_signup import (
    CLIENT_REDIRECT,
    authorization_server_metadata,
    authorize_with,
    token_grant,
)
from ..mcp_agents import (
    McpAgentFactory,
    bound_agent_id,
    key_directory,
    revocation_list,
    role_marker,
    signature_agent_card,
    thumbprints,
)
from ..identity_requests import PRICED_URI
from ..mcp_client import McpSession, mcp_session
from ..mcp_helpers import first_discovered_offer
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: exchange-c sells the wooden door creak at 0.25 USD per access and accepts
#: purchases relayed through the Broker, which is how the identity service buys;
#: every sale mints a reporting obligation there. Its registration schema requires
#: these two members.
_SELLER = EXCHANGE_C_DOMAIN
_REGISTRATION_FIELDS = {"company_name": "Example Buyer", "billing_email": "billing@buyer.example"}

#: How long a test waits for an accepted report to show on the obligation.
_REPORT_WAIT_SECONDS = 15.0


async def _assert_input_refused(
    mcp: McpSession, tool: str, arguments: dict[str, Any], argument: str
) -> None:
    """Fail unless the input schema refuses ``argument`` and the service refuses the call."""
    await mcp.refused(tool, arguments, input_invalid=f"{argument!r} is not a declared member")


async def _buy(mcp: McpSession, offer: dict[str, Any]) -> dict[str, Any]:
    """License ``offer`` without fetching its content; return the delivered item."""
    executed = (
        await mcp.call("fora_execute", {"offers": [offer], "embed_content": False})
    ).structured
    items = executed["items"]
    assert len(items) == 1, f"one offer answered {len(items)} items: {executed}"
    item: dict[str, Any] = items[0]
    assert item.get("retrieval_endpoint"), f"the purchase delivered nothing: {item}"
    return item


async def _status_entry(mcp: McpSession, exchange: str) -> dict[str, Any]:
    """The one entry ``exchange`` itself answers fora_status with."""
    status = (await mcp.call("fora_status", {"exchange": exchange})).structured
    assert len(status["accounts"]) == 1, status
    entry: dict[str, Any] = status["accounts"][0]
    assert entry["source"] == "exchange", f"the entry is not the Exchange's own answer: {entry}"
    return entry


async def _register_at_seller(mcp: McpSession) -> None:
    registered = (
        await mcp.call("fora_register", {"exchange": _SELLER, "fields": _REGISTRATION_FIELDS})
    ).structured
    assert registered["billing_ref"], f"the registration opened no account: {registered}"


def _obligation_of(transaction_id: str) -> ObligationEvidence:
    """The reporting obligation the seller holds for ``transaction_id``."""
    held = transaction_evidence(_SELLER, transaction_id).obligation
    assert held is not None, f"{_SELLER} minted no reporting obligation for {transaction_id}"
    return held


@obligation(Obligation.MCP_002)
async def test_two_users_sign_as_two_distinct_agents(
    compose_stack: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
) -> None:
    """Two users buying the same free offer get URLs bound to their own keys.

    Each user's purchase is answered with a delivery URL whose ``agent_id`` is the
    thumbprint of a key in that user's own subdomain directory, and not of any key
    in the other's. The two calls are interleaved on two open MCP sessions, so a
    service that kept one signer per process, or picked it up from the previous
    call, would bind both URLs to the same key.
    """
    first, second = fresh_mcp_agent_factory(), fresh_mcp_agent_factory()
    uri = seeded.free.uri

    async with (
        mcp_session(compose_stack, first.bearer) as one,
        mcp_session(compose_stack, second.bearer) as two,
    ):
        offer_one = await first_discovered_offer(one, uri)
        offer_two = await first_discovered_offer(two, uri)
        bought_one = await _buy(one, offer_one)
        bought_two = await _buy(two, offer_two)

    bound_one = bound_agent_id(bought_one["retrieval_endpoint"])
    bound_two = bound_agent_id(bought_two["retrieval_endpoint"])
    keys_one = thumbprints(key_directory(first).message)
    keys_two = thumbprints(key_directory(second).message)
    assert bound_one in keys_one, f"{first.subdomain} bought with {bound_one}, not its {keys_one}"
    assert bound_two in keys_two, f"{second.subdomain} bought with {bound_two}, not its {keys_two}"
    assert bound_one != bound_two, f"two users were bound to one key: {bound_one}"
    assert not keys_one & keys_two, f"two users' directories share a key: {keys_one & keys_two}"


@obligation(Obligation.MCP_003, negative=True)
async def test_no_tool_argument_chooses_the_agent_signed_for(
    compose_stack: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seller's seat and catalog must exist before Register.
    capture_proxy: CaptureProxy,
) -> None:
    """Only the bearer says who is signed for; an argument naming another agent is refused.

    The caller holds an account at the seller and the other agent holds none, so
    the same fora_status call answers differently for each, by bearer alone. Then
    the caller sends fora_status and fora_execute with an extra argument naming
    the other agent, by subdomain and by directory URL. The published input schema
    refuses each of those arguments, and the service answers each call with a tool
    error. The refused purchase leaves the caller's balance where it was, and the
    other agent still has no account at the seller.

    No outbound request is made for a refused call: the stack's capture proxy
    recorded no request naming either agent while they ran. The status query sent
    afterwards is in the same record, so the record sees the caller's calls.
    """
    caller, other = fresh_mcp_agent_factory(), fresh_mcp_agent_factory()
    naming_other = {
        "subdomain": other.subdomain,
        "directory": f"{other.origin}{WBA_DIRECTORY_PATH}",
    }

    async with mcp_session(compose_stack, caller.bearer) as mcp:
        await _register_at_seller(mcp)
        own = await _status_entry(mcp, _SELLER)
        assert own["registered"] is True, f"the caller's own account is not reported: {own}"
        offer = await first_discovered_offer(mcp, PRICED_URI)
        as_caller = capture_proxy.watch(caller.subdomain)
        as_other = capture_proxy.watch(other.subdomain)
        for argument, value in naming_other.items():
            await _assert_input_refused(
                mcp, "fora_status", {"exchange": _SELLER, argument: value}, argument
            )
            await _assert_input_refused(
                mcp,
                "fora_execute",
                {"offers": [offer], "embed_content": False, argument: value},
                argument,
            )
        sent_while_refused = as_caller.requests() + as_other.requests()
        after = await _status_entry(mcp, _SELLER)
    as_caller.await_carrying(GetAccountStatusRequest, caller="identity", target=_SELLER)
    assert not sent_while_refused, (
        f"the refused calls were followed by outbound requests: "
        f"{[(r.caller, r.target, r.path) for r in sent_while_refused]}"
    )
    async with mcp_session(compose_stack, other.bearer) as mcp:
        others = await _status_entry(mcp, _SELLER)

    assert others["registered"] is False, f"the other agent was registered at the seller: {others}"
    assert after["balances"] == own["balances"], (
        f"a refused purchase moved the caller's balance from {own['balances']} to "
        f"{after['balances']}"
    )


@obligation(Obligation.MCP_005)
def test_agent_subdomain_serves_its_key_directory_card_and_revocation_list(
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """The new agent's subdomain serves its directory, card and revocation list.

    The SDK's directory reader accepts the directory, which it does only under
    ``application/http-message-signatures-directory+json`` and only when the body passes
    the strict WBAFile schema. The directory names a revocation list on the agent's own host, which
    answers as ``application/json``, and so does the Signature Agent Card.
    """
    agent = fresh_mcp_agent_factory()

    directory = key_directory(agent)
    assert directory.media_type == WBA_DIRECTORY_MEDIA_TYPE, directory.media_type
    keys = directory.message.keys or []
    assert keys, f"the directory of {agent.subdomain} publishes no key"
    for key in keys:
        assert (key.kty, key.crv) == ("OKP", "Ed25519"), f"not an Ed25519 key: {key}"

    revocations = revocation_list(directory.message)
    assert urlsplit(revocations.url).hostname == agent.subdomain, (
        f"the revocation list of {agent.subdomain} is served from {revocations.url}"
    )
    assert revocations.media_type == "application/json", revocations.media_type

    card = signature_agent_card(agent)
    assert card.status == 200, f"the card of {agent.subdomain} answered {card.status}"
    assert card.media_type == "application/json", card.media_type
    assert card.members, f"the card of {agent.subdomain} is not a JSON object with members"


@obligation(Obligation.MCP_006)
def test_agent_fora_json_is_a_role_marker_and_nothing_more(
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """The new agent's fora.json carries ver, role ROLE_AGENT and its subdomain, nothing else.

    Read through the SDK's manifest reader, which refuses a wrong media type, a
    version it does not recognise and a body failing the strict schema. The members
    are counted on the bytes as served, so an empty list or a key the schema
    happened to allow would still be seen.
    """
    agent = fresh_mcp_agent_factory()

    marker = role_marker(agent)
    assert marker.media_type == MANIFEST_MEDIA_TYPE, marker.media_type
    served = json.loads(marker.body)
    assert set(served) == {"ver", "role", "domain"}, f"the role marker carries {sorted(served)}"
    assert served["ver"], f"the role marker carries no version: {served}"
    assert marker.message.role is Role.ROLE_AGENT, served
    assert marker.message.domain == agent.subdomain, served


@obligation(Obligation.MCP_014, negative=True)
async def test_fora_report_takes_no_endpoint_and_reaches_the_seller(
    compose_stack: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,  # The seller's seat and catalog must exist before Register.
) -> None:
    """fora_report declares no endpoint, refuses one, and files the report the seller records.

    The tool's input schema names no endpoint or URL and admits nothing undeclared.
    After a paid purchase at exchange-c, the seller holds a pending reporting
    obligation. A report carrying an ``endpoint`` or a ``url`` argument, which the
    published input schema refuses, is answered with a tool error, and the
    obligation is still pending afterwards. The report
    without one is accepted, and the obligation at exchange-c is fulfilled.
    """
    require_admin_plane()
    agent = fresh_mcp_agent_factory()

    async with mcp_session(compose_stack, agent.bearer) as mcp:
        schema = mcp.contract("fora_report").input_schema
        await _register_at_seller(mcp)
        offer = await first_discovered_offer(mcp, PRICED_URI)
        assert offer.get("exchange") == _SELLER, f"the priced resource is sold elsewhere: {offer}"
        item = await _buy(mcp, offer)

    declared = set(schema.get("properties", {}))
    assert not declared & {"endpoint", "url"}, f"fora_report declares {sorted(declared)}"
    assert schema.get("additionalProperties") is False, f"fora_report admits more: {schema}"
    transaction_id = str(item["transaction_id"])
    assert _obligation_of(transaction_id).fulfilled_at is None, "a report was filed before any"

    report = {
        "exchange": offer["exchange"],
        "transaction_id": transaction_id,
        "idempotency_key": f"report-{uuid.uuid4().hex}",
        "function": ["ai-input"],
    }
    if item.get("billing_id"):
        report["billing_id"] = item["billing_id"]
    async with mcp_session(compose_stack, agent.bearer) as mcp:
        for argument in ("endpoint", "url"):
            arguments = {**report, argument: f"http://{_SELLER}/"}
            await _assert_input_refused(mcp, "fora_report", arguments, argument)
    assert _obligation_of(transaction_id).fulfilled_at is None, "a refused report was filed"

    async with mcp_session(compose_stack, agent.bearer) as mcp:
        filed = (await mcp.call("fora_report", report)).structured
    assert filed["report_id"], f"the report was accepted with no report_id: {filed}"
    poll_until(
        lambda: _obligation_of(transaction_id).fulfilled_at,
        _REPORT_WAIT_SECONDS,
        failure=lambda: f"{_SELLER} never recorded the report for {transaction_id}",
    )


@obligation(Obligation.MCP_017)
async def test_s256_sign_in_returns_a_code_with_iss_and_a_bearer_the_endpoint_accepts(
    compose_stack: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
) -> None:
    """The whole sign-in an MCP client runs: S256, a code with iss, a bearer that works.

    The metadata offers only the ``authorization_code`` grant and only ``S256``.
    The consent approval redirects to the client with the code, its state and the
    issuer the metadata names. The token endpoint exchanges the code and the PKCE
    verifier for a bearer, and fora_status answers that bearer for a new agent that
    holds no account anywhere.
    """
    metadata = authorization_server_metadata(compose_stack.identity)
    assert metadata["grant_types_supported"] == ["authorization_code"], metadata
    assert metadata["code_challenge_methods_supported"] == ["S256"], metadata
    assert metadata["authorization_response_iss_parameter_supported"] is True, metadata

    signed_in = fresh_mcp_agent_factory.sign_in()
    assert signed_in.response.get("iss") == metadata["issuer"], signed_in.response
    assert signed_in.response.get("state") == "e2e-state", signed_in.response

    async with mcp_session(compose_stack, signed_in.access_token) as mcp:
        status = (await mcp.call("fora_status", {})).structured
    assert status["accounts"] == [], f"a new agent reports accounts: {status}"


@obligation(Obligation.MCP_017, negative=True)
@pytest.mark.parametrize("method", ["plain", None], ids=["plain", "no_challenge"])
def test_authorize_issues_no_code_without_an_s256_challenge(
    identity_ready: StackURLs, method: str | None
) -> None:
    """An authorization request with plain PKCE or no challenge gets no code.

    The client and its redirect are registered, so the server answers through the
    redirect as RFC 6749 §4.1.2.1 has it: ``invalid_request``, the issuer, and no
    code.
    """
    issuer = authorization_server_metadata(identity_ready.identity)["issuer"]

    answer = authorize_with(identity_ready.identity, challenge_method=method)
    assert answer.status == 302, f"authorize answered {answer.status}: {answer.params}"
    assert answer.target == CLIENT_REDIRECT, f"refused through {answer.target!r}"
    assert answer.params.get("error") == "invalid_request", answer.params
    assert "code" not in answer.params, f"a code was issued: {answer.params}"
    assert answer.params.get("iss") == issuer, answer.params


@obligation(Obligation.MCP_017, negative=True)
def test_token_endpoint_refuses_client_credentials(identity_ready: StackURLs) -> None:
    """A client_credentials token request is refused 400 unsupported_grant_type, with no token."""
    answer = token_grant(identity_ready.identity, grant_type="client_credentials")
    assert answer.status == 400, f"the token endpoint answered {answer.status}: {answer.error}"
    assert answer.error == "unsupported_grant_type", answer.error
    assert not answer.access_token, "a token was issued for client_credentials"
