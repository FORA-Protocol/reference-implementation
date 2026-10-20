"""Obligation KEYS-013: a key's validity window does not move when its service restarts.

The protocol author's decision: a key's validity window is fixed for the key. The
key directory of ``fora.proto`` in the pinned module
``github.com/FORA-Protocol/protocol`` defines the window's bounds as instants:
``not_before`` is the "RFC3339 timestamp. Key is invalid before this instant." So
a service that restarts, or rebuilds its directory, and publishes the same key
again, publishes it with the same ``not_before`` and ``not_after``.

THE SURFACE. Each participant's directory is read through the SDK's
``read_wba_directory`` at the origin a verifier reads it from: one Exchange
(exchange-d), the Broker and the publisher console. The service is restarted by
stopping and starting its container (``_compose.stopped_service``), which keeps
its configuration and volumes, so a key it loads from configuration comes back
unchanged. The directory is read again once the service answers.

A restart disturbs every test running at that moment. The e2e runner runs one test
at a time, and each case waits, before it returns, until the restarted service is
serving its part of the stack again: exchange-d answering a Resolve through the
Broker, the Broker's fan-out verified by every Exchange, or the console's
directory served.
"""

from __future__ import annotations

import os
from collections.abc import Callable
from dataclasses import dataclass

import pytest
from fora_sdk.client import CallError
from fora_sdk.resolvers.errors import DirectoryUnavailableError
from wire.models import WBAFile

from .._compose import stopped_service
from ..defects import KEY_WINDOW_MOVES_ON_RESTART
from ..exchanges import EXCHANGE_D_DOMAIN, exchange_url
from ..fresh_agent import FreshAgent
from ..identity_requests import every_exchange_verifies_the_broker, offer_for
from ..money_path import LECTURE_NOTES
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..protocol_documents import wba_directory_at
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: How long a restarted service may take to serve its part of the stack again.
_RESTART_S = 120.0

#: A published key's window, by its public key ``x``.
Windows = dict[str, tuple[str | None, str | None]]


@dataclass(frozen=True)
class Participant:
    """A service whose directory the case reads, and how to tell it is serving again."""

    service: str
    origin: Callable[[StackURLs], str]
    serving: Callable[[FreshAgent], bool]


def _directory_served(origin: str) -> bool:
    try:
        wba_directory_at(origin)
    except DirectoryUnavailableError:
        return False
    return True


def _archive_resolves(agent: FreshAgent) -> bool:
    """Whether the Broker resolves exchange-d's archive item to a verified offer."""
    try:
        offer_for(agent, LECTURE_NOTES)
    except (CallError, AssertionError):
        return False
    return True


def _console_origin(stack: StackURLs) -> str:
    assert stack.console, "this stack runs no publisher console"
    return stack.console


_PARTICIPANTS = {
    "exchange": Participant(
        "exchange-d", lambda stack: exchange_url(stack, EXCHANGE_D_DOMAIN), _archive_resolves
    ),
    "broker": Participant("broker", lambda stack: stack.broker, every_exchange_verifies_the_broker),
    "console": Participant(
        "console", _console_origin, lambda agent: _directory_served(_console_origin(agent.stack))
    ),
}


def _windows(directory: WBAFile) -> Windows:
    return {str(key.x): (key.not_before, key.not_after) for key in directory.keys or []}


@obligation(Obligation.KEYS_013)
@KEY_WINDOW_MOVES_ON_RESTART.mark
@pytest.mark.parametrize("name", list(_PARTICIPANTS))
def test_a_keys_window_is_the_same_after_its_service_restarts(
    compose_stack: StackURLs, fresh_agent: FreshAgent, name: str
) -> None:
    """Every key published both before and after a restart carries the same window.

    At least one key must be published both times, or there is nothing to compare:
    each service here loads its signing key from its configuration, so a restart
    publishes the same key again.
    """
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip("restarting a service needs the runner's Docker socket (FORA_E2E_IN_NETWORK=1)")
    participant = _PARTICIPANTS[name]
    origin = participant.origin(compose_stack)
    before = _windows(wba_directory_at(origin).message)

    with stopped_service(participant.service):
        pass
    poll_until(
        lambda: _directory_served(origin) and participant.serving(fresh_agent),
        _RESTART_S,
        failure=lambda: (
            f"{participant.service} was not serving again {_RESTART_S:.0f}s after it restarted"
        ),
    )
    after = _windows(wba_directory_at(origin).message)

    kept = sorted(set(before) & set(after))
    assert kept, f"{participant.service} published no key both before and after its restart"
    moved = {x: (before[x], after[x]) for x in kept if before[x] != after[x]}
    KEY_WINDOW_MOVES_ON_RESTART.check(
        not moved, f"{participant.service} moved the window of {len(moved)} key(s): {moved}"
    )
