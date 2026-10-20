"""Waiting: the harness's one poller, and the readiness wait built on it.

Lives beside stack_urls.py rather than in conftest.py, for the same reason and
with the same consequence. conftest.py is the pytest plugin file that every
module in this package already imports, so anything defined there cannot be
imported BACK by a module conftest itself uses — and the poller is used by both.
That is what forced mcp_session.py's two imports inside fixture bodies, with a
comment naming the cycle as the reason. Defined here, both become ordinary
top-level imports.

A test that waits for something waits through :func:`poll_until` (ADR-028 D8,
H6). Nothing in the stack runs on a test clock, so behaviour that depends on
time is tested by shortening the window that governs it and polling until the
behaviour changes, never by sleeping for a guessed interval.
"""

from __future__ import annotations

import httpx

from .poll import poll_until


def wait_healthy(url: str, timeout_seconds: float = 90.0) -> None:
    """Poll ``url`` until it returns 2xx or timeout."""
    last_err: str | None = None

    def healthy() -> bool:
        nonlocal last_err
        try:
            resp = httpx.get(url, timeout=2.0)
        except (httpx.HTTPError, OSError) as exc:
            last_err = str(exc)
            return False
        if 200 <= resp.status_code < 300:
            return True
        last_err = f"{resp.status_code} {resp.text[:128]}"
        return False

    poll_until(
        healthy,
        timeout_seconds,
        failure=lambda: f"{url} not healthy after {timeout_seconds}s (last: {last_err})",
    )
