"""The pyauth ``http-message-signatures`` pieces the WBA oracle is built from.

pyauth 2.0.1 resolves a covered header component to the whole header value and
ignores the component's ``key`` parameter. For WBA that is the one component that
matters: ``"signature-agent";key="agent2"`` resolves to ``agent2="https://..."``
instead of the member value ``"https://..."`` that RFC 9421 Section 2.1.2 names,
so every dictionary-form Signature-Agent signature fails. pyauth also has no
``req`` parameter (RFC 9421 Section 2.4), which a signed directory response
covers. :class:`WBAComponentResolver` adds both by extending pyauth's resolver
class, which pyauth takes as a constructor argument (``component_resolver_class``). The
library itself is not patched or forked.

What shows the extension is right is the draft's Appendix E: the vectors under
``testdata/wba-httpsig-protocol-00-vectors.json`` cover the dictionary form, the
legacy form and a signed directory response, and ``test_wba_oracle.py``
reproduces each signature byte for byte through this resolver.

Key handling goes through ``joserfc`` (RFC 7638 thumbprints, JWK import), not
through the FORA protocol SDK, for the reason the oracle module states.
"""

from __future__ import annotations

import datetime as dt
from collections.abc import Mapping
from typing import Any

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from http_message_signatures import (
    HTTPMessageVerifier,
    HTTPSignatureKeyResolver,
    algorithms,
    http_sfv,
)
from http_message_signatures.exceptions import HTTPMessageSignaturesException
from http_message_signatures.resolvers import HTTPSignatureComponentResolver
from joserfc.jwk import JWKRegistry, OKPKey


class WBAComponentResolver(HTTPSignatureComponentResolver):
    """pyauth's component resolver plus the ``key`` and ``req`` parameters.

    ``key`` selects one member of a Dictionary Structured Field and resolves to
    that member's serialized value, parameters included. ``req`` resolves a
    component of a response signature against the request that produced the
    response. Every other component is pyauth's own resolution, unchanged.
    """

    def resolve(self, component_node: http_sfv.Item) -> Any:
        params = component_node.params
        if params.get("req"):
            request_node = http_sfv.Item(component_node.value)
            request_node.params.update({k: v for k, v in params.items() if k != "req"})
            return type(self)(self.message.request).resolve(request_node)
        value = super().resolve(component_node)
        name = str(component_node.value)
        key = params.get("key")
        if key is None or name.startswith("@"):
            return value
        members = http_sfv.Dictionary()
        try:
            members.parse(str(value).encode())
        except ValueError as exc:
            msg = f'"{name}" is not a Dictionary Structured Field'
            raise HTTPMessageSignaturesException(msg) from exc
        if key not in members:
            raise HTTPMessageSignaturesException(f'"{name}" has no member "{key}"')
        return str(members[key])


class FixedKeys(HTTPSignatureKeyResolver):
    """Resolves exactly the keys it was built with, by keyid."""

    def __init__(
        self,
        *,
        private: Mapping[str, Ed25519PrivateKey] | None = None,
        public: Mapping[str, Ed25519PublicKey] | None = None,
    ) -> None:
        self._private = dict(private or {})
        self._public = dict(public or {})

    def resolve_private_key(self, key_id: str) -> Ed25519PrivateKey:
        return self._private[key_id]

    def resolve_public_key(self, key_id: str) -> Ed25519PublicKey:
        return self._public[key_id]


class _CryptoOnlyVerifier(HTTPMessageVerifier):
    """pyauth's verifier with its wall-clock time policy switched off.

    The oracle applies its own time checks against the caller's ``now`` before
    this runs, so the Appendix E vectors, whose ``created`` is in 2025, can be
    checked at a fixed instant. pyauth's own check reads the real clock and
    would reject them.
    """

    def validate_created_and_expires(self, _sig_input: Any, max_age: Any = None) -> None:
        return None


def verify_signature(
    message: httpx.Request | httpx.Response,
    *,
    label: str,
    tag: str,
    key: Ed25519PublicKey,
) -> None:
    """Verify one labelled Ed25519 signature on ``message``; raise on any failure.

    Only the RFC 9421 base reconstruction and the signature check run here.
    """
    verifier = _CryptoOnlyVerifier(
        signature_algorithm=algorithms.ED25519,
        key_resolver=FixedKeys(public={key_thumbprint(key): key}),
        component_resolver_class=WBAComponentResolver,
    )
    verifier.verify(message, expect_tag=tag, expect_label=label)


def utc(seconds: int) -> dt.datetime:
    """An aware datetime for epoch ``seconds``, the type pyauth's signer takes."""
    return dt.datetime.fromtimestamp(seconds, tz=dt.UTC)


def key_thumbprint(key: Mapping[str, Any] | Ed25519PublicKey | Ed25519PrivateKey) -> str:
    """RFC 7638 SHA-256 thumbprint, base64url without padding (the WBA keyid)."""
    if isinstance(key, Mapping):
        return JWKRegistry.import_key(dict(key)).thumbprint()
    return OKPKey.import_key(key).thumbprint()


def fresh_key() -> Ed25519PrivateKey:
    """A new Ed25519 key that no directory publishes, for the oracle's own signatures.

    The oracle signs with a raw key because it stands in for a third-party signer
    that holds one. The harness bans minting a key inside a test module, so the
    tests that need a key the stack has never seen take it from here.
    """
    return Ed25519PrivateKey.generate()


def private_key_from_jwk(jwk: Mapping[str, Any]) -> Ed25519PrivateKey:
    """The Ed25519 private key a JWK carrying ``d`` describes."""
    key = OKPKey.import_key(dict(jwk)).private_key
    if not isinstance(key, Ed25519PrivateKey):
        raise ValueError("JWK is not an Ed25519 private key")
    return key


def public_jwk(either_half: Ed25519PrivateKey | Ed25519PublicKey) -> dict[str, Any]:
    """The public JWK (kty, crv, x) for an Ed25519 key, given either half of it."""
    return dict(OKPKey.import_key(either_half).as_dict(private=False))


def private_jwk(key: Ed25519PrivateKey) -> dict[str, Any]:
    """The private JWK (kty, crv, x, d) for an Ed25519 key."""
    return dict(OKPKey.import_key(key).as_dict(private=True))


def ed25519_keys(jwks_or_key: Mapping[str, Any] | Ed25519PublicKey) -> dict[str, Ed25519PublicKey]:
    """Index the Ed25519 keys of a JWKS, a single JWK or a key by RFC 7638 thumbprint.

    Keys of other types are skipped: the oracle verifies Ed25519 only.
    """
    if isinstance(jwks_or_key, Ed25519PublicKey):
        return {key_thumbprint(jwks_or_key): jwks_or_key}
    listed = jwks_or_key.get("keys")
    jwk_list = listed if isinstance(listed, list) else [jwks_or_key]
    keys: dict[str, Ed25519PublicKey] = {}
    for jwk in jwk_list:
        if not isinstance(jwk, Mapping) or jwk.get("kty") != "OKP" or jwk.get("crv") != "Ed25519":
            continue
        public = OKPKey.import_key({k: jwk[k] for k in ("kty", "crv", "x")}).public_key
        if isinstance(public, Ed25519PublicKey):
            keys[key_thumbprint(public)] = public
    return keys
