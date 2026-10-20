"""The MCP send seam: every MCP tool call a test makes goes through this module.

ADR-028 D3 puts one send seam in front of every surface and checks each answer
against a published contract before any assertion reads it. On the ``mcp``
surface the contract is the tool's own declaration in ``tools/list``:

* **the output.** Every tool must declare an ``outputSchema``, and every
  successful result must carry ``structuredContent`` that passes it. The check
  is strict: an object schema that declares ``properties`` and says nothing
  about ``additionalProperties`` is closed before it is applied, the way the
  protocol's strict schema variant refuses unknown fields. An object the tool
  declares open on purpose (``additionalProperties: true``, as on the offers it
  passes through unchanged) stays open.
* **the input.** Every call's arguments are checked against the tool's
  ``inputSchema`` before they are sent, and the verdict has to agree with what
  the test declared. An undeclared call must pass it. A call declared invalid
  with ``refused(..., input_invalid=why)`` must fail it, so a test that claims
  "the input schema refuses this argument" is checked against the schema the
  service publishes, not against wording in the reply. This is ADR-028 D5's
  schema-verdict rule, applied to tool arguments.

A tool error is raised as :class:`McpToolError`, never returned. It carries the
``structuredContent`` the server attached to the error, or None when the server
sent only text. The text is kept out of reach on purpose (ADR-028 D4: message
text is never asserted); it appears only in the exception's string, for a
failure message. A tool error's structured content must be
``{"refusal": UpstreamRefusal}``: the party that refused, the Connect code it
answered with and its FORA ErrorDetail unchanged. The service sends text alone
on every tool error today (``MCP_TOOL_ERROR_DETAIL_DROPPED`` in
``defects.py``), so a test can assert that a call was refused and what it left
behind, and nothing about the wording.

Test modules may not import ``fastmcp`` or ``mcp`` (``ruff-harness.toml``), so
this module is the only way a test reaches the endpoint as an MCP client. The
transport-level reads, below any MCP client, are in ``mcp_transport.py``.
"""

from __future__ import annotations

import base64
import copy
import json
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager
from dataclasses import dataclass
from typing import Any

from fastmcp import Client
from jsonschema import Draft202012Validator, FormatChecker
from mcp import types

from .stack_urls import StackURLs

#: How long one call may take before the client gives up, in seconds.
DEFAULT_TIMEOUT_SEC = 30.0


def _closed(schema: Any) -> Any:
    """``schema`` with every object that declares properties closed to undeclared members.

    An explicit ``additionalProperties`` is left as written, whichever way it
    goes: only the silent default is tightened.
    """
    if isinstance(schema, list):
        return [_closed(item) for item in schema]
    if not isinstance(schema, dict):
        return schema
    out = {key: _closed(value) for key, value in schema.items()}
    if "properties" in out and "additionalProperties" not in out:
        out["additionalProperties"] = False
    return out


def _validator(schema: Mapping[str, Any]) -> Draft202012Validator:
    Draft202012Validator.check_schema(schema)
    return Draft202012Validator(schema, format_checker=FormatChecker())


def _violations(validator: Draft202012Validator, instance: object) -> list[str]:
    """Each way ``instance`` fails ``validator``, as ``path: message``."""
    return [
        f"{'/'.join(str(p) for p in error.absolute_path) or '<root>'}: {error.message}"
        for error in validator.iter_errors(instance)
    ]


@dataclass(frozen=True)
class ToolContract:
    """What one tool declares in ``tools/list``: its input and output schemas."""

    name: str
    input_schema: dict[str, Any]
    output_schema: dict[str, Any]

    @classmethod
    def of(cls, tool: Mapping[str, Any]) -> ToolContract:
        """The contract of one ``tools/list`` entry, read as the JSON the server sent."""
        name = str(tool["name"])
        output = tool.get("outputSchema")
        assert isinstance(output, dict), f"{name} declares no outputSchema: {tool}"
        assert output.get("type") == "object", f"{name}'s outputSchema is not an object: {output}"
        return cls(name=name, input_schema=dict(tool["inputSchema"]), output_schema=output)

    def check_output(self, structured: object) -> dict[str, Any]:
        """``structured`` as a tool result, failing unless it passes the closed outputSchema."""
        assert isinstance(structured, dict), (
            f"{self.name} returned no structuredContent object: {structured!r}"
        )
        problems = _violations(_validator(_closed(self.output_schema)), structured)
        assert not problems, f"{self.name}'s result fails its outputSchema: {problems}"
        return structured

    def input_violations(self, arguments: Mapping[str, Any]) -> list[str]:
        """Each way ``arguments`` fail the inputSchema, empty when they pass."""
        return _violations(_validator(self.input_schema), dict(arguments))


def contracts_of(tools: list[Mapping[str, Any]]) -> dict[str, ToolContract]:
    """The contract of every tool a ``tools/list`` result names, by name."""
    contracts = {str(tool["name"]): ToolContract.of(tool) for tool in tools}
    assert contracts, "tools/list named no tools"
    return contracts


@dataclass(frozen=True)
class EmbeddedResource:
    """One resource a tool result carries inline, with its bytes decoded."""

    uri: str
    mime_type: str
    body: bytes


