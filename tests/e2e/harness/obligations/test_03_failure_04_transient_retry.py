"""Obligation BROWSE-004: discovery is safe to retry.

``ExchangeService.DiscoverResources`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol``: discovery has no side effects, so it is safe to
retry. A transport failure, such as an unreachable Exchange or a timeout, is the
client's concern, and the protocol defines no transient-failure code for it. A retry
is therefore the same discovery sent again, and what the protocol promises about it
is that it gets the same answer and changes nothing.

Both tests run as the test's own agent, funded at exchange-d, against the archive's
lecture notes, which exchange-d sells. "The same answer" is the same offers in the
same order: for the same resource, at the same Exchange, with the same pricing,
delivery method, reporting obligation and term. An offer's id, expiry and signature
are minted per answer, so they are not compared. "Changes nothing" is the buyer's
balance, read through ``GetAccountStatus``, unchanged across every attempt, and the
control buys the offer the last attempt surfaced: the same reading drops by exactly
the item's cost, so an attempt that had held or charged money would have shown.

1. The same discovery sent three times, one after the other.
2. The same discovery retried after exchange-d's container is stopped and started
   again. While it is stopped, the attempt gets no answer at all, which the SDK
   reports as its own UNREACHABLE transport error; the protocol defines no code for
   it. Stopping a service needs the runner's Docker socket, so this test runs
   in-network only, like every other test that takes a party of the stack down.
"""

from __future__ import annotations

import os
from typing import Any

import pytest
from fora_sdk.client import CallError, CallErrorKind
from fora_sdk.core import VerifiedOffer

from .._compose import stopped_service
from ..exchanges import EXCHANGE_D_DOMAIN
from ..fresh_agent import FreshAgent
from ..money_path import LECTURE_NOTES, discover_offers
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..seed import wait_until_broker_routes
from .flow import buy_verified_and_observe_charge

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEAT = EXCHANGE_D_DOMAIN
#: The compose service that answers for exchange-d.
_SERVICE = "exchange-d"
#: How long exchange-d may take to answer again after its container starts.
_RESTART_S = 90.0
#: How many times the same discovery is sent.
_REPEATS = 3
#: The parts of an offer that state what is offered. The rest (offer_id, expires_at,
#: signature) is minted for each answer.
_OFFERED = ("exchange", "identity", "pricing", "delivery_method", "reporting", "terms")


def _answer(offers: list[VerifiedOffer]) -> list[dict[str, Any]]:
    """What a discovery answer offers, in its order, without the per-answer fields."""
    return [{part: offer.offer.get(part) for part in _OFFERED} for offer in offers]


@obligation(Obligation.BROWSE_004)
def test_the_same_discovery_repeated_gets_the_same_answer_and_moves_no_money(
    fresh_agent: FreshAgent,
) -> None:
    """Three identical discoveries: one answer, no money moved; the control purchase moves it."""
    fresh_agent.fund(_SEAT)
    before = fresh_agent.balances(_SEAT)

    answers = [discover_offers(fresh_agent, LECTURE_NOTES, _SEAT) for _ in range(_REPEATS)]

    first = _answer(answers[0])
    assert first, f"the discovery of {LECTURE_NOTES} returned no offer"
    for attempt, offers in enumerate(answers[1:], start=2):
        assert _answer(offers) == first, (
            f"attempt {attempt} of the same discovery offered {_answer(offers)}, "
            f"the first offered {first}"
        )
    after = fresh_agent.balances(_SEAT)
    assert after == before, (
        f"{_REPEATS} identical discoveries moved the buyer's money: {before} -> {after}"
    )
    buy_verified_and_observe_charge(fresh_agent, _SEAT, answers[-1][0], "control purchase")


@obligation(Obligation.BROWSE_004)
def test_a_discovery_retried_after_the_exchange_gave_no_answer_gets_the_same_answer(
    fresh_agent: FreshAgent,
) -> None:
    """No answer while exchange-d is down; retried once it answers: the same offer, no money moved."""
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip("stopping a service needs the runner's Docker socket (FORA_E2E_IN_NETWORK=1)")
    fresh_agent.fund(_SEAT)
    control = discover_offers(fresh_agent, LECTURE_NOTES, _SEAT)
    before = fresh_agent.balances(_SEAT)

    surfaced: list[list[VerifiedOffer]] = []
    last: list[CallError] = []

    def answered() -> bool:
        # While the container starts, the Exchange may not answer yet, or may answer
        # that it is not ready; either is a retry, never the outcome under test.
        try:
            surfaced.append(discover_offers(fresh_agent, LECTURE_NOTES, _SEAT))
        except CallError as unanswered:
            last.append(unanswered)
            return False
        return True

    try:
        with stopped_service(_SERVICE):
            with pytest.raises(CallError) as unreachable:
                discover_offers(fresh_agent, LECTURE_NOTES, _SEAT)
            # The SDK's own transport error: nothing answered, so no protocol code.
            assert unreachable.value.kind is CallErrorKind.UNREACHABLE, unreachable.value
    finally:
        poll_until(
            answered,
            _RESTART_S,
            failure=lambda: (
                f"exchange-d did not answer the retry within {_RESTART_S:.0f}s; last: {last[-1:]}"
            ),
        )
        # A Broker health probe that ran while exchange-d was down holds it
        # unroutable until the next pass; a Broker-routed test after this one
        # must find it routed again.
        wait_until_broker_routes(fresh_agent.stack, fresh_agent.broker)

    retried = surfaced[-1]
    assert _answer(retried) == _answer(control), (
        f"the retry offered {_answer(retried)}, the discovery before the outage "
        f"offered {_answer(control)}"
    )
    after = fresh_agent.balances(_SEAT)
    assert after == before, (
        f"the failed attempt and its retry moved the buyer's money: {before} -> {after}"
    )
    buy_verified_and_observe_charge(fresh_agent, _SEAT, retried[0], "control purchase")
