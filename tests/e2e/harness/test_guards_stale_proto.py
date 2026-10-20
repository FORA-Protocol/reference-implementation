"""Exercise the actual stale-name guard against isolated source trees."""

import subprocess

import pytest

from .conftest import REPO_ROOT
from .guard_harness import guard_marks, install_gate

GATE = REPO_ROOT / "scripts/check-stale-proto-names.sh"
pytestmark = guard_marks(skip_when=not GATE.is_file())


@pytest.mark.parametrize("prefix", ["RAMP", "FORA"])
@pytest.mark.parametrize("suffix", ["Request", "Response"])
def test_guard_rejects_obsolete_name(tmp_path, prefix, suffix):
    for directory in ("src", "internal", "tests"):
        (tmp_path / directory).mkdir()
    guard = install_gate(tmp_path, GATE.name)
    source = tmp_path / "src/example.go"
    source.write_text("// DiscoveryRequest DiscoveryResponse\n")
    assert subprocess.run(["bash", str(guard)], capture_output=True).returncode == 0
    source.write_text(f"// {prefix}{suffix}\n")
    result = subprocess.run(["bash", str(guard)], capture_output=True, text=True)
    assert result.returncode == 1, result.stdout + result.stderr
    assert f"{prefix}{suffix}" in result.stdout
