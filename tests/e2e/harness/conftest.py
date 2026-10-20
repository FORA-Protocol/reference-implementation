"""Shared fixtures for the docker-compose E2E harness.

Scope: a `compose_stack` session fixture that either (a) assumes the stack
is already up (``FORA_E2E_REUSE_STACK=1``, which the runner container sets) or
(b) brings a stack up from the host before tests and takes it down after. The
host run reaches the stack through published ports, so (b) adds the opt-in
``docker-compose.e2e.ports.yml``; the stack goes through
``scripts/e2e-stack.sh``, which gives it its own network, under the project
``_compose.host_project()`` names: ``E2E_PROJECT``, or by default
``<checkout>-e2e``, which is also the Makefile's default unless
``COMPOSE_PROJECT`` or ``COMPOSE_PROJECT_NAME`` is set.
"""

from __future__ import annotations

import os
import subprocess
from collections.abc import Iterator
from pathlib import Path

import pytest

# COMPOSE_FILE and REPO_ROOT are resolved in _compose.py, beside the port lookup
# that reads them, and re-exported from here under the names every suite already
# imports.
from ._compose import COMPOSE_FILE, REPO_ROOT, host_project, resolve_host_port
from .e2e_catalog import SeededFixture
from .fresh_agent import AgentFactory, FreshAgent
from .publisher_accounts import PublisherFactory
from .exchanges import (
    EXCHANGE_A_INTERNAL_URL,
    EXCHANGE_B_INTERNAL_URL,
    EXCHANGE_C_INTERNAL_URL,
    EXCHANGE_D_INTERNAL_URL,
)
from .lambda_edge import wait_ready as wait_lambda_ready
from .mcp_agents import McpAgentFactory
from .capture_proxy import CONTROL_URL as CAPTURE_PROXY_URL
from .capture_proxy import CaptureProxy
from .misbehaving_exchange import CONTROL_URL as MISBEHAVING_EXCHANGE_URL
from .misbehaving_exchange import MisbehavingExchange
from .mcp_session import (
    await_identity,
    mcp_session_agent,
    mcp_session_bearer,
    mcp_spending_agent,
    mcp_unregistered_agent,
    require_in_network,
)

from .readiness import wait_healthy

# Re-export: the existing tests import StackURLs from conftest; its home is
# stack_urls.py so non-pytest consumers (smoke_staging.py) can import it
# without touching a pytest-convention file.
from .publish_harness import make_public_remote, make_source_repo
from .stack_urls import StackURLs
from .wba_oracle_ts import TSOracle

# The obligation registry and its report (ADR-028 D1).
pytest_plugins = ["harness.obligation_registry"]


@pytest.fixture(scope="session")
def ts_oracle() -> TSOracle:
    oracle = TSOracle()
    oracle.wait_ready()
    return oracle


def _resolve_stack_urls(compose_file: Path) -> StackURLs:
    """Resolve service URLs for the running stack.

    Inside the compose network (``FORA_E2E_IN_NETWORK=1``) services
    reach each other by docker-DNS name. From the host we read the
    ephemeral host ports Docker published for the stack ``host_project()``
    names, whether this session brought it up or reuses it
    (``FORA_E2E_REUSE_STACK=1``). The project is passed, never discovered:
    other stacks of this checkout may be running from the same compose file.
    """
    # The edges now listen on port 80 so the demo publisher-domain aliases
    # resolve natively for both the manifest fetch and the signed-URL content
    # fetch (Phase 2b). Service-DNS URLs target port 80; host runs discover the
    # ephemeral host port mapped to container port 80.
    if os.environ.get("FORA_E2E_IN_NETWORK") == "1":
        return StackURLs(
            exchange=EXCHANGE_A_INTERNAL_URL,
            exchange_b=EXCHANGE_B_INTERNAL_URL,
            exchange_c=EXCHANGE_C_INTERNAL_URL,
            exchange_d=EXCHANGE_D_INTERNAL_URL,
            broker="http://broker:8082",
            edge="http://edge:80",
            aws_edge="http://aws-edge:80",
            fastly_edge="http://fastly-edge:80",
            lambda_edge="http://lambda-edge:8080",
            lambda_edge_no_wba="http://lambda-edge-no-wba:8080",
            identity="http://identity",
            zitadel="http://zitadel:8080",
            console="http://console:8085",
            agent_directory="http://agent-directory",
            sites="http://sites",
            misbehaving_exchange=MISBEHAVING_EXCHANGE_URL,
            capture_proxy=CAPTURE_PROXY_URL,
            zitadel_publishers="http://zitadel-publishers:8080",
            mailpit="http://mailpit:8025",
        )
    project = host_project()

    def url(service: str, container_port: int) -> str:
        port = resolve_host_port(compose_file, service, container_port, project=project)
        return f"http://127.0.0.1:{port}"

    return StackURLs(
        exchange=url("exchange", 8081),
        exchange_b=url("exchange-b", 8081),
        exchange_c=url("exchange-c", 8081),
        exchange_d=url("exchange-d", 8081),
        broker=url("broker", 8082),
        edge=url("edge", 80),
        aws_edge=url("aws-edge", 80),
        fastly_edge=url("fastly-edge", 80),
        lambda_edge=url("lambda-edge", 8080),
        lambda_edge_no_wba=url("lambda-edge-no-wba", 8080),
        identity="",
        zitadel="",
        console=url("console", 8085),
        agent_directory=url("agent-directory", 80),
    )


