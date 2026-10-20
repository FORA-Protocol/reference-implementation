"""Guard: every leg the wire obligations read runs through the capture proxy, and its record is the bytes sent.

ADR-028 D8 H12 makes the capture structural: the services resolve the Broker's
and the Exchanges' names to the capture proxy, and nothing else routes them
there (``x-through-capture-proxy`` in docker-compose.e2e.yml). A service that
lost those entries would send its requests straight to the party, every wire
obligation would read an empty record, and a test asserting that something was
NOT sent would pass for the wrong reason. So each leg a test reads is driven
here and must show up in the record, from the service that sends it, at every
party it goes to:

* the Broker's fan-out query, at each of the four Exchanges;
* the identity service's requests as a custodial agent, at the Broker and at an
  Exchange;
* each Exchange's reads of the Broker's revocation list.

A leg that bypassed the proxy is missing from the record, and its test fails
with what the proxy did see.

The record is also checked to be the request as it was signed: the Broker's RFC
9421 signature on its fan-out query verifies, under the key the Broker publishes,
over the recorded method, URL, headers and body. A proxy that altered a byte, or
a record that kept other bytes than the ones forwarded, fails that check.

The Broker and identity legs are scoped like every wire obligation: by an
X-Request-ID this test chose, or by its own agent's host. The revocation-list
reads belong to no test; the Exchanges send them once a second on their own.
"""

from __future__ import annotations

import secrets
import time

import pytest
import yaml
from fora_sdk.b64 import b64url_decode
from fora_sdk.httpsig import content_digest, verify_request
from fora_sdk.thumbprint import thumbprint
from wire.models import (
    DiscoveryRequest,
    GetAccountStatusRequest,
    ResourceQuery,
)

from ._compose import COMPOSE_FILE
from .broker_purchase import resolve_request
from .capture_proxy import BROKER_DOMAIN, CaptureProxy, CapturedRequest
from .constants import REVOCATION_PATH, WBA_DIRECTORY_PATH
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_D_DOMAIN, SERVICE_IDENTITIES
from .fresh_agent import FreshAgent
from .identity_requests import ROUTED_URIS
from .local_ca import https_request, public_roots_context, require_in_network
from .mcp_agents import McpAgentFactory
from .mcp_client import mcp_session
from .money_path import LECTURE_NOTES
from .poll import poll_until
from .protocol_documents import wba_directory_at
from .seed import SeededFixture
from .stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The names the Broker is addressed by: its identity on the plain-http leg, and
#: its https origin (BROKER_PUBLIC_ORIGIN), where its revocation list is read.
_BROKER_TARGETS = (BROKER_DOMAIN, "broker")

#: One resource per Exchange, so one Resolve fans out to all four.
_ONE_PER_EXCHANGE = {**ROUTED_URIS, EXCHANGE_D_DOMAIN: LECTURE_NOTES}


def _broker_key(stack: StackURLs, keyid: str) -> bytes:
    """The public key the Broker publishes under ``keyid``."""
    for key in wba_directory_at(stack.broker).message.keys or []:
        raw = b64url_decode(str(key.x))
        if thumbprint(raw) == keyid:
            return raw
    raise AssertionError(f"the Broker's directory publishes no key {keyid}")


def _assert_signed_as_recorded(stack: StackURLs, request: CapturedRequest) -> None:
    """The Broker's signature verifies over the recorded request, under the Broker's key."""
    assert request.header("content-digest") == content_digest(request.body), (
        f"request {request.seq}: the recorded body is not the one its Content-Digest names"
    )
    (keyid,) = request.keyids
    verified = verify_request(
        method=request.method,
        url=f"http://{request.target}{request.path}",
        body=request.body,
        signature_input=request.header("signature-input"),
        signature=request.header("signature"),
        content_digest=request.header("content-digest"),
        authorization=request.header("authorization"),
        pubkey=_broker_key(stack, keyid),
        now=int(time.time()),
        signature_agent=request.header("signature-agent"),
    )
    assert verified.valid, (
        f"request {request.seq} to {request.target}: the Broker's signature does not verify "
        f"over the recorded bytes: {verified.reason}"
    )


def test_the_brokers_fan_out_reaches_every_exchange_through_the_proxy(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists every Exchange's catalog
    fresh_agent: FreshAgent,
    capture_proxy: CaptureProxy,
) -> None:
    """One Resolve, one resource per Exchange: a recorded query at each, signed as recorded."""
    request_id = f"capture-guard-{secrets.token_hex(8)}"
    traffic = capture_proxy.watch(request_id)
    client = fresh_agent.broker_client(request_id=lambda: request_id)
    client.resolve(resolve_request(list(_ONE_PER_EXCHANGE.values())))

    queries = traffic.await_carrying(
        ResourceQuery, caller="broker", targets=list(_ONE_PER_EXCHANGE)
    )
    for query in queries:
        assert query.header("x-request-id") == request_id, query
        _assert_signed_as_recorded(compose_stack, query)


