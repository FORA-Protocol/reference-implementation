"""The identity service's MCP endpoint read at the HTTP level, below any MCP client.

The transport rules of the endpoint are rules about HTTP: which methods it
answers, which ``Mcp-Protocol-Version`` values it accepts, what it does with the
``Mcp-Method`` and ``Mcp-Name`` headers, whether it issues ``Mcp-Session-Id``,
and how it refuses a caller with no usable bearer. An MCP client hides every one
of them, so the obligations about them are driven here, as raw JSON-RPC posts.
The contract is the "MCP transport" section of the identity service overview in
the pinned protocol module ``github.com/FORA-Protocol/protocol``
(``website/src/content/docs/components/identity/overview.mdx``), and the MCP
Streamable HTTP transport it names.

A request is built once, correctly, by :func:`rpc`, and a negative changes it
through one of :class:`McpHttpRequest`'s ``with_*`` / ``without_*`` methods, so
the alteration under test is the only difference from the control request
(ADR-028 D5). An answer comes back as :class:`McpHttpAnswer`, a typed reading of
the status, the headers these rules are about and the JSON-RPC message. A tool
result read through :meth:`McpHttpAnswer.tool_result` passes the same
outputSchema check the MCP send seam applies (``mcp_client.py``).

Tool calls a test makes as an MCP client go through ``mcp_client.py``. This
module is for the rules an MCP client cannot show.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field, replace
from http import HTTPStatus
from typing import Any

import httpx

from .mcp_client import ToolContract, contracts_of

#: The protocol version that drops the handshake and carries routing headers.
STATELESS_VERSION = "2026-07-28"

#: The older versions the endpoint still serves, newest first.
LEGACY_VERSIONS = ("2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05")

#: Every version the overview says the endpoint accepts.
SUPPORTED_VERSIONS = (STATELESS_VERSION, *LEGACY_VERSIONS)

VERSION_HEADER = "Mcp-Protocol-Version"
METHOD_HEADER = "Mcp-Method"
NAME_HEADER = "Mcp-Name"
SESSION_HEADER = "Mcp-Session-Id"

#: The ``_meta`` key a stateless-version request names its version under.
META_VERSION = "io.modelcontextprotocol/protocolVersion"

_CLIENT_INFO = {"name": "fora-e2e-transport", "version": "1"}
_TIMEOUT_SEC = 30.0


@dataclass(frozen=True)
class McpHttpRequest:
    """One HTTP request to the MCP endpoint: the method, the headers and the JSON-RPC body."""

    http_method: str
    bearer: str | None
    headers: tuple[tuple[str, str], ...] = ()
    body: dict[str, Any] | None = None

    def with_header(self, name: str, value: str) -> McpHttpRequest:
        """This request with ``name`` set to ``value``, replacing any value it had."""
        kept = tuple((k, v) for k, v in self.headers if k.lower() != name.lower())
        return replace(self, headers=(*kept, (name, value)))

    def without_header(self, name: str) -> McpHttpRequest:
        """This request without the header ``name``."""
        return replace(
            self, headers=tuple((k, v) for k, v in self.headers if k.lower() != name.lower())
        )

    def without_meta(self, key: str) -> McpHttpRequest:
        """This request with ``key`` removed from the body's ``params._meta``."""
        assert self.body is not None, "a request with no body carries no _meta"
        body = json.loads(json.dumps(self.body))
        meta = body.get("params", {}).get("_meta", {})
        assert key in meta, f"the request carries no _meta {key!r} to remove: {self.body}"
        del meta[key]
        return replace(self, body=body)

    def with_version(self, version: str) -> McpHttpRequest:
        """This request naming ``version`` everywhere it names one: the header and ``_meta``."""
        changed = self.with_header(VERSION_HEADER, version)
        if self.body is None:
            return changed
        body = json.loads(json.dumps(self.body))
        meta = body.get("params", {}).get("_meta")
        if isinstance(meta, dict) and META_VERSION in meta:
            meta[META_VERSION] = version
        return replace(changed, body=body)

    def with_bearer(self, bearer: str | None) -> McpHttpRequest:
        """This request presenting ``bearer``, or no credential at all."""
        return replace(self, bearer=bearer)


