"""Web Bot Auth conformance oracle over a third-party RFC 9421 implementation.

The WBA conformance tests check FORA against code FORA did not write. This module
is the Python half of that: it signs and verifies requests the way
draft-ietf-webbotauth-httpsig-protocol-00 ("WG-00") describes, using the pyauth
``http-message-signatures`` package for every byte of the RFC 9421 signature base
and every signature operation. Nothing here imports the FORA protocol SDK or any
FORA service code, and nothing here may: an oracle that shares code with the
implementation under test agrees with it by construction.

The TypeScript half is the Cloudflare ``web-bot-auth`` sidecar, driven through
``wba_oracle_ts.py``. The two are written by different people in different
languages, and ``test_wba_oracle_ts.py`` checks that each verifies what the
other signs.

Three entry points:

- :func:`sign` produces the WG-00 request headers.
- :func:`verify` applies the WG-00 verifier checks (``wba_checks.py``) and then
  pyauth's signature check, and names the first check that fails.
- :func:`check_directory` (``wba_directory.py``) applies the WG-00 key directory
  rules to a live directory URL.

pyauth needed one extension to verify the dictionary form of Signature-Agent at
all; ``wba_pyauth.py`` describes it and the Appendix E vectors that prove it.
"""

from __future__ import annotations

import base64
import secrets
import time
from collections.abc import Mapping, Sequence
from typing import Any

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from http_message_signatures import HTTPMessageSigner, algorithms, http_sfv
from http_message_signatures.exceptions import HTTPMessageSignaturesException

from .wba_checks import (
    Check,
    Require,
    VerifyResult,
    WBAFailure,
    check_coverage,
    check_keyid_shape,
    check_signature_params,
    parse_signature_fields,
)

# The suites import the oracle's whole surface from here, the directory check and
# the key helpers included. The `X as X` form marks the names this module does not
# use itself as deliberate re-exports.
from .wba_directory import (
    check_directory as check_directory,
    check_directory_response as check_directory_response,
)
from .wba_pyauth import (
    FixedKeys,
    WBAComponentResolver,
    ed25519_keys,
    fresh_key as fresh_key,
    key_thumbprint,
    private_jwk as private_jwk,
    private_key_from_jwk as private_key_from_jwk,
    public_jwk as public_jwk,
    utc,
    verify_signature,
)

WBA_TAG = "web-bot-auth"
NONCE_BYTES = 64
DEFAULT_TTL_SECONDS = 300
_WBA_HEADERS = frozenset({"signature", "signature-input", "signature-agent"})


#: The request the oracle signs and verifies. pyauth signs an ``httpx.Request`` in
#: place, so that is the oracle's message type; a test builds one with
#: :func:`message` rather than importing the HTTP client itself.
Message = httpx.Request


def message(
    method: str,
    url: str,
    *,
    headers: Mapping[str, str] | Sequence[tuple[str, str]] | None = None,
    content: bytes = b"",
) -> Message:
    """An unsent request for the oracle to sign: ``method`` to ``url`` with ``headers``."""
    return httpx.Request(method, url, headers=headers, content=content)


def fresh_nonce() -> str:
    """64 random bytes in standard base64 with padding, the form Appendix E uses."""
    return base64.b64encode(secrets.token_bytes(NONCE_BYTES)).decode()


def signature_agent_header(
    origin: str, member_key: str, *, legacy: bool = False, unquoted: bool = False
) -> str:
    """The Signature-Agent value naming ``origin``: ``key="origin"``, or the legacy string.

    ``unquoted`` writes the bare origin with no quoting at all, the form the FORA
    protocol SDK v1.0.8 sends. It is neither the WG-00 dictionary nor the legacy
    sf-string, and exists so a test can show a verifier refusing it.
    """
    if unquoted:
        return origin
    value = http_sfv.Item(origin)
    if legacy:
        return str(value)
    members = http_sfv.Dictionary()
    members[member_key] = value
    return str(members)


