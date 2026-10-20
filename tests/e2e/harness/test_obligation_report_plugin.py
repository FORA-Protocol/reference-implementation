"""The obligation report: each obligation's state, derived from the tests that claim it.

Host tier: no compose stack, no database. The surface is a real pytest session
(``pytester``, in a subprocess) that loads a copy of the shipped plugin module
with ``-p harness.obligation_registry`` beside a temporary registry. Nothing in
the loaded module is patched. Each session asserts back through what the plugin
publishes: the terminal summary, ``<rootpath>/obligation-report.json`` and the
exit status.

The state rule, in precedence order: *open* when ``question`` is set; *untested*
when no collected test case claims the obligation; *skipped* when every claiming
case skipped at run time, with each skip's reason in the report; then, over the
cases that did not skip, *xfail* when every one is a strict xfail and *partial*
when only some are. When none is, an entry that lists ``negative`` cases is
*partial* until a case claims it with ``negative=True``, and any other entry is
*tested*. An xfailed case is not a skip, although pytest reports it as one. A
retired obligation is left out. A claim counts wherever the marker sits: on the
function, on its class, on the module through ``pytestmark``, or on one
parametrized case. The report never changes the exit status. A raw
``pytest.mark.obligation("<ID>")`` that bypassed the decorator is a malformed
claim, and so is a ``negative`` flag that is not a bool, any other keyword, or a
negative claimed on an entry that lists none: the session refuses each as a
usage error naming the test.

The last test checks this repository's own wiring: the harness conftest loads
the plugin, and the e2e pytest configuration makes every xfail strict.
"""

from __future__ import annotations

import re
from typing import Any

import pytest

from .obligation_builders import (
    entry_fields,
    plant_session,
    read_report,
    read_states,
    registry_module,
    run_session,
)

pytest_plugins = ["pytester"]

# No compose stack and no database. Declaring `isolated` keeps the autouse
# cleanup chain, which needs a live Postgres, a no-op.
pytestmark = pytest.mark.stack_isolation("isolated")

_IMPORT = "import pytest\nfrom harness.obligation_registry import Obligation, obligation\n"

# One module per way of placing a claim. Every strict xfail fails on purpose, so
# it reports as xfailed; every other test passes.
_CLAIMS_ON_FUNCTIONS = (
    _IMPORT
    + """
@obligation(Obligation.OPEN_002)
def test_open_obligation_claimed_by_a_pass():
    pass


@pytest.mark.xfail(reason="strict through the ini default")
@obligation(Obligation.BROKEN_001)
def test_broken_by_default_strictness():
    assert False


@pytest.mark.xfail(strict=True, reason="explicitly strict")
@obligation(Obligation.BROKEN_002)
def test_broken_explicitly_strict():
    assert False


@pytest.mark.xfail(True, reason="condition true")
@obligation(Obligation.BROKEN_002)
def test_broken_condition_true():
    assert False


@pytest.mark.parametrize(
    "case",
    [
        pytest.param(1, marks=pytest.mark.xfail(reason="first case")),
        pytest.param(2, marks=pytest.mark.xfail(reason="second case")),
    ],
)
@obligation(Obligation.BROKEN_003)
def test_broken_in_every_case(case):
    assert False


@obligation(Obligation.PROVEN_001)
def test_proven_plainly():
    pass


@pytest.mark.xfail(False, reason="condition false, so an ordinary test")
@obligation(Obligation.PROVEN_004)
def test_proven_under_a_false_xfail_condition():
    pass


@obligation(Obligation.PROVEN_005, Obligation.MIXED_001)
def test_one_marker_claims_two():
    pass


@pytest.mark.xfail(reason="strict")
@obligation(Obligation.MIXED_001)
def test_mixed_strict_half():
    assert False


@pytest.mark.xfail(strict=True, reason="strict")
@obligation(Obligation.MIXED_002)
def test_mixed_strict():
    assert False


@pytest.mark.xfail(strict=False, reason="not strict, so not an xfail claim")
@obligation(Obligation.MIXED_002)
def test_mixed_not_strict():
    assert False


@pytest.mark.parametrize("case", [1, pytest.param(2, marks=pytest.mark.xfail(reason="one case"))])
@obligation(Obligation.MIXED_003)
def test_mixed_by_one_case(case):
    assert case == 1
"""
)
_CLAIMS_ON_CLASSES = (
    _IMPORT
    + """
@obligation(Obligation.PROVEN_003)
class TestClaimedByTheClass:
    def test_first(self):
        pass

    def test_second(self):
        pass


@obligation(Obligation.MIXED_004)
class TestClassWithOneStrictXfail:
    def test_passes(self):
        pass

    @pytest.mark.xfail(reason="strict")
    def test_xfails(self):
        assert False
"""
)
_CLAIM_ON_THE_MODULE = (
    _IMPORT
    + """
pytestmark = obligation(Obligation.PROVEN_002)


def test_claimed_by_the_module():
    pass
"""
)

