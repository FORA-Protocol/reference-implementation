"""Meta-guard — each demo publisher's origin answers with that publisher's own discovery documents.

The demo publishers serve their discovery documents (``/.well-known/fora.json``
and ``/rsl.txt``) from the stack's static origin, the ``publisher`` service, the
way any publisher serves its own files. The publishers share that one nginx, so
each one's edge forwards to a port of its own and the port picks whose documents
answer. Four files state that and nothing running checks them against one
another, and a mismatch fails as something unrelated: a catalog push refused
for its payee or its contributor, the Broker routing a resolve to the wrong
Exchange.

* THE PORTS. Each HTTP edge's ``ORIGIN_URL`` (``docker-compose.e2e.yml``) is a
  port the origin listens on, and ``deploy/publisher/nginx.conf`` maps that port
  to the documents of the publisher the edge fronts: the directory named after
  the domain the edge answers as, its network alias. The origin mounts the
  documents where the map looks for them.
* THE DOCUMENTS. Each publisher's ``fora.json`` is a ``ROLE_PUBLISHER`` document
  for its own domain that names exactly one Exchange, at that Exchange's own
  address, with the demo payee the boot fixtures declare. For an edge-fronted
  publisher it is the Exchange the seed's ingest pushes that publisher's catalog
  to (``seed.E2E_PUBLISHERS``), and the document lists the harness contributor,
  which pushes on the publisher's behalf. The Lambda@Edge publisher has no edge on
  this network; its documents sit on a port of their own, the origin CloudFront
  would fetch them from, and name the Exchange its function verifies against
  (``tests/e2e/lambda-edge/write-configs.mjs``). The profile list in each document
  is held to the Exchange's by ``internal/forawellknown/profiles_test.go``.
* NOTHING ELSE. Every document directory is mapped by exactly one port, and every
  mapped directory exists.
* THE HARNESS. ``publisher_origins.PUBLISHER_ORIGINS``, which the tests read a
  publisher's documents from in an edge's or CloudFront's place, names for each
  publisher the port the nginx map answers it on.

Pure file reading: no stack, no Docker. The e2e runner image carries none of
these files, so the suite skips there and runs in the per-commit tier.
"""

from __future__ import annotations

import copy
import json
import re
from collections.abc import Callable
from typing import Any

import pytest
import yaml

from .conftest import REPO_ROOT
from .e2e_catalog import CATALOG_CONTRIBUTOR_ID, DEMO_PUBLISHER_ID
from .guard_harness import guard_marks
from .lambda_edge import PUBLISHER as LAMBDA_PUBLISHER
from .publisher_origins import PUBLISHER_ORIGINS
from .seed import E2E_PUBLISHERS

_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"
_NGINX = REPO_ROOT / "deploy/publisher/nginx.conf"
_DOCUMENTS = REPO_ROOT / "tests/e2e/mock-publisher/documents"
_LAMBDA_CONFIGS = REPO_ROOT / "tests/e2e/lambda-edge/write-configs.mjs"

pytestmark = guard_marks(
    skip_when=not (
        _COMPOSE.is_file()
        and _NGINX.is_file()
        and _DOCUMENTS.is_dir()
        and _LAMBDA_CONFIGS.is_file()
    ),
    reason="the compose file, the origin's nginx.conf, the documents or the Lambda configs are absent",
)

#: The services that put an edge in front of a demo publisher and forward to the origin.
_HTTP_EDGES = ("edge", "fastly-edge", "aws-edge")
_ORIGIN_SERVICE = "publisher"
#: Where the origin mounts the documents, and the tree it mounts there.
_DOCUMENTS_MOUNT = "/usr/share/nginx/documents"
_DOCUMENTS_VOLUME = f"./tests/e2e/mock-publisher/documents:{_DOCUMENTS_MOUNT}:ro"
_ORIGIN_URL = re.compile(rf"^http://{_ORIGIN_SERVICE}:(\d+)$")
_LISTEN = re.compile(r"^\s*listen\s+(\d+);", re.MULTILINE)
_MAP = re.compile(r"map \$server_port \$publisher_documents \{(.*?)\}", re.DOTALL)
_MAP_ENTRY = re.compile(rf"^\s*(\d+)\s+{re.escape(_DOCUMENTS_MOUNT)}/([^;\s]+);", re.MULTILINE)
_LAMBDA_EXCHANGE = re.compile(r"^const EXCHANGE_URL = 'http://([^']+)';", re.MULTILINE)
#: The contributor an edge-fronted publisher authorizes: the harness's own.
_HARNESS_CONTRIBUTOR = {"domain": CATALOG_CONTRIBUTOR_ID, "relationship": "harness"}


def _nginx() -> dict[str, Any]:
    text = _NGINX.read_text()
    block = _MAP.search(text)
    return {
        "listens": {int(p) for p in _LISTEN.findall(text)},
        "ports": {int(p): d for p, d in _MAP_ENTRY.findall(block.group(1) if block else "")},
    }


