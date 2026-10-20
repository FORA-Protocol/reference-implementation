"""Reading the edge: delivered content, proof-free probes, plain browser reads.

It also holds the one unsigned POST the harness sends to a route outside the
protocol, because that answer is read the same way as an edge's.

Under Phase 2b every demo publisher domain resolves in-network to its edge on
port 80 via a docker-compose network alias, so a broker-returned signed URL
(``http://demo.fora-protocol.org/...``) is fetched DIRECTLY: no netloc rewrite,
no Host override (both are forbidden).

The ways to read an edge, and what each sends:

* :func:`fetch_signed` fetches delivered content through the SDK's own ``fetch``
  verb, which mints the proof of possession of the agent's key inside the SDK.
  The harness signs nothing itself.
* :func:`fetch_without_proof` sends a delivery URL with NO proof of possession.
  It is the one way a test drives the edge's refusal of a leaked or tampered
  URL, and its name says what is left out.
* :func:`browse` is an ordinary browser-style GET with a chosen User-Agent, for
  the publisher's own pages and the bot gate in front of them.
* :func:`present` sends one request with exactly the headers it is given, for a
  case the SDK's ``fetch`` cannot express: a proof of possession the harness
  altered, or a valid proof sent under a chosen User-Agent. The headers come
  from ``harness.edge_runtimes``, which mints every proof through the SDK.
* :func:`post_unsigned_json` POSTs a JSON body with no request signature to a
  route the protocol does not define, for a test that proves the route is gone.

Each returns a parsed, typed answer, never a raw response: the SDK's
``Content`` for delivered bytes, :class:`EdgeAnswer` for the others.
"""

from __future__ import annotations

import json
import os
import re
from contextlib import closing
from collections.abc import Mapping
from dataclasses import dataclass, field
from http import HTTPStatus
from pathlib import Path

import httpx
from fora_sdk.client import Content

from .sdk_client import sdk_content_client
from .stack_urls import StackURLs

_TIMEOUT_SEC = 30.0


@dataclass(frozen=True)
class EdgeAnswer:
    """One edge answer, read whole: its status, media type, body and headers.

    ``headers`` is keyed by lowercased name. A header the answer repeats is joined
    with ", " as HTTP allows, so a test that requires one value sees the repeat.
    """

    status: HTTPStatus
    media_type: str
    body: bytes
    headers: Mapping[str, str] = field(default_factory=dict)

    def header(self, name: str) -> str | None:
        """One response header, or None when the answer does not carry it."""
        return self.headers.get(name.lower())

    @property
    def text(self) -> str:
        """The body as text, with undecodable bytes replaced rather than raised on."""
        return self.body.decode("utf-8", errors="replace")

    @property
    def reason(self) -> str:
        """The edge's refusal token, or a description of why there was none.

        A 403 that arrives as HTML or empty is exactly the failure worth
        diagnosing, so a body that is not a JSON object with a ``reason`` yields a
        marker the assertion message can show, rather than an exception that hides
        what arrived.
        """
        if self.media_type != "application/json":
            return f"<non-JSON body: {self.text[:120]!r}>"
        try:
            payload = json.loads(self.body)
        except ValueError:
            return f"<unparseable JSON: {self.text[:120]!r}>"
        if not isinstance(payload, dict):
            return f"<JSON is {type(payload).__name__}, not an object>"
        return str(payload.get("reason", "<no reason field>"))


def edge_fetch_target(signed: str, compose_stack: StackURLs) -> tuple[str, dict[str, str]]:
    """Return ``(url, headers)`` to fetch a broker-returned signed URL.

    In-network (the runner) the demo domain resolves to its edge on port 80, so
    the signed URL is fetched verbatim with no extra headers. Host runs (no
    ``FORA_E2E_IN_NETWORK``) cannot resolve the demo domains without /etc/hosts
    entries; the suites' primary execution mode is in-network.
    """
    del compose_stack  # in-network: the demo domain resolves natively
    if os.environ.get("FORA_E2E_IN_NETWORK") == "1":
        return signed, {}
    # Host fallback: the demo domains do not resolve off the compose network.
    # Returning the URL unchanged lets a host run surface a clear DNS error
    # rather than silently rewriting to the wrong authority (the Host-override
    # trick is forbidden). Run the suite in-network (`make test-e2e`).
    return signed, {}


def tamper_query_param(signed: str, param: str = "sig") -> str:
    """Flip one base64url character in the middle of ``param``'s value.

    The single home for the "corrupt a signed URL" idiom every edge runtime's
    tamper negative needs. One character is enough — the signature covers the
    whole URL — and changing it in the middle keeps the value's length and
    encoding valid, so what the edge rejects is the signature itself and not a
    malformed parameter.
    """
    match = re.search(rf"{param}=([^&]+)", signed)
    assert match, f"no {param}= in {signed}"
    value = match.group(1)
    middle = len(value) // 2
    replacement = "B" if value[middle] != "B" else "C"
    tampered = value[:middle] + replacement + value[middle + 1 :]
    return signed.replace(f"{param}={value}", f"{param}={tampered}")