_EXPECTED_STATES = {
    "OPEN-001": "open",
    "OPEN-002": "open",
    "GAP-001": "untested",
    "BROKEN-001": "xfail",
    "BROKEN-002": "xfail",
    "BROKEN-003": "xfail",
    "PROVEN-001": "tested",
    "PROVEN-002": "tested",
    "PROVEN-003": "tested",
    "PROVEN-004": "tested",
    "PROVEN-005": "tested",
    "MIXED-001": "partial",
    "MIXED-002": "partial",
    "MIXED-003": "partial",
    "MIXED-004": "partial",
}


def _five_state_registry() -> list[dict[str, object]]:
    # The retired entry comes first, so skipping it cannot also skip the live entries after it.
    # No entry lists a negative, so the negative rule pinned further down plays no part here.
    entries = [entry_fields("GONE-001", retired=True)]
    entries += [
        entry_fields(oid, question="Is a redirect a delivery?", negative=[])
        if state == "open"
        else entry_fields(oid, negative=[])
        for oid, state in _EXPECTED_STATES.items()
    ]
    return entries


def test_report_derives_every_state_from_the_claiming_tests(pytester: pytest.Pytester) -> None:
    plant_session(
        pytester,
        _five_state_registry(),
        {
            "test_claims_on_functions.py": _CLAIMS_ON_FUNCTIONS,
            "test_claims_on_classes.py": _CLAIMS_ON_CLASSES,
            "test_claim_on_the_module.py": _CLAIM_ON_THE_MODULE,
        },
    )

    result = run_session(pytester)

    # Exit 0 although GAP-001 is untested: the report never fails the run.
    assert result.ret == pytest.ExitCode.OK, result.stdout.str() + result.stderr.str()
    result.assert_outcomes(passed=9, xfailed=10)
    assert read_states(pytester) == _EXPECTED_STATES

    out = result.stdout.str()
    counts = [
        ("open", 2),
        ("untested", 1),
        ("skipped", 0),
        ("xfail", 3),
        ("tested", 5),
        ("partial", 4),
    ]
    for state, count in counts:
        assert re.search(rf"^.*\b{state}\b.*\b{count}\b.*$", out, re.M), (
            f"no summary line gives {state} = {count}:\n{out}"
        )
    for oid, state in _EXPECTED_STATES.items():
        if state == "tested":
            assert oid not in out, f"tested obligation {oid} listed:\n{out}"
        else:
            assert oid in out, f"{state} obligation {oid} not listed:\n{out}"
    assert "GONE-001" not in out


def test_report_counts_only_the_tests_left_after_deselection(pytester: pytest.Pytester) -> None:
    plant_session(
        pytester,
        [entry_fields("AREA-001", negative=[]), entry_fields("AREA-002", negative=[])],
        {
            "test_pair.py": _IMPORT
            + """
@obligation(Obligation.AREA_001)
def test_one():
    pass


@obligation(Obligation.AREA_002)
def test_two():
    pass
"""
        },
    )

    result = run_session(pytester, "-k", "test_one")

    assert result.ret == pytest.ExitCode.OK, result.stdout.str() + result.stderr.str()
    result.assert_outcomes(passed=1, deselected=1)
    assert read_states(pytester) == {"AREA-001": "tested", "AREA-002": "untested"}


def test_report_leaves_a_failing_run_failing(pytester: pytest.Pytester) -> None:
    """The report changes the exit status in neither direction."""
    plant_session(
        pytester,
        [entry_fields("AREA-001", negative=[])],
        {
            "test_failing.py": _IMPORT
            + """
@obligation(Obligation.AREA_001)
def test_fails():
    assert False
"""
        },
    )

    result = run_session(pytester)

    assert result.ret == pytest.ExitCode.TESTS_FAILED
    result.assert_outcomes(failed=1)
    assert read_states(pytester) == {"AREA-001": "tested"}


def test_report_of_an_empty_registry_is_empty(pytester: pytest.Pytester) -> None:
    plant_session(pytester, [], {"test_plain.py": "def test_plain():\n    pass\n"})

    result = run_session(pytester)

    assert result.ret == pytest.ExitCode.OK, result.stdout.str() + result.stderr.str()
    result.assert_outcomes(passed=1)
    assert read_report(pytester) == {}


