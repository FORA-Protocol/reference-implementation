"""Meta-guard — every e2e health check is paced: often while starting, rarely after.

Each health check is a process Docker starts inside the container, six of them a
node process. The stack used to run its 31 checks every two to five seconds for
the whole run, about 12.6 a second. Every check now takes its timing from one
anchor, ``x-health-timing`` in ``docker-compose.e2e.yml``: every 2 s while the
container starts, every minute once it is healthy. This guard keeps it that way:

* every health check in every compose file the stack loads sets the anchor's
  interval, timeout, retries, start_period and start_interval. A file that cannot
  read the anchor writes the values out, and is held to them too;
* the anchor paces: its interval is at least 30 s, its start_interval at most
  5 s, its timeout shorter than its interval, and once healthy the stack's checks
  together run fewer than one a second;
* a control: the guard reads at least as many checks as the base file defines,
  so a read that found none could not pass.

Pure file reading: no stack, no Docker, no database.
"""

from __future__ import annotations

import copy
import re
from collections.abc import Callable

import pytest
import yaml

from ._compose import COMPOSE_FILE, stack_files
from .guard_harness import guard_marks

pytestmark = guard_marks(
    skip_when=not COMPOSE_FILE.is_file(),
    reason="docker-compose.e2e.yml is absent from the e2e runner image; "
    "this guard runs on the host",
)

_ANCHOR = "x-health-timing"
_TIMING = ("interval", "timeout", "retries", "start_period", "start_interval")
#: The checks the base file defines. Fewer means the read missed some.
_MIN_CHECKS = 20
_DURATION = re.compile(r"(?P<count>\d+)(?P<unit>ms|s|m|h)")
_UNIT_SECONDS = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}


def _seconds(value: object) -> float:
    match = _DURATION.fullmatch(str(value))
    assert match, f"{value!r} is not a duration this guard reads"
    return int(match["count"]) * _UNIT_SECONDS[match["unit"]]


def _anchor() -> dict:
    anchor: dict = yaml.safe_load(COMPOSE_FILE.read_text())[_ANCHOR]
    return anchor


def _checks() -> dict[str, dict]:
    """Every service's health check, by service name, from every file the stack loads."""
    checks: dict[str, dict] = {}
    for path in stack_files():
        for name, service in (yaml.safe_load(path.read_text()).get("services") or {}).items():
            if isinstance(service, dict) and isinstance(service.get("healthcheck"), dict):
                checks[name] = service["healthcheck"]
    return checks


def _timing_problems(anchor: dict, checks: dict[str, dict]) -> list[str]:
    """Every way the anchor fails to pace the stack, and every check that ignores it."""
    interval, start_interval, timeout = (
        _seconds(anchor.get(key, "0s")) for key in ("interval", "start_interval", "timeout")
    )
    problems = []
    if interval < 30:
        problems.append(
            f"{_ANCHOR} checks a healthy container every {interval:g}s, not 30s or more"
        )
    if not 0 < start_interval <= 5:
        problems.append(
            f"{_ANCHOR} checks a starting container every {start_interval:g}s, not within 5s"
        )
    if timeout >= interval:
        problems.append(f"{_ANCHOR} times a check out after {timeout:g}s, not within its interval")
    if interval and len(checks) / interval >= 1:
        problems.append(
            f"{len(checks)} checks every {interval:g}s run {len(checks) / interval:.2f} a second "
            "once healthy, not fewer than one"
        )
    for name, check in sorted(checks.items()):
        own = [key for key in _TIMING if check.get(key) != anchor.get(key)]
        if own:
            problems.append(f"{name} sets its own {', '.join(own)}, not the shared timing")
    return problems


def test_every_health_check_is_paced_by_the_shared_timing() -> None:
    checks = _checks()
    assert len(checks) >= _MIN_CHECKS, (
        f"read {len(checks)} health checks, fewer than the {_MIN_CHECKS} the base file "
        "defines; the read is missing some, so the timing check below would pass on nothing"
    )
    assert _timing_problems(_anchor(), checks) == []


def _check_edit(service: str, key: str, value: str | None) -> Callable[[dict, dict], None]:
    """An edit that sets ``key`` in ``service``'s health check, or removes it."""

    def edit(anchor: dict, checks: dict) -> None:
        if value is None:
            del checks[service][key]
        else:
            checks[service][key] = value

    return edit


def _anchor_edit(key: str, value: str) -> Callable[[dict, dict], None]:
    """An edit to the shared anchor, which every check that merges it follows."""

    def edit(anchor: dict, checks: dict) -> None:
        old = anchor[key]
        anchor[key] = value
        for check in checks.values():
            if check.get(key) == old:
                check[key] = value

    return edit


@pytest.mark.parametrize(
    ("edit", "problems"),
    [
        pytest.param(
            _check_edit("redis", "start_interval", None),
            ["redis sets its own start_interval, not the shared timing"],
            id="start-interval-missing",
        ),
        pytest.param(
            _check_edit("postgres", "interval", "2s"),
            ["postgres sets its own interval, not the shared timing"],
            id="own-interval",
        ),
        pytest.param(
            _anchor_edit("interval", "25s"),
            [f"{_ANCHOR} checks a healthy container every 25s, not 30s or more"],
            id="anchor-checks-healthy-often",
        ),
        pytest.param(
            _anchor_edit("start_interval", "10s"),
            [f"{_ANCHOR} checks a starting container every 10s, not within 5s"],
            id="anchor-checks-starting-slowly",
        ),
        pytest.param(
            _anchor_edit("timeout", "60s"),
            [f"{_ANCHOR} times a check out after 60s, not within its interval"],
            id="anchor-timeout-too-long",
        ),
    ],
)
def test_the_timing_check_refuses_each_mistake(
    edit: Callable[[dict, dict], None], problems: list[str]
) -> None:
    """Each mistake the check exists to catch makes it report exactly that mistake."""
    anchor, checks = copy.deepcopy(_anchor()), copy.deepcopy(_checks())
    edit(anchor, checks)
    assert _timing_problems(anchor, checks) == problems


def test_the_timing_check_refuses_a_stack_checked_once_a_second() -> None:
    """As many checks as seconds in the interval make one check a second once healthy."""
    anchor = _anchor()
    count = int(_seconds(anchor["interval"]))
    checks = {f"service-{n}": dict(anchor) for n in range(count)}
    assert _timing_problems(anchor, checks) == [
        f"{count} checks every {count}s run 1.00 a second once healthy, not fewer than one"
    ]
