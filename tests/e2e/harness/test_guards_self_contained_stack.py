"""Meta-guard — every e2e stack keeps to itself.

Any number of e2e stacks share one Docker daemon: two runs from one checkout,
runs from two checkouts, an integration run beside them. That holds only while a
stack touches nothing outside its own network. A stack that breaks one of the
rules below still comes up and passes when it runs alone. It fails only when a
second stack, or something else on the machine, already holds what it asked for.
No gate starts two stacks, so without this guard nothing would notice.

The rules apply to every compose file a gate loads: ``docker-compose.e2e.yml``
and any other ``docker-compose.e2e*.yml`` beside it, except the host-access
opt-in ``docker-compose.e2e.ports.yml``.

1. No service publishes a port on the host: no ``ports:`` and no
   ``network_mode: host``. The test runner is a container on the stack's network
   and reaches every service by name. A developer who wants a host port adds
   ``docker-compose.e2e.ports.yml``, which no gate loads.
2. No address is written out. ``scripts/e2e-stack.sh`` creates each stack's
   network on a /24 that Docker picks, so a fixed address, or a fixed subnet, can
   belong to one stack at most. Loopback addresses and ``0.0.0.0`` are not
   addresses on that network, and are allowed.
3. Every fixed address is written ``${FORA_E2E_NET}.<n>``, with n from 128 to
   254. The script hands the stack's prefix to compose as FORA_E2E_NET, and it
   gives Docker the lower half of the /24 (.0 to .127) for the containers that
   name no address of their own, so an address there can be handed to another
   container first. A default value (``${FORA_E2E_NET:-172.30.0}``), or another
   variable that holds a prefix, puts a fixed address back, and is reported too.
4. No service mounts a folder from the checkout with write access. Every stack
   started from one checkout would share that folder, so one stack's writes would
   reach another stack's services. A stack keeps what it writes in a named volume,
   which belongs to that stack alone.

Two behaviours worth knowing. YAML anchors and merge keys are resolved the way
compose resolves them, so a value written once in an ``x-`` block is reported
where it is written and at every service that uses it. And a compose-only tag
the YAML loader does not know makes the load raise, so the guard fails instead
of passing a file it could not read.

Pure file reading: no stack, no Docker, no database. The marks come from
``guard_harness.guard_marks``, whose docstring says why the isolation one is
there.
"""

from __future__ import annotations

import ipaddress
import re
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest
import yaml

from .conftest import REPO_ROOT
from .guard_harness import guard_marks

_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"

# The one e2e compose file no gate loads: it publishes host ports on purpose,
# for a developer debugging from the host.
_HOST_ACCESS_OPT_IN = "docker-compose.e2e.ports.yml"

_NET = "FORA_E2E_NET"

# The upper half of the stack's /24, without the broadcast address. The lower
# half is Docker's automatic range (scripts/e2e-stack.sh passes --ip-range .0/25).
_STATIC_HOSTS = range(128, 255)

# A written-out IPv4 address that is not part of a longer dotted number or name.
_LITERAL_ADDRESS = re.compile(r"(?<![\w.])(?:\d{1,3}\.){3}\d{1,3}(?![\w.])")

# One compose interpolation: the escaped dollar $$, ${...}, or $NAME.
_INTERPOLATION = re.compile(r"\$\$|\$\{(?P<braced>[^}]*)\}|\$(?P<bare>[A-Za-z_][A-Za-z0-9_]*)")
_VARIABLE_NAME = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")

# The host number after a prefix variable: ".183" in "${FORA_E2E_NET}.183".
_HOST_PART = re.compile(r"\.(\d{1,3})(?![\w.])")

# An octet and a dot just before a variable: "172.30.0." in "172.30.0.${HOST}".
_OCTET_BEFORE = re.compile(r"\d\.\Z")

pytestmark = guard_marks(
    skip_when=not _COMPOSE.is_file(),
    reason="docker-compose.e2e.yml is not beside this harness, and this guard reads it",
)


