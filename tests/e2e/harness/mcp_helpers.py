"""Shared MCP discovery setup for the harness, over the MCP send seam (``mcp_client.py``)."""

from __future__ import annotations

from typing import Any

from .mcp_client import McpSession


async def first_discovered_offer(mcp: McpSession, uri: str) -> dict[str, Any]:
    """Discover one URI over MCP and return its first group's first offer, unchanged."""
    groups = (await mcp.call("fora_discover", {"uris": [uri]})).structured["offer_groups"]
    assert groups and groups[0]["offers"], f"no offers discovered for {uri!r}: {groups}"
    offer: dict[str, Any] = groups[0]["offers"][0]
    return offer
