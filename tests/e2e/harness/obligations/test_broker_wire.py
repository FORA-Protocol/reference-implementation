"""Obligations IDENTITY-009, PROFILE-010, PROFILE-012 and RELAY-018: what the Broker sends each Exchange.

The contract is ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol``, and one decision of its author:

* the Broker verifies the agent's request signature at Resolve and copies the
  verified agent's domain into every fan-out ``ResourceQuery``, under its own
  signature (IDENTITY-009, a DECISION entry);
* it forwards the agent's ``supported_profiles`` unchanged in every
  ``ResourceQuery`` it sends an Exchange, and an absent list as absent
  (PROFILE-010);
* it forwards the agent's ``acceptable_restrictions`` to the Exchanges in
  ``ResourceQuery.acceptable_restrictions`` (PROFILE-012);
* it forwards the agent's ``agent_request_acceptance`` unchanged on every
  sub-request it projects a purchase into (RELAY-018).

None of that is in any answer the agent reads, so each test reads the requests
the Broker sent, as the stack's capture proxy recorded them on their way to each
Exchange (``harness.capture_proxy``, ADR-028 D8 H12). Each request is read through
the generated protocol model of the message its bytes carry.

THE LEGS. The agent's call goes in through the Broker's public RPC, through the
test's own agent's SDK client (``fresh_agent``). The requests the Broker sent are
read off the capture proxy's record, scoped by an X-Request-ID the test chose,
which the Broker copies onto every request it sends for the call. A test sees no
other test's traffic. ``harness/test_capture_proxy.py`` proves the Broker's
requests to every Exchange run through the proxy, so an empty record cannot pass
for a forwarded field.
"""

from __future__ import annotations

import secrets
from typing import Any

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import DiscoveryResult
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import BrokerClient
from wire.models import AcceptableRestriction, ResourceQuery, TransactionRequest

from ..after_signing import Capture
from ..broker_purchase import resolve_request
from ..capture_proxy import CaptureProxy, Traffic
from ..defects import (
    BROKER_DROPS_SUPPORTED_PROFILES,
    BROKER_FAN_OUT_NAMES_UNVERIFIED_REQUESTER,
    BROKER_NARROWS_ACCEPTABLE_RESTRICTIONS,
)
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..identity_requests import PROBE_URI, ROUTED_URIS, answered_with_offer
from ..malformed import Alteration, Mutation, Request, SeamHTTP, malformed_outcome, patch_body
from ..money_path import SCI_FI_DOOR, THALES, assert_bought, resolve_offers
from ..obligation_registry import Obligation, obligation
from ..sdk_client import BeforeSign
from ..seed import SeededFixture

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The Broker's compose service: the sender of every request read here.
_BROKER = "broker"

#: A profile every Exchange of this stack declares in its manifest.
_COMP = "fora-comp-v1"

#: Two axes, one with two values, so a reordered or narrowed list shows.
_RESTRICTIONS = [
    {"axis": "RESTRICTION_KIND_FUNCTION", "values": ["ai-input"]},
    {"axis": "RESTRICTION_KIND_GEOGRAPHY", "values": ["US", "CA"]},
]


def _request_id() -> str:
    return f"broker-wire-{secrets.token_hex(8)}"


def _fan_out(traffic: Traffic) -> list[ResourceQuery]:
    """The query the Broker sent each of the three Exchanges ``ROUTED_URIS`` names."""
    sent = traffic.await_carrying(ResourceQuery, caller=_BROKER, targets=list(ROUTED_URIS))
    return [request.message_as(ResourceQuery) for request in sent]


def _resolve_everywhere(
    agent: FreshAgent, capture_proxy: CaptureProxy, **fields: Any
) -> list[ResourceQuery]:
    """Resolve one resource per Exchange as ``agent``; the queries the Broker fanned out."""
    request_id = _request_id()
    traffic = capture_proxy.watch(request_id)
    request = resolve_request(list(ROUTED_URIS.values()))
    request.update(fields)
    agent.broker_client(request_id=lambda: request_id).resolve(request)
    return _fan_out(traffic)


@obligation(Obligation.IDENTITY_009)
def test_each_fan_out_query_names_the_agent_the_broker_verified(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists every Exchange's catalog
    fresh_agent: FreshAgent,
    capture_proxy: CaptureProxy,
) -> None:
    """A Resolve signed as the agent: every query the Broker sends names the agent's host."""
    queries = _resolve_everywhere(fresh_agent, capture_proxy)
    named = [query.requester.domain if query.requester else None for query in queries]
    assert named == [fresh_agent.host] * len(queries), (
        f"the fan-out names {named}, want the agent's host {fresh_agent.host}"
    )


