"""Host regression: resolve real Compose ports under custom project names."""

import shutil
import socket
import subprocess
import uuid

import pytest

from ._compose import resolve_host_port

pytestmark = [
    pytest.mark.stack_isolation("isolated"),
    pytest.mark.skipif(
        shutil.which("docker") is None,
        reason="Compose CLI is absent from the runner image; run this suite on the host",
    ),
]


@pytest.fixture(params=[False, True], ids=["default-project", "named-project"])
def compose_probe(tmp_path, request, monkeypatch):
    monkeypatch.delenv("COMPOSE_PROJECT_NAME", raising=False)
    config = tmp_path / "compose.yml"
    config.write_text(
        'services:\n  probe:\n    image: redis:7-alpine\n    ports: ["127.0.0.1::6379"]\n'
    )
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
    port = resolve_host_port(config, "probe", 6379)
    with socket.create_connection(("127.0.0.1", port), timeout=5) as connection:
        connection.sendall(b"PING\r\n")
        assert connection.recv(64) == b"+PONG\r\n"
    with pytest.raises(RuntimeError, match="service='absent' not running"):
        resolve_host_port(config, "absent", 6379)