def _strings(node: Any, place: str) -> Iterator[tuple[str, str]]:
    """Every key and scalar under ``node``, each with the place it sits at.

    Places read like ``services.dns.command[2]``. The value of an ``image`` key is
    not read: an image reference names a registry and a tag, never an address,
    and a tag can look like one.
    """
    if isinstance(node, dict):
        for key, value in node.items():
            here = f"{place}.{key}" if place else str(key)
            yield here, str(key)
            if key != "image":
                yield from _strings(value, here)
    elif isinstance(node, list):
        for index, item in enumerate(node):
            yield from _strings(item, f"{place}[{index}]")
    elif node is not None:
        yield place, str(node)


def _services(document: Any) -> dict[str, Any]:
    """The ``services`` mapping of a parsed compose file, or an empty one."""
    services = document.get("services") if isinstance(document, dict) else None
    return services if isinstance(services, dict) else {}


def _published_ports(document: Any) -> list[str]:
    """Rule 1: every service that publishes a port on the host."""
    problems: list[str] = []
    for name, service in _services(document).items():
        if not isinstance(service, dict):
            continue
        if service.get("ports"):
            problems.append(f"services.{name}.ports: publishes a port on the host")
        if service.get("network_mode") == "host":
            problems.append(f"services.{name}.network_mode: runs on the host's network")
    return problems


def _literal_addresses(document: Any) -> list[str]:
    """Rule 2: every written-out address other than loopback and 0.0.0.0."""
    problems: list[str] = []
    for place, text in _strings(document, ""):
        for match in _LITERAL_ADDRESS.finditer(text):
            try:
                address = ipaddress.IPv4Address(match.group())
            except ValueError:
                continue  # an octet over 255: a dotted number, not an address
            if not (address.is_loopback or address.is_unspecified):
                problems.append(f"{place}: names the fixed address {address}")
    return problems


def _address_spellings(document: Any) -> list[tuple[str, str, bool]]:
    """Every interpolation that builds an address: its place, how it is written, and if that is right.

    An interpolation builds an address when it names FORA_E2E_NET, when a host
    number follows it, or when an octet and a dot come just before it. It is
    right only when written ``${FORA_E2E_NET}.<n>`` with n in ``_STATIC_HOSTS``.
    ``$$`` is compose's escaped dollar, not an interpolation.
    """
    found: list[tuple[str, str, bool]] = []
    for place, text in _strings(document, ""):
        for match in _INTERPOLATION.finditer(text):
            if match.group() == "$$":
                continue
            braced = match.group("braced")
            name = _VARIABLE_NAME.match(braced if braced is not None else match.group("bare"))
            host = _HOST_PART.match(text, match.end())
            names_the_prefix = name is not None and name.group() == _NET
            completes_an_address = _OCTET_BEFORE.search(text, 0, match.start()) is not None
            if not (names_the_prefix or host or completes_an_address):
                continue
            right = braced == _NET and host is not None and int(host.group(1)) in _STATIC_HOSTS
            written = text[match.start() : host.end() if host else match.end()]
            found.append((place, written, right))
    return found


def _misspelled_addresses(document: Any) -> list[str]:
    """Rule 3: every address built any way but ``${FORA_E2E_NET}.<n>`` in the upper half."""
    return [
        f"{place}: '{written}' is not written ${{FORA_E2E_NET}}.<n> with n from 128 to 254"
        for place, written, right in _address_spellings(document)
        if not right
    ]


