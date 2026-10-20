"""Full e2e: the REAL fora-ingest binary over the live stack (demo feeds).

The seed already ingests the three demo feeds through the production
``cmd/fora-ingest`` binary (signed ``CatalogService/PushResources`` RPCs, one
per submission of at most the wire bound; each demo feed fits in one) — there
is no SQL backdoor and no Python mapper. This module proves that path directly
and idempotently:

* re-running the binary against a demo feed exits 0 and reports every entry
  accepted (the production parse → map → sign → push pipeline is stable across
  runs);
* a requester discovers the ingested FREE socrates term, the resolved cost is
  term-derived (0), and the Exchange-signed URL delivers origin bytes through
  the Cloudflare edge;
* a requester that once mapped to a non-permitted geography (US on plato) is now
  LICENSED and receives a signed URL: per ADR-014's 2026-06-15 amendment the
  Exchange filters by resource_id + scopes ONLY and no longer excludes a term by
  the requester's self-declared user_type / geography — the restrictions ride on
  the returned offer for the agent to self-honour.

The binary ships in the runner image at ``/usr/local/bin/fora-ingest``
(Go builder stage of tests/e2e/Dockerfile); we invoke it via subprocess,
mirroring how the seed shells out to it.
"""

from __future__ import annotations

import subprocess
from decimal import Decimal
from pathlib import Path


from .broker_purchase import buy_through_broker
from .conftest import StackURLs
from .edge_fetch import fetch_signed
from .protocol_documents import manifest_at, wba_directory_at
from .seed import (
    CATALOG_CONTRIBUTOR_ID,
    CATALOG_DIR,
    CONTRIBUTOR_KEY_PATH,
    SEAT_OF_PHILOSOPHY,
    SELFPUB_PHILOSOPHY_KEY_PATH,
    SeededFixture,
)

# The philosophy feed (10 records) + the philosophy publisher's SELF-PUBLISH
# key (kid == demo.fora-protocol.org). The Exchange learns this key ONLY by
# fetching the philosophy edge's Web Bot Auth directory (Gate-1
# self-signup) — there is NO fora.agents pre-seed (Core Invariant).
#
# Taken from the seed's own CATALOG_DIR rather than rebuilt here: this test
# re-pushes the feed the seed already ingested and asserts the result is
# unchanged, so a second path would let the two drift and turn an idempotence
# test into a comparison of two different feeds.
_PHILOSOPHY_FEED = CATALOG_DIR / "philosophy.jsonl"
_PHILOSOPHY_DOMAIN = "demo.fora-protocol.org"
_INGEST_BIN = "/usr/local/bin/fora-ingest"


def _run_ingester(
    exchange_url: str, tenant_id: str, feed: Path, key_path: Path
) -> subprocess.CompletedProcess[str]:
    """Run the real fora-ingest binary against the live Exchange.

    Exercises the exact production parse → map → sign → PushResources pipeline.
    ``key_path`` is the signing identity; its kid becomes the push caller_id, so
    the Exchange must learn that key via the well-known fetch (no DB pre-seed).
    Returns the completed process so callers can assert exit code + the
    structured accepted/warnings report written to stderr.
    """
    return subprocess.run(
        [
            _INGEST_BIN,
            "--exchange-url",
            exchange_url,
            "--tenant",
            tenant_id,
            "--key",
            str(key_path),
            str(feed),
        ],
        capture_output=True,
        text=True,
        check=False,
        timeout=180,
    )


def test_ingester_pushes_via_rpc_and_exits_clean(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed registers tenant + contributor
) -> None:
    """Re-running the real ingester on the philosophy feed is idempotent and stores every entry.

    Re-pushing via the signed RPC under the philosophy publisher's OWN
    self-publish key (kid == demo.fora-protocol.org) must again exit 0 and
    report accepted=10 — the Exchange stores or refuses a submission whole and
    the binary exits non-zero on a refusal, so the exit code is the verdict —
    confirming the production push path (parse → map → sign → push → verdict)
    is stable and the self-publish key remains resolvable via the well-known
    fetch across runs (no DB pre-seed).
    """
    proc = _run_ingester(
        compose_stack.exchange,
        SEAT_OF_PHILOSOPHY,
        _PHILOSOPHY_FEED,
        SELFPUB_PHILOSOPHY_KEY_PATH,
    )
    assert proc.returncode == 0, (
        f"ingester failed: rc={proc.returncode}\nSTDERR:\n{proc.stderr}\nSTDOUT:\n{proc.stdout}"
    )
    assert "push: accepted=10 warnings=" in proc.stderr, proc.stderr


