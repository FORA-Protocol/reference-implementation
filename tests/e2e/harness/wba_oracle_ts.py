"""Client for the TypeScript WBA oracle: Cloudflare's ``web-bot-auth`` in a sidecar.

The sidecar (``tests/e2e/wba-oracle-ts/``, service ``wba-oracle-ts`` in
``docker-compose.e2e.yml``) exposes the library's own ``sign`` and ``verify``
over HTTP, plus its JWK import (``verifierFromJWK``), and makes no decision of
its own. It publishes no host port, so only
a harness running inside the compose network can reach it; the tests that use it
skip elsewhere.

The two calls mirror :func:`wba_oracle.sign` and :func:`wba_oracle.verify` so a
test can swap one oracle for the other. Two differences are the library's:

- its verifier enforces its own profile, which is stricter than WG-00 in places
  (it requires ``alg``, a 64-byte nonce when one is present, and no clock skew
  on ``created`` by default) and does not require a Signature-Agent header;
- it takes no request body. No WBA component covers the body; a signer that
  wants it covered sets a Content-Digest header and covers that.

:meth:`TSOracle.import_keys` has no Python twin: it answers whether the library
would verify with each key of a directory, which a directory test asks of every
participant.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from typing import Any

import httpx

from .readiness import wait_healthy

TS_ORACLE_URL = "http://wba-oracle-ts:8090"
_WBA_HEADERS = frozenset({"signature", "signature-input", "signature-agent"})


class TSOracleRefused(Exception):
    """The library refused to sign. Carries its error code and message."""

    def __init__(self, code: str | None, error: str) -> None:
        super().__init__(f"{code}: {error}")
        self.code = code
        self.error = error


@dataclass(frozen=True)
class TSVerifyResult:
    """What the library's ``verify`` returned, or the error it threw."""

    ok: bool
    code: str | None = None
    error: str | None = None
    label: str | None = None
    keyid: str | None = None
    signature_agent: dict[str, Any] | None = None
    raw: dict[str, Any] = field(default_factory=dict)


@dataclass(frozen=True)
class TSImportResult:
    """Which JWKs the library imported (by its own keyid) and its refusals."""

    keyids: tuple[str, ...]
    refused: tuple[str, ...]


def _unsigned_headers(headers: Mapping[str, str] | httpx.Headers) -> dict[str, str]:
    return {k: v for k, v in httpx.Headers(headers).items() if k.lower() not in _WBA_HEADERS}


@dataclass(frozen=True)
class TSOracle:
    """The sidecar at ``base_url``."""

    base_url: str = TS_ORACLE_URL
    timeout: float = 10.0

    def wait_ready(self) -> None:
        wait_healthy(f"{self.base_url}/healthz")

    def _post(self, path: str, payload: Mapping[str, Any]) -> httpx.Response:
        return httpx.post(f"{self.base_url}{path}", json=payload, timeout=self.timeout)

    def sign(
        self,
        request: httpx.Request,
        private_jwk: Mapping[str, Any],
        directory_origin: str,
        *,
        extra_components: Sequence[str | Mapping[str, Any]] = (),
        label: str = "sig1",
        agent_key: str | None = None,
        nonce: str | None = None,
        include_nonce: bool = True,
        created: int | None = None,
        expires: int | None = None,
        target: str = "@authority",
        legacy: bool = False,
    ) -> dict[str, str]:
        """Sign through the library; return Signature-Agent, Signature-Input, Signature.

        A component with parameters is passed as the library's descriptor,
        ``{"name": "signature-agent", "parameters": {"key": "x"}}``. ``nonce``
        absent means the library's ``generateNonce()``.
        """
        payload: dict[str, Any] = {
            "method": request.method,
            "url": str(request.url),
            "headers": _unsigned_headers(request.headers),
            "private_jwk": dict(private_jwk),
            "signature_agent": directory_origin,
            "label": label,
            "extra_components": list(extra_components),
            "target": target,
            "legacy": legacy,
        }
        optional = {"agent_key": agent_key, "created": created, "expires": expires}
        payload.update({k: v for k, v in optional.items() if v is not None})
        if not include_nonce:
            payload["nonce"] = None
        elif nonce is not None:
            payload["nonce"] = nonce
        response = self._post("/sign", payload)
        if response.status_code == 422:
            body = response.json()
            raise TSOracleRefused(body.get("code"), body.get("error", ""))
        response.raise_for_status()
        return dict(response.json()["headers"])

    def import_keys(self, jwks: Mapping[str, Any]) -> TSImportResult:
        """Hand each key of ``jwks`` to the library's ``verifierFromJWK``.

        Says which keys the library would verify with (by the keyid it computes)
        and the library's message for each key it refuses.
        """
        response = self._post("/import", {"jwks": {"keys": list(jwks.get("keys") or [])}})
        response.raise_for_status()
        body = response.json()
        return TSImportResult(
            keyids=tuple(str(entry["keyid"]) for entry in body.get("imported") or []),
            refused=tuple(str(message) for message in body.get("refused") or []),
        )

    def verify(
        self,
        request_headers: Mapping[str, str] | httpx.Headers,
        method: str,
        url: str,
        jwks_or_jwk: Mapping[str, Any],
        *,
        label: str | None = None,
        now: int | None = None,
        max_age: int | None = None,
        clock_skew: int | None = None,
    ) -> TSVerifyResult:
        """Verify through the library. ``jwks_or_jwk`` is a JWKS or one JWK."""
        keys = jwks_or_jwk.get("keys")
        jwks = {"keys": list(keys)} if isinstance(keys, list) else {"keys": [dict(jwks_or_jwk)]}
        payload: dict[str, Any] = {
            "method": method,
            "url": url,
            "headers": dict(httpx.Headers(request_headers).items()),
            "jwks": jwks,
        }
        optional = {"label": label, "now": now, "max_age": max_age, "clock_skew": clock_skew}
        payload.update({k: v for k, v in optional.items() if v is not None})
        response = self._post("/verify", payload)
        response.raise_for_status()
        body = response.json()
        return TSVerifyResult(
            ok=bool(body.get("ok")),
            code=body.get("code"),
            error=body.get("error"),
            label=body.get("label"),
            keyid=body.get("keyid"),
            signature_agent=body.get("signature_agent"),
            raw=body,
        )
