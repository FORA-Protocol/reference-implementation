"""What the stack's services sent the Broker and the Exchanges, read from the capture proxy.

ADR-028 D8 H12: every request a service sends the Broker or an Exchange is
captured with its bytes. The ``capture-proxy`` compose service does it, built from
``tests/e2e/capture-proxy/``: the Broker, each Exchange, the identity service, the
console and the runner resolve those parties' names to the proxy, which records
each request and forwards it unchanged to the party the name names. A test reads
here what one party sent another, which no answer shows: the query the Broker
fanned out, the sub-request it relayed, the Register the identity service sent as
the agent.

**Each test reads its own traffic only.** :meth:`CaptureProxy.watch` takes a
scope, a string only the test's own requests carry, and returns the
:class:`Traffic` that carries it from that moment on. The scope is the test's own
agent's host, which every request signed as the agent names in its
Signature-Agent and every query on its behalf names as its requester, or an
X-Request-ID the test chose, which the Broker copies onto every request it sends
for the agent's call. Requests of another test, or sent before the watch began,
are never read.

**What a test reads.** Each :class:`CapturedRequest` carries the bytes as they
arrived (method, request target, headers, body), the party it was sent to, the
compose service that sent it, and for a FORA RPC the request message decoded from
those bytes by the generated protocol types. A test asks for requests by the
generated model of the message they carry (``ResourceQuery``,
``TransactionRequest``, ``RegisterRequest``), so no RPC path is written here.

**Waiting.** A request is captured before it is forwarded, so it is in the record
by the time its caller hears back. A request sent behind the scenes is waited for
through :meth:`Traffic.await_carrying`, which polls with the harness's one poller.
An assertion that something was NOT sent is made only after the same test has
seen a request of the same scope arrive on the same leg, so an empty answer cannot
mean the proxy was bypassed; ``harness/test_capture_proxy.py`` proves each leg
runs through it.

**Either leg.** The proxy is also the stack's one TLS endpoint, so a request may
arrive over https on 443 as well as over plain http on the party's own port. It
is recorded the same way, with ``scheme`` naming the leg; ``target`` is the Host
the caller named, which carries no port on the https leg.

In-network only: the proxy publishes no host port, and the services reach it by
names only the compose network resolves.
"""

from __future__ import annotations

import base64
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from typing import Any, TypeVar

import httpx
from fora_sdk import sfv
from pydantic import BaseModel

from ._compose import project_services
from .poll import poll_until

M = TypeVar("M", bound=BaseModel)

#: Where a run inside the compose network reads the record.
CONTROL_URL = "http://capture-proxy:9090"
#: The Broker's identity and the name its callers address it by: BROKER_DOMAIN in
#: docker-compose.e2e.yml.
BROKER_DOMAIN = "broker:8082"

#: The protocol package every FORA request message is declared in.
_PACKAGE = "fora.v1"
#: How long a request sent behind the scenes may take to reach the record.
DEFAULT_DEADLINE_S = 30.0
_TIMEOUT_SEC = 10.0


@dataclass(frozen=True)
class CapturedRequest:
    """One request as it reached the proxy, on its way from ``caller`` to ``target``."""

    seq: int
    #: The leg it arrived on: "http", or "https" on the proxy's TLS listener.
    scheme: str
    #: The party the request was sent to, as the host:port its caller addressed.
    target: str
    #: The compose service that sent it; "" when the sender's address names none.
    caller: str
    method: str
    #: The request target exactly as sent, query included.
    path: str
    headers: tuple[tuple[str, str], ...]
    body: bytes
    #: The full protocol name of the message the body carries, for a FORA RPC.
    message_type: str
    message: dict[str, Any] | None
    #: Why a body sent to a FORA RPC was not decoded; "" when it was, or is no RPC.
    decode_problem: str

    def header_values(self, name: str) -> list[str]:
        """Every value of header ``name``, in arrival order."""
        return [value for key, value in self.headers if key.lower() == name.lower()]

    def header(self, name: str) -> str:
        """The one value of header ``name``; "" when it is absent."""
        values = self.header_values(name)
        assert len(values) <= 1, f"{name} arrived {len(values)} times on request {self.seq}"
        return values[0] if values else ""

    @property
    def keyids(self) -> list[str]:
        """The keyid of every signature the request carries, in label order."""
        values = self.header_values("signature-input")
        if not values:
            return []
        try:
            members = sfv.parse_dictionary(values)
        except sfv.StructuredFieldError as exc:
            raise AssertionError(f"request {self.seq} carries a malformed Signature-Input") from exc
        return [str(member.params.get("keyid", "")) for member in members.values()]

    def carries(self, model: type[BaseModel]) -> bool:
        """Whether the body is a ``model`` message, the request of the RPC it was sent to."""
        return self.message_type == _type_name(model)

    def message_as(self, model: type[M]) -> M:
        """The decoded request message, read through the generated ``model``."""
        assert self.carries(model), (
            f"request {self.seq} to {self.target} carries {self.message_type or 'no message'}, "
            f"not {_type_name(model)}"
        )
        assert self.message is not None, (
            f"request {self.seq} to {self.target}: its {self.message_type} did not decode: "
            f"{self.decode_problem}"
        )
        return model.model_validate(self.message)


