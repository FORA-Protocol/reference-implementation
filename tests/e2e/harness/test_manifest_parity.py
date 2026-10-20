"""Drift guard: the AWS CloudFront shim and the Hono edge worker build the
publisher discovery documents from the SAME shared builders
(``src/edge/src/well-known.mjs``), so both endpoints MUST serve an identical
``/.well-known/fora.json`` (modulo ``domain``) and a structurally-identical WBA
directory.

The raw-Node ``aws-edge`` shim (``tests/e2e/aws-edge/server.mjs``) imports a
build-time copy of that module (``tests/e2e/aws-edge/Dockerfile`` ``COPY``s it
in); the Hono edge worker (``src/edge``) imports the same module, re-exported
from ``src/edge/src/types.ts``. Both are configured from the same
``EXCHANGES_JSON`` / ``CATALOG_CONTRIBUTORS_JSON`` in ``docker-compose.e2e.yml``
and differ only in ``PROVIDER`` (the ``domain`` field) and their per-domain
self-publish key. After the WBA split the overlay manifest is KEYLESS — identity
keys moved to the WBA directory (``/.well-known/http-message-signatures-directory``)
and carry NO kid (named by RFC 7638 thumbprint).

Both documents are read through the SDK's document readers, which refuse a
document the SDK's strict check does not accept, and a directory not served as
``application/http-message-signatures-directory+json``. That is what makes "keyless" and "no kid" hold here
without a test reading raw members: a manifest still carrying ``public_keys`` or a
directory key carrying a ``kid`` fails the read.
"""

from __future__ import annotations

from wire.models import Role

from .conftest import StackURLs
from .e2e_catalog import DEMO_PHILOSOPHY_DOMAIN, DEMO_SFX_DOMAIN
from .protocol_documents import manifest_at, wba_directory_at


def test_aws_shim_manifest_matches_canonical_publisher(compose_stack: StackURLs) -> None:
    """aws-edge shim manifest == edge-worker (canonical) manifest, modulo domain."""
    canonical = manifest_at(compose_stack.edge).message
    shim = manifest_at(compose_stack.aws_edge).message

    # Guard against a trivially-matching empty / zero-value document on either side.
    assert canonical.role is Role.ROLE_PUBLISHER, canonical
    assert canonical.exchanges, "canonical edge manifest unexpectedly has no exchanges"

    # Multi-exchange topology: the philosophy edge (`edge`, exchange-a) and
    # the sfx aws-edge (exchange-c) are deliberately fronted by DIFFERENT exchanges,
    # so their `exchanges[]` legitimately differ (exchange:8081 vs exchange-c:8081).
    # That is publisher config, not a buildPublisherManifest drift signal — assert
    # each side carries exactly one well-formed exchange entry (so a missing/empty
    # exchanges still fails), then normalise it away before the structural compare.
    for label, doc in (("edge", canonical), ("aws-edge", shim)):
        exchanges = doc.exchanges or []
        assert len(exchanges) == 1, f"{label} must list exactly one exchange; got {exchanges!r}"
        assert exchanges[0].domain and exchanges[0].endpoint, exchanges[0]

    # Same contributors config + shape on both producers; domain + exchanges are
    # per-publisher by design (multi-exchange topology), so they are normalised
    # away before the structural comparison.
    per_publisher = {"domain": "", "exchanges": None}
    assert shim.model_copy(update=per_publisher) == canonical.model_copy(update=per_publisher), (
        "aws-edge shim manifest drifted from buildPublisherManifest output; "
        "rebuild the aws-edge image so tests/e2e/aws-edge/well-known.mjs "
        "matches src/edge/src/well-known.mjs"
    )


def test_edge_and_shim_serve_wba_directory_self_key(compose_stack: StackURLs) -> None:
    """Each edge serves its OWN self-publish key in a WBA directory (no kid).

    The key is LEGITIMATELY per-domain: each edge serves its own
    self-publish signing key, named by thumbprint. Assert both producers serve
    exactly one well-formed Ed25519 JWK. The SDK's reader refuses a kid, a
    directory not served under the Web Bot Auth media type, and one whose response
    the listed key did not sign for the host it was fetched from, so none of those
    is asserted here. Each is read at its publisher's https origin, the host its
    response is signed for, as the Exchange reads it.
    """
    del compose_stack  # ordering only: the stack is up
    for label, host in (("edge", DEMO_PHILOSOPHY_DOMAIN), ("aws-edge", DEMO_SFX_DOMAIN)):
        keys = wba_directory_at(f"https://{host}").message.keys or []
        assert len(keys) == 1, f"{label} must serve exactly one self-publish key; got {keys!r}"
        key = keys[0]
        assert key.kty == "OKP" and key.crv == "Ed25519", key
        assert key.x is not None and len(key.x) == 43, key
