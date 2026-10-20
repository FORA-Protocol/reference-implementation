"""Full-stack E2E: MCP → Broker → Exchange → Edge → Publisher (demo catalog).

Proves the 3-way cryptographic chain works end-to-end against a real
dockerised stack, driven entirely against the demo-catalog resources the real
``fora-ingest`` binary ingested (one signed PushResources RPC per feed) —
no test in this suite pushes demo content.

Each demo publisher domain resolves in-network to its edge runtime via a
docker-compose network alias on port 80:

* ``demo.fora-protocol.org``        → Cloudflare (Miniflare) ed25519 edge
* ``music.demo.fora-protocol.org``  → Fastly Compute (Viceroy) ed25519 edge
* ``sfx.demo.fora-protocol.org``    → AWS CloudFront RSA edge shim

So the Exchange mints a signed URL on the demo domain and the agent fetches it
DIRECTLY — no Host override, no netloc rewrite. A real row in
fora.transaction_log backs every successful resolve.
"""

from __future__ import annotations

from http import HTTPStatus

from wire.models import TransactionResultItem

from .broker_purchase import buy_through_broker
from .conftest import StackURLs
from .edge_fetch import fetch_signed, fetch_without_proof, tamper_query_param
from .obligation_registry import Obligation, obligation
from .seed import DemoResource, SeededFixture

# The refusal tests below fetch through ``edge_fetch.fetch_without_proof``. The
# ed25519 edges enforce binding, so a bound URL fetched without proof is refused
# 403 ``missing_agent_key``, which is what the two "refused without the key" tests
# assert; the tamper negatives send no proof either. A positive fetch goes through
# ``edge_fetch.fetch_signed``, which presents the proof through the SDK.


def _delivered_url(item: TransactionResultItem) -> str:
    """The signed delivery URL of one bought item; fails closed when there is none."""
    assert item.retrieval_endpoint, f"the purchase answered no delivery URL: {item}"
    return item.retrieval_endpoint


def _deliver_demo(compose_stack: StackURLs, res: DemoResource) -> TransactionResultItem:
    """Resolve a DemoResource at the Broker and buy the winner through the Broker.

    Broker Resolve is discovery-only, so the signed URL is minted on the
    purchase, not on Resolve. intended_use ``ai-input`` is a real function every
    demo term permits. Buyer is the currency-matched demo buyer.
    """
    _, item = buy_through_broker(
        compose_stack,
        res.uri,
        agent_id=res.buyer_agent_id,
        key_path=res.buyer_key_path,
    )
    return item