def test_requester_discovers_free_term_and_gets_content(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """A requester discovers the ingested FREE socrates term and is delivered bytes.

    The term was produced by the real ingester from the philosophy feed's FREE
    offer. The Exchange no longer filters by the requester's user_type / geography
    (ADR-014, 2026-06-15), so the requester discovers the term, the term-derived
    cost is 0 (FREE), and the Exchange-signed URL delivers the origin content
    through the Cloudflare edge.

    Broker Resolve is discovery-only (it returns ranked signed offers, no signed
    URL). The agent then buys the winning offer through the Broker, and the
    Exchange mints the signed URL and returns the term-derived cost on the result
    item.
    """
    res = seeded.free  # socrates: FREE EUR
    _, item = buy_through_broker(
        compose_stack, res.uri, agent_id=res.buyer_agent_id, key_path=res.buyer_key_path
    )
    signed_url = item.retrieval_endpoint
    assert signed_url, f"the purchase answered no delivery URL: {item}"
    # ADR-019: the FREE-term delivered price rides on the canonical typed
    # per-item cost. socrates' headline term is FREE, so the agent observes a
    # zero amount. Money is a decimal string on the wire.
    assert item.cost is not None, item
    assert Decimal(str(item.cost.amount)) == 0, item.cost

    # The URL is bound to the buying agent — fetch_signed attaches
    # RFC 9421 proof-of-possession headers when key_path is set and the URL carries
    # agent_id= (edge enforces agent_id == keyid == thumbprint of presented key).
    content = fetch_signed(signed_url, compose_stack, key_path=res.buyer_key_path)
    assert content.body.strip(), content.body[:256]


def test_us_requester_now_licensed_on_geo_restricted_resource(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """A requester whose geography plato's terms do not permit is now LICENSED (flip).

    Pre-ADR-014 a US commercial requester got no offer on plato (its terms permit
    only EU). The Exchange no longer excludes a term by the requester's
    self-declared geography — the geography restriction rides on the returned
    offer for the agent to self-honour — so the requester is licensed and
    receives a signed URL. (user_type / geography are no longer sent on the wire.)

    Broker Resolve, then a purchase of the winning offer through the Broker; the
    signed URL is minted on the purchase.
    """
    res = seeded.restricted  # plato
    _, item = buy_through_broker(
        compose_stack, res.uri, agent_id=res.buyer_agent_id, key_path=res.buyer_key_path
    )
    assert item.denial_reason is None, item
    assert item.retrieval_endpoint, item


def test_contributor_identity_is_served_via_wellknown(compose_stack: StackURLs) -> None:
    """Guard: the contributor's served well-known key matches its signing fixture.

    The well-known-trust rule removed the fora.agents pre-seed: the Exchange learns the
    ``catalog-contributor-e2e`` key ONLY by fetching that contributor's own
    discovery documents (the ``catalog-contributor-e2e-jwks`` compose host). This guard
    fetches them through the real host and asserts (a) the keyless overlay
    manifest's ``domain`` equals the contributor's agent_id (matched by
    caller_id), and (b) the key served in the contributor's WBA directory (x)
    byte-matches the private signing fixture's public half — so the key the
    Exchange fetches is exactly the key the harness signs with. A drift here
    would fail Gate-1 for a reason unrelated to the push path.
    """
    import base64
    import json

    doc = json.loads(Path(CONTRIBUTOR_KEY_PATH).read_text())
    assert doc["kid"] == CATALOG_CONTRIBUTOR_ID
    assert _PHILOSOPHY_FEED.is_file(), _PHILOSOPHY_FEED

    # Read the contributor's served overlay + WBA directory THROUGH the real
    # host alias. After the WBA split the overlay is keyless; the signing key
    # lives in the WBA directory keys[] (no kid, named by thumbprint). Both SDK reads
    # refuse a document the strict check does not accept, so a manifest still
    # carrying public_keys, or a directory key carrying a kid, fails here.
    origin = f"http://{CATALOG_CONTRIBUTOR_ID}"
    manifest = manifest_at(origin).message
    assert manifest.domain == CATALOG_CONTRIBUTOR_ID, manifest
    served = wba_directory_at(origin).message.keys or []
    assert len(served) == 1, f"contributor WBA directory must serve one key; got {served!r}"
    x = served[0].x or ""
    # The served `x` (base64url, no pad) must equal the fixture's public half.
    fixture_pub = doc["public_key"].rstrip("=")
    assert x.rstrip("=") == fixture_pub, (
        f"served contributor key drifted from signing fixture: "
        f"served={x!r} fixture={doc['public_key']!r}"
    )
    # Sanity: the served x decodes to a 32-byte Ed25519 raw key.
    raw = base64.urlsafe_b64decode(x + "=" * (-len(x) % 4))
    assert len(raw) == 32, f"served key not 32 raw bytes: {len(raw)}"