_E2E_STACK = REPO_ROOT / "scripts" / "e2e-stack.sh"
_HOST_PORTS_FILE = REPO_ROOT / "docker-compose.e2e.ports.yml"


def _compose(*args: str) -> None:
    """Run compose against the host run's own stack, creating its network for ``up``.

    The network is created before ``up`` and removed after ``down``, as the
    Makefile's e2e-up and e2e-down do.
    """
    project = host_project()
    if args[0] == "up":
        subprocess.run([str(_E2E_STACK), "network-up", project], check=True, cwd=REPO_ROOT)
    cmd = [str(_E2E_STACK), "compose", project, "-f", str(_HOST_PORTS_FILE), *args]
    subprocess.run(cmd, check=True, cwd=REPO_ROOT)
    if args[0] == "down":
        subprocess.run([str(_E2E_STACK), "network-down", project], check=True, cwd=REPO_ROOT)


@pytest.fixture(scope="session")
def seeded(compose_stack: StackURLs):
    """Seed the demo catalog once per session and expose its resources.

    Runs the production ``fora-ingest`` binary over the three demo feeds
    (one signed PushResources RPC per feed) after provisioning each exchange's
    seat and demo sites and registering the buyers. Shared session-wide so the
    ingest runs once; every suite consumes the same demo-catalog resources.
    """
    from .seed import seed_stack, wait_until_broker_routes

    fixture: SeededFixture = seed_stack(
        str(COMPOSE_FILE),
        compose_stack.exchange,
        exchange_b_host_url=compose_stack.exchange_b,
        exchange_c_host_url=compose_stack.exchange_c,
        exchange_d_host_url=compose_stack.exchange_d,
    )
    wait_until_broker_routes(compose_stack)
    return fixture


@pytest.fixture
def fresh_agent_factory(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed opens the seats a fresh agent funds itself at
    tmp_path: Path,
) -> Iterator[AgentFactory]:
    """Mints agents of this test's own: ``agent = fresh_agent_factory()``, once per agent needed.

    For a test that needs more than one party. Every agent is new to the stack:
    a new key under a new host, known to no Exchange before its first call, so
    nothing a previous test or a previous run did reaches it (ADR-028 D7). See
    ``fresh_agent.py``.
    """
    factory = AgentFactory(compose_stack, tmp_path)
    yield factory
    factory.close()


@pytest.fixture
def fresh_agent(fresh_agent_factory: AgentFactory) -> FreshAgent:
    """One agent of this test's own; ``fresh_agent.fund(...)`` opens its accounts."""
    return fresh_agent_factory()


@pytest.fixture
def misbehaving_exchange(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the Broker routes once the seed waited for it
) -> MisbehavingExchange:
    """The Exchange impostor the Broker's fault-path suites buy through.

    In-network only: the impostor publishes no host port, and the Broker reaches it
    by network aliases only the compose network resolves. See
    ``misbehaving_exchange.py``.
    """
    if not compose_stack.misbehaving_exchange:
        pytest.skip("the Exchange impostor is reachable only inside the compose network")
    return MisbehavingExchange(compose_stack.misbehaving_exchange)