def replace_query_param(signed: str, param: str, value: str) -> str:
    """Set ``param``'s value in ``signed`` to ``value``, leaving every other byte alone.

    For a URL that a party other than the Exchange rewrites: a buyer's delivery
    URL whose ``agent_id`` is changed to another agent's thumbprint, say. The
    signature is left as it was, so the edge is handed a URL the Exchange did not
    sign. ``param`` must occur exactly once.
    """
    pattern = rf"([?&]){re.escape(param)}=[^&#]*"
    rewritten, count = re.subn(pattern, lambda m: f"{m.group(1)}{param}={value}", signed)
    assert count == 1, f"{param}= occurs {count} times in {signed}, want once"
    return rewritten


def fetch_signed(
    signed: str,
    compose_stack: StackURLs,
    *,
    key_path: Path,
    expect_marker: str | None = None,
) -> Content:
    """Fetch a delivered signed URL through the SDK as the agent at ``key_path``.

    The single home for the happy-path content-delivery idiom. The SDK's
    ``fetch`` presents proof of possession of the agent's key (X-FORA-Agent-Key
    plus an RFC 9421 signature over the GET), which an agent-bound URL requires
    (ADR-013): the edge checks agent_id == keyid == thumbprint(presented key), so
    ``key_path`` MUST be the key the Exchange bound the URL to at execute time. A
    CloudFront RSA URL carries no binding and the shim ignores the proof.

    A refusal raises the SDK's typed ``CallError``. When ``expect_marker`` is
    given, the delivered body must contain it, so a 200 edge error-stub or a
    wrong-route body fails here.
    """
    url, _headers = edge_fetch_target(signed, compose_stack)
    with closing(sdk_content_client(compose_stack, key_path)) as client:
        content = client.fetch(url)
    if expect_marker is not None:
        text = content.body.decode("utf-8", errors="replace")
        assert expect_marker in text, (
            f"delivered body missing expected marker {expect_marker!r}: {text[:256]}"
        )
    return content


def fetch_without_proof(url: str) -> EdgeAnswer:
    """GET a delivery URL presenting NO proof of possession, for a refusal test.

    A bound URL fetched this way must be refused ``missing_agent_key``; a tampered
    one must be refused whatever the binding. Redirects are followed, so a refusal
    is read from the edge that finally answers.
    """
    return _send("GET", url, {})


def browse(url: str, *, user_agent: str) -> EdgeAnswer:
    """GET ``url`` the way a client announcing ``user_agent`` would, with nothing else sent."""
    return _send("GET", url, {"User-Agent": user_agent})


def present(
    url: str,
    *,
    headers: Mapping[str, str],
    method: str = "GET",
    body: bytes | None = None,
) -> EdgeAnswer:
    """Send ``method`` to ``url`` with exactly ``headers`` and ``body``; redirects are not followed.

    Exactly: with no User-Agent in ``headers`` the request carries none. The answer
    is the one the edge this URL names gave, so a refusal is the edge's own and a
    served read is what the edge forwarded from the origin.
    """
    return _send(
        method, url, dict(headers), content=body, follow_redirects=False, exact_headers=True
    )


def post_unsigned_json(url: str, payload: dict[str, str]) -> EdgeAnswer:
    """POST ``payload`` as JSON to ``url`` with no request signature and no proof.

    The protocol signs every request it defines, so this reaches only a route it
    does not define. Redirects are not followed, so the answer is the one ``url``
    gave.
    """
    return _send(
        "POST",
        url,
        {"Content-Type": "application/json"},
        content=json.dumps(payload).encode(),
        follow_redirects=False,
    )


def _send(
    method: str,
    url: str,
    headers: dict[str, str],
    *,
    content: bytes | None = None,
    follow_redirects: bool = True,
    exact_headers: bool = False,
) -> EdgeAnswer:
    """Send one request. ``exact_headers`` drops the User-Agent httpx adds by default.

    Without it, a request whose caller sent no User-Agent would still carry
    "python-httpx/<version>", and the edge would judge that string instead of an
    absent header.
    """
    with httpx.Client(timeout=_TIMEOUT_SEC, follow_redirects=follow_redirects) as client:
        request = client.build_request(method, url, headers=headers, content=content)
        if exact_headers and "user-agent" not in {name.lower() for name in headers}:
            del request.headers["user-agent"]
        response = client.send(request)
    return EdgeAnswer(
        status=HTTPStatus(response.status_code),
        media_type=response.headers.get("content-type", "").split(";")[0].strip(),
        body=response.content,
        headers={name: ", ".join(response.headers.get_list(name)) for name in response.headers},
    )