def _bind(entry: Any) -> tuple[str, bool] | None:
    """The checkout path ``entry`` mounts and whether the mount is read-only.

    None when the entry mounts no checkout path: a named volume, an absolute host
    path such as the Docker socket, or a path read from a variable. In the short
    syntax compose takes a source that is not a path for a named volume, so a
    checkout path there starts with a dot. In the long syntax a relative source
    is a checkout path with or without the dot.
    """
    if isinstance(entry, str):
        source, _, rest = entry.partition(":")
        target, _, mode = rest.partition(":")
        if not target or not source.startswith("."):
            return None
        return source, "ro" in mode.split(",")
    if isinstance(entry, dict) and entry.get("type") == "bind":
        source = str(entry.get("source", ""))
        if source.startswith(("/", "~", "$")):
            return None
        return source, entry.get("read_only") is True
    return None


def _checkout_binds(document: Any) -> list[tuple[str, str, bool]]:
    """Every checkout path a service mounts: its place, the path, and whether it is read-only."""
    found: list[tuple[str, str, bool]] = []
    for name, service in _services(document).items():
        if not isinstance(service, dict):
            continue
        for index, entry in enumerate(service.get("volumes") or []):
            bind = _bind(entry)
            if bind is not None:
                found.append((f"services.{name}.volumes[{index}]", *bind))
    return found


def _writable_binds(document: Any) -> list[str]:
    """Rule 4: every checkout path a service mounts with write access."""
    return [
        f"{place}: binds {source} from the checkout without read-only"
        for place, source, read_only in _checkout_binds(document)
        if not read_only
    ]


def _isolation_problems(text: str) -> list[str]:
    """Everything in one compose file that ties a stack to the machine or to another stack.

    Each problem starts with the place it is about, then a colon. The list is
    empty when the file follows the four rules in the module docstring.
    """
    document = yaml.safe_load(text) or {}
    return [
        *_published_ports(document),
        *_literal_addresses(document),
        *_misspelled_addresses(document),
        *_writable_binds(document),
    ]


def _gate_compose_files() -> list[Path]:
    """Every ``docker-compose.e2e*.yml`` beside the harness except the host-access opt-in."""
    return sorted(
        path
        for path in REPO_ROOT.glob("docker-compose.e2e*.yml")
        if path.name != _HOST_ACCESS_OPT_IN
    )


# A stack that follows every rule, in every form the rules must accept: an
# address in an anchor used through an alias and through a merge key, compose's
# escaped dollar, loopback and 0.0.0.0, an image tag that looks like an address,
# /32 ranges, read-only mounts in both syntaxes, a named volume and the Docker
# socket.
_CLEAN_STACK = """\
x-resolver: &resolver
  - "${FORA_E2E_NET}.153"
x-ledger-env: &ledger-env
  LEDGER_ADDRESS: "${FORA_E2E_NET}.200:3000"
services:
  resolver:
    image: registry.example/resolver:1.2.3.4
    command: ["serve", "--upstream=127.0.0.11", "--answer=/zone.example/${FORA_E2E_NET}.183"]
    ports: []
    environment:
      LISTEN: "0.0.0.0:53"
      SCHEMA: '{"$$schema":"https://json-schema.org/draft/2020-12/schema"}'
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O- http://127.0.0.1/healthz"]
    networks:
      default:
        ipv4_address: ${FORA_E2E_NET}.153
  app:
    image: app
    dns: *resolver
    environment:
      <<: *ledger-env
      ADMIN_CIDRS: "${FORA_E2E_NET}.128/32,${FORA_E2E_NET}.254/32"
    volumes:
      - ./config/app.yml:/etc/app.yml:ro
      - ./config/certs:/certs:z,ro
      - type: bind
        source: ./config/extra
        target: /extra
        read_only: true
      - state:/state
      - /var/run/docker.sock:/var/run/docker.sock
networks:
  default:
    name: "${FORA_E2E_NETWORK:?run the stack through its script}"
    external: true
volumes:
  state:
"""


def _edited(old: str, new: str) -> str:
    """The clean stack with its first ``old`` replaced by ``new``."""
    assert old in _CLEAN_STACK, (
        f"the clean stack no longer holds {old!r}, so this case tests nothing"
    )
    return _CLEAN_STACK.replace(old, new, 1)