@dataclass(frozen=True)
class CaptureProxy:
    """The capture proxy's record, read at ``control_url``."""

    control_url: str

    def watch(self, scope: str) -> Traffic:
        """The requests carrying ``scope`` from now on: the test's own agent host, or its request id."""
        assert scope, "a watch needs a scope only this test's requests carry"
        return Traffic(self, scope, after=int(self._get("/latest", {})["latest"]))

    def read(self, scope: str, *, after: int) -> list[dict[str, Any]]:
        """The raw records carrying ``scope`` that arrived after sequence number ``after``.

        Refuses a window whose first records were evicted, so a reader never takes
        a missing record for one that was not sent.
        """
        window = self._get("/captured", {"after": after, "contains": scope})
        assert window["oldest"] <= after + 1, (
            f"the capture proxy no longer holds the records after {after}: its oldest is "
            f"{window['oldest']}"
        )
        return list(window["records"])

    def _get(self, path: str, params: dict[str, str | int]) -> dict[str, Any]:
        response = httpx.get(f"{self.control_url}{path}", params=params, timeout=_TIMEOUT_SEC)
        assert response.status_code == httpx.codes.OK, (
            f"the capture proxy answered {path} with {response.status_code}: {response.text}"
        )
        answer: dict[str, Any] = response.json()
        return answer


@dataclass(frozen=True)
class Traffic:
    """The requests carrying one test's ``scope`` that arrived after sequence number ``after``."""

    proxy: CaptureProxy
    scope: str
    after: int

    def requests(self) -> list[CapturedRequest]:
        """Every request in the window so far, in arrival order."""
        raw = self.proxy.read(self.scope, after=self.after)
        if not raw:
            return []
        services = project_services()
        return [_captured(record, services) for record in raw]

    def carrying(
        self, model: type[BaseModel], *, target: str | None = None, caller: str | None = None
    ) -> list[CapturedRequest]:
        """The requests so far whose body is a ``model`` message, optionally to ``target`` from ``caller``."""
        return [
            request
            for request in self.requests()
            if request.carries(model)
            and (target is None or request.target == target)
            and (caller is None or request.caller == caller)
        ]

    def await_carrying(
        self,
        model: type[BaseModel],
        *,
        target: str | None = None,
        caller: str | None = None,
        targets: Sequence[str] = (),
        deadline_s: float = DEFAULT_DEADLINE_S,
    ) -> list[CapturedRequest]:
        """Wait until a ``model`` request has arrived, at each of ``targets`` when they are named.

        Returns every matching request in the window at that moment.
        """
        wanted = set(targets) if targets else None
        seen: list[list[CapturedRequest]] = [[]]

        def arrived() -> list[CapturedRequest] | None:
            seen[0] = self.carrying(model, target=target, caller=caller)
            reached = {request.target for request in seen[0]}
            done = bool(seen[0]) and (wanted is None or wanted <= reached)
            return seen[0] if done else None

        return poll_until(
            arrived,
            deadline_s,
            failure=_describe_wait(model, caller, target, wanted, lambda: seen[0]),
        )


def _describe_wait(
    model: type[BaseModel],
    caller: str | None,
    target: str | None,
    wanted: set[str] | None,
    seen: Callable[[], list[CapturedRequest]],
) -> Callable[[], str]:
    def failure() -> str:
        where = sorted(wanted) if wanted else (target or "any party")
        return (
            f"no {_type_name(model)} from {caller or 'any service'} to {where} reached the "
            f"capture proxy; it saw {[(r.caller, r.target) for r in seen()]}"
        )

    return failure


def _type_name(model: type[BaseModel]) -> str:
    return f"{_PACKAGE}.{model.__name__}"


def _caller(record: dict[str, Any], services: dict[str, str]) -> str:
    """The compose service that sent ``record``: by the container its address names, else the address."""
    for name in record.get("caller_names") or []:
        container = name.split(".", 1)[0]
        if container in services:
            return services[container]
    return services.get(record["caller_address"], "")


def _captured(record: dict[str, Any], services: dict[str, str]) -> CapturedRequest:
    return CapturedRequest(
        seq=int(record["seq"]),
        scheme=record.get("scheme", "http"),
        target=record["target"],
        caller=_caller(record, services),
        method=record["method"],
        path=record["path"],
        headers=tuple((name, value) for name, value in record.get("headers") or []),
        body=base64.b64decode(record.get("body") or ""),
        message_type=record.get("message_type", ""),
        message=record.get("message"),
        decode_problem=record.get("decode_problem", ""),
    )
