"""The harness's one poller: ``poll_until(predicate, deadline_s)`` (ADR-028 D8 H6).

Every wait in the harness is a question asked again until it is answered or the
deadline passes. Written out by hand, each wait repeats the same loop with its own
deadline arithmetic and its own pause between attempts, and each copy can get it
wrong on its own: a pause after the last attempt, a deadline read from the wall
clock, a loop that never asks one final time.

This module is the only place in the harness that pauses. The scoped harness ruff
configuration (``tests/e2e/ruff-harness.toml``) refuses ``time.sleep`` and
``asyncio.sleep`` in every test module, so a test that has to wait calls this.
"""

from __future__ import annotations

import time
from collections.abc import Callable
from typing import TypeVar

T = TypeVar("T")

#: The pause between two attempts when the caller names none.
DEFAULT_INTERVAL_S = 1.0


def poll_until(
    predicate: Callable[[], T | None],
    deadline_s: float,
    *,
    failure: Callable[[], str],
    interval_s: float = DEFAULT_INTERVAL_S,
) -> T:
    """Call ``predicate`` until it returns a truthy value, and return that value.

    A predicate that answers "not yet" returns None (or another falsy value), so
    the value returned is never None.

    ``deadline_s`` is how long to keep asking, measured on the monotonic clock, so
    a wall-clock step cannot cut a wait short or stretch it. The predicate runs at
    least once, and an exception it raises ends the wait at once: a predicate
    asserts on an answer that is wrong rather than merely not ready yet.

    When the deadline passes, ``TimeoutError`` is raised with the message
    ``failure`` returns. It is a callable so the message can describe the last
    answer the predicate saw, which it records itself.
    """
    deadline = time.monotonic() + deadline_s
    while True:
        answer = predicate()
        if answer:
            return answer
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError(failure())
        time.sleep(min(interval_s, remaining))
