"""Compose-derived runtime helpers for the E2E harness.

Resolves project-name-aware values (host ports, volume names) and
materializes named-volume content at fixture time so two compose stacks
brought up under different project names from different cwds (main tree
+ worktree) coexist on a single Docker daemon without compile-time
literal collisions.

Uses the docker-py SDK (``docker``) against the daemon socket rather
than shelling out to the ``docker`` / ``docker compose`` CLI. The SDK
is a hard test-side dependency (tests/e2e/pyproject.toml) and gives
typed exceptions, no argv assembly, and no subprocess overhead.
Compose-CLI semantics we cannot reach via the SDK (project name +
service ports) are reconstructed from container labels: every compose-
managed container carries ``com.docker.compose.project``,
``com.docker.compose.service``, and ``com.docker.compose.project.config_files``
labels, which together pin the project context exactly.
"""

from __future__ import annotations

import os
import re
import secrets
import socket
from collections.abc import Iterator
from contextlib import contextmanager
from pathlib import Path
from typing import TYPE_CHECKING, Any

import docker
import yaml

if TYPE_CHECKING:
    from docker import DockerClient
    from docker.models.containers import Container


def resolve_compose_file() -> Path:
    """Locate docker-compose.e2e.yml.

    Inside the runner container we bind-mount it at FORA_E2E_COMPOSE_FILE.
    From the repo root (host run) it lives three dirs up from this file.

    It lives here, beside the port lookup that reads it, rather than in
    ``conftest``: a module that only needs to ask compose a question should not
    have to import a pytest-convention file to do it. ``register_staging_agent``
    is the case that proves it — not a pytest run at all, and it would otherwise
    pull in pytest and the fixture stack to answer one question about a host.
    """
    env_path = os.environ.get("FORA_E2E_COMPOSE_FILE")
    if env_path:
        return Path(env_path)
    return Path(__file__).resolve().parents[3] / "docker-compose.e2e.yml"


COMPOSE_FILE = resolve_compose_file()

# The tree COMPOSE_FILE sits in. Defined beside it rather than in conftest.py for
# the reason readiness.py states about the poller: conftest.py is the pytest
# plugin file every module in this package already imports, so anything defined
# there cannot be imported BACK by a module conftest itself uses. REPO_ROOT was
# the last value still breaking that rule, and publish_harness.py reading it from
# conftest is what forced conftest to import publish_harness inside a fixture
# body. Defined here, both become ordinary top-level imports.
REPO_ROOT = COMPOSE_FILE.parent


# Compose's own project-name normalization (cli/utils.go::NormalizeProjectName):
# lowercase the cwd basename, then drop characters that aren't [a-z0-9_-].
_PROJECT_NAME_FORBIDDEN = re.compile(r"[^a-z0-9_-]")

_LABEL_PROJECT = "com.docker.compose.project"
_LABEL_SERVICE = "com.docker.compose.service"
_LABEL_CONFIG_FILES = "com.docker.compose.project.config_files"


def _client() -> DockerClient:
    """Return a docker-py client bound to the daemon socket."""
    return docker.from_env()


def host_project() -> str:
    """Return the compose project of the stack a host run of this harness drives.

    ``E2E_PROJECT`` when it is set. Otherwise ``<checkout>-e2e``, lowercased,
    with every character outside ``[a-z0-9_-]`` replaced by ``-``: the name
    ``make e2e-up`` gives the stack unless ``COMPOSE_PROJECT`` or
    ``COMPOSE_PROJECT_NAME`` is set. For a stack under any other name, export
    ``E2E_PROJECT``; ``make -s e2e-project`` prints the name make uses.

    Host-side lookups of this harness's own stack pass this to
    :func:`resolve_host_port` rather than discovering the project from
    container labels: several stacks of one checkout can run at once, all of
    them list the same compose file, and discovery can return another of them.
    """
    default = _PROJECT_NAME_FORBIDDEN.sub("-", f"{REPO_ROOT.name.lower()}-e2e")
    return os.environ.get("E2E_PROJECT") or default


def _resolve_project_name(compose_file: Path) -> str:
    """Return the compose project name for ``compose_file``.

    Walks all containers on the daemon, finds one whose
    ``com.docker.compose.project.config_files`` label lists ``compose_file``,
    and returns its ``com.docker.compose.project`` label. Catches
    ``-p <name>`` and ``COMPOSE_PROJECT_NAME`` overrides because the
    label captures whatever project name the stack was actually brought
    up under.

    Falls back to compose's cwd-basename normalization rule when no
    container matches (stack not running yet).

    ``E2E_PROJECT``, when set, names the project outright: several stacks of
    one checkout can run at once (``make test-e2e`` gives each run a project of
    its own), and all of them list the same compose file.
    """
    named = os.environ.get("E2E_PROJECT")
    if named:
        return named
    target = compose_file.resolve()
    for container in _client().containers.list(all=True):
        labels = container.labels or {}
        config_files = labels.get(_LABEL_CONFIG_FILES, "") or ""
        if any(Path(path).resolve() == target for path in config_files.split(",") if path):
            project = labels.get(_LABEL_PROJECT, "")
            if project:
                return project
    return _PROJECT_NAME_FORBIDDEN.sub("", compose_file.parent.name.lower())


