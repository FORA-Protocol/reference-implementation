"""The send seam's own checks, driven against a local stub instead of the stack.

ADR-028 D3 makes every answer pass the published strict JSON Schema before a test
reads it, and D5 makes every negative a declared mutation whose request-schema
verdict must agree with its declaration. Both are checks that only matter when
they fire, and against the real stack they never should. So each is driven here
against a stub Exchange that answers exactly what the case needs, and seen to
fail:

* a client built by ``harness.sdk_client`` refuses an answer carrying a field the
  protocol does not define, as a ``MALFORMED`` ``CallError``, and refuses to send
  anything but the verb's own generated request model (``strict_requests``);
* such a client signs only as an agent its caller names: built with neither a
  keyfile nor a signer it is refused, so no client falls back to a fixed identity;
* ``malformed`` returns the refusal of a declared malformed message, refuses a
  declaration the bytes contradict before they are sent (both ways), refuses a
  control the peer refused, and refuses a raw-mode base that is not a whole message;
* ``broker_purchase.unstamped`` and ``model_edits.with_members`` refuse a field name
  their model does not declare, and ``with_members`` replaces only the members it
  names, keeping off the wire every member the original left unset;
* the lint ban that keeps ``ClientConfig`` inside ``harness.sdk_client`` fires.

No stack, no Docker: the stub is a loopback HTTP server in this process.
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
import threading
from collections.abc import Callable, Iterator
from contextlib import closing
from dataclasses import replace
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest
from pydantic import BaseModel
from fora_sdk import ProtocolVersion, to_wire
from fora_sdk.client import CallError, CallErrorKind
from fora_sdk.core import DiscoveryResult
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import Client
from wire.models import GetAccountStatusRequest, ResourceQuery

from .after_signing import after_signing, append_field_line
from .constants import requester
from .broker_purchase import resource_query, unstamped
from .exchanges import EXCHANGE_A_DOMAIN
from .httpsig_signer import unpublished_signer
from .malformed import Alteration, Mutation, Request, SeamHTTP, malformed
from .model_edits import with_members
from .sdk_client import BeforeSign, no_offer_verifier, sdk_exchange_client
from .stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("isolated")

_E2E_DIR = Path(__file__).resolve().parents[1]
#: A model, not a dict: raw mode alters a generated model's wire form. It names a
#: requester because the pinned protocol requires one; the stub never reads it.
_QUERY = ResourceQuery(
    exchange=EXCHANGE_A_DOMAIN,
    uris=["https://publisher.example/a"],
    requester=requester("agent.example"),
)


class _Stub(BaseHTTPRequestHandler):
    """Answers DiscoverResources: an empty ResourceResponse, plus ``extra`` fields.

    A request whose ``uris`` is not a list is refused as Connect
    ``invalid_argument``, the way an Exchange refuses a malformed query.
    """

    extra: dict[str, Any] = {}
    #: How many requests reached the stub, so a test can tell what was never sent.
    received: int = 0

    def do_POST(self) -> None:  # noqa: N802 -- the stdlib handler's naming
        _Stub.received += 1
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if not isinstance(body.get("uris"), list):
            self._answer(400, {"code": "invalid_argument", "message": "uris must be a list"})
            return
        answer = {"ver": ProtocolVersion, "exchange": EXCHANGE_A_DOMAIN, "offer_groups": []}
        self._answer(200, {**answer, **self.extra})

    def _answer(self, status: int, payload: dict[str, Any]) -> None:
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *_args: object) -> None:
        return


@pytest.fixture
def stub() -> Iterator[StackURLs]:
    """A stub Exchange A on loopback; the stack URLs the harness's builders read."""
    _Stub.extra = {}
    _Stub.received = 0
    server = ThreadingHTTPServer(("127.0.0.1", 0), _Stub)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    url = f"http://127.0.0.1:{server.server_address[1]}"
    yield StackURLs(
        exchange=url,
        exchange_b="",
        exchange_c="",
        exchange_d="",
        broker="",
        edge="",
        aws_edge="",
        fastly_edge="",
        lambda_edge="",
        lambda_edge_no_wba="",
        identity="",
        zitadel="",
    )
    server.shutdown()
    server.server_close()


#: The agent every stub client signs as: a key no directory publishes, under this host.
_AGENT = "agent.example"
_SIGNER = unpublished_signer(_AGENT)


