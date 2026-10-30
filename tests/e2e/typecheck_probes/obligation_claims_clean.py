"""Probe: a claim made with enum members, which mypy must accept.

The members named here are not in the real registry. The static declaration of
``Obligation`` accepts any member name, so the type checker judges the type of
each argument and never the registry's contents. ``check.py`` beside this file
requires mypy to report nothing here.
"""

from harness.obligation_registry import Obligation, obligation


@obligation(Obligation.PROBE_001, Obligation.PROBE_002)
def test_claims_two() -> None:
    pass