def _spelling_problem(written: str) -> str:
    """The rule-3 problem for the resolver's address written as ``written``."""
    return (
        f"services.resolver.networks.default.ipv4_address: '{written}' is not written "
        "${FORA_E2E_NET}.<n> with n from 128 to 254"
    )


def test_a_clean_stack_is_not_reported() -> None:
    """Without this the cases that follow prove nothing.

    A check that reported everything would satisfy all of them. The counts show
    the clean stack is not empty: all eight addresses are read, through the alias
    and the merge key too, and all three checkout mounts are read as read-only.
    """
    document = yaml.safe_load(_CLEAN_STACK)

    assert _isolation_problems(_CLEAN_STACK) == []
    assert [right for _, _, right in _address_spellings(document)] == [True] * 8
    assert [read_only for _, _, read_only in _checkout_binds(document)] == [True] * 3


@pytest.mark.parametrize(
    "ports",
    ['["8081:8081"]', '["127.0.0.1::8081"]', "[{target: 8081, published: 0, mode: host}]"],
)
def test_a_published_port_is_reported(ports: str) -> None:
    """Every syntax compose takes, the ephemeral host port included."""
    text = _edited("    ports: []\n", f"    ports: {ports}\n")

    assert _isolation_problems(text) == ["services.resolver.ports: publishes a port on the host"]


def test_host_networking_is_reported() -> None:
    """On the host's network every port a service opens is a port on the host."""
    text = _edited("    ports: []\n", "    network_mode: host\n")

    assert _isolation_problems(text) == [
        "services.resolver.network_mode: runs on the host's network"
    ]


def test_an_address_in_an_anchor_is_reported_where_written_and_where_merged() -> None:
    text = _edited('"${FORA_E2E_NET}.200:3000"', '"172.30.10.11:3000"')

    assert _isolation_problems(text) == [
        "x-ledger-env.LEDGER_ADDRESS: names the fixed address 172.30.10.11",
        "services.app.environment.LEDGER_ADDRESS: names the fixed address 172.30.10.11",
    ]


def test_an_address_behind_an_alias_is_reported_at_each_use() -> None:
    text = _edited('  - "${FORA_E2E_NET}.153"\n', '  - "172.30.0.53"\n')

    assert _isolation_problems(text) == [
        "x-resolver[0]: names the fixed address 172.30.0.53",
        "services.app.dns[0]: names the fixed address 172.30.0.53",
    ]


def test_a_fixed_subnet_is_reported() -> None:
    """The shape that allowed one stack per daemon: a subnet written into the file."""
    text = _edited(
        "    external: true\n",
        "    ipam:\n      config:\n        - subnet: 172.30.0.0/16\n          ip_range: 172.30.0.0/24\n",
    )

    assert _isolation_problems(text) == [
        "networks.default.ipam.config[0].subnet: names the fixed address 172.30.0.0",
        "networks.default.ipam.config[0].ip_range: names the fixed address 172.30.0.0",
    ]


def test_every_address_in_one_value_is_reported() -> None:
    text = _edited(
        "--answer=/zone.example/${FORA_E2E_NET}.183",
        "--answer=/zone.example/172.30.0.83 --answer=/agents.example/172.30.0.84",
    )

    assert _isolation_problems(text) == [
        "services.resolver.command[2]: names the fixed address 172.30.0.83",
        "services.resolver.command[2]: names the fixed address 172.30.0.84",
    ]


def test_an_address_in_a_key_is_reported() -> None:
    text = _edited(
        "    dns: *resolver\n", "    dns: *resolver\n    labels:\n      peer-172.30.0.9: x\n"
    )

    assert _isolation_problems(text) == [
        "services.app.labels.peer-172.30.0.9: names the fixed address 172.30.0.9"
    ]


