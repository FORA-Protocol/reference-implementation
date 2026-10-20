#!/usr/bin/env python3
"""Supplemental structural guard: local PostgreSQL belongs in one fallback.

Behavior remains covered by remote-psql-test.py. This guards the bypass pattern
across all operator scripts, with meta-tests proving the scanner catches a new
caller and permits the shared fallback and normal remote_psql calls.
"""
from pathlib import Path
import re
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
LOCAL_EXEC = re.compile(r"\bexec\s+(?:-[\w-]+\s+)*(?:postgres|zitadel-db)\s+psql\b")


def bypasses(root):
    findings = []
    for path in sorted(root.rglob("*.sh")):
        relative = path.relative_to(root)
        if "tests" in relative.parts or relative == Path("lib/staging-env.sh"):
            continue
        text = re.sub(r"\\\n\s*", " ", path.read_text())
        text = "\n".join(line for line in text.splitlines() if not line.lstrip().startswith("#"))
        if LOCAL_EXEC.search(text):
            findings.append(str(relative))
    return findings


class OperatorSqlGuard(unittest.TestCase):
    def test_repository_has_no_shared_helper_bypasses(self):
        self.assertEqual(bypasses(SCRIPTS), [], "route operator SQL through remote_psql")

    def test_scanner_accepts_helper_and_flags_present_and_future_callers(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "lib").mkdir()
            (root / "lib/staging-env.sh").write_text('docker compose exec -T postgres psql "$@"\n')
            (root / "legitimate.sh").write_text('remote_psql fora -c "SELECT 1"\n')
            self.assertEqual(bypasses(root), [])
            (root / "existing.sh").write_text('docker compose exec -T postgres \\\n psql -U fora\n')
            self.assertEqual(bypasses(root), ["existing.sh"])
            (root / "new-operator.sh").write_text('docker compose exec -T zitadel-db psql -U zitadel\n')
            self.assertEqual(bypasses(root), ["existing.sh", "new-operator.sh"])


if __name__ == "__main__":
    unittest.main()