@dataclass(frozen=True)
class ToolResult:
    """A successful tool call, read after its ``structuredContent`` passed the contract."""

    tool: str
    #: The structured result, already checked against the tool's outputSchema.
    structured: dict[str, Any]
    #: The JSON the first content block carries when it is a text block, else None.
    #: MCP has a tool that returns structured content repeat it as text, for clients
    #: that read only the content blocks.
    text_copy: Any
    #: Every embedded resource among the content blocks, in order.
    embedded: tuple[EmbeddedResource, ...]
    #: How many content blocks the result carried in all.
    block_count: int


class McpToolError(Exception):
    """A tool call the server answered with ``isError``.

    ``structured`` is the ``structuredContent`` the server attached to the
    error, or None when it sent only text. The text is not an attribute: it is
    in the string, for a failure message, and nowhere a test can assert on it.
    """

    def __init__(self, tool: str, structured: dict[str, Any] | None, text: str) -> None:
        super().__init__(f"{tool} answered a tool error: {text[:500]}")
        self.tool = tool
        self.structured = structured


def _embedded(block: types.ContentBlock) -> EmbeddedResource | None:
    if not isinstance(block, types.EmbeddedResource):
        return None
    resource = block.resource
    if isinstance(resource, types.BlobResourceContents):
        body = base64.b64decode(resource.blob)
    else:
        body = resource.text.encode()
    return EmbeddedResource(
        uri=str(resource.uri), mime_type=str(resource.mimeType or ""), body=body
    )


def _text_copy(blocks: list[types.ContentBlock]) -> Any:
    if not blocks or not isinstance(blocks[0], types.TextContent):
        return None
    try:
        return json.loads(blocks[0].text)
    except json.JSONDecodeError:
        return None


def _text_of(blocks: list[types.ContentBlock]) -> str:
    return "".join(block.text for block in blocks if isinstance(block, types.TextContent))


class McpSession:
    """One open MCP session at the identity service, as one agent."""

    def __init__(self, client: Client, contracts: dict[str, ToolContract]) -> None:
        self._client = client
        self._contracts = contracts

    def contract(self, tool: str) -> ToolContract:
        """What ``tool`` declared in ``tools/list``; fails when the server lists no such tool."""
        assert tool in self._contracts, f"tools/list names no {tool}: {sorted(self._contracts)}"
        return self._contracts[tool]

    async def call(self, tool: str, arguments: Mapping[str, Any] | None = None) -> ToolResult:
        """Call ``tool`` with arguments its inputSchema accepts, and return the checked result.

        Raises :class:`McpToolError` when the server answers a tool error.
        """
        args = dict(arguments or {})
        problems = self.contract(tool).input_violations(args)
        assert not problems, (
            f"{tool} was called with arguments its inputSchema refuses, without declaring it "
            f"(use refused(..., input_invalid=...)): {problems}"
        )
        return await self._send(tool, args)

    async def refused(
        self,
        tool: str,
        arguments: Mapping[str, Any],
        *,
        input_invalid: str | None = None,
    ) -> McpToolError:
        """Call ``tool`` and return the tool error it must be answered with.

        ``input_invalid`` declares, in words, why the tool's inputSchema refuses
        ``arguments``; the seam checks that it does. Left None, the arguments must
        pass the inputSchema, so the refusal is one the tool's handler made.
        """
        args = dict(arguments)
        problems = self.contract(tool).input_violations(args)
        if input_invalid is None:
            assert not problems, (
                f"{tool}'s inputSchema refuses these arguments, which the call did not "
                f"declare: {problems}"
            )
        else:
            assert problems, (
                f"declared refused by {tool}'s inputSchema ({input_invalid}), but the "
                f"published schema accepts {args}"
            )
        try:
            answered = await self._send(tool, args)
        except McpToolError as refusal:
            return refusal
        raise AssertionError(f"{tool} accepted {args}: {answered.structured}")

    async def _send(self, tool: str, args: dict[str, Any]) -> ToolResult:
        result = await self._client.call_tool_mcp(tool, args)
        blocks = list(result.content)
        if result.isError:
            raise McpToolError(tool, result.structuredContent, _text_of(blocks))
        structured = self.contract(tool).check_output(result.structuredContent)
        embedded = tuple(e for e in (_embedded(block) for block in blocks) if e is not None)
        return ToolResult(
            tool=tool,
            structured=copy.deepcopy(structured),
            text_copy=_text_copy(blocks),
            embedded=embedded,
            block_count=len(blocks),
        )


@asynccontextmanager
async def mcp_session(
    stack: StackURLs, bearer: str, *, timeout: float = DEFAULT_TIMEOUT_SEC
) -> AsyncIterator[McpSession]:
    """Open an MCP session at the identity service's ``/mcp`` endpoint as ``bearer``.

    Lists the tools first and reads every contract out of the answer, failing
    when a tool declares no outputSchema, so no call can be made against a tool
    whose result could not be checked.
    """
    async with Client(f"{stack.identity}/mcp", auth=bearer, timeout=timeout) as client:
        listed = await client.list_tools_mcp()
        tools = [tool.model_dump(mode="json", by_alias=True) for tool in listed.tools]
        yield McpSession(client, contracts_of(tools))