def _resolve_by(
    agent: FreshAgent, ids: list[str], scope: str
) -> Request[BrokerClient, DiscoveryResult]:
    """``agent``'s Resolve of the probe resource, each send under a request id of its own."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> BrokerClient:
        request_id = f"{scope}-{len(ids)}"
        ids.append(request_id)
        return agent.broker_client(
            signer=signer, before_sign=before_sign, http=http, request_id=lambda: request_id
        )

    return Request(client=client, verb=lambda c: c.resolve, message=resolve_request([PROBE_URI]))


def _set_requester_domain(domain: str) -> BeforeSign:
    def patch(body: dict[str, Any]) -> None:
        body["requester"]["domain"] = domain

    return patch_body(patch)


@obligation(Obligation.IDENTITY_009)
@BROKER_FAN_OUT_NAMES_UNVERIFIED_REQUESTER.mark
def test_the_fan_out_never_names_a_requester_the_broker_did_not_verify(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists exchange-a's catalog
    fresh_agent_factory: AgentFactory,
    capture_proxy: CaptureProxy,
) -> None:
    """Signed as the agent, naming another agent's host: answered, and the fan-out names the signer.

    The control is the agent's own Resolve. The altered Resolve is the same, signed
    by the agent over a body whose requester.domain names a second agent. The
    Broker refuses nothing for the domain; the query it sends exchange-a names the
    host of the Signature-Agent it verified, never the second agent.
    """
    agent, other = fresh_agent_factory(), fresh_agent_factory()
    scope = _request_id()
    # The control is sent first, under the first id; the altered Resolve under the second.
    control_traffic = capture_proxy.watch(f"{scope}-0")
    altered_traffic = capture_proxy.watch(f"{scope}-1")
    outcome = malformed_outcome(
        Mutation.IDENTITY,
        "requester.domain names another agent's directory",
        control=_resolve_by(agent, [], scope),
        alter=Alteration(before_sign=_set_requester_domain(other.host)),
        accepted=answered_with_offer(),
    )
    assert not isinstance(outcome, CallError), f"the Broker refused the Resolve: {outcome}"
    assert _named(control_traffic) == {agent.host}, "control: the fan-out does not name the agent"
    named = _named(altered_traffic)
    BROKER_FAN_OUT_NAMES_UNVERIFIED_REQUESTER.check(
        named == {agent.host},
        f"the fan-out names {sorted(named)}; the signer is {agent.host}, "
        f"the body named {other.host}",
    )


def _named(traffic: Traffic) -> set[str]:
    """The requester.domain of every query the Broker sent exchange-a in ``traffic``."""
    sent = traffic.await_carrying(ResourceQuery, caller=_BROKER, target=EXCHANGE_A_DOMAIN)
    queries = [request.message_as(ResourceQuery) for request in sent]
    return {query.requester.domain if query.requester else "" for query in queries}


@obligation(Obligation.PROFILE_010)
@pytest.mark.parametrize(
    "declared",
    [
        pytest.param([_COMP], id="a-declared-list", marks=BROKER_DROPS_SUPPORTED_PROFILES.marker),
        pytest.param(None, id="an-absent-list"),
    ],
)
def test_every_fan_out_query_carries_the_agents_supported_profiles(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists every Exchange's catalog
    fresh_agent: FreshAgent,
    capture_proxy: CaptureProxy,
    declared: list[str] | None,
) -> None:
    """The query to each Exchange carries the agent's list unchanged, and no list when it sent none."""
    fields = {} if declared is None else {"supported_profiles": declared}
    queries = _resolve_everywhere(fresh_agent, capture_proxy, **fields)
    forwarded = [query.supported_profiles or None for query in queries]
    if declared is None:
        assert forwarded == [None] * len(queries), f"an absent list arrived as {forwarded}"
        return
    BROKER_DROPS_SUPPORTED_PROFILES.check(
        forwarded == [declared] * len(queries),
        f"the agent declared {declared}; the Exchanges received {forwarded}",
    )


@obligation(Obligation.PROFILE_012)
@BROKER_NARROWS_ACCEPTABLE_RESTRICTIONS.mark
def test_every_fan_out_query_carries_the_agents_acceptable_restrictions(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists every Exchange's catalog
    fresh_agent: FreshAgent,
    capture_proxy: CaptureProxy,
) -> None:
    """FUNCTION and GEOGRAPHY in the Resolve: each query carries exactly those axes and values, in order."""
    queries = _resolve_everywhere(fresh_agent, capture_proxy, acceptable_restrictions=_RESTRICTIONS)
    want = [AcceptableRestriction.model_validate(r) for r in _RESTRICTIONS]
    forwarded = [query.acceptable_restrictions or [] for query in queries]
    BROKER_NARROWS_ACCEPTABLE_RESTRICTIONS.check(
        all(got == want for got in forwarded),
        f"the agent sent {_RESTRICTIONS}; the Exchanges received "
        f"{[[r.model_dump(mode='json', exclude_none=True) for r in got] for got in forwarded]}",
    )


@obligation(Obligation.RELAY_018)
def test_every_sub_request_carries_the_agents_request_acceptance_unchanged(
    fresh_agent: FreshAgent, capture_proxy: CaptureProxy
) -> None:
    """One purchase of two Exchanges' items: each sub-request carries the agent's acceptance as signed.

    The agent buys an item exchange-c sells and one exchange-a sells, in one
    purchase through the Broker. What the agent sent is read off its own client's
    transport; what the Broker sent each Exchange off the capture proxy. Both
    sub-requests carry the agent's AgentRequestAcceptance, its payload field for
    field and its signature unchanged, and each Exchange buys its item.
    """
    fresh_agent.fund(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN)
    offers = resolve_offers(fresh_agent, [SCI_FI_DOOR, THALES])
    request_id = _request_id()
    traffic = capture_proxy.watch(request_id)
    sent_by_agent = Capture()
    client = fresh_agent.broker_client(http=sent_by_agent.client(), request_id=lambda: request_id)

    answer = client.execute(offers)

    for item in answer.items or []:
        assert_bought(item, "a relayed item")
    agents = TransactionRequest.model_validate_json(sent_by_agent.last().content)
    assert agents.agent_request_acceptance is not None, "the agent sent no request acceptance"
    sub_requests = traffic.await_carrying(
        TransactionRequest, caller=_BROKER, targets=[EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN]
    )
    assert sorted(r.target for r in sub_requests) == sorted(
        [EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN]
    ), f"want one sub-request per Exchange, got {[r.target for r in sub_requests]}"
    for sub_request in sub_requests:
        forwarded = sub_request.message_as(TransactionRequest).agent_request_acceptance
        assert forwarded == agents.agent_request_acceptance, (
            f"the sub-request to {sub_request.target} carries {forwarded}, "
            f"the agent signed {agents.agent_request_acceptance}"
        )
