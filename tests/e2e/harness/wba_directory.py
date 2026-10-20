"""The WG-00 key directory rules, applied to a directory a test fetches.

draft-ietf-webbotauth-httpsig-protocol-00 ("WG-00") Section 5.5 and Appendix B
say what a verifier may rely on when it resolves a Signature-Agent origin to keys:
an https URL, a 200 with no redirect followed, a fixed media type, a JWKS whose
``kid`` values are thumbprints, and optionally a response signature proving the
directory holds the keys it lists. One :class:`DirectoryCheck` per rule, so a
failing conformance test names the rule.

The response signature is checked with pyauth through the oracle's resolver,
which handles the ``"@authority";req`` component it covers.
"""

from __future__ import annotations

import hashlib
import json
import time
import urllib.parse
from collections.abc import Mapping
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

import httpx
from http_message_signatures import http_sfv
from http_message_signatures.exceptions import HTTPMessageSignaturesException

from .wba_checks import WBAFailure, b64_any, check_keyid_shape
from .wba_pyauth import ed25519_keys, key_thumbprint, verify_signature

DIRECTORY_PATH = "/.well-known/http-message-signatures-directory"
DIRECTORY_MEDIA_TYPE = "application/http-message-signatures-directory+json"
DIRECTORY_TAG = "http-message-signatures-directory"
# The HTTP Signature Algorithms registry (RFC 9421 Section 6.2.2). WG-00 Section
# 5.5.1 restricts a directory JWK's alg to these names, so the JOSE name "EdDSA"
# is not among them.
SIGNATURE_ALGORITHMS = frozenset(
    {
        "rsa-pss-sha512",
        "rsa-v1_5-sha256",
        "hmac-sha256",
        "ecdsa-p256-sha256",
        "ecdsa-p384-sha384",
        "ed25519",
    }
)
# The members each key type needs, per RFC 7517/7518/8037.
_REQUIRED_MEMBERS = {"OKP": ("crv", "x"), "EC": ("crv", "x", "y"), "RSA": ("n", "e")}
# Private-key members that must never appear in a published directory.
_PRIVATE_MEMBERS = frozenset({"d", "p", "q", "dp", "dq", "qi", "oth", "k"})
_DIGESTS = {"sha-256": hashlib.sha256, "sha-512": hashlib.sha512}


class DirectoryCheck(StrEnum):
    """One WG-00 directory rule, listed in the order a fetch meets them.

    JWKS and KID are applied key by key, so a later key's malformed entry is
    reported after an earlier key's wrong kid.
    """

    # The directory URL is https (WG-00 Section 5.5.1 MUST).
    HTTPS = "https"
    # 200 OK; any other status, a redirect included, is a discovery failure
    # (WG-00 Section 5.5 MUST, and the verifier MUST NOT follow redirects).
    STATUS = "status"
    # Content-Type is application/http-message-signatures-directory+json
    # (WG-00 Section 5.5.1 MUST at the well-known URI).
    MEDIA_TYPE = "media_type"
    # The body is a JWKS (RFC 7517 Section 5) of well-formed public keys whose
    # alg, when present, is a registered HTTP signature algorithm (WG-00 5.5.1),
    # or, when the caller names an expected alg, is present and equals it.
    JWKS = "jwks"
    # A kid, when present, equals the key's RFC 7638 thumbprint (WG-00 5.5).
    KID = "kid"
    # A response signature tagged http-message-signatures-directory, when
    # present or required, meets WG-00 Appendix B.1 and verifies.
    SIGNED_RESPONSE = "signed_response"


@dataclass(frozen=True)
class DirectoryResult:
    """The outcome of one directory check."""

    ok: bool
    failed: DirectoryCheck | None
    detail: str
    jwks: dict[str, Any] = field(default_factory=dict)
    # Thumbprints of the keys a valid response signature proves possession of.
    signed_by: tuple[str, ...] = ()


class _DirectoryFailure(Exception):
    def __init__(self, check: DirectoryCheck, detail: str) -> None:
        super().__init__(f"{check}: {detail}")
        self.check = check
        self.detail = detail


