"""Cross-language byte-exactness gate for the harness offer-acceptance signer.

The e2e harness's JCS canonicalization of ``AgentAcceptancePayload``
(now ``fora_sdk.core``, ``JCS(protojson(payload))`` via the vetted
``rfc8785`` library) MUST reproduce the bytes the REAL Go SDK produces
(``helpers.SignOfferAcceptance``), and the hex Ed25519 signature MUST
equal the Go signature for the same fixed seed. The shared oracle is
``testdata/acceptance-vectors.json``, generated from the Go SDK at the pinned
protocol revision. The vectors MUST be regenerated from the Go oracle at every
protocol re-pin: a stale vector file pins a stale byte layout and lets signer
drift ride silently (that is exactly how the JCS switch was missed).

A divergence here means the relay-driven happy path would present an acceptance
the Exchange rejects, which makes this the highest-risk surface in the signing
chain.

Both vector files also carry a ``refused`` list: acceptances that name an empty
requester, with the bytes and signature a signer without that check would
produce. The SDK must refuse each one, signing and verifying alike. The harness's
own unchecked rendering (``harness/requester_alteration.py``), which builds the
empty-requester purchases the requester obligations send, must reproduce each
one exactly, so those purchases carry acceptances that verify over the bytes
they name. It is unit-tested because it is a cryptographic primitive and a parser,
two of the categories the testing doctrine admits unit tests for.
"""

from __future__ import annotations

import base64
import json
from pathlib import Path
from typing import Any

import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey

from fora_sdk.core import (
    ACCEPTANCE_SIGNATURE_ALGORITHM,
)
from fora_sdk.core import (
    jcs_acceptance_payload as canonical_acceptance_payload,
)
from fora_sdk.core import (
    jcs_request_acceptance_payload,
    verify_offer_acceptance_jcs,
    verify_request_acceptance_jcs,
)
from fora_sdk.identity import generate_key
from fora_sdk.signing_transport import SigningTransport
from .conftest import REPO_ROOT
from .requester_alteration import (
    acceptance_bytes,
    key_from_seed,
    request_acceptance_bytes,
    sign_hex,
)

# Pure crypto-primitive test — no compose stack, no DB. Declare `isolated` so the
# autouse `_stack_isolation_dispatch` per-test cleanup chain (which needs a live
# Postgres DSN) is a no-op for this module.
pytestmark = pytest.mark.stack_isolation("isolated")

# The shared cross-language oracle. Resolved via the harness's REPO_ROOT idiom
# (conftest.REPO_ROOT == the compose-file's dir) so the path holds in BOTH the
# host checkout (repo-root/testdata) AND the runner container, where REPO_ROOT is
# the bind-mounted /runner and testdata/ is COPYed in (tests/e2e/Dockerfile).
# parents[3] would IndexError inside the container's /runner/harness layout.
_VECTORS_PATH = REPO_ROOT / "testdata" / "acceptance-vectors.json"
_REQUEST_VECTORS_PATH = REPO_ROOT / "testdata" / "request-acceptance-vectors.json"


def _doc(path: Path) -> dict[str, Any]:
    doc: dict[str, Any] = json.loads(path.read_text())
    # The vector file self-describes its canonicalization; refuse to run the
    # JCS assertions against a stale (pre-JCS) vector file.
    assert doc.get("canonicalization") == "jcs"
    return doc


def _vectors() -> list[dict[str, str]]:
    vectors: list[dict[str, str]] = _doc(_VECTORS_PATH)["vectors"]
    assert vectors
    return vectors


def _refused(path: Path) -> list[dict[str, Any]]:
    """The file's empty-requester cases; both requester fields must be covered."""
    refused: list[dict[str, Any]] = _doc(path)["refused"]
    assert {vec["empty"] for vec in refused} == {"requester_id", "requester_domain"}, refused
    return refused


def _signer(seed: bytes) -> SigningTransport:
    """The SDK's request signer over ``seed``: the object a client signs its acceptance with.

    The acceptance is signed through the signer rather than the module-level function
    it delegates to, so this pins the path every purchase takes. The keyid is never
    part of an acceptance, so any label serves.
    """
    return SigningTransport(signer_seed=seed, keyid="acceptance-vector")


def test_canonical_bytes_match_go_oracle() -> None:
    """The rendered payload reproduces the Go ``canonicalSignPayload`` bytes exactly.

    Pins the JCS(protojson) layout: lexicographically sorted keys and minimal
    separators.
    """
    for vec in _vectors():
        got = canonical_acceptance_payload(
            offer_sig=vec["offer_sig"],
            requester_id=vec["requester_id"],
            requester_domain=vec["requester_domain"],
            idempotency_key=vec["idempotency_key"],
        )
        assert got == vec["canonical_jcs"].encode("utf-8"), vec["name"]


