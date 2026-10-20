"""Service URL tuple shared by the pytest harness and standalone scripts.

Lives outside conftest.py so non-pytest consumers (smoke_staging.py) can
import it without depending on a pytest-convention file; conftest re-exports
it for the existing test imports.
"""

from __future__ import annotations

from typing import NamedTuple


class StackURLs(NamedTuple):
    """Host-side URLs for each compose service."""

    exchange: str
    # Multi-exchange topology: exchange-a is `exchange`; exchange-b serves
    # the music catalog (DB fora_b), exchange-c the sfx catalog (DB fora_c),
    # exchange-d the archive catalog (DB fora_d) on a seat that refuses the
    # Broker relay.
    exchange_b: str
    exchange_c: str
    exchange_d: str
    broker: str
    edge: str
    aws_edge: str
    fastly_edge: str
    # Lambda@Edge on the AWS Lambda runtime emulator. These are the base URLs of
    # the invoke endpoint's host, not of an HTTP server: the emulator answers
    # only POSTs to the function-invocation path (see harness/lambda_edge.py).
    # lambda_edge_no_wba runs the same worker built without a publisher signing
    # key, so its Web Bot Auth directory route answers 404.
    lambda_edge: str
    lambda_edge_no_wba: str
    # The identity service (Go MCP adapter) and its OIDC provider. identity is
    # empty on host runs — it publishes no host port, so the MCP-endpoint test is
    # in-network only.
    identity: str
    zitadel: str
    # The publisher console. Host runs resolve its published port, but signing
    # in goes through the login provider, whose address resolves only inside
    # the compose network, so the console suite skips unless it runs there.
    # Empty only for callers outside the compose harness that have no console.
    console: str = ""
    # The key-directory host of the per-test agents (see fresh_agent.py), where
    # a test registers each agent it mints. Empty only for callers outside the
    # compose harness, which mint no agents.
    agent_directory: str = ""
    # The origin host of the per-test publisher sites (see publisher_sites.py),
    # where a test registers the files each site serves. It publishes no host
    # port: the publisher suites sign in to the console, which works only inside
    # the compose network, so a host run leaves this empty.
    sites: str = ""
    # The Exchange impostor the Broker's fault-path suites buy through (see
    # misbehaving_exchange.py), where a test sets how it answers. It publishes no
    # host port: the Broker reaches it by its network aliases, and only a run
    # inside the compose network can use those names too, so a host run leaves
    # this empty.
    misbehaving_exchange: str = ""
    # The capture proxy's control listener (see capture_proxy.py), where a test
    # reads what the stack's services sent the Broker and the Exchanges. It
    # publishes no host port, and the services reach it by names only the compose
    # network resolves, so a host run leaves this empty.
    capture_proxy: str = ""
    # The publishers' Zitadel instance, which the console signs people in
    # through, and the Mailpit both instances send their mail to. In-network
    # only: empty on host runs.
    zitadel_publishers: str = ""
    mailpit: str = ""
