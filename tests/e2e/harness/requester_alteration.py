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

The canonical form is written here rather than taken from the SDK because the
SDK's canonicalizers refuse an empty requester before they render anything. The
form is the protocol's one canonical signing form: RFC 8785 JCS over proto-JSON,
where proto-JSON omits an empty string field. The pinned module records the bytes
and signatures a signer without the check produces, in the ``refused`` lists of
``acceptance-vectors.json`` and ``request-acceptance-vectors.json``, and
``harness/test_acceptance.py`` replays both lists through this module, so the
form here cannot drift from the protocol's.

:func:`requester_named` is the same alteration with a non-empty value, for a
direct purchase that names a requester domain other than the host of the
directory the request is signed from: the acceptances name that domain too, so
the mismatch is the one thing wrong with the request.

:func:`acceptance_signed_by` re-signs one item's acceptance, over the same canonical
bytes, with another key: for a relayed purchase whose one item carries an
acceptance no key the requester's directory publishes verifies, while every other
signature on the purchase does.

Infrastructure, outside the scope of the harness bans: a test module never signs
with a raw key, and reaches this only through ``malformed()``'s ``before_sign``.
"""

from __future__ import annotations

from typing import Any, Literal

import rfc8785
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fora_sdk.core import ACCEPTANCE_SIGNATURE_ALGORITHM

from .malformed import patch_body
from .sdk_client import BeforeSign

#: The requester field a case empties, by its name on ``Requester``.
RequesterField = Literal["id", "domain"]


def _without_empty_strings(payload: dict[str, Any]) -> dict[str, Any]:
    """Drop every empty string member, as proto-JSON omits an unpopulated field."""
    return {k: v for k, v in payload.items() if not (isinstance(v, str) and v == "")}


def acceptance_bytes(
    *, offer_sig: str, requester_id: str, requester_domain: str, idempotency_key: str
) -> bytes:
    """The canonical ``AgentAcceptancePayload`` bytes, with no refusal of an empty requester."""
    return rfc8785.dumps(
        _without_empty_strings(
            {
                "offer_sig": offer_sig,
                "requester_id": requester_id,
                "requester_domain": requester_domain,
                "idempotency_key": idempotency_key,
            }
        )
    )


def request_acceptance_bytes(
    *,
    items: list[dict[str, str]],
    requester_id: str,
    requester_domain: str,
    idempotency_key: str,
) -> bytes:
    """The canonical ``AgentRequestAcceptancePayload`` bytes, with no refusal of an empty requester."""
    return rfc8785.dumps(
        _without_empty_strings(
            {
                "items": [{"offer_sig": i["offer_sig"], "exchange": i["exchange"]} for i in items],
                "requester_id": requester_id,
                "requester_domain": requester_domain,
                "idempotency_key": idempotency_key,
            }
        )
    )


def key_from_seed(seed: bytes) -> Ed25519PrivateKey:
    """The Ed25519 key over a raw 32-byte seed, for replaying a vector's fixed key."""
    return Ed25519PrivateKey.from_private_bytes(seed)


def sign_hex(key: Ed25519PrivateKey, payload: bytes) -> str:
    """The hex Ed25519 signature over ``payload``, the encoding every acceptance uses."""
    return key.sign(payload).hex()


def requester_named(field: RequesterField, value: str, key: Ed25519PrivateKey) -> BeforeSign:
    """Set ``requester.<field>`` to ``value`` in a purchase and re-sign its acceptances.

    ``key`` is the agent's own key: the one the request is signed with and the one
    its directory publishes, so every re-signed acceptance verifies against the
    key the receiver resolves for the agent. The request acceptance's payload
    names the same value, so it still matches the request's requester.
    """

    def patch(body: dict[str, Any]) -> None:
        requester = body["requester"]
        requester[field] = value
        requester_id = requester.get("id", "")
        requester_domain = requester.get("domain", "")
        idempotency_key = body["idempotency_key"]
        for item in body["items"]:
            item["agent_acceptance"] = {
                "signature": sign_hex(
                    key,
                    acceptance_bytes(
                        offer_sig=item["offer"]["signature"],
                        requester_id=requester_id,
                        requester_domain=requester_domain,
                        idempotency_key=idempotency_key,
                    ),
                ),
                "signature_algorithm": ACCEPTANCE_SIGNATURE_ALGORITHM,
            }
        envelope = body.get("agent_request_acceptance")
        if envelope is None:
            return
        payload = envelope["payload"]
        if value:
            payload[f"requester_{field}"] = value
        else:
            # Absent rather than "", as proto-JSON renders an empty string field.
            payload.pop(f"requester_{field}", None)
        envelope["signature"] = sign_hex(
            key,
            request_acceptance_bytes(
                items=payload["items"],
                requester_id=requester_id,
                requester_domain=requester_domain,
                idempotency_key=idempotency_key,
            ),
        )

    return patch_body(patch)


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
        item["agent_acceptance"] = {
            "signature": sign_hex(
                key,
                acceptance_bytes(
                    offer_sig=item["offer"]["signature"],
                    requester_id=requester.get("id", ""),
                    requester_domain=requester.get("domain", ""),
                    idempotency_key=body["idempotency_key"],
                ),
            ),
            "signature_algorithm": ACCEPTANCE_SIGNATURE_ALGORITHM,
        }

    return patch_body(patch)