def resolve_host_port(
    compose_file: Path, service: str, container_port: int, *, project: str | None
) -> int:
    """Return the host port Docker assigned to ``service:container_port``.

    ``project`` is the compose project to look in. ``None`` discovers it from
    ``compose_file`` (:func:`_resolve_project_name`). Every host-side lookup of
    this harness's own stack passes :func:`host_project` instead, because
    container labels cannot tell apart several stacks of one compose file. The
    keyword has no default, so no call site falls back to discovery unnoticed.

    Looks up the running container by its compose labels (project +
    service), then reads the host binding for ``container_port/tcp``
    from ``container.ports``. Equivalent to ``docker compose port
    <svc> <container_port>`` but goes straight to the daemon API.
    """
    if project is None:
        project = _resolve_project_name(compose_file)
    containers = _client().containers.list(
        filters={
            "label": [f"{_LABEL_PROJECT}={project}", f"{_LABEL_SERVICE}={service}"],
        },
    )
    if not containers:
        msg = (
            f"compose port lookup failed: service={service!r} not running "
            f"in project={project!r} (compose_file={compose_file})"
        )
        raise RuntimeError(msg)
    bindings = (containers[0].ports or {}).get(f"{container_port}/tcp", [])
    if not bindings:
        msg = (
            f"compose port lookup failed: service={service!r} "
            f"port={container_port} not published in {compose_file}"
        )
        raise RuntimeError(msg)
    return int(bindings[0]["HostPort"])


def _runner() -> Container:
    """The runner's own container, found by hostname (Docker sets it to the container id)."""
    return _client().containers.get(socket.gethostname())


def _runner_project() -> str:
    project = (_runner().labels or {}).get(_LABEL_PROJECT)
    if not project:
        msg = "the runner container carries no compose project label"
        raise RuntimeError(msg)
    return project


def _running_service(service: str) -> Container:
    """The one running ``service`` container in the runner's own compose project."""
    project = _runner_project()
    matches = _client().containers.list(
        filters={
            "label": [f"{_LABEL_PROJECT}={project}", f"{_LABEL_SERVICE}={service}"],
            "status": "running",
        },
    )
    if len(matches) != 1:
        msg = f"want exactly one running {service!r} in project={project!r}, found {len(matches)}"
        raise RuntimeError(msg)
    return matches[0]


def running_service_env(service: str) -> dict[str, str]:
    """Return the environment of the one running ``service`` container.

    In-network only: the runner looks only in its own container's compose
    project. Zero or several running matches raise, so a test never reads a stale
    container or one from another stack on the same daemon.
    """
    env = _running_service(service).attrs["Config"]["Env"] or []
    return dict(item.partition("=")[::2] for item in env)


def _addresses(container: Container) -> list[str]:
    networks = container.attrs["NetworkSettings"]["Networks"] or {}
    return [net["IPAddress"] for net in networks.values() if net.get("IPAddress")]


def runner_addresses() -> list[str]:
    """The IPv4 addresses the runner's own container holds. In-network only."""
    return _addresses(_runner())


def project_addresses() -> dict[str, list[str]]:
    """Every running container of the runner's compose project, by name, with its addresses.

    The runner's own container is included, under its own name.
    """
    project = _runner_project()
    containers = _client().containers.list(
        filters={"label": [f"{_LABEL_PROJECT}={project}"], "status": "running"},
    )
    return {container.name: _addresses(container) for container in containers}


def project_services() -> dict[str, str]:
    """The compose service of every running container of the runner's project. In-network only.

    Keyed by container name and by each address the container holds, so a caller
    seen by either one is named.
    """
    project = _runner_project()
    containers = _client().containers.list(
        filters={"label": [f"{_LABEL_PROJECT}={project}"], "status": "running"},
    )
    services: dict[str, str] = {}
    for container in containers:
        service = (container.labels or {}).get(_LABEL_SERVICE, "")
        for key in (container.name, *_addresses(container)):
            services[key] = service
    return services


@contextmanager
def stopped_service(service: str) -> Iterator[None]:
    """``service`` stopped for the length of the block and started again on exit. In-network only.

    For a test that needs a party to be unreachable, such as the Broker whose key
    directory an Exchange must not depend on for a direct request. The container is
    stopped, not removed, so it comes back with its own configuration, volumes and
    network aliases. Starting it is all this does on exit: the caller waits until the
    service answers again, because only the caller knows what "answers" means for it.
    """
    container = _running_service(service)
    container.stop(timeout=10)
    try:
        yield
    finally:
        container.start()


def restart_service(service: str) -> None:
    """Restart the one running ``service`` container in place. In-network only.

    For a test that needs a service's process state gone, such as an edge's cache of
    a key directory, while its configuration, volumes and network aliases stay as
    they were. The container is running again when this returns; the caller waits
    until the service answers, because only the caller knows what "answers" means
    for it.
    """
    _running_service(service).restart(timeout=10)


