"""Alter a request after the SDK has signed it, for signature negatives.

The SDK signs inside the client and hands the finished request to its HTTP client.
An ``httpx.Client`` built on :class:`AfterSigning` receives exactly those signed
bytes, applies one declared alteration, and sends the result. Every signature
negative is therefore the SDK's own request with one thing changed, never a
request assembled by hand.
"""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass, field

import httpx
from fora_sdk import sfv

#: One alteration of a signed request. Returning the request unchanged is the control.
Alteration = Callable[[httpx.Request], httpx.Request]


#: Receives every request exactly as it goes on the wire, after the alteration.
Observer = Callable[[httpx.Request], None]


class AfterSigning(httpx.BaseTransport):
    """Sends each request ``sends`` times after applying ``alter``; returns the last answer.

    ``sends=2`` with no alteration delivers the same signed bytes twice, which is a
    replay of the first request. ``observe``, when set, sees each request as sent.
    """

    def __init__(
        self,
        alter: Alteration | None = None,
        *,
        sends: int = 1,
        observe: Observer | None = None,
    ) -> None:
        self._alter = alter
        self._sends = sends
        self._observe = observe
        self._inner = httpx.HTTPTransport()

    def handle_request(self, request: httpx.Request) -> httpx.Response:
        request.read()
        if self._alter is not None:
            request = self._alter(request)
        response = self._send(request)
        for _ in range(self._sends - 1):
            response.read()
            response.close()
            response = self._send(_copy(request))
        return response

    def _send(self, request: httpx.Request) -> httpx.Response:
        if self._observe is not None:
            self._observe(request)
        return self._inner.handle_request(request)

    def close(self) -> None:
        self._inner.close()


def after_signing(
    alter: Alteration | None = None, *, sends: int = 1, observe: Observer | None = None
) -> httpx.Client:
    """An HTTP client for the SDK that alters each signed request before sending it."""
    return httpx.Client(transport=AfterSigning(alter, sends=sends, observe=observe))


@dataclass
class Capture:
    """Records every request an SDK client sends, exactly as it goes on the wire.

    Hand :meth:`client` to an SDK client as its ``http``. The SDK sends every leg
    through an injected client, the guarded ones included (a usage report, a
    content fetch), so the record is complete. A test reads it through
    :meth:`header_values`, for an assertion about what it sent; what it was
    answered it reads from the SDK's typed results, as always.
    """

    _sent: list[httpx.Request] = field(default_factory=list)

    def client(self) -> httpx.Client:
        """An HTTP client for the SDK that sends each request unaltered and records it."""
        return after_signing(observe=self._sent.append)

    def last(self) -> httpx.Request:
        """The last request recorded, exactly as sent: what :func:`resend` sends again."""
        assert self._sent, "no request was recorded: the capture was never handed to a client"
        return self._sent[-1]

    def header_values(self, name: str) -> list[tuple[str, str]]:
        """``(url, value)`` of header ``name`` on every recorded request; ``""`` when absent."""
        assert self._sent, "no request was recorded: the capture was never handed to a client"
        return [(str(request.url), request.headers.get(name, "")) for request in self._sent]


def _copy(request: httpx.Request, *, content: bytes | None = None, drop: str = "") -> httpx.Request:
    headers = [
        (k, v)
        for k, v in request.headers.multi_items()
        if k.lower() not in {drop.lower(), "content-length"}
    ]
    body = request.content if content is None else content
    return httpx.Request(request.method, request.url, headers=headers, content=body)


def drop_header(name: str) -> Alteration:
    """Remove one header the signature may cover."""
    return lambda request: _copy(request, drop=name)


def edit_body(edit: Callable[[bytes], bytes]) -> Alteration:
    """Send edited body bytes under the signature made over the original ones."""
    return lambda request: _copy(request, content=edit(request.content))


def resend(original: httpx.Request) -> Alteration:
    """Send ``original``'s signed bytes in place of the request the SDK just signed.

    For a replay sent long after the first send: the test keeps the first request
    from a :class:`Capture` and later hands this alteration to a new client, so the
    SDK still decodes the answer while the bytes on the wire are the old ones.
    """
    return lambda _signed: _copy(original)


def append_field_line(name: str, value: str) -> Alteration:
    """Add a second ``name`` field line carrying ``value`` beside the one the SDK signed."""

    def alter(request: httpx.Request) -> httpx.Request:
        headers = [
            (k, v) for k, v in request.headers.multi_items() if k.lower() != "content-length"
        ]
        headers.append((name, value))
        return httpx.Request(request.method, request.url, headers=headers, content=request.content)

    return alter


def second_signature_agent_line(origin: str) -> Alteration:
    """Add a second Signature-Agent field line that redefines the signed member as ``origin``.

    Under the Web Bot Auth profile a signature covers one member of the
    Signature-Agent dictionary (``"signature-agent";key="<label>"``), not the whole
    field. A second field line that adds another member leaves the covered one
    unchanged, which is a request carrying a further party, not an altered one. A
    second line that names the covered member again changes its value, because a
    Structured Field Dictionary keeps the last value given for a key, and that is
    the alteration this builds.
    """

    def alter(request: httpx.Request) -> httpx.Request:
        signed = sfv.parse_dictionary(request.headers.get_list("signature-agent"))
        assert len(signed) == 1, f"the request carries {len(signed)} Signature-Agent members"
        (member,) = signed
        return append_field_line("signature-agent", f'{member}="{origin}"')(request)

    return alter


def strip_signature(request: httpx.Request) -> httpx.Request:
    """Remove the signature entirely: both Signature and Signature-Input."""
    return drop_header("signature-input")(drop_header("signature")(request))
