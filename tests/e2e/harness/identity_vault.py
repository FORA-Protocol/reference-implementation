"""The identity service's Vault, driven through Vault's HTTP API as its key custody does.

identity-vault-init hands the identity service a token scoped to one policy. The
vault-scope suite proves that scope by making the calls key custody makes, under
that token, and showing what is refused. This module is those calls, each
returning a typed :class:`VaultAnswer` rather than a raw response, so a test
asserts on a status and the parsed ``data`` member and nothing else.

In-network only: identity-vault publishes no host port.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from http import HTTPStatus
from typing import Any

import httpx

VAULT_URL = "http://identity-vault:8200"

_TIMEOUT_SEC = 10.0


@dataclass(frozen=True)
class VaultAnswer:
    """One Vault answer: its status and, for a JSON answer, its ``data`` member."""

    status: HTTPStatus
    data: dict[str, Any] = field(default_factory=dict)
    #: The start of the answer body, for a failure message.
    excerpt: str = ""


class VaultSession:
    """Vault's HTTP API under one token. Close it, or use it as a context manager."""

    def __init__(self, token: str) -> None:
        self._http = httpx.Client(
            base_url=VAULT_URL, headers={"X-Vault-Token": token}, timeout=_TIMEOUT_SEC
        )

    def __enter__(self) -> VaultSession:
        return self

    def __exit__(self, *_exc: object) -> None:
        self.close()

    def close(self) -> None:
        self._http.close()

    def write(self, path: str, data: dict[str, Any]) -> VaultAnswer:
        """Write a KV v2 secret at ``path`` (a ``/v1/<mount>/data/...`` path)."""
        return _answer(self._http.post(path, json={"data": data}))

    def read(self, path: str, *, version: int | None = None) -> VaultAnswer:
        """Read the secret at ``path``, the latest version unless ``version`` names one."""
        params = {"version": version} if version is not None else None
        return _answer(self._http.get(path, params=params))

    def list_keys(self, path: str) -> VaultAnswer:
        """List the keys under ``path`` (a ``/v1/<mount>/metadata/...`` path)."""
        return _answer(self._http.request("LIST", path))

    def delete(self, path: str) -> VaultAnswer:
        """Delete ``path``; on a metadata path that erases every version."""
        return _answer(self._http.delete(path))

    def lookup_token(self, token: str) -> VaultAnswer:
        """Look up another token, which needs more than the identity policy grants."""
        return _answer(self._http.post("/v1/auth/token/lookup", json={"token": token}))

    def lookup_self(self) -> VaultAnswer:
        """Look up this session's own token."""
        return _answer(self._http.get("/v1/auth/token/lookup-self"))

    def enable_kv_mount(self, name: str) -> VaultAnswer:
        """Enable a new KV mount called ``name``."""
        return _answer(self._http.post(f"/v1/sys/mounts/{name}", json={"type": "kv"}))


def _answer(response: httpx.Response) -> VaultAnswer:
    data: dict[str, Any] = {}
    # Vault labels even an empty 204 answer application/json, so the body decides.
    if response.content and response.headers.get("content-type", "").startswith("application/json"):
        payload = response.json()
        if isinstance(payload, dict) and isinstance(payload.get("data"), dict):
            data = payload["data"]
    return VaultAnswer(
        status=HTTPStatus(response.status_code), data=data, excerpt=response.text[:200]
    )
