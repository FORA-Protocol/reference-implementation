"""An independent check of a transaction's signed evidence, as a publisher's dashboard shows it.

The console's transaction page carries the evidence of a sale: the offer the
Exchange signed and the acceptance the agent signed, each as the exact bytes, the
signature and the key it verifies under. The page's own Verify button checks the
two signatures in the browser, against the keys recorded with the transaction.
That proves the record is intact. It does not prove who holds the keys.

This oracle checks the same evidence from outside the console and the Exchange:

* **both signatures**, through the protocol SDK's own verification functions:
  the offer through ``fora_sdk.Verifier`` over ``canonical_offer_payload``, the
  acceptance through ``verify_offer_acceptance_jcs``. Before each check the
  recorded bytes are required to be exactly the canonical bytes the SDK derives
  from them, so the signature checked is the signature over what was stored and
  not over a re-encoding of it;
* **the link**: the acceptance names this offer's signature as the one it
  accepted;
* **who holds the keys**: the Exchange's key is one the Exchange publishes in its
  Web Bot Auth directory, and the agent's key is one the agent publishes in its
  own, each directory read through the SDK's document reader at the party's own
  origin.

Nothing here signs anything: the SDK's verify functions are the only Ed25519
calls, and the keys come from the evidence and the directories.

The offer's freshness is not this oracle's question. The evidence is read after
the offer has lapsed, so the SDK verifier's clock is set inside the offer's own
validity, the moment before it expired; the signature check does not depend on it.
"""

from __future__ import annotations

import base64
import json
from dataclasses import dataclass
from datetime import datetime
from typing import Any

from fora_sdk import (
    Verifier,
    canonical_offer_payload,
    jcs_acceptance_payload,
    verify_offer_acceptance_jcs,
)
from fora_sdk.b64 import b64url_decode
from fora_sdk.core import Mode, StaticOfferKeyResolver

from .console_dashboard import Evidence
from .protocol_documents import wba_directory_at

_ACCEPTANCE_FIELDS = ("offer_sig", "requester_id", "requester_domain", "idempotency_key")


@dataclass(frozen=True)
class EvidenceVerdict:
    """What the oracle found, one fact per field; every field True means the evidence holds."""

    offer_bytes_canonical: bool
    offer_signature: bool
    acceptance_bytes_canonical: bool
    acceptance_signature: bool
    linked: bool
    exchange_key_published: bool
    agent_key_published: bool

    @property
    def holds(self) -> bool:
        return all(vars(self).values())


def _published_keys(origin: str) -> set[bytes]:
    """The raw Ed25519 keys the Web Bot Auth directory at ``origin`` publishes."""
    directory = wba_directory_at(origin).message
    return {
        b64url_decode(key.x)
        for key in directory.keys or []
        if (key.kty or "").upper() == "OKP" and (key.crv or "").lower() == "ed25519" and key.x
    }


def _offer_verdict(evidence: Evidence, exchange: str) -> tuple[bool, bool]:
    """Whether the offer bytes are canonical, and whether its signature verifies."""
    offer: dict[str, Any] = json.loads(evidence.offer_canonical_bytes)
    signed = {**offer, "signature": evidence.offer_sig}
    canonical = canonical_offer_payload(signed) == evidence.offer_canonical_bytes
    expires_at = datetime.fromisoformat(str(offer.get("expires_at", "")))
    verifier = Verifier(
        mode=Mode.STRICT,
        resolver=StaticOfferKeyResolver({exchange: evidence.exchange_signing_public_key}),
        now=lambda: int(expires_at.timestamp()) - 1,
    )
    sorted_offers = verifier.sort([signed])
    return canonical, len(sorted_offers.verified) == 1 and not sorted_offers.rejected


def _acceptance_verdict(evidence: Evidence) -> tuple[bool, bool, bool]:
    """Whether the acceptance bytes are canonical, its signature verifies, and it names the offer."""
    payload: dict[str, Any] = json.loads(evidence.agent_acceptance_canonical_bytes)
    fields = {name: str(payload.get(name, "")) for name in _ACCEPTANCE_FIELDS}
    canonical = jcs_acceptance_payload(**fields) == evidence.agent_acceptance_canonical_bytes
    signature = verify_offer_acceptance_jcs(
        pubkey_b64=base64.b64encode(evidence.agent_public_key).decode(),
        signature_hex=evidence.agent_acceptance_signature,
        **fields,
    )
    return canonical, signature, fields["offer_sig"] == evidence.offer_sig


def verify_evidence(
    evidence: Evidence, *, exchange: str, exchange_origin: str, agent_origin: str
) -> EvidenceVerdict:
    """Check ``evidence`` of a sale by the Exchange ``exchange`` to the agent at ``agent_origin``.

    ``exchange`` is the Exchange's identity as offers name it; ``exchange_origin``
    and ``agent_origin`` are where each party's key directory is served.
    """
    offer_canonical, offer_signature = _offer_verdict(evidence, exchange)
    acceptance_canonical, acceptance_signature, linked = _acceptance_verdict(evidence)
    return EvidenceVerdict(
        offer_bytes_canonical=offer_canonical,
        offer_signature=offer_signature,
        acceptance_bytes_canonical=acceptance_canonical,
        acceptance_signature=acceptance_signature,
        linked=linked,
        exchange_key_published=evidence.exchange_signing_public_key
        in _published_keys(exchange_origin),
        agent_key_published=evidence.agent_public_key in _published_keys(agent_origin),
    )