def _discovery(stack: StackURLs) -> Request[Client, DiscoveryResult]:
    def client(
        *,
        signer: SigningTransport | None,
        before_sign: BeforeSign | None,
        http: SeamHTTP,
    ) -> Client:
        return sdk_exchange_client(
            stack,
            EXCHANGE_A_DOMAIN,
            no_offer_verifier(),
            agent_id=_AGENT,
            signer=signer or _SIGNER,
            before_sign=before_sign,
            http=http,
        )

    return Request(client=client, verb=lambda c: c.discover, message=_QUERY)


def _uris_not_a_list(wire: dict[str, Any]) -> dict[str, Any]:
    return {**wire, "uris": "https://publisher.example/a"}


def test_a_request_rebuilt_after_signing_keeps_the_clients_timeout(stub: StackURLs) -> None:
    """Every request the after-signing transport sends carries the SDK client's timeout.

    httpx keeps a client's timeouts in ``request.extensions``. A request an
    alteration builds anew (here, one more field line), and the copy a second send
    makes, would otherwise carry none, and the transport would then wait on a
    silent peer for ever.
    """
    seen: list[Any] = []
    alter = append_field_line("x-probe", "1")
    with (
        after_signing(alter, sends=2, observe=seen.append) as http,
        closing(
            sdk_exchange_client(
                stub,
                EXCHANGE_A_DOMAIN,
                no_offer_verifier(),
                agent_id=_AGENT,
                signer=_SIGNER,
                http=http,
            )
        ) as client,
    ):
        client.discover(_QUERY)
    timeouts = [request.extensions.get("timeout") for request in seen]
    assert len(seen) == 2, seen
    assert all(timeout and timeout.get("read") for timeout in timeouts), timeouts


def test_the_strict_shape_check_refuses_an_unknown_field(stub: StackURLs) -> None:
    """The same answer passes, then fails once it carries a field the protocol does not define."""
    with closing(
        sdk_exchange_client(
            stub, EXCHANGE_A_DOMAIN, no_offer_verifier(), agent_id=_AGENT, signer=_SIGNER
        )
    ) as c:
        assert c.discover(_QUERY).groups == []

        _Stub.extra = {"surprise": True}
        with pytest.raises(CallError) as refused:
            c.discover(_QUERY)

    assert refused.value.kind is CallErrorKind.MALFORMED, refused.value
    assert "surprise" in str(refused.value), refused.value


def test_a_client_builder_refuses_to_sign_as_nobody(stub: StackURLs) -> None:
    """A client signs only as an agent its caller names: with no keyfile and no signer, none is built."""
    with pytest.raises(TypeError, match="signs as an agent"):
        sdk_exchange_client(stub, EXCHANGE_A_DOMAIN, no_offer_verifier(), agent_id=_AGENT)


@pytest.mark.parametrize(
    ("message", "given"),
    [
        (to_wire(_QUERY), "a plain dict"),
        (
            GetAccountStatusRequest(exchange=EXCHANGE_A_DOMAIN),
            "a value of type GetAccountStatusRequest",
        ),
    ],
    ids=["a-dict", "a-model-of-another-message"],
)
def test_a_message_that_is_not_the_verbs_model_never_leaves_the_sdk(
    stub: StackURLs, message: dict[str, Any] | BaseModel, given: str
) -> None:
    """strict_requests refuses anything but the verb's own model before it signs or sends."""
    with closing(
        sdk_exchange_client(
            stub, EXCHANGE_A_DOMAIN, no_offer_verifier(), agent_id=_AGENT, signer=_SIGNER
        )
    ) as c:
        with pytest.raises(CallError) as refused:
            c.discover(message)
    assert refused.value.kind is CallErrorKind.MALFORMED, refused.value
    assert given in str(refused.value), refused.value
    assert _Stub.received == 0, f"the refused message reached the peer: {_Stub.received}"


def test_malformed_returns_the_refusal_of_a_declared_malformed_message(stub: StackURLs) -> None:
    refusal = malformed(
        Mutation.MESSAGE,
        "uris is a string, not a list",
        control=_discovery(stub),
        alter=Alteration(raw=_uris_not_a_list),
    )
    assert refusal.code == "invalid_argument", refusal


def _another_uri(wire: dict[str, Any]) -> dict[str, Any]:
    return {**wire, "uris": ["https://publisher.example/b"]}


