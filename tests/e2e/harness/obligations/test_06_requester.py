"""Obligations REQUESTER-001 to -003: the requester a purchase names.

Surface: the Exchange's and the Broker's ExecuteTransaction RPCs through the SDK's
own clients. Each negative is a declared mutation (``harness.malformed``): the
control purchase is sent first and bought, then the SAME purchase is sent with
one requester field changed by a pre-signing alteration. The alteration also
re-signs every acceptance, with the agent's own key, over the bytes that name
the changed field (``harness.requester_alteration``). So the request signature
and every acceptance signature verify, and the changed requester is the only
thing wrong with what is sent.

* REQUESTER-001 and -002: an acceptance names a non-empty requester id and
  domain. The SDK refuses to sign one that does not, which is why the alteration
  exists. The pinned proto marks both ``Requester`` fields REQUIRED, so a
  validating receiver refuses the request as ``invalid_argument``, a generic
  validation failure with no typed reason.
* REQUESTER-003: on a direct request, ``requester.domain`` must be the host of
  the covered ``Signature-Agent``; a mismatch is ``unauthenticated`` with
  ``request_auth_failure`` SIGNATURE_INVALID. The other domain is a second agent
  minted for the test, so it names a published, resolvable directory.

Every negative reads the buyer's balance after the control and after the
altered request, and asserts it did not move. The legs are protocol round
trips: each purchase goes RPC to service to ledger, and each balance is read
back through GetAccountStatus.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import VerifiedOffer
from fora_sdk.errordetail import reason as typed_reason
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import BrokerClient, Client

from ..broker_purchase import (
    discover_group,
    one_group_per_uri,
    only_item,
    only_verified_offer,
    resolve_request,
)
from ..defects import DIRECT_REQUESTER_DOMAIN_UNBOUND
from ..e2e_catalog import DEMO_SFX_DOMAIN
from ..exchanges import EXCHANGE_C_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..identity_requests import SIGNATURE_INVALID, assert_auth_refusal
from ..ledger import Balances
from ..malformed import Alteration, Mutation, Request, SeamHTTP, malformed_outcome
from ..obligation_registry import Obligation, obligation
from ..requester_alteration import RequesterField, empty_requester, requester_named
from ..sdk_client import BeforeSign

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: Sold by exchange-c at 0.25 USD per access: priced, so a purchase moves money.
_URI = f"http://{DEMO_SFX_DOMAIN}/sfx/wooden-door-creak.json"

#: How each path builds the control purchase: (agent, offer) to a Request.
Purchase = Callable[[FreshAgent, VerifiedOffer], Request[Any, Any]]


def _direct(agent: FreshAgent, offer: VerifiedOffer) -> Request[Client, Any]:
    """The control: ``agent`` buys ``offer`` directly at exchange-c."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> Client:
        return agent.exchange_client(
            EXCHANGE_C_DOMAIN, signer=signer, before_sign=before_sign, http=http
        )

    return Request(client=client, verb=lambda c: c.execute, message=offer)


def _brokered(agent: FreshAgent, offer: VerifiedOffer) -> Request[BrokerClient, Any]:
    """The control: ``agent`` buys ``offer`` through BrokerService.ExecuteTransaction."""

    def client(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> BrokerClient:
        return agent.broker_client(signer=signer, before_sign=before_sign, http=http)

    return Request(client=client, verb=lambda c: c.execute, message=[offer])


def _direct_offer(agent: FreshAgent) -> VerifiedOffer:
    leg = f"discovery of {_URI} at exchange-c"
    return only_verified_offer(
        discover_group(agent.exchange(EXCHANGE_C_DOMAIN), EXCHANGE_C_DOMAIN, _URI, leg), leg
    )


def _brokered_offer(agent: FreshAgent) -> VerifiedOffer:
    result = agent.broker.resolve(resolve_request([_URI]))
    leg = f"broker resolve of {_URI}"
    return only_verified_offer(one_group_per_uri(result, {_URI}, leg)[_URI], leg)


_PATHS: dict[str, tuple[Callable[[FreshAgent], VerifiedOffer], Purchase]] = {
    "direct": (_direct_offer, _direct),
    "broker": (_brokered_offer, _brokered),
}


def _outcome_after_control(
    agent: FreshAgent,
    path: str,
    *,
    kind: Mutation,
    why: str,
    alteration: BeforeSign,
) -> object:
    """Buy once unaltered (charged), then send the altered purchase; its answer or refusal.

    Asserts the control was bought and charged, and that the altered purchase
    moved no balance at exchange-c.
    """
    agent.fund(EXCHANGE_C_DOMAIN)
    discover, purchase = _PATHS[path]
    offer = discover(agent)
    before: dict[str, Balances] = agent.balances(EXCHANGE_C_DOMAIN)
    after_control: dict[str, Balances] = {}

    def bought(response: Any) -> None:
        item = only_item(response, f"control {path} purchase")
        assert item.denial_reason is None, f"control: denied: {item}"
        assert item.retrieval_endpoint, f"control: no delivery URL: {item}"
        after_control.update(agent.balances(EXCHANGE_C_DOMAIN))
        assert after_control != before, "control: the purchase moved no money"

    outcome = malformed_outcome(
        kind,
        why,
        control=purchase(agent, offer),
        alter=Alteration(before_sign=alteration),
        accepted=bought,
    )
    if isinstance(outcome, CallError):
        assert agent.balances(EXCHANGE_C_DOMAIN) == after_control, "a refused purchase moved money"
    return outcome


_EMPTY_CASES = [
    pytest.param(path, field, id=f"{path}-empty-{field}", marks=obligation(proves, negative=True))
    for path, proves in (("direct", Obligation.REQUESTER_001), ("broker", Obligation.REQUESTER_002))
    for field in ("id", "domain")
]


@pytest.mark.parametrize(("path", "field"), _EMPTY_CASES)
def test_a_purchase_naming_an_empty_requester_is_refused(
    fresh_agent: FreshAgent, path: str, field: RequesterField
) -> None:
    """The acceptances name an empty requester field: refused, and nothing is charged."""
    refusal = _outcome_after_control(
        fresh_agent,
        path,
        kind=Mutation.MESSAGE,
        why=f"requester.{field} is empty, and every acceptance names it empty",
        alteration=empty_requester(field, fresh_agent.ed25519),
    )
    assert isinstance(refusal, CallError), f"the altered request was answered: {refusal!r}"
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
    assert refusal.detail is None or typed_reason(refusal.detail) is None, (
        f"a validation failure carries no typed reason: {refusal.detail}"
    )


@obligation(Obligation.REQUESTER_003, negative=True)
@DIRECT_REQUESTER_DOMAIN_UNBOUND.mark
def test_a_direct_purchase_naming_another_requester_domain_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """requester.domain names another agent's directory on a direct purchase: refused.

    The defect's one check is that the altered purchase was refused at all; a
    refused control or any other failure fails the test outright.
    """
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    outcome = _outcome_after_control(
        buyer,
        "direct",
        kind=Mutation.IDENTITY,
        why="requester.domain names another agent's directory, not the Signature-Agent host",
        alteration=requester_named("domain", other.host, buyer.ed25519),
    )
    DIRECT_REQUESTER_DOMAIN_UNBOUND.check(
        isinstance(outcome, CallError), f"requester.domain {other.host!r} was answered: {outcome!r}"
    )
    assert isinstance(outcome, CallError)
    assert_auth_refusal(outcome, SIGNATURE_INVALID)