@pytest.mark.parametrize(
    "module",
    [
        """
import pytest


@pytest.mark.obligation("RELAY-004")
def test_raw_claim():
    pass
""",
        """
import pytest

pytestmark = pytest.mark.obligation("RELAY-004")


def test_raw_claim():
    pass
""",
    ],
    ids=["on-the-function", "on-the-module"],
)
def test_a_raw_string_claim_is_a_usage_error_naming_the_test_and_argument(
    pytester: pytest.Pytester, module: str
) -> None:
    plant_session(pytester, [entry_fields("RELAY-004")], {"test_raw.py": module})

    result = run_session(pytester)

    assert result.ret == pytest.ExitCode.USAGE_ERROR, result.stdout.str() + result.stderr.str()
    stderr = result.stderr.str()
    assert "suite/test_raw.py::test_raw_claim" in stderr
    assert "RELAY-004" in stderr
    assert "passed" not in result.stdout.str()


# Every way a case can skip at run time, beside cases that must not read as skips.
_SKIPS = (
    _IMPORT
    + """
@pytest.fixture
def absent_runtime():
    pytest.skip("the fixture's runtime is absent")


@obligation(Obligation.SKIP_001)
def test_skips_in_its_body():
    pytest.skip("the go binary is absent")


@pytest.mark.skip(reason="the lambda runtime is absent")
@obligation(Obligation.SKIP_002)
def test_skipped_by_a_mark():
    pass


@pytest.mark.skipif(True, reason="the condition holds")
@obligation(Obligation.SKIP_003)
def test_skipped_by_a_true_condition():
    pass


@obligation(Obligation.SKIP_004)
def test_skipped_by_its_fixture(absent_runtime):
    pass


@pytest.mark.parametrize("case", [1, pytest.param(2, marks=pytest.mark.skip(reason="one case"))])
@obligation(Obligation.RAN_001)
def test_one_case_skips_and_one_passes(case):
    pass


@pytest.mark.skipif(False, reason="the condition fails, so an ordinary test")
@obligation(Obligation.RAN_002)
def test_under_a_false_skip_condition():
    pass


@pytest.mark.xfail(reason="strict")
@obligation(Obligation.RAN_003)
def test_an_xfail_is_not_a_skip():
    assert False


@obligation(Obligation.RAN_004)
def test_skips_beside_an_xfail():
    pytest.skip("the edge runtime is absent")


@pytest.mark.xfail(reason="strict")
@obligation(Obligation.RAN_004)
def test_the_xfail_beside_a_skip():
    assert False
"""
)

_SKIP_REPORT: dict[str, dict[str, Any]] = {
    "SKIP-001": {"state": "skipped", "skips": ["the go binary is absent"]},
    "SKIP-002": {"state": "skipped", "skips": ["the lambda runtime is absent"]},
    "SKIP-003": {"state": "skipped", "skips": ["the condition holds"]},
    "SKIP-004": {"state": "skipped", "skips": ["the fixture's runtime is absent"]},
    "RAN-001": {"state": "tested", "skips": ["one case"]},
    "RAN-002": {"state": "tested"},
    "RAN-003": {"state": "xfail"},
    "RAN-004": {"state": "xfail", "skips": ["the edge runtime is absent"]},
}


def test_a_runtime_skip_is_reported_skipped_with_its_reason(pytester: pytest.Pytester) -> None:
    plant_session(
        pytester,
        [entry_fields(oid, negative=[]) for oid in _SKIP_REPORT],
        {"test_skips.py": _SKIPS},
    )

    result = run_session(pytester)

    assert result.ret == pytest.ExitCode.OK, result.stdout.str() + result.stderr.str()
    result.assert_outcomes(passed=2, skipped=6, xfailed=2)
    assert read_report(pytester) == _SKIP_REPORT
    out = result.stdout.str()
    assert re.search(r"^skipped\s+4\s+SKIP-001 SKIP-002 SKIP-003 SKIP-004$", out, re.M), out
    for oid, status in _SKIP_REPORT.items():
        for reason in status.get("skips", []):
            assert f"{oid} skipped: {reason}" in out, f"{oid}'s skip reason not shown:\n{out}"


