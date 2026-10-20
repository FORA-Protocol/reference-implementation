"""Host regression: resolve real Compose ports under custom project names."""

import shutil
import socket
import subprocess
import uuid
from pathlib import Path

import pytest

from ._compose import resolve_host_port

pytestmark = [
    pytest.mark.stack_isolation("isolated"),
    pytest.mark.skipif(
        shutil.which("docker") is None,
        reason="Compose CLI is absent from the runner image; run this suite on the host",
    ),
]

# One Redis probe, published on a loopback port Docker picks.
_PROBE_COMPOSE = 'services:\n  probe:\n    image: redis:7-alpine\n    ports: ["127.0.0.1::6379"]\n'


@pytest.fixture(params=[False, True], ids=["default-project", "named-project"])
def compose_probe(tmp_path, request, monkeypatch):
    monkeypatch.delenv("COMPOSE_PROJECT_NAME", raising=False)
    # E2E_PROJECT names a project outright, and these cases test discovery.
    monkeypatch.delenv("E2E_PROJECT", raising=False)
    config = tmp_path / "compose.yml"
    config.write_text(_PROBE_COMPOSE)
    command = ["docker", "compose", "-f", str(config)]
    if request.param:
        command += ["-p", f"lookup-{uuid.uuid4().hex}"]
    try:
        subprocess.run(command + ["up", "-d", "--wait"], check=True, timeout=60)
        yield config
    finally:
        subprocess.run(command + ["down", "-v"], check=True, timeout=60)


@pytest.mark.parametrize("alias", [False, True], ids=["direct-path", "symlink"])
def test_resolved_port_reaches_the_named_service(compose_probe, alias):
    config = compose_probe
    if alias:
        config = compose_probe.with_name("alias.yml")
        config.symlink_to(compose_probe)
    port = resolve_host_port(config, "probe", 6379, project=None)
    with socket.create_connection(("127.0.0.1", port), timeout=5) as connection:
        connection.sendall(b"PING\r\n")
        assert connection.recv(64) == b"+PONG\r\n"
    with pytest.raises(RuntimeError, match="service='absent' not running"):
        resolve_host_port(config, "absent", 6379, project=None)


@pytest.fixture
def two_stacks(tmp_path, monkeypatch):
    """Two stacks of one compose file, the second started after the first.

    Several e2e stacks of one checkout run this way: all of them list the same
    compose file, so container labels alone cannot tell them apart.
    """
    monkeypatch.delenv("COMPOSE_PROJECT_NAME", raising=False)
    monkeypatch.delenv("E2E_PROJECT", raising=False)
    config = tmp_path / "compose.yml"
    config.write_text(_PROBE_COMPOSE)
    older, newer = (f"lookup-{uuid.uuid4().hex}" for _ in range(2))
    commands = [["docker", "compose", "-f", str(config), "-p", name] for name in (older, newer)]
    try:
        for command in commands:
            subprocess.run(command + ["up", "-d", "--wait"], check=True, timeout=60)
        yield config, older, newer
    finally:
        downs = [
            subprocess.run(command + ["down", "-v"], check=False, timeout=60)
            for command in commands
        ]
        assert all(down.returncode == 0 for down in downs), "a probe stack did not come down"


def _published_port(config: Path, project: str) -> int:
    """Return the probe's host port as compose itself reports it."""
    out = subprocess.run(
        ["docker", "compose", "-f", str(config), "-p", project, "port", "probe", "6379"],
        capture_output=True,
        text=True,
        check=True,
        timeout=30,
    ).stdout.strip()
    return int(out.rsplit(":", 1)[1])


def test_explicit_project_beats_a_newer_stack_of_the_same_file(two_stacks):
    config, older, newer = two_stacks
    assert _published_port(config, older) != _published_port(config, newer)
    port = resolve_host_port(config, "probe", 6379, project=older)
    assert port == _published_port(config, older)


def test_e2e_project_names_the_stack_discovery_returns(two_stacks, monkeypatch):
    config, older, _newer = two_stacks
    monkeypatch.setenv("E2E_PROJECT", older)
    port = resolve_host_port(config, "probe", 6379, project=None)
    assert port == _published_port(config, older)


def test_explicit_project_with_no_such_stack_raises(two_stacks):
    config, _older, _newer = two_stacks
    absent = f"lookup-absent-{uuid.uuid4().hex}"
    with pytest.raises(RuntimeError, match=f"not running in project='{absent}'"):
        resolve_host_port(config, "probe", 6379, project=absent)