@pytest.fixture
def capture_proxy(compose_stack: StackURLs) -> CaptureProxy:
    """The record of what the stack's services sent the Broker and the Exchanges.

    In-network only: the proxy publishes no host port. A test reads only the
    traffic of a scope of its own, through ``capture_proxy.watch(...)``. See
    ``capture_proxy.py``.
    """
    if not compose_stack.capture_proxy:
        pytest.skip("the capture proxy is reachable only inside the compose network")
    return CaptureProxy(compose_stack.capture_proxy)


@pytest.fixture
def fresh_publisher(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: registration needs the seed's seat
) -> Iterator[PublisherFactory]:
    """Mints publishers of this test's own: ``fresh_publisher(verified=True)``, once per publisher.

    Each call creates a login the login provider has never seen and signs it in to
    the console, so no account, publisher, domain or sale a previous test or run
    made reaches it (ADR-028 D7). ``verified=False`` creates the login with an
    address the provider has not verified. See ``publisher_accounts.py``.

    In-network only: the sign-in goes through the provider, whose address resolves
    only inside the compose network, and the runner holds the provider's admin
    token only there.
    """
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1" or not compose_stack.sites:
        pytest.skip("a fresh publisher signs in through the provider, in-network only")
    factory = PublisherFactory(compose_stack)
    yield factory
    factory.close()


@pytest.fixture(scope="session")
def compose_stack() -> Iterator[StackURLs]:
    """Bring the docker-compose stack up for the whole test session."""
    if os.environ.get("FORA_E2E_SKIP") == "1":
        pytest.skip("FORA_E2E_SKIP=1")

    reuse = os.environ.get("FORA_E2E_REUSE_STACK") == "1"
    if not reuse:
        _compose("up", "-d", "--build")
    try:
        urls = _resolve_stack_urls(COMPOSE_FILE)
        wait_healthy(f"{urls.exchange}/healthz")
        # Multi-exchange topology: exchange-b/c/d must be reachable too.
        wait_healthy(f"{urls.exchange_b}/healthz")
        wait_healthy(f"{urls.exchange_c}/healthz")
        wait_healthy(f"{urls.exchange_d}/healthz")
        wait_healthy(f"{urls.broker}/healthz")
        wait_healthy(f"{urls.edge}/healthz")
        wait_healthy(f"{urls.aws_edge}/healthz")
        wait_healthy(f"{urls.console}/healthz")
        # Fastly Compute (Viceroy) has no /healthz; its readiness probe is the
        # well-known manifest. Docker reports the container healthy once its
        # internal 127.0.0.1 probe passes, but Viceroy may not yet be bound on
        # the bridge interface — so PushResources-driven manifest fetches from
        # the Exchange to fastly-edge race connection-refused for the first few
        # seconds on a cold stack. Wait on the bridge-reachable URL too.
        wait_healthy(f"{urls.fastly_edge}/.well-known/fora.json")
        # The Lambda runtime emulator serves no GET route at all — readiness is
        # an actual function invocation, so it needs its own poller.
        wait_lambda_ready(urls.lambda_edge)
        wait_lambda_ready(urls.lambda_edge_no_wba)
        yield urls
    finally:
        if not reuse and os.environ.get("FORA_E2E_KEEP_UP") != "1":
            _compose("down", "-v")


# ---------------------------------------------------------------------------
# ADR-008 D5 — `shared-clean-fixtures` cleanup chain.
#
# The `stack_isolation` marker scaffolding — the marker registration,
# the collection check and the autouse dispatch fixture — is defined
# further down this same file. ``SHARED_CLEAN_FIXTURES`` is a plain list
# of zero-argument callables the dispatch fixture runs, in list order,
# before each test in that mode; a test in any other isolation mode never
# runs it. Further cleanup callables are appended to it without any of
# them having to know about the others.
#
# The chain is empty. Its one entry used to write probe_state='LIVE' into
# the Broker's database before every test, for any Exchange row a probe
# pass had moved off LIVE. That is test-side state the Broker owns, and
# the Broker's own refresher already maintains it: every pass probes every
# unblocked row, so a row a pass marked DOWN comes back on the next pass
# once the Exchange answers. What an Exchange "answering" means is now the
# Exchange's own to say: the Broker probes the health_endpoint its
# manifest names, and each Exchange this stack seeds publishes one. A row
# that stays off LIVE is an Exchange that really is not answering, which a
# test should see rather than have rewritten under it.
#
# There is no reporting-obligation purge either. An unreported obligation
# counts against an agent only once its deadline has passed, and the
# deployment's default window is 24 hours. The tests that shorten a seat's
# window (the money-report, reporting-overdue, admin-plane and tenant-isolation
# suites) buy as their own fresh agent while it is short and restore the
# default however they end, and each obligation keeps the window it was minted
# with. So on a stack brought up for the run no obligation of the fixed demo
# buyers comes due, and a purge would restore nothing a test reads. A stack kept up for more than a day
# is different: the fixed demo buyers then carry due, unreported
# obligations and are refused REPORTING_OVERDUE. The remedy for that is a
# buyer per test (ADR-028 D7), not a truncate of the Exchange's table: a
# test arranges state only through the public surfaces, never by writing a
# datastore. The ``fresh_agent`` fixture below is that buyer; the money-path,
# ledger, idempotency and reporting suites buy as one.
# ---------------------------------------------------------------------------
from collections.abc import Callable  # noqa: E402

