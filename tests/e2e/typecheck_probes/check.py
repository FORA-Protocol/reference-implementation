"""mypy refuses a string claim where an ``Obligation`` goes.

``obligation()`` takes members of the ``Obligation`` enum. A string claim fails at
import, which harness/test_obligation_registry.py grades through a real import.
This script grades the other half: mypy, with the harness configuration, refuses
the string before anything runs.

It runs mypy over the two probe modules beside it. The result must be exactly the
errors the bad probe marks with ``# expect: <code>``, nothing in the clean probe,
and exit status 1. A harness package mypy cannot find counts as a failure too:
the configuration ignores missing imports, so ``Obligation`` would become ``Any``
and mypy would report nothing at all.

``make typecheck`` runs this right after the main mypy pass, which leaves the
cache warm. It is not in the per-commit pytest tier because mypy already runs in
this target, and a cold mypy run cost that tier about five seconds.

Exits 0 when mypy reports exactly the marked errors, 1 otherwise.
"""

from __future__ import annotations

import os
import re
import sys
from pathlib import Path

from mypy import api

_HERE = Path(__file__).resolve().parent
_E2E_ROOT = _HERE.parent
_PROBES = (_HERE / "obligation_claims_clean.py", _HERE / "obligation_claims_bad.py")
_EXPECT = re.compile(r"#\s*expect:\s*([a-z-]+)\s*$")
_ERROR = re.compile(r"^(.+?):(\d+): error: .*\[([a-z-]+)\]$", re.M)

Finding = tuple[str, int, str]


def expected_errors() -> list[Finding]:
    """Every ``# expect: <code>`` marker in the probes, as (file, line, code)."""
    found = []
    for probe in _PROBES:
        for number, line in enumerate(probe.read_text(encoding="utf-8").splitlines(), 1):
            marker = _EXPECT.search(line)
            if marker:
                found.append((probe.name, number, marker.group(1)))
    return sorted(found)


def main() -> int:
    want = expected_errors()
    if not want:
        print("FAIL  no probe line is marked '# expect:', so there is nothing to compare")
        return 1
    # mypy finds the harness package through the working directory.
    os.chdir(_E2E_ROOT)
    stdout, stderr, status = api.run(
        [
            "--config-file",
            str(_E2E_ROOT / "pyproject.toml"),
            "--no-error-summary",
            "--show-error-codes",
            *(str(probe) for probe in _PROBES),
        ]
    )
    got = sorted(
        (Path(m.group(1)).name, int(m.group(2)), m.group(3)) for m in _ERROR.finditer(stdout)
    )
    if got != want or status != 1:
        print(
            f"FAIL  mypy did not refuse exactly the marked string claims: expected {want}, "
            f"got {got} (exit status {status})\n{stdout}{stderr}"
        )
        return 1
    print(f"PASS  mypy refuses each string claim in the probe, and nothing else ({len(want)})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