def directory_url(url: str) -> str:
    """The well-known directory URL for ``url``: itself, or its origin's well-known path."""
    parts = urllib.parse.urlsplit(url)
    if parts.path in ("", "/"):
        return urllib.parse.urlunsplit((parts.scheme, parts.netloc, DIRECTORY_PATH, "", ""))
    return url


def check_directory(
    url: str,
    http_client: httpx.Client,
    *,
    require_signed: bool = False,
    now: int | None = None,
    expected_alg: str | None = None,
) -> DirectoryResult:
    """Fetch the directory at ``url`` (an origin or the full URL) and check it.

    The fetch never follows a redirect, and a URL that is not https is refused
    before it is fetched, because WG-00 serves a key directory over https only.
    ``require_signed`` makes a missing response signature a failure instead of a
    pass, as Cloudflare's registration does.

    ``expected_alg`` replaces the WG-00 Section 5.5.1 rule for a JWK's ``alg``
    (a registered HTTP signature algorithm name, when present) with a stricter
    one of the caller's choosing: every key MUST carry ``alg`` and it MUST
    equal ``expected_alg``. A caller that sets it states the reason, because it
    departs from WG-00.
    """
    target = directory_url(url)
    if urllib.parse.urlsplit(target).scheme != "https":
        return DirectoryResult(
            ok=False, failed=DirectoryCheck.HTTPS, detail=f"{target} is not https"
        )
    response = http_client.get(
        target, headers={"Accept": DIRECTORY_MEDIA_TYPE}, follow_redirects=False
    )
    return check_directory_response(
        response, require_signed=require_signed, now=now, expected_alg=expected_alg
    )


def check_directory_response(
    response: httpx.Response,
    *,
    require_signed: bool = False,
    now: int | None = None,
    expected_alg: str | None = None,
) -> DirectoryResult:
    """Check a fetched directory response; ``response.request`` must be the fetch.

    ``expected_alg`` is as for :func:`check_directory`.
    """
    at = int(time.time()) if now is None else now
    try:
        _check_status(response)
        jwks = _check_jwks(response, expected_alg)
        signed_by = _check_signed_response(response, jwks, require_signed=require_signed, now=at)
    except _DirectoryFailure as failure:
        return DirectoryResult(ok=False, failed=failure.check, detail=failure.detail)
    return DirectoryResult(ok=True, failed=None, detail="", jwks=jwks, signed_by=signed_by)


def _check_status(response: httpx.Response) -> None:
    if response.status_code != 200:
        location = response.headers.get("Location")
        detail = f"status {response.status_code}" + (f" to {location}" if location else "")
        raise _DirectoryFailure(DirectoryCheck.STATUS, detail)
    media_type = response.headers.get("Content-Type", "").split(";")[0].strip().lower()
    if media_type != DIRECTORY_MEDIA_TYPE:
        raise _DirectoryFailure(DirectoryCheck.MEDIA_TYPE, f"Content-Type is {media_type!r}")


def _check_jwks(response: httpx.Response, expected_alg: str | None) -> dict[str, Any]:
    try:
        jwks = json.loads(response.content)
    except ValueError as exc:
        raise _DirectoryFailure(DirectoryCheck.JWKS, "body is not JSON") from exc
    keys = jwks.get("keys") if isinstance(jwks, dict) else None
    if not isinstance(keys, list) or not keys:
        raise _DirectoryFailure(DirectoryCheck.JWKS, 'body has no non-empty "keys" array')
    for index, jwk in enumerate(keys):
        _check_jwk(index, jwk, expected_alg)
    return jwks