def _documents() -> dict[str, Any]:
    return {
        path.parent.parent.name: json.loads(path.read_text())
        for path in sorted(_DOCUMENTS.glob("*/.well-known/fora.json"))
    }


def _lambda_exchange() -> str:
    found = _LAMBDA_EXCHANGE.search(_LAMBDA_CONFIGS.read_text())
    return found.group(1) if found else ""


def _services() -> dict[str, Any]:
    services: dict[str, Any] = yaml.safe_load(_COMPOSE.read_text())["services"]
    return services


def _edge_problem(service: str, svc: dict[str, Any], nginx: dict[str, Any]) -> str | None:
    """The first way ``service`` fails to reach its own publisher's documents, or None."""
    aliases = ((svc.get("networks") or {}).get("default") or {}).get("aliases") or []
    origin = str((svc.get("environment") or {}).get("ORIGIN_URL", ""))
    port = _ORIGIN_URL.match(origin)
    if len(aliases) != 1 or port is None:
        return f"{service} fronts {aliases} and forwards to {origin!r}, not one publisher's origin port"
    number = int(port.group(1))
    if number not in nginx["listens"]:
        return f"{service} forwards to port {number}, which the origin does not listen on"
    mapped = nginx["ports"].get(number)
    if mapped is None:
        return f"{service} forwards to port {number}, which answers with no publisher's documents"
    if mapped != aliases[0]:
        return f"{service} forwards to port {number}, which answers with {mapped}'s documents, not {aliases[0]}'s"
    return None


def _document_problem(
    domain: str, document: dict[str, Any], exchange: str, edge_fronted: bool
) -> str | None:
    """The first way ``domain``'s fora.json differs from what its publisher must attest, or None."""
    if document.get("role") != "ROLE_PUBLISHER" or document.get("domain") != domain:
        return f"{domain}'s fora.json is {document.get('role')} for {document.get('domain')!r}"
    entry = {
        "domain": exchange,
        "endpoint": f"http://{exchange}",
        "ext": {"resource_owner_id": DEMO_PUBLISHER_ID},
    }
    listed = [{k: e.get(k) for k in entry} for e in document.get("exchanges") or []]
    if listed != [entry]:
        return f"{domain}'s fora.json lists the exchanges {listed}, not only {entry}"
    contributors = document.get("catalog_contributors") or []
    want = [_HARNESS_CONTRIBUTOR] if edge_fronted else []
    if contributors != want:
        return f"{domain}'s fora.json lists the contributors {contributors}, not {want}"
    return None


def _harness_problems(
    nginx: dict[str, Any], documents: dict[str, Any], known: set[str], origins: dict[str, str]
) -> list[str]:
    """Every way the harness's origin table disagrees with the ports the origin answers on."""
    by_domain = {domain: port for port, domain in nginx["ports"].items()}
    problems = []
    for domain in sorted(set(documents) & known):
        origin = origins.get(domain)
        if origin is None:
            problems.append(f"the harness has no origin for {domain}")
            continue
        port = _ORIGIN_URL.match(origin)
        if domain in by_domain and (port is None or by_domain[domain] != int(port.group(1))):
            problems.append(
                f"the harness reads {domain}'s documents at {origin!r}, "
                "not the port the origin answers it on"
            )
    return problems


def _problems(
    services: dict[str, Any],
    nginx: dict[str, Any],
    documents: dict[str, Any],
    lambda_exchange: str,
    origins: dict[str, str],
) -> list[str]:
    """Every way the origin's ports, its map and the documents disagree with each other or the harness."""
    problems = []
    if _DOCUMENTS_VOLUME not in (services.get(_ORIGIN_SERVICE) or {}).get("volumes", []):
        problems.append(f"{_ORIGIN_SERVICE} does not mount {_DOCUMENTS_VOLUME}")
    for service in _HTTP_EDGES:
        problem = _edge_problem(service, services.get(service) or {}, nginx)
        if problem:
            problems.append(problem)
    exchanges = {domain: exchange for domain, exchange, _feed, _key in E2E_PUBLISHERS}
    exchanges[LAMBDA_PUBLISHER] = lambda_exchange
    for domain, document in sorted(documents.items()):
        if domain not in exchanges:
            problems.append(f"{domain} has documents but is no publisher the harness knows")
            continue
        problem = _document_problem(domain, document, exchanges[domain], domain != LAMBDA_PUBLISHER)
        if problem:
            problems.append(problem)
    mapped = list(nginx["ports"].values())
    problems += [
        f"no port answers with {domain}'s documents"
        for domain in sorted(documents)
        if domain not in mapped
    ]
    problems += [
        f"port {port} answers with {domain}'s documents, which do not exist"
        for port, domain in sorted(nginx["ports"].items())
        if domain not in documents
    ]
    problems += [
        f"{domain}'s documents are mapped by more than one port"
        for domain in sorted(set(mapped))
        if mapped.count(domain) > 1
    ]
    if LAMBDA_PUBLISHER not in documents:
        problems.append(f"the Lambda@Edge publisher {LAMBDA_PUBLISHER} has no documents")
    return problems + _harness_problems(nginx, documents, set(exchanges), origins)


