"""Minting the holder-bound JWT delegation chain a subscription-gated term is reached with.

The pinned protocol module (``sdk/go/helpers/delegation.go``, and the authentication
page it documents) defines the profile an Exchange verifies: one compact JWS per
token, ``typ fora-delegation+jwt``, ``alg EdDSA``, claims ``iss``, ``aud``, ``exp``,
``cnf.jkt`` (the RFC 7638 thumbprint of the key the next token, or the request,
must prove) and ``scope``; a child embeds its signed parent in ``fora_parent`` and
carries its own public key in the protected ``jwk`` header. The Exchange never
signs one, so the signer sits here beside the tests, and the verifier is the only
party that reads what it signs.

Only the chains the suite needs are minted: a root token for the agent, and a
root plus one child. Everything the verifier refuses is produced by changing one
input of the happy path, so a test names only what it breaks.
"""

from __future__ import annotations

import base64
import json
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from typing import Any

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from fora_sdk.thumbprint import thumbprint

TOKEN_TYPE = "fora-delegation+jwt"


def _b64url(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def public_key_b64url(key: Ed25519PrivateKey) -> str:
    """The raw public key of ``key`` in base64url, the shape a subscription record stores."""
    return _b64url(key.public_key().public_bytes_raw())


def jkt(public_key: Ed25519PublicKey) -> str:
    """The RFC 7638 thumbprint of ``public_key``, the value ``cnf.jkt`` carries."""
    return thumbprint(public_key.public_bytes_raw())


def _sign_compact(key: Ed25519PrivateKey, header: dict[str, Any], claims: dict[str, Any]) -> str:
    signing_input = (
        _b64url(json.dumps(header, separators=(",", ":")).encode())
        + "."
        + _b64url(json.dumps(claims, separators=(",", ":")).encode())
    )
    return signing_input + "." + _b64url(key.sign(signing_input.encode()))


@dataclass(frozen=True)
class Chain:
    """A chain root-first. ``keys[i]`` signs token ``i``; token ``i``'s ``cnf.jkt`` names
    ``keys[i+1]``'s public key, and the terminal token's names ``holder``, the key the
    request carrying the chain must prove. ``root_issuer`` is the identity the
    subscription record trusts; ``audience`` the Exchange's own host; ``scope`` the
    space-separated scope claim every token carries. ``claims`` and ``headers`` hold
    one override map per token; a ``None`` value deletes the member."""

    keys: tuple[Ed25519PrivateKey, ...]
    holder: Ed25519PublicKey
    root_issuer: str
    audience: str
    scope: str
    expires: datetime = field(
        default_factory=lambda: datetime.now(timezone.utc) + timedelta(hours=1)
    )
    claims: tuple[dict[str, Any], ...] = ()
    headers: tuple[dict[str, Any], ...] = ()


def mint(chain: Chain) -> str:
    """The terminal token of ``chain``: the compact JWS ``Delegation.token`` carries."""
    parent: str | None = None
    for i, key in enumerate(chain.keys):
        public = key.public_key()
        holder = chain.keys[i + 1].public_key() if i + 1 < len(chain.keys) else chain.holder
        header: dict[str, Any] = {"alg": "EdDSA", "typ": TOKEN_TYPE}
        issuer = chain.root_issuer
        if i > 0:
            header["jwk"] = {
                "kty": "OKP",
                "crv": "Ed25519",
                "x": _b64url(public.public_bytes_raw()),
            }
            issuer = jkt(public)
        claims: dict[str, Any] = {
            "iss": issuer,
            "aud": [chain.audience],
            "exp": int(chain.expires.timestamp()),
            "cnf": {"jkt": jkt(holder)},
            "scope": chain.scope,
        }
        if parent is not None:
            claims["fora_parent"] = parent
        for target, overrides in ((header, chain.headers), (claims, chain.claims)):
            if i < len(overrides):
                for name, value in overrides[i].items():
                    if value is None:
                        target.pop(name, None)
                    else:
                        target[name] = value
        parent = _sign_compact(key, header, claims)
    assert parent is not None, "a chain needs at least one key"
    return parent


def token_wire(token: str) -> str:
    """``token`` as proto JSON carries ``Delegation.token``: the JWS bytes in base64."""
    return base64.b64encode(token.encode()).decode()


def delegation_wire(token: str, **mirrors: Any) -> dict[str, Any]:
    """A ``Delegation`` message carrying ``token`` and the plaintext mirror fields given."""
    return {"token": token_wire(token), **mirrors}