def test_signature_matches_go_oracle_for_fixed_seed() -> None:
    """Signing the canonical bytes with the vector seed reproduces the Go signature.

    Ed25519 is deterministic, so a fixed seed yields one signature; the hex MUST
    equal the value the Go ``ed25519.Sign`` produced, proving the whole chain
    (canonicalization + sign + hex) is byte-identical across languages.
    """
    for vec in _vectors():
        seed = bytes.fromhex(vec["seed_hex"])
        signature_hex, algorithm = _signer(seed).sign_offer_acceptance(
            offer_sig=vec["offer_sig"],
            requester_id=vec["requester_id"],
            requester_domain=vec["requester_domain"],
            idempotency_key=vec["idempotency_key"],
        )
        assert signature_hex == vec["signature_hex"], vec["name"]
        assert algorithm == ACCEPTANCE_SIGNATURE_ALGORITHM


def test_signature_verifies_under_vector_pubkey() -> None:
    """The harness signature verifies under the vector's published public key.

    Independent of the byte-equality leg: even if both sides shared a
    canonicalization regression, a signature that verifies under the
    Go-published pubkey over the harness-built canonical bytes proves the agent
    and Exchange agree on the signed bytes.
    """
    for vec in _vectors():
        payload = canonical_acceptance_payload(
            offer_sig=vec["offer_sig"],
            requester_id=vec["requester_id"],
            requester_domain=vec["requester_domain"],
            idempotency_key=vec["idempotency_key"],
        )
        pub = Ed25519PublicKey.from_public_bytes(base64.b64decode(vec["pubkey_b64"]))
        pub.verify(bytes.fromhex(vec["signature_hex"]), payload)


def test_reject_empty_offer_sig() -> None:
    """An empty offer signature is fail-closed (mirror Go ``acceptance.go``).

    An empty anchor would let the acceptance float free of any concrete offer,
    so signing over it must raise rather than produce a floating signature.
    """
    with pytest.raises(ValueError, match="offer"):
        canonical_acceptance_payload(
            offer_sig="",
            requester_id="agent-1",
            requester_domain="agent.example.com",
            idempotency_key="idem-1",
        )
    priv, _ = generate_key()
    with pytest.raises(ValueError, match="offer"):
        _signer(priv.private_bytes_raw()).sign_offer_acceptance(
            offer_sig="",
            requester_id="agent-1",
            requester_domain="agent.example.com",
            idempotency_key="idem-1",
        )


def test_sdk_refuses_an_acceptance_naming_an_empty_requester() -> None:
    """Each refused offer acceptance: no bytes, no signature, and no verification.

    The vector's signature does verify over its bytes; the verifier must refuse
    it anyway, because the bytes name nobody.
    """
    for vec in _refused(_VECTORS_PATH):
        fields = {
            "offer_sig": vec["offer_sig"],
            "requester_id": vec["requester_id"],
            "requester_domain": vec["requester_domain"],
            "idempotency_key": vec["idempotency_key"],
        }
        with pytest.raises(ValueError, match="empty requester"):
            canonical_acceptance_payload(**fields)
        with pytest.raises(ValueError, match="empty requester"):
            _signer(bytes.fromhex(vec["seed_hex"])).sign_offer_acceptance(**fields)
        with pytest.raises(ValueError, match="empty requester"):
            verify_offer_acceptance_jcs(
                pubkey_b64=vec["pubkey_b64"], signature_hex=vec["signature_hex"], **fields
            )


def test_sdk_refuses_a_request_acceptance_naming_an_empty_requester() -> None:
    """Each refused request acceptance: no bytes, and the verifier answers False."""
    for vec in _refused(_REQUEST_VECTORS_PATH):
        items = [(i["offer_sig"], i["exchange"]) for i in vec["items"]]
        fields = {
            "requester_id": vec["requester_id"],
            "requester_domain": vec["requester_domain"],
            "idempotency_key": vec["idempotency_key"],
        }
        with pytest.raises(ValueError, match="empty requester"):
            jcs_request_acceptance_payload(items=items, **fields)
        assert not verify_request_acceptance_jcs(
            pubkey_b64=vec["pubkey_b64"],
            signature_hex=vec["signature_hex"],
            items=items,
            **fields,
        ), vec["name"]


def test_unchecked_rendering_reproduces_the_refused_offer_acceptances() -> None:
    """The harness's empty-requester acceptance is byte-identical to the protocol's record."""
    for vec in _refused(_VECTORS_PATH):
        payload = acceptance_bytes(
            offer_sig=vec["offer_sig"],
            requester_id=vec["requester_id"],
            requester_domain=vec["requester_domain"],
            idempotency_key=vec["idempotency_key"],
        )
        assert payload == vec["canonical_jcs"].encode(), vec["name"]
        signature = sign_hex(key_from_seed(bytes.fromhex(vec["seed_hex"])), payload)
        assert signature == vec["signature_hex"], vec["name"]


def test_unchecked_rendering_reproduces_the_refused_request_acceptances() -> None:
    """The harness's empty-requester request acceptance is byte-identical to the record."""
    for vec in _refused(_REQUEST_VECTORS_PATH):
        payload = request_acceptance_bytes(
            items=vec["items"],
            requester_id=vec["requester_id"],
            requester_domain=vec["requester_domain"],
            idempotency_key=vec["idempotency_key"],
        )
        assert payload == vec["canonical_jcs"].encode(), vec["name"]
        signature = sign_hex(key_from_seed(bytes.fromhex(vec["seed_hex"])), payload)
        assert signature == vec["signature_hex"], vec["name"]
