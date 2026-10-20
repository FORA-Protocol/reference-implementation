"""Real-AWS acceptance tests for the demo deployment.

Gated by ``FORA_E2E_AWS=1`` so the suite stays no-op by default. Drives the
deployed stack via its public HTTPS surface: the agent discovers an offer with
``BrokerService/Resolve``, buys it with ``BrokerService/ExecuteTransaction``,
and receives a CloudFront RSA-signed URL that CloudFront verifies natively
before serving the origin bytes.

Both Broker calls go through the SDK's own ``BrokerClient``, signed with the
agent's key, exactly as the compose harness buys (``harness.broker_purchase``).
Discovery no longer hands out a signed URL: a delivery URL exists only on the
result of a purchase, so every test that needs one buys first.

Prereqs:
    FORA_E2E_AWS=1
    FORA_E2E_BROKER_URL       e.g. https://broker.example
    FORA_E2E_CLOUDFRONT_URL   e.g. https://cdn.example
    FORA_E2E_AGENT_ID         the label the agent puts in requester.id
    FORA_E2E_AGENT_KEY_PATH   the agent's keyfile, in the format
                              scripts/gen-e2e-keys.sh writes; its kid is the
                              agent's Web Bot Auth directory, which the deployed
                              Broker and Exchange must be able to fetch
    FORA_E2E_EXCHANGE_DOMAIN  the Exchange that sells the resource; its offer
                              key is read from its Web Bot Auth directory over
                              https, so the offer can be verified before buying
    FORA_E2E_RESOURCE_URI     catalog URI seeded on that Exchange for an
                              AWS_CLOUDFRONT_RSA site, reachable via
                              FORA_E2E_CLOUDFRONT_URL
"""

from __future__ import annotations

import os
import re
import sys
from contextlib import closing
from pathlib import Path

import httpx
import pytest

# The e2e-aws suite lives outside the harness package; mirror demo_proof.py and
# put tests/e2e on sys.path so the harness helpers import.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "e2e"))

from harness.broker_purchase import only_item, resolve_offer  # noqa: E402
from harness.constants import AI_BOT_UA  # noqa: E402
from harness.sdk_client import deployed_broker_client  # noqa: E402

pytestmark = pytest.mark.skipif(
    os.environ.get("FORA_E2E_AWS") != "1",
    reason="set FORA_E2E_AWS=1 to hit deployed AWS infra",
)


def _env(name: str) -> str:
    value = os.environ.get(name)
    if not value:
        pytest.skip(f"missing env {name}")
    return value


@pytest.fixture(scope="module")
def signed_url() -> str:
    """Buy the resource once through the Broker and return its delivery URL.

    One purchase serves every test in this module: each reads the same URL, and
    a purchase per test would bill the agent three times for one resource.
    """
    broker_url = _env("FORA_E2E_BROKER_URL")
    agent_id = _env("FORA_E2E_AGENT_ID")
    key_path = Path(_env("FORA_E2E_AGENT_KEY_PATH"))
    exchange = _env("FORA_E2E_EXCHANGE_DOMAIN")
    uri = _env("FORA_E2E_RESOURCE_URI")
    # The harness's deployed client: https resolvers. The resource is sold on the
    # CloudFront RSA scheme, whose signed URL CloudFront verifies natively, which
    # the tests below drive.
    client = deployed_broker_client(
        broker_url, agent_id=agent_id, key_path=key_path, exchange=exchange
    )
    with closing(client):
        offer = resolve_offer(client, uri)
        response = client.execute([offer])
    item = only_item(response, f"broker purchase of {uri}")
    assert item.denial_reason is None, f"the Exchange denied the purchase: {item}"
    assert item.retrieval_endpoint, f"a delivered item carries no retrieval_endpoint: {item}"
    return item.retrieval_endpoint


def test_purchase_returns_cloudfront_signed_url(signed_url: str) -> None:
    """The purchase answers with a CloudFront canned-policy signed URL."""
    for name in ("Expires", "Signature", "Key-Pair-Id"):
        assert name in signed_url, f"{name} missing in {signed_url}"


def test_signed_url_delivers_content_via_cloudfront(signed_url: str) -> None:
    """Real CloudFront natively verifies the signed URL and serves the origin."""
    content_resp = httpx.get(signed_url, timeout=30.0)
    assert content_resp.status_code == httpx.codes.OK, content_resp.text
    assert len(content_resp.content) > 0


def test_tampered_signature_is_rejected_by_cloudfront(signed_url: str) -> None:
    """CloudFront native verify returns 403 on a mutated Signature param."""
    match = re.search(r"Signature=([^&]+)", signed_url)
    assert match, signed_url
    sig = match.group(1)
    mid = len(sig) // 2
    tampered_sig = sig[:mid] + ("B" if sig[mid] != "B" else "C") + sig[mid + 1 :]
    tampered = signed_url.replace(f"Signature={sig}", f"Signature={tampered_sig}")
    bad = httpx.get(tampered, timeout=30.0)
    assert bad.status_code == httpx.codes.FORBIDDEN, bad.text


def test_bot_redirect_without_signature() -> None:
    """Lambda@Edge should 403 + X-Content-Rules when a bot UA hits unsigned URL."""
    cloudfront_url = _env("FORA_E2E_CLOUDFRONT_URL")
    resp = httpx.get(
        f"{cloudfront_url}/any-path",
        headers={"User-Agent": AI_BOT_UA},
        timeout=30.0,
    )
    # Acceptable outcomes: CloudFront's own missing-key-pair 403 (native)
    # OR Lambda@Edge's bot-redirect 403. Either way, not 200.
    assert resp.status_code == httpx.codes.FORBIDDEN, resp.text
    # X-Content-Rules is our Lambda@Edge signal; allow either path.
    if "x-content-rules" in {k.lower() for k in resp.headers}:
        assert "fora.json" in resp.headers.get("x-content-rules", "").lower()