SHARED_CLEAN_FIXTURES: list[Callable[[], object]] = []


# ---------------------------------------------------------------------------
# ADR-008 D5 — `stack_isolation` marker contract.
#
# Every test under tests/e2e/harness/obligations/ MUST declare a
# `stack_isolation` marker with one of three modes:
#
#   * "isolated"                — the orchestration tears down and rebuilds
#                                 shared infra between this test and the
#                                 next. Reserved for tests that mutate
#                                 persistent state irrecoverably.
#   * "shared-clean-fixtures"   — DEFAULT for the bulk of the suite. The
#                                 `SHARED_CLEAN_FIXTURES` chain runs before
#                                 each such test.
#   * "shared-without-cleanup"  — no setup/teardown. Reserved for tests
#                                 that observe stable cross-test invariants
#                                 (service-health probes, version sentinels)
#                                 and are genuinely indifferent to state.
#
# The `--strict-markers` flag in tests/e2e/pyproject.toml makes any
# unknown marker fail the collection. The `pytest_collection_modifyitems`
# hook below additionally requires that every test in the obligations
# tree DECLARE a mode — silence is not a default, it is a collection
# error.
# ---------------------------------------------------------------------------

_STACK_ISOLATION_MODES = frozenset(
    {"isolated", "shared-clean-fixtures", "shared-without-cleanup"},
)
_OBLIGATIONS_DIR_PART = "obligations"


def pytest_configure(config: pytest.Config) -> None:
    """Register the `stack_isolation` marker so --strict-markers accepts it."""
    config.addinivalue_line(
        "markers",
        "stack_isolation(mode): declare per-test isolation contract — one of "
        "'isolated', 'shared-clean-fixtures', 'shared-without-cleanup' (ADR-008 D5)",
    )


def pytest_collection_modifyitems(
    config: pytest.Config,  # noqa: ARG001 — required hook signature
    items: list[pytest.Item],
) -> None:
    """Fail collection when an obligation test omits `stack_isolation`.

    Per ADR-008 D5: every test in the obligations tree MUST declare its
    isolation contract. The default ("shared-clean-fixtures") still
    applies once declared, but silence is treated as a forgotten obligation
    and collection refuses it.
    """
    errors: list[str] = []
    for item in items:
        path = str(getattr(item, "path", "") or item.fspath)
        if f"/{_OBLIGATIONS_DIR_PART}/" not in path and not path.endswith(
            f"/{_OBLIGATIONS_DIR_PART}"
        ):
            continue
        marker = item.get_closest_marker("stack_isolation")
        if marker is None:
            errors.append(
                f"{item.nodeid}: missing @pytest.mark.stack_isolation(...) "
                "(ADR-008 D5 — required for tests under tests/e2e/harness/obligations/)"
            )
            continue
        if not marker.args or marker.args[0] not in _STACK_ISOLATION_MODES:
            valid = ", ".join(sorted(_STACK_ISOLATION_MODES))
            errors.append(
                f"{item.nodeid}: stack_isolation mode "
                f"{marker.args[0] if marker.args else '<missing>'!r} not in {{{valid}}}"
            )
    if errors:
        msg = "ADR-008 D5 stack_isolation contract violations:\n  - " + "\n  - ".join(errors)
        raise pytest.UsageError(msg)


