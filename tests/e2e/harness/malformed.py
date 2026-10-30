"""Declared negatives: ``malformed(kind, why, control=..., alter=...)`` (ADR-028 D5).

A negative test sends a request that differs from an accepted one in exactly one
declared way, and reads the refusal. This module is the one way to do that:

1. The test hands over the **control**: the unaltered request, as the client
   factory that builds an SDK client, the verb, and the verb's argument. The
   control is sent first and must be answered; ``accepted`` may check the answer.
2. The test declares **one alteration**, built on the SDK's own seams:

   * ``before_sign``: the SDK's pre-signing hook. The SDK builds, stamps and
     validates the request and checks it against the strict schema
     (``strict_requests``); the hook then patches it, and the SDK signs the
     patched bytes, so the signature is valid over what is sent;
   * ``after_signing`` and ``sends``: the transport in :mod:`harness.after_signing`
     receives the signed bytes, applies the alteration, and sends the result
     ``sends`` times; ``sends=2`` with no alteration is a replay;
   * ``signer``: a different signer, for a key the agent's directory does not
     publish or a signing window outside the verifier's;
   * ``raw``: the SDK's raw mode. The function receives the message's wire form
     and returns the body sent verbatim: the SDK fills in nothing and checks
     nothing about it, and still signs it and decodes the answer.

3. The altered request is sent, and the SDK's typed ``CallError`` is returned for
   the test to assert on: the Connect code and the typed reason, never the
   message text. Where the protocol carries the refusal inside an answer (a
   per-item denial), :func:`malformed_answer` returns the SDK's typed answer
   instead, for the test to read the denial from.

Before each request leaves, its bytes are checked as the request message by the
SDK's strict check (``fora_sdk.check_strict``: the published strict JSON Schema and
the cross-field rules), and the verdict must agree with the declared ``kind``. The
control must pass the check. A ``MESSAGE`` mutation must fail it: it claims the
message is malformed. Every other kind must pass it: a signature or identity
negative that also broke the message's shape would pass whether or not the check it
targets works. The check runs in the transport, after every alteration and before
the bytes go out, so what is checked is exactly what would be sent, and a request
whose verdict contradicts its declaration never reaches the peer.

Every client is built through ``harness.sdk_client``, so both sends run the
universal strict shape check on the answer.
"""

from __future__ import annotations

import json
from collections.abc import Callable
from contextlib import closing
from dataclasses import dataclass
from enum import Enum
from typing import Any, Generic, Protocol, TypeAlias, TypeVar

import httpx
from fora_sdk import StrictViolationError, check_strict, to_wire
from fora_sdk.client import CallError, CallErrorKind, RawBody
from fora_sdk.signing_transport import SigningTransport
from pydantic import BaseModel

from . import after_signing as after
from .sdk_client import BeforeSign

T = TypeVar("T")


class _Closable(Protocol):
    def close(self) -> None: ...


C = TypeVar("C", bound=_Closable)
C_co = TypeVar("C_co", bound=_Closable, covariant=True)


class Mutation(Enum):
    """What a declared alteration breaks, which decides the request-schema verdict."""

    #: The message is well formed; its signature is absent, stale, replayed, made
    #: by a key the signer's directory does not publish, or made over other bytes.
    SIGNATURE = "signature"
    #: The message is well formed and validly signed, and names a party the
    #: signature does not prove, such as another agent's directory.
    IDENTITY = "identity"
    #: The message itself breaks the contract's shape.
    MESSAGE = "message"


#: The HTTP client the seam hands a :class:`ClientFactory`. A factory passes it to
#: the SDK client it builds and does nothing else with it: the seam records and
#: alters what goes out through it, and the test reads only the SDK's answer.
SeamHTTP: TypeAlias = httpx.Client


class ClientFactory(Protocol[C_co]):
    """Builds one SDK client with the declared seams applied; ``harness.sdk_client`` builds it."""

    def __call__(
        self,
        *,
        signer: SigningTransport | None,
        before_sign: BeforeSign | None,
        http: SeamHTTP,
    ) -> C_co: ...


@dataclass(frozen=True)
class Request(Generic[C, T]):
    """The control request: who sends it, through which verb, with what argument."""

    #: Builds the client the request is sent through.
    client: ClientFactory[C]
    #: Picks the verb off a client, for example ``lambda c: c.discover``.
    verb: Callable[[C], Callable[[Any], T]]
    #: The verb's argument: a generated request model, or the offers to buy.
    message: Any