def test_each_demo_publishers_origin_answers_with_its_own_documents() -> None:
    """The compose file, the origin's nginx.conf, the documents and the harness's constants agree."""
    assert (
        _problems(_services(), _nginx(), _documents(), _lambda_exchange(), PUBLISHER_ORIGINS) == []
    )


# The edits below each break one thing the check exists to catch, on a copy of
# the real files, and require the check to report exactly it.


def _swapped_port(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    services["edge"]["environment"]["ORIGIN_URL"] = "http://publisher:8002"


def _unlistened_port(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    services["aws-edge"]["environment"]["ORIGIN_URL"] = "http://publisher:8009"


def _origin_on_port_80(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    services["fastly-edge"]["environment"]["ORIGIN_URL"] = "http://publisher:80"


def _dropped_mount(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    services["publisher"]["volumes"].remove(_DOCUMENTS_VOLUME)


def _wrong_domain(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    documents["music.demo.fora-protocol.org"]["domain"] = "demo.fora-protocol.org"


def _wrong_exchange(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    documents["sfx.demo.fora-protocol.org"]["exchanges"][0]["domain"] = "exchange:8081"


def _other_payee(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    documents["demo.fora-protocol.org"]["exchanges"][0]["ext"]["resource_owner_id"] = "someone-else"


def _dropped_contributor(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    del documents["music.demo.fora-protocol.org"]["catalog_contributors"]


def _lambda_on_another_exchange(
    services: dict, nginx: dict, documents: dict, origins: dict
) -> None:
    documents[LAMBDA_PUBLISHER]["exchanges"][0]["endpoint"] = "http://exchange-b:8081"


def _unmapped_documents(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    del nginx["ports"][8004]


def _documents_nobody_knows(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    documents["elsewhere.example"] = copy.deepcopy(documents["demo.fora-protocol.org"])
    nginx["ports"][8009] = "elsewhere.example"
    nginx["listens"].add(8009)


def _harness_on_another_port(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    origins["music.demo.fora-protocol.org"] = "http://publisher:8004"


def _harness_without_an_origin(services: dict, nginx: dict, documents: dict, origins: dict) -> None:
    del origins[LAMBDA_PUBLISHER]


@pytest.mark.parametrize(
    ("edit", "fragment"),
    [
        pytest.param(
            _swapped_port, "edge forwards to port 8002, which answers with", id="swapped-port"
        ),
        pytest.param(
            _unlistened_port,
            "aws-edge forwards to port 8009, which the origin does not",
            id="listen",
        ),
        pytest.param(
            _origin_on_port_80,
            "fastly-edge forwards to port 80, which answers with no",
            id="port-80",
        ),
        pytest.param(_dropped_mount, "publisher does not mount", id="mount"),
        pytest.param(
            _wrong_domain,
            "music.demo.fora-protocol.org's fora.json is ROLE_PUBLISHER for",
            id="domain",
        ),
        pytest.param(
            _wrong_exchange,
            "sfx.demo.fora-protocol.org's fora.json lists the exchanges",
            id="exchange",
        ),
        pytest.param(
            _other_payee, "demo.fora-protocol.org's fora.json lists the exchanges", id="payee"
        ),
        pytest.param(
            _dropped_contributor,
            "music.demo.fora-protocol.org's fora.json lists the contributors",
            id="contributor",
        ),
        pytest.param(
            _lambda_on_another_exchange,
            f"{LAMBDA_PUBLISHER}'s fora.json lists the exchanges",
            id="lambda",
        ),
        pytest.param(
            _unmapped_documents,
            f"no port answers with {LAMBDA_PUBLISHER}'s documents",
            id="unmapped",
        ),
        pytest.param(
            _documents_nobody_knows,
            "elsewhere.example has documents but is no publisher",
            id="unknown",
        ),
        pytest.param(
            _harness_on_another_port,
            "the harness reads music.demo.fora-protocol.org's documents at",
            id="harness-port",
        ),
        pytest.param(
            _harness_without_an_origin,
            f"the harness has no origin for {LAMBDA_PUBLISHER}",
            id="harness-missing",
        ),
    ],
)
def test_the_documents_check_reports_each_mistake(
    edit: Callable[[dict, dict, dict, dict], None], fragment: str
) -> None:
    """Each mistake the check exists to catch makes it report that mistake and nothing else."""
    services, nginx, documents, origins = (
        copy.deepcopy(_services()),
        copy.deepcopy(_nginx()),
        copy.deepcopy(_documents()),
        dict(PUBLISHER_ORIGINS),
    )
    edit(services, nginx, documents, origins)
    problems = _problems(services, nginx, documents, _lambda_exchange(), origins)
    assert len(problems) == 1 and problems[0].startswith(fragment), problems