def exec_in_service(service: str, argv: list[str]) -> tuple[int, str]:
    """Run ``argv`` inside the one running ``service`` container; return its exit code and output.

    For a check that has to originate from another container's address, which a
    request from the runner cannot. In-network only.
    """
    exit_code, output = _running_service(service).exec_run(argv)
    return int(exit_code), output.decode(errors="replace")


_LABEL_WORKING_DIR = "com.docker.compose.project.working_dir"

# The compose variable every static address in COMPOSE_FILE is written with: the
# first three octets of the stack's /24, which scripts/e2e-stack.sh hands compose.
_NET_VARIABLE = "${FORA_E2E_NET}"

# An IPv4 subnet as Docker reports it, e.g. "172.18.0.0/24".
_IPV4_SUBNET = re.compile(r"\d+\.\d+\.\d+\.\d+/\d+")


def _network_prefix(network: str) -> str:
    """The value compose reads as FORA_E2E_NET for a stack on ``network``.

    The first three octets of the network's IPv4 subnet, which scripts/e2e-stack.sh
    always creates as a /24, read the way that script reads them.
    """
    config = _client().networks.get(network).attrs["IPAM"]["Config"] or []
    for entry in config:
        subnet = str(entry.get("Subnet", ""))
        if _IPV4_SUBNET.fullmatch(subnet):
            return subnet.split("/", 1)[0].rsplit(".", 1)[0]
    msg = f"network {network!r} has no IPv4 subnet: {config}"
    raise RuntimeError(msg)


def _service_definition(service: str) -> dict[str, Any]:
    """``service``'s definition in COMPOSE_FILE, with its anchors and merge keys resolved."""
    services = yaml.safe_load(COMPOSE_FILE.read_text()).get("services") or {}
    definition = services.get(service)
    if not isinstance(definition, dict):
        msg = f"no {service!r} service in {COMPOSE_FILE}"
        raise RuntimeError(msg)
    return definition


def _bind_mounts(volumes: list[str], working_dir: str) -> dict[str, dict[str, str]]:
    """Compose's short bind syntax (``./source:target[:ro]``) as docker-py's ``volumes``.

    Only relative bind mounts are accepted, resolved against the project's working
    directory, which is where compose resolves them for the services it starts.
    """
    mounts: dict[str, dict[str, str]] = {}
    for volume in volumes:
        source, target, *mode = volume.split(":")
        if not source.startswith("./"):
            msg = f"a job's volume must be a relative bind mount, got {volume!r}"
            raise RuntimeError(msg)
        mounts[str(Path(working_dir) / source)] = {
            "bind": target,
            "mode": mode[0] if mode else "rw",
        }
    return mounts


def run_job(service: str, *, image_of: str, timeout_s: float) -> tuple[int, str]:
    """Run the profiled one-off ``service`` once, to completion; return its exit code and output.

    What ``docker compose --profile <p> run --rm <service>`` does, through the
    docker socket the runner holds, since the runner carries no compose CLI. The
    container is built from ``service``'s own definition in COMPOSE_FILE (its
    entrypoint, environment, DNS and bind mounts) and runs the image the running
    ``image_of`` service was built from, on that service's network. A job service
    is never started by ``docker compose up``, so its own image may not exist;
    ``image_of`` names the service whose build it shares.

    The definition is read as YAML, which leaves compose's variables unfilled, and
    a DNS server such as the wildcard sidecar is written ``${FORA_E2E_NET}.<n>``.
    So the environment and the DNS servers get that variable filled in from the
    network the job joins, the value compose fills in for the services it starts.

    The container is removed however the run ends, so no job outlives the call or
    holds the network when the stack comes down. In-network only.
    """
    definition = _service_definition(service)
    anchor = _running_service(image_of)
    labels = anchor.labels or {}
    working_dir = labels.get(_LABEL_WORKING_DIR, "")
    if not working_dir:
        msg = f"the running {image_of!r} carries no compose working directory label"
        raise RuntimeError(msg)
    networks = list((anchor.attrs["NetworkSettings"]["Networks"] or {}).keys())
    if len(networks) != 1:
        msg = f"want {image_of!r} on exactly one network, found {networks}"
        raise RuntimeError(msg)
    prefix = _network_prefix(networks[0])
    environment = {
        key: str(value).replace(_NET_VARIABLE, prefix)
        for key, value in (definition.get("environment") or {}).items()
    }
    dns = [str(server).replace(_NET_VARIABLE, prefix) for server in definition.get("dns") or []]
    container = _client().containers.run(
        anchor.image,
        entrypoint=definition.get("entrypoint"),
        environment=environment,
        dns=dns or None,
        volumes=_bind_mounts(definition.get("volumes") or [], working_dir),
        network=networks[0],
        name=f"{labels.get(_LABEL_PROJECT, 'e2e')}-{service}-{secrets.token_hex(4)}",
        detach=True,
    )
    try:
        status = container.wait(timeout=timeout_s)
        output = container.logs(stdout=True, stderr=True).decode(errors="replace")
    finally:
        container.remove(force=True)
    return int(status.get("StatusCode", -1)), output