@dataclass(frozen=True)
class Alteration:
    """One declared alteration of the control request. Set exactly one field group."""

    before_sign: BeforeSign | None = None
    after_signing: after.Alteration | None = None
    sends: int = 1
    signer: SigningTransport | None = None
    raw: Callable[[dict[str, Any]], dict[str, Any]] | None = None

    def groups(self) -> int:
        """How many of the four alteration kinds this sets: the pre-signing hook, the
        after-signing transport (an alteration or extra sends), the signer, raw mode."""
        return sum(
            (
                self.before_sign is not None,
                self.after_signing is not None or self.sends != 1,
                self.signer is not None,
                self.raw is not None,
            )
        )


#: The request message of each RPC the harness alters, by the path the SDK sends to.
#: Extend it when a negative alters another RPC; an unknown path fails the verdict
#: check rather than skipping it.
_REQUEST_MESSAGES = {
    "/fora.v1.ExchangeService/DiscoverResources": "fora.v1.ResourceQuery",
    "/fora.v1.ExchangeService/ExecuteTransaction": "fora.v1.TransactionRequest",
    "/fora.v1.ExchangeService/ReportUsage": "fora.v1.UsageReport",
    "/fora.v1.ExchangeService/DisputeTransaction": "fora.v1.DisputeRequest",
    "/fora.v1.BrokerService/Resolve": "fora.v1.DiscoveryRequest",
    "/fora.v1.BrokerService/ExecuteTransaction": "fora.v1.TransactionRequest",
    "/fora.v1.CatalogService/PushResources": "fora.v1.PushResourcesRequest",
    "/fora.v1.CatalogService/RemoveResources": "fora.v1.RemoveResourcesRequest",
    "/fora.admin.v1.AdminService/SetReportingPolicy": "fora.admin.v1.SetReportingPolicyRequest",
}


def patch_body(patch: Callable[[dict[str, Any]], None]) -> BeforeSign:
    """A pre-signing alteration that edits the request's JSON body in place.

    ``patch`` receives the body the SDK built, stamped and validated, and changes
    it. The SDK then signs the patched bytes, so the signature is valid over what
    is sent and the edit is the only thing wrong with the request.
    """

    def alter(request: httpx.Request) -> httpx.Request:
        body = json.loads(request.content)
        patch(body)
        altered = json.dumps(body, separators=(",", ":")).encode()
        return httpx.Request(request.method, request.url, headers=request.headers, content=altered)

    return alter


def malformed(
    kind: Mutation,
    why: str,
    *,
    control: Request[C, T],
    alter: Alteration,
    accepted: Callable[[T], object] | None = None,
) -> CallError:
    """Send ``control``, then the same request altered by ``alter``; return the refusal.

    ``why`` names what the alteration breaks; it heads every failure message.
    ``accepted`` checks the control's answer beyond "it was not refused".
    """
    outcome = _declared(kind, why, control=control, alter=alter, accepted=accepted)
    if not isinstance(outcome, CallError):
        raise AssertionError(f"{why}: the altered request was answered: {outcome!r}")
    return outcome


def malformed_answer(
    kind: Mutation,
    why: str,
    *,
    control: Request[C, T],
    alter: Alteration,
    accepted: Callable[[T], object] | None = None,
) -> T:
    """:func:`malformed` for a negative the protocol answers in the body.

    Some refusals are part of an answer rather than a refused call: an
    ExecuteTransaction item whose offer no longer verifies is a per-item denial
    in a 200 answer, so the SDK returns the answer and raises nothing. This sends
    the control and the altered request exactly as :func:`malformed` does,
    applies the same request-schema verdicts, and returns the altered request's
    typed answer for the test to read the denial from.
    """
    outcome = _declared(kind, why, control=control, alter=alter, accepted=accepted)
    if isinstance(outcome, CallError):
        raise AssertionError(f"{why}: the altered request was refused, not answered: {outcome}")
    return outcome


def malformed_outcome(
    kind: Mutation,
    why: str,
    *,
    control: Request[C, T],
    alter: Alteration,
    accepted: Callable[[T], object] | None = None,
) -> T | CallError:
    """:func:`malformed` for a negative whose answer shape is itself under test.

    The protocol may require a refusal in the body, and an implementation may
    refuse the whole call instead. This sends and checks exactly as the other two
    forms do, and returns whichever arrived: the typed answer, or the SDK's
    ``CallError``. The test asserts which one the protocol requires.
    """
    return _declared(kind, why, control=control, alter=alter, accepted=accepted)


