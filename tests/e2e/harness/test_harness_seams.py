"""The send seam's own checks, driven against a local stub instead of the stack.

ADR-028 D3 makes every answer pass the published strict JSON Schema before a test
reads it, and D5 makes every negative a declared mutation whose request-schema
verdict must agree with its declaration. Both are checks that only matter when
they fire, and against the real stack they never should. So each is driven here
against a stub Exchange that answers exactly what the case needs, and seen to
fail:

* a client built by ``harness.sdk_client`` refuses an answer carrying a field the
  protocol does not define, as a ``MALFORMED`` ``CallError``;
* ``malformed`` returns the refusal of a declared malformed message, refuses a
  declaration the sent bytes contradict, and refuses a control the peer refused;
* the lint ban that keeps ``ClientConfig`` inside ``harness.sdk_client`` fires.

No stack, no Docker: the stub is a loopback HTTP server in this process.
"""

from __future__ import annotations

import json
import subprocess
import sys
import threading
from collections.abc import Iterator
from contextlib import closing
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError, CallErrorKind
from fora_sdk.core import DiscoveryResult
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import Client
from wire.models import ResourceQuery

from .constants import requester
from .exchanges import EXCHANGE_A_DOMAIN
from .httpsig_signer import unpublished_signer
from .malformed import Alteration, Mutation, Request, SeamHTTP, malformed
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

    def do_POST(self) -> None:  # noqa: N802 -- the stdlib handler's naming
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


_SIGNER = unpublished_signer("agent.example")


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
            signer=signer or _SIGNER,
            before_sign=before_sign,
            http=http,
        )

    return Request(client=client, verb=lambda c: c.discover, message=_QUERY)


def _uris_not_a_list(wire: dict[str, Any]) -> dict[str, Any]:
    return {**wire, "uris": "https://publisher.example/a"}


def test_the_strict_shape_check_refuses_an_unknown_field(stub: StackURLs) -> None:
    """The same answer passes, then fails once it carries a field the protocol does not define."""
    with closing(
        sdk_exchange_client(stub, EXCHANGE_A_DOMAIN, no_offer_verifier(), signer=_SIGNER)
    ) as c:
        assert c.discover(_QUERY).groups == []

        _Stub.extra = {"surprise": True}
        with pytest.raises(CallError) as refused:
            c.discover(_QUERY)

    assert refused.value.kind is CallErrorKind.MALFORMED, refused.value
    assert "surprise" in str(refused.value), refused.value


def test_malformed_returns_the_refusal_of_a_declared_malformed_message(stub: StackURLs) -> None:
    refusal = malformed(
        Mutation.MESSAGE,
        "uris is a string, not a list",
        control=_discovery(stub),
        alter=Alteration(raw=_uris_not_a_list),
    )
    assert refusal.code == "invalid_argument", refusal


def test_malformed_refuses_a_declaration_the_sent_bytes_contradict(stub: StackURLs) -> None:
    """Declared a signature negative, the body it sent breaks the message's schema."""
    with pytest.raises(AssertionError, match="fails the strict check"):
        malformed(
            Mutation.SIGNATURE,
            "declared as a signature negative",
            control=_discovery(stub),
            alter=Alteration(raw=_uris_not_a_list),
        )


def test_malformed_refuses_a_control_the_peer_refused(stub: StackURLs) -> None:
    _Stub.extra = {"surprise": True}
    with pytest.raises(AssertionError, match="control: the unaltered request was refused"):
        malformed(
            Mutation.MESSAGE,
            "uris is a string, not a list",
            control=_discovery(stub),
            alter=Alteration(raw=_uris_not_a_list),
        )


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