async def test_the_identity_services_calls_reach_the_broker_and_an_exchange_through_the_proxy(
    identity_ready: StackURLs,
    fresh_mcp_agent_factory: McpAgentFactory,
    seeded: SeededFixture,
    capture_proxy: CaptureProxy,
) -> None:
    """fora_discover is recorded at the Broker, fora_status at exchange-a, both sent by identity."""
    agent = fresh_mcp_agent_factory()
    traffic = capture_proxy.watch(agent.subdomain)
    async with mcp_session(identity_ready, agent.bearer) as mcp:
        await mcp.call("fora_discover", {"uris": [seeded.free.uri]})
        await mcp.call("fora_status", {"exchange": EXCHANGE_A_DOMAIN})

    traffic.await_carrying(DiscoveryRequest, caller="identity", target=BROKER_DOMAIN)
    traffic.await_carrying(GetAccountStatusRequest, caller="identity", target=EXCHANGE_A_DOMAIN)


def test_every_exchange_reads_the_brokers_revocation_list_through_the_proxy(
    capture_proxy: CaptureProxy,
) -> None:
    """Each Exchange's next read of the Broker's revocation list is in the record."""
    traffic = capture_proxy.watch(REVOCATION_PATH)
    exchanges = set(SERVICE_IDENTITIES)
    seen: list[set[str]] = [set()]

    def every_exchange_read() -> bool:
        seen[0] = {
            request.caller
            for request in traffic.requests()
            if request.target in _BROKER_TARGETS and request.method == "GET"
        }
        return exchanges <= seen[0]

    poll_until(
        every_exchange_read,
        30.0,
        failure=lambda: (
            f"only {sorted(seen[0])} of {sorted(exchanges)} read the Broker's revocation list "
            f"through the capture proxy"
        ),
    )


# ───── The https leg ─────────────────────────────────────────────────────────
# The proxy is also the stack's one TLS endpoint (x-through-capture-proxy): the
# Web Bot Auth profile serves every key directory over https and names every
# signer by an https origin. These show that https is real (verification ON
# against the local CA), that it is the local CA that makes it verify, and that a
# request on the https leg is recorded like one on the plain-http leg.


def _proxied_hosts() -> list[str]:
    """The names the compose file resolves to the proxy (x-through-capture-proxy).

    Read from the compose file rather than restated, so a name added there is
    tested here without anyone editing this file. Read inside the test, never at
    import: collection must not depend on the compose file's content
    (test_guards_harness_collection.py collects against an empty one).
    """
    document = yaml.safe_load(COMPOSE_FILE.read_text())
    return sorted(document["x-through-capture-proxy"])


_SERVER_ERROR = 500


def test_every_proxied_name_answers_https_under_the_local_ca() -> None:
    """Each name's key-directory path, over https verified against the local CA alone.

    Any answer the party gave proves the leg: the proxy terminated TLS under a
    certificate naming the host and forwarded to the party. A transport failure,
    a certificate refusal or a server error is what this refuses.
    """
    require_in_network()
    answers = [
        https_request("GET", f"https://{host}{WBA_DIRECTORY_PATH}") for host in _proxied_hosts()
    ]
    failed = [a for a in answers if a.refused or a.status >= _SERVER_ERROR]
    assert not failed, "\n".join(f"{a.url}: {a.status} {a.refused or a.text}" for a in failed)


def test_an_agent_subdomain_answers_https_under_the_local_ca(mcp_agent: str) -> None:
    """The identity service's agent zone reaches the proxy through the wildcard-DNS sidecar."""
    require_in_network()
    answer = https_request("GET", f"https://{mcp_agent}{WBA_DIRECTORY_PATH}")
    assert answer.status == 200, answer


def test_the_public_roots_alone_refuse_the_proxy_certificate() -> None:
    """Without the local CA, verification fails for the same URL.

    The positive control runs first, against the same URL, so a refusal below can
    only come from the trust store and not from a proxy that is down.
    """
    require_in_network()
    url = f"https://broker{WBA_DIRECTORY_PATH}"
    assert https_request("GET", url).status == 200
    refused = https_request("GET", url, trust=public_roots_context())
    assert refused.certificate_refused, refused


def test_a_request_on_the_https_leg_is_recorded_byte_for_byte(capture_proxy: CaptureProxy) -> None:
    """Every byte value, sent over https, comes back from the record unchanged, with its leg.

    The path exists on no service, so the Exchange answers 404 and nothing in the
    stack changes.
    """
    require_in_network()
    body = bytes(range(256)) * 4
    request_id = f"capture-tls-{secrets.token_hex(8)}"
    traffic = capture_proxy.watch(request_id)
    answer = https_request(
        "POST",
        "https://exchange/fora-capture-probe",
        body=body,
        headers={"X-Request-ID": request_id},
    )
    assert answer.status == 404, answer

    (recorded,) = traffic.requests()
    assert (recorded.scheme, recorded.target, recorded.method) == ("https", "exchange", "POST")
    assert recorded.path == "/fora-capture-probe", recorded
    assert recorded.body == body, "the recorded body differs from the one sent"