def sign(
    request: httpx.Request,
    key: Ed25519PrivateKey,
    directory_origin: str,
    *,
    extra_components: Sequence[str] = (),
    label: str = "sig1",
    agent_key: str | None = None,
    nonce: str | None = None,
    include_nonce: bool = True,
    created: int | None = None,
    expires: int | None = None,
    target: str = "@authority",
    legacy: bool = False,
    unquoted: bool = False,
    tag: str | None = WBA_TAG,
) -> dict[str, str]:
    """Sign ``request`` as a WG-00 agent and return the three WBA headers.

    The covered components are ``target`` (bare ``@authority`` by default), the
    Signature-Agent member keyed ``agent_key`` (the label unless given), then
    ``extra_components`` in order, written as in Signature-Input: ``"@method"``,
    ``"content-digest"``, ``'"@query-param";name="q"'``. A covered header must
    already be on ``request``.

    The parameters come out as ``created;keyid;alg;expires;nonce;tag``, the order
    Appendix E uses, so a fixed ``created``/``expires``/``nonce`` reproduces the
    vectors byte for byte (Ed25519 signatures are deterministic). keyid is the
    RFC 7638 thumbprint of ``key``. ``nonce`` defaults to 64 fresh random bytes;
    ``include_nonce=False`` omits it. ``expires`` defaults to five minutes after
    ``created``. ``legacy=True`` emits the sf-string Signature-Agent, which WG-00
    Section 5.2.1 lets verifiers accept and forbids signers to send.
    ``unquoted=True`` emits the bare, unquoted origin instead (see
    :func:`signature_agent_header`); like the legacy form it is covered as the
    bare ``"signature-agent"``. ``tag=None`` leaves the WG-00 tag out, which a
    conformant signer never does; it exists for refusal tests.

    ``request`` is not modified. Its own Signature* headers are replaced.
    """
    member_key = label if agent_key is None else agent_key
    agent_header = signature_agent_header(
        directory_origin, member_key, legacy=legacy, unquoted=unquoted
    )
    whole_field = legacy or unquoted
    agent_component = (
        '"signature-agent"' if whole_field else f'"signature-agent";key="{member_key}"'
    )
    message = httpx.Request(
        request.method,
        request.url,
        headers=[
            *[(k, v) for k, v in request.headers.multi_items() if k.lower() not in _WBA_HEADERS],
            ("Signature-Agent", agent_header),
        ],
        content=request.content,
    )
    keyid = key_thumbprint(key)
    created_at = int(time.time()) if created is None else created
    expires_at = created_at + DEFAULT_TTL_SECONDS if expires is None else expires
    signer = HTTPMessageSigner(
        signature_algorithm=algorithms.ED25519,
        key_resolver=FixedKeys(private={keyid: key}),
        component_resolver_class=WBAComponentResolver,
    )
    signer.sign(
        message,
        key_id=keyid,
        created=utc(created_at),
        expires=utc(expires_at),
        nonce=(nonce or fresh_nonce()) if include_nonce else None,
        label=label,
        tag=tag,
        covered_component_ids=[target, agent_component, *extra_components],
    )
    return {
        "Signature-Agent": agent_header,
        "Signature-Input": message.headers["Signature-Input"],
        "Signature": message.headers["Signature"],
    }


def verify(
    request_headers: Mapping[str, str] | httpx.Headers,
    method: str,
    url: str,
    body: bytes,
    jwks_or_key: Mapping[str, Any] | Ed25519PublicKey,
    *,
    require: Require | None = None,
    label: str | None = None,
    now: int | None = None,
) -> VerifyResult:
    """Apply the WG-00 verifier checks to one request and name the first failure.

    ``jwks_or_key`` is a JWKS (``{"keys": [...]}``), a single JWK, or an Ed25519
    public key. The key is selected by matching ``keyid`` against each key's RFC
    7638 thumbprint; a ``kid`` in the JWKS plays no part. ``label`` picks one
    signature; without it the request must carry exactly one signature tagged
    ``web-bot-auth``. ``now`` (epoch seconds) defaults to the wall clock.

    The checks run in the order :class:`Check` lists them, and the first failure
    ends the run, so ``failed`` names the rule a WG-00 verifier reports first.
    """
    policy = require or Require()
    at = int(time.time()) if now is None else now
    headers = httpx.Headers(request_headers)
    try:
        selected = parse_signature_fields(headers, label=label, tag=WBA_TAG)
        check_signature_params(selected.params, policy, now=at)
        agent = check_coverage(headers, selected, policy)
        keyid = check_keyid_shape(selected.params.get("keyid"))
        keys = ed25519_keys(jwks_or_key)
        if keyid not in keys:
            raise WBAFailure(Check.KEYID, f"no verification key has thumbprint {keyid!r}")
        message = httpx.Request(method, url, headers=headers, content=body)
        try:
            verify_signature(message, label=selected.label, tag=WBA_TAG, key=keys[keyid])
        except (HTTPMessageSignaturesException, KeyError, ValueError) as exc:
            raise WBAFailure(Check.SIGNATURE, f"{type(exc).__name__}: {exc}") from exc
    except WBAFailure as failure:
        return VerifyResult.failure(failure)
    return VerifyResult(
        ok=True,
        failed=None,
        detail="",
        label=selected.label,
        keyid=keyid,
        signature_agent=agent,
        covered=selected.covered,
        params=dict(selected.params),
    )