def test_broker_resolve_returns_signed_url_and_writes_tx_log(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """A purchase through the Broker mints an Exchange-backed signed URL and a tx.

    Observed through the purchase's answer only (Testing Doctrine pt9): a
    populated transaction_id means the Exchange committed the transaction_log row
    before returning the signed URL (the write-before-sign invariant). Resolve is
    discovery-only, so the URL and transaction_id are minted on the purchase, not
    on Resolve. Driven on the FREE demo resource (socrates) as the EUR buyer.
    """
    res = seeded.free
    item = _deliver_demo(compose_stack, res)
    assert item.retrieval_endpoint, f"retrieval_endpoint missing from the item: {item}"
    assert item.transaction_id, f"transaction_id missing from the item: {item}"


def test_signed_url_fetches_origin_content_via_cloudflare_edge(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """The Exchange-signed URL on the demo domain verifies at the Cloudflare edge and delivers bytes."""
    res = seeded.cloudflare  # socrates, demo.fora-protocol.org → Cloudflare
    signed_url = _delivered_url(_deliver_demo(compose_stack, res))
    # socrates body is the real Stoa Press article content. Assert the ACTUAL
    # delivered article ("Socrates"), never a bare non-empty fallback — that
    # would pass on a 200 edge error-stub or a wrong-route body.
    fetch_signed(signed_url, compose_stack, expect_marker="Socrates", key_path=res.buyer_key_path)


def _assert_bound_url_refused_without_key(
    compose_stack: StackURLs, res: DemoResource, body_marker: str
) -> None:
    """Drive the leaked-bound-URL refusal for one edge runtime.

    Shared between the Cloudflare and Fastly cases: two runs are the point (the
    runtimes reach the check through different Ed25519 primitives), two copies of
    the assertions are not — they had already drifted in wording.
    """
    signed_url = _delivered_url(_deliver_demo(compose_stack, res))

    # The URL must be agent-bound, or there is nothing to enforce and the refusal
    # below would be proving something else.
    assert "agent_id=" in signed_url, f"delivery URL is not agent-bound: {signed_url}"

    # No proof of possession -> refused, and refused for the RIGHT reason.
    plain = fetch_without_proof(signed_url)
    assert plain.status == HTTPStatus.FORBIDDEN, (
        f"a bound URL fetched with no proof of possession must be refused, "
        f"got {plain.status}: {plain.text[:256]}"
    )
    assert plain.reason == "missing_agent_key", plain.reason
    assert body_marker not in plain.text, f"the refusal leaked the body ({body_marker})"


@obligation(Obligation.DELIVERY_004, negative=True)
def test_bound_url_is_refused_without_the_key_at_cloudflare_edge(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """A leaked bound URL is worthless: the edge requires the key it names.

    This is the security property ADR-013 D3 buys, driven end-to-end against the
    Cloudflare (Ed25519) edge. The test above delivers the SAME resource
    successfully by presenting the buyer key, so the only variable here is the
    proof — which makes this a statement about the check rather than about the
    URL being malformed. URL integrity is a separate axis: see
    test_tampered_signature_is_rejected.
    """
    _assert_bound_url_refused_without_key(compose_stack, seeded.cloudflare, "Socrates")


def test_tampered_signature_is_rejected(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """Mutating the ed25519 signature flips the Cloudflare edge verify to 403."""
    res = seeded.cloudflare
    tampered = tamper_query_param(_delivered_url(_deliver_demo(compose_stack, res)))
    bad = fetch_without_proof(tampered)
    assert bad.status == HTTPStatus.FORBIDDEN, bad.text


def test_aws_path_resolve_returns_cloudfront_signed_url(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """The sfx demo tenant (AWS_CLOUDFRONT_RSA scheme) mints a canned-policy signed URL."""
    res = seeded.aws  # rain-on-tin-roof, sfx.demo.fora-protocol.org → AWS
    signed = _delivered_url(_deliver_demo(compose_stack, res))
    for name in ("Expires", "Signature", "Key-Pair-Id"):
        assert name in signed, f"{name} missing in {signed}"


def test_aws_signed_url_fetches_origin_via_cloudfront_shim(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """RSA canned policy verifies at the AWS shim, which proxies to the publisher origin."""
    res = seeded.aws
    delivered = _deliver_demo(compose_stack, res)
    # seeded.aws is rain-on-tin-roof (foleyworks sfx JSON). Assert the ACTUAL
    # delivered asset by its unique slug, not merely non-empty — a 200 edge
    # error-stub or wrong-route body must fail. An RSA URL carries no binding: the
    # SDK's fetch presents the buyer's proof anyway and the shim ignores it.
    fetch_signed(
        _delivered_url(delivered),
        compose_stack,
        expect_marker="rain-on-tin-roof",
        key_path=res.buyer_key_path,
    )


def test_aws_tampered_signature_is_rejected(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """Mutating a byte inside the CloudFront Signature flips the AWS shim verifier to 403."""
    res = seeded.aws
    delivered = _deliver_demo(compose_stack, res)
    tampered = tamper_query_param(_delivered_url(delivered), param="Signature")
    bad = fetch_without_proof(tampered)
    assert bad.status == HTTPStatus.FORBIDDEN, bad.text


def test_fastly_signed_url_fetches_origin_via_viceroy(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """Ed25519-signed URL on the music demo domain verifies on Fastly Compute (Viceroy)."""
    res = seeded.fastly  # paper-satellites, music.demo.fora-protocol.org → Fastly
    # seeded.fastly is paper-satellites (harmonia-records lyrics). Assert the
    # ACTUAL delivered lyric by its title, not merely non-empty.
    fetch_signed(
        _delivered_url(_deliver_demo(compose_stack, res)),
        compose_stack,
        expect_marker="Paper Satellites",
        key_path=res.buyer_key_path,
    )


@obligation(Obligation.DELIVERY_004, negative=True)
def test_bound_url_is_refused_without_the_key_at_fastly_edge(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """The Cloudflare property above, proved on the second enforcing runtime.

    Worth driving twice because the two edges reach the check by different paths:
    Cloudflare runs the SDK's WebCrypto verifier, while Fastly Compute cannot
    import Ed25519 through SubtleCrypto and substitutes its own primitive. A
    substitution that verified nothing would serve this fetch, and every positive
    Fastly test would still pass — the refusal is what tells the two apart.

    The test above delivers the SAME resource successfully by presenting the buyer
    key, so the only variable here is the proof.
    """
    _assert_bound_url_refused_without_key(compose_stack, seeded.fastly, "Paper Satellites")


def test_fastly_tampered_signature_is_rejected(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """Mutating the sig param trips Fastly Compute's ed25519 verify."""
    res = seeded.fastly
    tampered = tamper_query_param(_delivered_url(_deliver_demo(compose_stack, res)))
    bad = fetch_without_proof(tampered)
    assert bad.status == HTTPStatus.FORBIDDEN, bad.text


def test_discover_execute_delivers_content(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """discover → execute → fetch delivers the real publisher payload end to end.

    The one test that walks the whole agent story on the demo catalog: resolve an
    offer, buy it through the Broker (a one-resource purchase is a batch of one),
    then fetch the signed URL the Exchange minted and confirm the bytes are the
    publisher's.

    It drives the Broker over HTTP as an agent does. It used to import the Python
    shim's tool functions in-process; the shim is gone, and the MCP surface itself
    is covered by its own suite (``test_mcp_endpoint.py`` (in-network runs only)) plus the Go integration
    tests. What is proven here is the FORA path, not the MCP wrapper around it.
    """
    # paper-satellites is FLAT USD with no geo/user-type restriction, so it
    # resolves cleanly via the canonical DiscoveryRequest (which carries no facet
    # fields). It is fronted by the Fastly demo edge.
    res = seeded.fastly

    signed_url = _delivered_url(_deliver_demo(compose_stack, res))

    # Assert the ACTUAL delivered lyric by its title, not merely a 200: an edge
    # error-stub or a wrong-route body would otherwise pass.
    fetch_signed(
        signed_url,
        compose_stack,
        expect_marker="Paper Satellites",
        key_path=res.buyer_key_path,
    )