@pytest.mark.parametrize(
    ("kind", "alter", "complaint"),
    [
        (Mutation.SIGNATURE, _uris_not_a_list, "fails the strict check"),
        (
            Mutation.MESSAGE,
            _another_uri,
            "declared a malformed message, and the fora.v1.ResourceQuery about to be sent "
            "passes the strict check",
        ),
    ],
    ids=["signature-negative-that-breaks-the-message", "message-negative-that-breaks-nothing"],
)
def test_malformed_refuses_a_declaration_the_sent_bytes_contradict(
    stub: StackURLs,
    kind: Mutation,
    alter: Callable[[dict[str, Any]], dict[str, Any]],
    complaint: str,
) -> None:
    """The verdict contradicts the declaration, either way, and the request is never sent.

    A signature negative whose body breaks the message's schema would pass whatever
    the signature check does; a message negative whose body passes the schema breaks
    nothing. Either one is refused before it leaves, so only the control reaches the peer.
    """
    with pytest.raises(AssertionError, match=re.escape(complaint)):
        malformed(
            kind,
            f"declared as a {kind.value} negative",
            control=_discovery(stub),
            alter=Alteration(raw=alter),
        )
    assert _Stub.received == 1, f"the contradicted request reached the peer: {_Stub.received}"


def test_malformed_refuses_a_control_the_peer_refused(stub: StackURLs) -> None:
    _Stub.extra = {"surprise": True}
    with pytest.raises(AssertionError, match="control: the unaltered request was refused"):
        malformed(
            Mutation.MESSAGE,
            "uris is a string, not a list",
            control=_discovery(stub),
            alter=Alteration(raw=_uris_not_a_list),
        )


def test_unstamped_refuses_a_field_the_model_does_not_declare() -> None:
    """A misspelled member is refused, where model_construct would drop it unseen."""
    with pytest.raises(
        AssertionError, match=re.escape("ResourceQuery declares no field ['exchnage']")
    ):
        unstamped(ResourceQuery, exchnage=EXCHANGE_A_DOMAIN, uris=["https://publisher.example/a"])


def test_with_members_refuses_a_member_the_model_does_not_declare() -> None:
    """A misspelled member is refused, where model_copy would keep it off the wire unseen."""
    with pytest.raises(
        AssertionError, match=re.escape("ResourceQuery declares no field ['ext_critcal']")
    ):
        with_members(_QUERY, ext_critcal=["acme.must"])


def test_with_members_replaces_only_the_named_member() -> None:
    """The copy carries the new member and every member that was set; the original is untouched.

    ``ver`` is declared with a default and was never set, so it stays off the wire,
    as it does on the original. Re-validating the copy would set every member.
    """
    copy = with_members(_QUERY, exchange="exchange-b.example")
    assert to_wire(copy) == {**to_wire(_QUERY), "exchange": "exchange-b.example"}, to_wire(copy)
    assert "ver" not in to_wire(copy)
    assert _QUERY.exchange == EXCHANGE_A_DOMAIN


def test_raw_mode_refuses_a_message_built_without_its_requester(stub: StackURLs) -> None:
    """Raw mode sends its base as given, so a base with no requester is refused before it leaves."""
    control = replace(
        _discovery(stub), message=resource_query(EXCHANGE_A_DOMAIN, ["https://publisher.example/a"])
    )
    with pytest.raises(AssertionError, match="raw mode alters a complete message"):
        malformed(
            Mutation.MESSAGE,
            "uris is a string, not a list",
            control=control,
            alter=Alteration(raw=_uris_not_a_list),
        )
    assert _Stub.received == 1, f"the incomplete raw message reached the peer: {_Stub.received}"


@pytest.mark.parametrize(
    ("module", "fires"), [("harness/probe.py", True), ("harness/sdk_client.py", False)]
)
def test_the_client_config_ban_fires_outside_the_seam(module: str, fires: bool) -> None:
    """ruff refuses a ClientConfig import anywhere but harness/sdk_client.py."""
    pytest.importorskip("ruff", reason="ruff is a dev dependency; the runner image has none")
    result = subprocess.run(  # noqa: S603 -- fixed argv, this interpreter's own ruff
        [
            sys.executable,
            "-m",
            "ruff",
            "check",
            "--no-cache",
            "--select",
            "TID251",
            "--stdin-filename",
            module,
            "-",
        ],
        input="from fora_sdk.client import ClientConfig\n",
        capture_output=True,
        text=True,
        cwd=_E2E_DIR,
        check=False,
    )
    assert (result.returncode != 0) is fires, result.stdout + result.stderr
    assert ("TID251" in result.stdout) is fires, result.stdout