@pytest.mark.parametrize(
    ("written", "reported"),
    [
        ("$FORA_E2E_NET.183", "$FORA_E2E_NET.183"),
        ("${FORA_E2E_NET:-172.30.0}.183", "${FORA_E2E_NET:-172.30.0}.183"),
        ("${FORA_E2E_NET:?unset}.183", "${FORA_E2E_NET:?unset}.183"),
        ("${FORA_E2E_NET}.53", "${FORA_E2E_NET}.53"),
        ("${FORA_E2E_NET}.255", "${FORA_E2E_NET}.255"),
        ("${FORA_E2E_NET}.0/24", "${FORA_E2E_NET}.0"),
        ("${FORA_E2E_NET}", "${FORA_E2E_NET}"),
        ("${STACK_PREFIX:-172.30.0}.183", "${STACK_PREFIX:-172.30.0}.183"),
        ("172.30.0.${HOST}", "${HOST}"),
    ],
)
def test_an_address_written_any_other_way_is_reported(written: str, reported: str) -> None:
    """Each spelling either brings a fixed prefix back or lands where Docker hands out addresses."""
    text = _edited("ipv4_address: ${FORA_E2E_NET}.153", f'ipv4_address: "{written}"')

    assert _isolation_problems(text) == [_spelling_problem(reported)]


@pytest.mark.parametrize(
    "entry",
    [
        "./tests/state:/state",
        "./tests/state:/state:rw",
        "./tests/state:/state:z",
        "../shared:/state",
        ".:/workspace",
    ],
)
def test_a_writable_checkout_mount_is_reported(entry: str) -> None:
    text = _edited("      - state:/state\n", f"      - {entry}\n")
    source = entry.split(":", 1)[0]

    assert _isolation_problems(text) == [
        f"services.app.volumes[3]: binds {source} from the checkout without read-only"
    ]


@pytest.mark.parametrize(
    ("old", "new", "source"),
    [
        ("        read_only: true\n", "", "./config/extra"),
        (
            "        source: ./config/extra\n        target: /extra\n        read_only: true\n",
            "        source: config/extra\n        target: /extra\n",
            "config/extra",
        ),
    ],
)
def test_a_writable_long_syntax_mount_is_reported(old: str, new: str, source: str) -> None:
    """Compose reads a long-syntax source without a leading dot as a checkout path too."""
    text = _edited(old, new)

    assert _isolation_problems(text) == [
        f"services.app.volumes[2]: binds {source} from the checkout without read-only"
    ]


def test_the_stack_file_holds_what_the_rules_examine() -> None:
    """A pass on the real files means nothing if the rules found nothing to read.

    A renamed ``services`` key, a renamed prefix variable or a new mount syntax
    would leave the rules with nothing to examine, and the next case would pass.
    """
    document = yaml.safe_load(_COMPOSE.read_text()) or {}

    assert _services(document), f"{_COMPOSE.name} declares no services"
    assert any(right for _, _, right in _address_spellings(document)), (
        f"{_COMPOSE.name} writes no address as ${{{_NET}}}.<n>, so rule 3 read nothing"
    )
    assert any(read_only for _, _, read_only in _checkout_binds(document)), (
        f"{_COMPOSE.name} mounts no checkout path read-only, so rule 4 read nothing"
    )


def test_no_compose_file_a_gate_loads_ties_the_stack_to_the_machine() -> None:
    files = _gate_compose_files()
    assert _COMPOSE in files, f"{_COMPOSE.name} is not among the files this guard reads: {files}"

    found = [
        f"{path.name}: {problem}"
        for path in files
        for problem in _isolation_problems(path.read_text())
    ]

    assert found == [], (
        "a compose file a gate loads ties the stack to the machine or to another stack. "
        f"Publish no port and use no host networking (host access goes in {_HOST_ACCESS_OPT_IN}); "
        f"write no address, only ${{{_NET}}}.<n> with n from 128 to 254; mount no checkout "
        "folder with write access (keep the data in a named volume):\n" + "\n".join(found)
    )