def _declared(
    kind: Mutation,
    why: str,
    *,
    control: Request[C, T],
    alter: Alteration,
    accepted: Callable[[T], object] | None,
) -> T | CallError:
    """Send ``control``, then the altered request; the altered answer or its refusal."""
    assert why.strip(), "a declared mutation says what it breaks"
    assert alter.groups() == 1, (
        f"{why}: declare exactly one alteration, so it is the only difference "
        f"between the accepted call and the refused one"
    )

    sent: list[httpx.Request] = []
    try:
        answer = _send(control, Alteration(), _verdict(f"{why}: control", sent, must_pass=True))
    except CallError as exc:
        raise AssertionError(f"{why}: control: the unaltered request was refused: {exc}") from exc
    assert sent, f"{why}: control: nothing reached the wire"
    if accepted is not None:
        accepted(answer)

    sent.clear()
    outcome: T | CallError
    verdict = _verdict(why, sent, must_pass=kind is not Mutation.MESSAGE)
    try:
        outcome = _send(control, alter, verdict)
    except CallError as exc:
        assert exc.kind is not CallErrorKind.NOT_SENT and sent, (
            f"{why}: the altered request never left the SDK: {exc}"
        )
        outcome = exc
    assert sent, f"{why}: nothing reached the wire"
    return outcome


def _send(request: Request[C, T], alter: Alteration, observe: after.Observer) -> T:
    """Send ``request`` once with ``alter`` applied; ``observe`` sees each request before it leaves."""
    with after.after_signing(alter.after_signing, sends=alter.sends, observe=observe) as http:
        client = request.client(signer=alter.signer, before_sign=alter.before_sign, http=http)
        with closing(client):
            argument = request.message
            if alter.raw is not None:
                assert isinstance(argument, BaseModel), (
                    "raw mode alters a generated request model's wire form"
                )
                argument = RawBody(alter.raw(_complete_wire(argument)))
            return request.verb(client)(argument)


def _complete_wire(message: BaseModel) -> dict[str, Any]:
    """``message``'s wire form, refused unless the strict check accepts it unaltered.

    Raw mode sends what the alteration returns exactly as given: the SDK stamps
    nothing into it, the requester included. A message built without one (see
    ``harness.broker_purchase.unstamped``) would then fail a ``MESSAGE`` verdict for
    its missing requester, whatever the alteration did, so the base must be whole.
    """
    names = [
        name for name in _REQUEST_MESSAGES.values() if name.endswith(f".{type(message).__name__}")
    ]
    assert len(names) == 1, f"no request message is known for {type(message).__name__}"
    wire = to_wire(message)
    complaint = _strict_complaint(names[0], json.dumps(wire).encode())
    assert complaint is None, (
        f"raw mode alters a complete message, and the unaltered {names[0]} fails the "
        f"strict check: {complaint}"
    )
    return wire


def _verdict(why: str, sent: list[httpx.Request], *, must_pass: bool) -> after.Observer:
    """The transport's observer: check each request's verdict, then record it.

    The transport calls it after every alteration and just before the request
    leaves, so a request whose verdict contradicts the declaration is never sent.
    The ``AssertionError`` it raises is not an ``httpx`` error, so the SDK does not
    turn it into a ``CallError``: it reaches the test as it was raised.
    """

    def observe(request: httpx.Request) -> None:
        _check_verdict(why, request, must_pass=must_pass)
        sent.append(request)

    return observe


def _check_verdict(why: str, request: httpx.Request, *, must_pass: bool) -> None:
    """Require the strict check's verdict on ``request``'s body to be ``must_pass``."""
    name = _REQUEST_MESSAGES.get(request.url.path)
    assert name is not None, f"{why}: no request message is known for {request.url.path}"
    complaint = _strict_complaint(name, request.content)
    if must_pass:
        assert complaint is None, (
            f"{why}: the {name} about to be sent fails the strict check, so the negative "
            f"would pass whatever the check under test does: {complaint}"
        )
    else:
        assert complaint is not None, (
            f"{why}: declared a malformed message, and the {name} about to be sent passes "
            "the strict check"
        )


def _strict_complaint(name: str, body: bytes) -> str | None:
    """Why the SDK's strict check refuses ``body`` as a ``name``; ``None`` when it accepts."""
    try:
        payload = json.loads(body)
    except ValueError as exc:
        return f"not JSON: {exc}"
    try:
        check_strict(name, payload)
    except StrictViolationError as exc:
        return str(exc)
    return None
