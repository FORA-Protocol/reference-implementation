"""Probe: claims that pass a string where an ``Obligation`` goes.

mypy must refuse each line marked ``# expect: <code>`` with exactly that error
code, and report nothing else. ``check.py`` beside this file reads the markers and
compares them with what mypy reports. This module is not in the main mypy scope,
because its errors are deliberate.
"""

from harness.obligation_registry import Obligation, obligation


@obligation("RELAY-004")  # expect: arg-type
def test_string_claim() -> None:
    pass


@obligation(Obligation.RELAY_004, "RELAY-005")  # expect: arg-type
def test_member_then_string() -> None:
    pass
