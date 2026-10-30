"""A purchase whose acceptances name an empty requester, signed as a signer without the check would.

The protocol requires every acceptance to name its requester: ``requester_id`` and
``requester_domain`` are never empty in an ``AgentAcceptancePayload`` or an
``AgentRequestAcceptancePayload``, and both ``Requester`` fields are REQUIRED on
the wire. The SDK enforces that on the sending side, so its signers refuse to sign
such bytes and its purchase verbs refuse such a requester before anything is
sent. A test of the RECEIVING side therefore cannot get that request out of the
SDK's own signer.

:func:`empty_requester` is the pre-signing alteration that produces it. It takes
the purchase the SDK built, empties one requester field, and re-signs every
acceptance over the canonical bytes that now name the empty requester, with the
agent's own key. The SDK then signs the altered request, so the request signature
and every acceptance signature verify. The empty requester is the one thing wrong
with what is sent, which is what makes a refusal attributable to it.

Every acceptance here is signed by the SDK. The protocol's own signers sign one
that names its requester. They refuse an empty requester before they render
anything, so an acceptance that names one is signed by the SDK's test-only twins
in ``fora_sdk.testing``, which skip that refusal and nothing else. The pinned
module pins the twins to the ``refused`` rows of ``acceptance-vectors.json`` and
``request-acceptance-vectors.json`` byte for byte, so this module holds no
canonical form of its own to drift.

:func:`requester_named` is the same alteration with a non-empty value, for a
direct purchase that names a requester domain other than the host of the
directory the request is signed from: the acceptances name that domain too, so
the mismatch is the one thing wrong with the request.

:func:`acceptance_signed_by` re-signs one item's acceptance, over the same canonical
bytes, with another key: for a relayed purchase whose one item carries an
acceptance no key the requester's directory publishes verifies, while every other
signature on the purchase does.

Infrastructure, outside the scope of the harness bans: a test module never signs
an acceptance itself (the bans refuse ``fora_sdk.testing`` and the SDK's acceptance
signers there), and reaches this only through ``malformed()``'s ``before_sign``.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any, Literal

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk.core import sign_offer_acceptance_jcs, sign_request_acceptance_jcs
from fora_sdk.testing import (
    sign_offer_acceptance_any_requester,
    sign_request_acceptance_any_requester,
)

from .malformed import patch_body
from .sdk_client import BeforeSign

#: The requester field a case empties, by its name on ``Requester``.
RequesterField = Literal["id", "domain"]


def _offer_acceptance(
    key: Ed25519PrivateKey,
    *,
    offer_sig: str,
    requester_id: str,
    requester_domain: str,
    idempotency_key: str,
) -> dict[str, str]:
    """An item's ``AgentAcceptance`` over its canonical payload, signed by the SDK with ``key``."""
    named = bool(requester_id and requester_domain)
    sign = sign_offer_acceptance_jcs if named else sign_offer_acceptance_any_requester
    signature, algorithm = sign(
        seed=key.private_bytes_raw(),
        offer_sig=offer_sig,
        requester_id=requester_id,
        requester_domain=requester_domain,
        idempotency_key=idempotency_key,
    )
    return {"signature": signature, "signature_algorithm": algorithm}


def _request_acceptance_signature(
    key: Ed25519PrivateKey,
    *,
    items: list[dict[str, str]],
    requester_id: str,
    requester_domain: str,
    idempotency_key: str,
) -> str:
    """The request acceptance's signature over its canonical payload, made by the SDK with ``key``."""
    named = bool(requester_id and requester_domain)
    sign = sign_request_acceptance_jcs if named else sign_request_acceptance_any_requester
    signature, _algorithm = sign(
        seed=key.private_bytes_raw(),
        items=[(item["offer_sig"], item["exchange"]) for item in items],
        requester_id=requester_id,
        requester_domain=requester_domain,
        idempotency_key=idempotency_key,
    )
    return signature


def requester_named(field: RequesterField, value: str, key: Ed25519PrivateKey) -> BeforeSign:
    """Set ``requester.<field>`` to ``value`` in a purchase and re-sign its acceptances.

    ``key`` is the agent's own key: the one the request is signed with and the one
    its directory publishes, so every re-signed acceptance verifies against the
    key the receiver resolves for the agent. The request acceptance's payload
    names the same value, so it still matches the request's requester.
    """
    return patch_body(renamed(field, value, key))


def renamed(
    field: RequesterField,
    value: str,
    key: Ed25519PrivateKey,
) -> Callable[[dict[str, Any]], None]:
    """The edit :func:`requester_named` makes to a purchase's JSON body, applied in place.

    Exposed for the test that checks the acceptances it re-signs verify over the
    new name (``harness/test_acceptance.py``).
    """

    def patch(body: dict[str, Any]) -> None:
        requester = body["requester"]
        requester[field] = value
        requester_id = requester.get("id", "")
        requester_domain = requester.get("domain", "")
        idempotency_key = body["idempotency_key"]
        for item in body["items"]:
            item["agent_acceptance"] = _offer_acceptance(
                key,
                offer_sig=item["offer"]["signature"],
                requester_id=requester_id,
                requester_domain=requester_domain,
                idempotency_key=idempotency_key,
            )
        envelope = body.get("agent_request_acceptance")
        if envelope is None:
            return
        payload = envelope["payload"]
        if value:
            payload[f"requester_{field}"] = value
        else:
            # Absent rather than "", as proto-JSON renders an empty string field.
            payload.pop(f"requester_{field}", None)
        envelope["signature"] = _request_acceptance_signature(
            key,
            items=payload["items"],
            requester_id=requester_id,
            requester_domain=requester_domain,
            idempotency_key=idempotency_key,
        )

    return patch


def empty_requester(field: RequesterField, key: Ed25519PrivateKey) -> BeforeSign:
    """Empty ``requester.<field>`` in a purchase and re-sign its acceptances under ``key``."""
    return requester_named(field, "", key)


def acceptance_signed_by(index: int, key: Ed25519PrivateKey) -> BeforeSign:
    """Re-sign item ``index``'s AgentAcceptance under ``key``, over the same canonical bytes.

    Nothing else in the purchase changes: the payload is the one the agent signed (the
    item's offer signature, the request's requester and its idempotency key), the
    request acceptance is untouched, and the SDK signs the request afterwards. So the
    acceptance is a well-formed signature over the right bytes, and the one thing
    wrong with it is the key: when ``key`` is another party's, no key the directory
    ``requester.domain`` names publishes verifies it.
    """

    def patch(body: dict[str, Any]) -> None:
        requester = body["requester"]
        item = body["items"][index]
        item["agent_acceptance"] = _offer_acceptance(
            key,
            offer_sig=item["offer"]["signature"],
            requester_id=requester.get("id", ""),
            requester_domain=requester.get("domain", ""),
            idempotency_key=body["idempotency_key"],
        )

    return patch_body(patch)