def _check_jwk(index: int, jwk: object, expected_alg: str | None) -> None:
    if not isinstance(jwk, dict):
        raise _DirectoryFailure(DirectoryCheck.JWKS, f"keys[{index}] is not an object")
    required = _REQUIRED_MEMBERS.get(str(jwk.get("kty")))
    if required is None:
        raise _DirectoryFailure(DirectoryCheck.JWKS, f"keys[{index}] has kty {jwk.get('kty')!r}")
    missing = [m for m in required if not isinstance(jwk.get(m), str)]
    if missing:
        raise _DirectoryFailure(DirectoryCheck.JWKS, f"keys[{index}] lacks {missing}")
    leaked = sorted(_PRIVATE_MEMBERS & set(jwk))
    if leaked:
        raise _DirectoryFailure(DirectoryCheck.JWKS, f"keys[{index}] carries private {leaked}")
    if jwk["kty"] == "OKP" and (jwk["crv"] != "Ed25519" or len(b64_any(jwk["x"]) or b"") != 32):
        raise _DirectoryFailure(DirectoryCheck.JWKS, f"keys[{index}] is not an Ed25519 key")
    _check_alg(index, jwk.get("alg"), expected_alg)
    kid = jwk.get("kid")
    if kid is not None:
        thumbprint = key_thumbprint({m: jwk[m] for m in ("kty", *required)})
        if kid != thumbprint:
            detail = f"keys[{index}] kid {kid!r} is not its thumbprint {thumbprint!r}"
            raise _DirectoryFailure(DirectoryCheck.KID, detail)


def _check_alg(index: int, alg: object, expected_alg: str | None) -> None:
    if expected_alg is not None:
        if alg != expected_alg:
            detail = f"keys[{index}] alg is {alg!r}, not the expected {expected_alg!r}"
            raise _DirectoryFailure(DirectoryCheck.JWKS, detail)
        return
    if alg is not None and alg not in SIGNATURE_ALGORITHMS:
        detail = f"keys[{index}] alg {alg!r} is not a registered HTTP signature algorithm"
        raise _DirectoryFailure(DirectoryCheck.JWKS, detail)


def _check_signed_response(
    response: httpx.Response,
    jwks: Mapping[str, Any],
    *,
    require_signed: bool,
    now: int,
) -> tuple[str, ...]:
    inputs = http_sfv.Dictionary()
    if "Signature-Input" not in response.headers:
        if require_signed:
            raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, "no Signature-Input header")
        return ()
    try:
        inputs.parse(response.headers["Signature-Input"].encode())
    except ValueError as exc:
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, "bad Signature-Input") from exc
    labels = [name for name, sig in inputs.items() if sig.params.get("tag") == DIRECTORY_TAG]
    if not labels:
        if require_signed:
            raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, "no directory signature")
        return ()
    _check_content_digest(response)
    keys = ed25519_keys(jwks)
    return tuple(
        _check_one_signature(response, label, inputs[label], keys, now) for label in labels
    )


def _check_content_digest(response: httpx.Response) -> None:
    digests = http_sfv.Dictionary()
    try:
        digests.parse(response.headers.get("Content-Digest", "").encode())
    except ValueError as exc:
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, "no Content-Digest") from exc
    known = [name for name in digests if name in _DIGESTS]
    if not known:
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, "no sha-256/sha-512 digest")
    for name in known:
        if digests[name].value != _DIGESTS[name](response.content).digest():
            raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, f"{name} digest mismatch")


def _check_one_signature(
    response: httpx.Response,
    label: str,
    signature_input: Any,
    keys: Mapping[str, Any],
    now: int,
) -> str:
    covered = {str(c) for c in signature_input}
    for needed in ('"@authority";req', '"content-digest"'):
        if needed not in covered:
            raise _DirectoryFailure(
                DirectoryCheck.SIGNED_RESPONSE, f"{label}: {needed} not covered"
            )
    params = signature_input.params
    created, expires = params.get("created"), params.get("expires")
    if not isinstance(created, int) or not isinstance(expires, int):
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, f"{label}: created/expires missing")
    if created > now:
        raise _DirectoryFailure(
            DirectoryCheck.SIGNED_RESPONSE, f"{label}: created is in the future"
        )
    if expires <= now:
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, f"{label}: expires has passed")
    try:
        keyid = check_keyid_shape(params.get("keyid"))
    except WBAFailure as exc:
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, f"{label}: {exc.detail}") from exc
    if keyid not in keys:
        detail = f"{label}: keyid {keyid!r} names no Ed25519 key in this directory"
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, detail)
    try:
        verify_signature(response, label=label, tag=DIRECTORY_TAG, key=keys[keyid])
    except (HTTPMessageSignaturesException, KeyError, ValueError) as exc:
        detail = f"{label}: {type(exc).__name__}: {exc}"
        raise _DirectoryFailure(DirectoryCheck.SIGNED_RESPONSE, detail) from exc
    return keyid