# Entries NEG-* list a negative; NONE-* list none.
_NEGATIVES = (
    _IMPORT
    + """
@obligation(Obligation.NEG_001)
def test_only_the_success_of_an_entry_with_negatives():
    pass


@obligation(Obligation.NEG_002)
def test_the_success():
    pass


@obligation(Obligation.NEG_002, negative=True)
def test_the_refusal():
    pass


@obligation(Obligation.NEG_003, negative=True)
def test_a_control_and_its_refusal():
    pass


@obligation(Obligation.NEG_004)
def test_the_success_beside_an_unmet_negative():
    pass


@pytest.mark.xfail(reason="strict: the refusal is not implemented")
@obligation(Obligation.NEG_004, negative=True)
def test_an_unmet_negative():
    assert False


@obligation(Obligation.NEG_005)
def test_the_success_beside_a_skipped_negative():
    pass


@obligation(Obligation.NEG_005, negative=True)
def test_a_skipped_negative():
    pytest.skip("the second Exchange is absent")


@obligation(Obligation.NEG_006, negative=True)
@obligation(Obligation.NONE_001)
def test_a_negative_of_one_entry_and_the_success_of_another():
    pass


@obligation(Obligation.NONE_002)
def test_the_success_of_an_entry_without_negatives():
    pass


@obligation(Obligation.NEG_007, negative=True)
class TestNegativesClaimedByTheClass:
    def test_refused(self):
        pass


@pytest.mark.parametrize(
    "case", [pytest.param(1, marks=obligation(Obligation.NEG_008, negative=True)), 2]
)
def test_a_negative_claimed_on_one_case(case):
    pass
"""
)

_NO_NEGATIVE = {"state": "partial", "note": "no claiming test drives a listed negative"}
_NEGATIVE_REPORT = {
    "NEG-001": _NO_NEGATIVE,
    "NEG-002": {"state": "tested"},
    "NEG-003": {"state": "tested"},
    "NEG-004": {"state": "partial"},
    "NEG-005": {**_NO_NEGATIVE, "skips": ["the second Exchange is absent"]},
    "NEG-006": {"state": "tested"},
    "NEG-007": {"state": "tested"},
    "NEG-008": {"state": "tested"},
    "NONE-001": {"state": "tested"},
    "NONE-002": {"state": "tested"},
}


def test_an_entry_listing_negatives_is_tested_only_when_a_claim_drives_one(
    pytester: pytest.Pytester,
) -> None:
    entries = [
        entry_fields(oid, negative=[]) if oid.startswith("NONE") else entry_fields(oid)
        for oid in _NEGATIVE_REPORT
    ]
    plant_session(pytester, entries, {"test_negatives.py": _NEGATIVES})

    result = run_session(pytester)

    assert result.ret == pytest.ExitCode.OK, result.stdout.str() + result.stderr.str()
    result.assert_outcomes(passed=11, skipped=1, xfailed=1)
    assert read_report(pytester) == _NEGATIVE_REPORT


@pytest.mark.parametrize(
    ("claim", "named"),
    [
        ("@obligation(Obligation.NONE_001, negative=True)", "NONE-001"),
        ('@pytest.mark.obligation(Obligation.NEG_001, negative="yes")', "negative"),
        ("@pytest.mark.obligation(Obligation.NEG_001, negatve=True)", "negatve"),
    ],
    ids=["negative-of-an-entry-listing-none", "flag-not-a-bool", "unknown-keyword"],
)
def test_a_malformed_negative_claim_is_a_usage_error_naming_the_test(
    pytester: pytest.Pytester, claim: str, named: str
) -> None:
    plant_session(
        pytester,
        [entry_fields("NEG-001"), entry_fields("NONE-001", negative=[])],
        {"test_claim.py": f"{_IMPORT}\n\n{claim}\ndef test_claim():\n    pass\n"},
    )

    result = run_session(pytester)

    assert result.ret == pytest.ExitCode.USAGE_ERROR, result.stdout.str() + result.stderr.str()
    stderr = result.stderr.str()
    assert "suite/test_claim.py::test_claim" in stderr
    assert named in stderr
    assert "passed" not in result.stdout.str()


def test_this_session_loads_the_plugin_and_makes_every_xfail_strict(
    request: pytest.FixtureRequest,
) -> None:
    """The harness conftest loads the plugin; the e2e configuration sets xfail_strict."""
    mod = registry_module()
    config = request.config
    assert config.pluginmanager.get_plugin("harness.obligation_registry") is mod
    reports = [p for p in config.pluginmanager.get_plugins() if isinstance(p, mod.ObligationReport)]
    assert len(reports) == 1
    assert config.getini("xfail_strict") is True
    assert [line for line in config.getini("markers") if line.startswith("obligation")] != []