def rpc(
    method: str,
    params: dict[str, Any] | None = None,
    *,
    version: str,
    bearer: str | None,
    rpc_id: int = 1,
) -> McpHttpRequest:
    """A correct JSON-RPC request for ``method`` at ``version``.

    Every request names its version in ``Mcp-Protocol-Version``. At the stateless
    version it also carries the method in ``Mcp-Method``, a tool call's name in
    ``Mcp-Name``, and the version, client and capabilities in ``params._meta``,
    which is what that version has in place of a handshake.
    """
    params = json.loads(json.dumps(params or {}))
    headers = [(VERSION_HEADER, version)]
    if version >= STATELESS_VERSION:
        headers.append((METHOD_HEADER, method))
        if method == "tools/call":
            headers.append((NAME_HEADER, str(params["name"])))
        params["_meta"] = {
            META_VERSION: version,
            "io.modelcontextprotocol/clientInfo": _CLIENT_INFO,
            "io.modelcontextprotocol/clientCapabilities": {},
        }
    body = {"jsonrpc": "2.0", "id": rpc_id, "method": method, "params": params}
    return McpHttpRequest(http_method="POST", bearer=bearer, headers=tuple(headers), body=body)


def tool_call(
    name: str, arguments: dict[str, Any], *, version: str, bearer: str | None
) -> McpHttpRequest:
    """A correct ``tools/call`` of ``name`` with ``arguments`` at ``version``."""
    return rpc("tools/call", {"name": name, "arguments": arguments}, version=version, bearer=bearer)


def initialize(*, version: str, bearer: str | None) -> McpHttpRequest:
    """An ``initialize`` request asking for ``version``."""
    params = {"protocolVersion": version, "capabilities": {}, "clientInfo": _CLIENT_INFO}
    return rpc("initialize", params, version=version, bearer=bearer)


def bodyless(http_method: str, *, bearer: str | None) -> McpHttpRequest:
    """A request with no body, for the methods the endpoint does not serve."""
    return McpHttpRequest(http_method=http_method, bearer=bearer)


@dataclass(frozen=True)
class McpHttpAnswer:
    """How the endpoint answered one request, read at the HTTP level."""

    status: HTTPStatus
    #: The ``Content-Type`` essence, lowercased and without parameters.
    media_type: str
    #: The ``Allow`` header, empty when absent.
    allow: str
    #: The ``WWW-Authenticate`` challenge, empty when absent.
    www_authenticate: str
    #: The ``Mcp-Session-Id`` header, None when absent.
    session_id: str | None
    #: The JSON-RPC message the body carries, None when it is not one.
    message: dict[str, Any] | None
    #: The start of the body, for a failure message.
    excerpt: str = field(repr=False)

    def result(self) -> dict[str, Any]:
        """The JSON-RPC result, failing unless the answer is a 200 JSON body carrying one."""
        assert self.status == HTTPStatus.OK, f"answered {self.status}: {self.excerpt}"
        assert self.media_type == "application/json", (
            f"answered {self.media_type!r}, not one application/json body: {self.excerpt}"
        )
        assert self.message is not None and "error" not in self.message, self.excerpt
        result = self.message.get("result")
        assert isinstance(result, dict), f"the answer carries no result object: {self.excerpt}"
        return result

    def tool_result(self, contract: ToolContract) -> dict[str, Any]:
        """A successful tool call's structured result, checked against ``contract``."""
        result = self.result()
        assert not result.get("isError"), f"{contract.name} answered a tool error: {result}"
        return contract.check_output(result.get("structuredContent"))

    def contracts(self) -> dict[str, ToolContract]:
        """Every tool contract a ``tools/list`` answer declares, by name."""
        return contracts_of(self.result()["tools"])


def send(identity_url: str, request: McpHttpRequest) -> McpHttpAnswer:
    """Send ``request`` to the MCP endpoint at ``identity_url`` and read the answer."""
    headers = {"Accept": "application/json, text/event-stream"}
    if request.bearer is not None:
        headers["Authorization"] = f"Bearer {request.bearer}"
    content: bytes | None = None
    if request.body is not None:
        headers["Content-Type"] = "application/json"
        content = json.dumps(request.body).encode()
    headers.update(dict(request.headers))
    response = httpx.request(
        request.http_method,
        f"{identity_url}/mcp",
        headers=headers,
        content=content,
        timeout=_TIMEOUT_SEC,
    )
    media_type = response.headers.get("content-type", "").split(";", 1)[0].strip().lower()
    message: dict[str, Any] | None = None
    if media_type == "application/json" and response.content:
        try:
            parsed = json.loads(response.content)
        except json.JSONDecodeError:
            parsed = None
        message = parsed if isinstance(parsed, dict) else None
    return McpHttpAnswer(
        status=HTTPStatus(response.status_code),
        media_type=media_type,
        allow=response.headers.get("Allow", ""),
        www_authenticate=response.headers.get("WWW-Authenticate", ""),
        session_id=response.headers.get(SESSION_HEADER),
        message=message,
        excerpt=response.text[:300],
    )