@pytest.fixture(autouse=True)
def _stack_isolation_dispatch(request: pytest.FixtureRequest) -> Iterator[None]:
    """Per-test hook that reads the declared mode and runs the right chain.

    `shared-clean-fixtures` (default for declared tests) runs every callable
    in `SHARED_CLEAN_FIXTURES` before the test, in list order. `isolated` and `shared-without-cleanup`
    are no-ops at the per-test boundary; `isolated` relies on the
    orchestration layer to rebuild the stack between tests.

    Tests outside the obligations tree without the marker fall through
    silently — the marker is mandatory only inside the obligations tree
    (enforced by the collection hook above).
    """
    marker = request.node.get_closest_marker("stack_isolation")
    mode = marker.args[0] if marker is not None and marker.args else "shared-clean-fixtures"
    if mode == "shared-clean-fixtures":
        for cleanup in SHARED_CLEAN_FIXTURES:
            cleanup()
    yield


@pytest.fixture
def publish_fixture(tmp_path: Path) -> tuple[Path, Path, Path]:
    """A source repo, a bare stand-in for the public remote, and a worktree path.

    Shared by the three publish guard suites. It sits here rather than in
    ``publish_harness`` so each can request it by name without re-exporting a
    fixture, which pytest reads as a redefinition.
    """
    source = tmp_path / "source"
    bare = tmp_path / "public.git"
    worktree = tmp_path / "worktree"
    make_source_repo(source)
    make_public_remote(bare, source)
    return source, bare, worktree


@pytest.fixture(scope="session")
def identity_ready(compose_stack: StackURLs) -> StackURLs:
    """Skip unless the MCP endpoint is reachable, then wait for it to answer.

    The order is the point: the skip comes first so a host run leaves without
    touching anything, and the wait comes before any caller mints a bearer.
    Returns the stack, so a test taking this does not also have to take
    ``compose_stack`` to reach the endpoint it just waited for.

    Session-scoped because the endpoint does not become unready again once it has
    answered. Per test, the poll ran for every one of the eight consumers: seven
    reach it through ``mcp_bearer``, and the bearer-less negative takes it
    directly.
    """
    require_in_network(compose_stack)
    await_identity(compose_stack)
    return compose_stack


@pytest.fixture
def fresh_mcp_agent_factory(identity_ready: StackURLs) -> McpAgentFactory:
    """Signs up MCP agents of this test's own: ``agent = fresh_mcp_agent_factory()``.

    Each call imports a login the provider has never seen and signs it up at the
    identity service, so the agent holds no account, purchase or report any other
    test or run made (ADR-028 D7). See ``mcp_agents.py``. A sign-up costs a login
    flow, so a test asks for only as many agents as its claim needs.
    """
    return McpAgentFactory(identity_ready)


@pytest.fixture(scope="session")
def mcp_agent(identity_ready: StackURLs) -> str:
    """The one agent this session's MCP suites act as, signed up once.

    Session-scoped because sign-up is the expensive half of the flow and repeating
    it bought nothing. ``mcp_session_agent`` owns that rationale — what the flow
    costs, what stays idempotent across a repeat and what does not — and this
    fixture only binds it to a session and to the readiness poll above.
    """
    return mcp_session_agent(identity_ready)


@pytest.fixture
def mcp_bearer(mcp_agent: str) -> str:
    """A bearer for that agent, minted per test.

    Per test rather than per session because the bearer's TTL is ten minutes and
    a long run would outlive one. Minting makes no network call, so this is the
    cheap half.

    Separate from ``identity_ready`` rather than folded into it because the
    bearer-less negative needs the readiness half and must not have this one — it
    would be asking for a credential it exists to prove it can do without.
    """
    return mcp_session_bearer(mcp_agent)


@pytest.fixture(scope="session")
def unregistered_agent(identity_ready: StackURLs) -> str:
    """The agent that has no billing account at any exchange, signed up once.

    Session-scoped for the same reason as ``mcp_agent``: the sign-up is the
    expensive half. ``mcp_unregistered_agent`` states the reservation this rests
    on and why a consumer checks it rather than assumes it.
    """
    return mcp_unregistered_agent(identity_ready)


@pytest.fixture
def unregistered_bearer(unregistered_agent: str) -> str:
    """A bearer for the unregistered agent, minted per test like ``mcp_bearer``."""
    return mcp_session_bearer(unregistered_agent)


@pytest.fixture(scope="session")
def spending_agent(identity_ready: StackURLs) -> str:
    """The agent only the balance suite registers and spends as, signed up once."""
    return mcp_spending_agent(identity_ready)


@pytest.fixture
def spending_bearer(spending_agent: str) -> str:
    """A bearer for the spending agent, minted per test like ``mcp_bearer``."""
    return mcp_session_bearer(spending_agent)
