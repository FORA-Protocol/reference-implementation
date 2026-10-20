"""Obligation PAID-008: a tampered signed URL is refused at delivery.

Drives the scenario against the demo ``paper-satellites`` resource (FLAT USD, no
user-type restriction so it resolves via the facet-less canonical
DiscoveryRequest) as the USD demo buyer:

1. Acceptance — the Broker purchase returns a signed URL
   pointing at the music demo domain (→ Fastly edge).
2. Tamper — flip a base64url byte inside the ``sig=`` query parameter.
3. Refusal — the edge answers 403 with its ``signature_mismatch`` refusal and
   serves no publisher payload.

The refusal leg is the SDK's own content fetch, as the buyer, rather than the
shared ``fetch_signed`` helper: that helper asserts 200 and is for content
delivery, while this asserts the delivery does NOT happen. The SDK presents the
buyer's proof of possession, so a 403 here means the tampered signature was
refused, not that the fetch was rejected for lacking a binding proof it could
have supplied. The SDK returns content only for a successful answer; a refusal
comes back as its typed error carrying the edge's refusal token, which the edge
writes as a small JSON body in place of the publisher's bytes.
"""

from __future__ import annotations

import re
from contextlib import closing

import pytest
from fora_sdk.client import CallError

from ..broker_purchase import buy_through_broker
from ..conftest import StackURLs
from ..exchanges import EXCHANGE_A_DOMAIN
from ..sdk_client import sdk_exchange_client, stack_offer_verifier
from ..seed import SeededFixture
from ..obligation_registry import Obligation, obligation

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


# The edge's refusal token for a signed URL whose signature does not verify.
_SIGNATURE_MISMATCH = "signature_mismatch"


def _tamper_sig(signed_url: str) -> tuple[str, str]:
    sig_match = re.search(r"sig=([^&]+)", signed_url)
    assert sig_match is not None, f"signed URL has no sig= query param: {signed_url}"
    sig_val = sig_match.group(1)
    assert len(sig_val) >= 2, f"sig= value too short to mutate ({sig_val!r})"
    mid = len(sig_val) // 2
    replacement = "B" if sig_val[mid] != "B" else "C"
    tampered_sig = sig_val[:mid] + replacement + sig_val[mid + 1 :]
    assert tampered_sig != sig_val, f"tamper produced the same sig: {sig_val!r}"
    return signed_url.replace(f"sig={sig_val}", f"sig={tampered_sig}"), sig_val


@obligation(Obligation.PAID_008, negative=True)
def test_tampered_signed_url_refused_no_content_served(
    compose_stack: StackURLs,
    seeded: SeededFixture,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """Tampered ed25519-signed URL is refused; the publisher payload never leaks."""
    res = seeded.fastly  # paper-satellites: FLAT USD, no user-type restriction

    monkeypatch.setenv("FORA_AGENT_ID", res.buyer_agent_id)
    monkeypatch.setenv("FORA_AGENT_KEY_FILE", str(res.buyer_key_path))

    # (1) Acceptance — resolve at the Broker, then buy the winning offer through
    # the Broker. The signed URL the purchase hands back is the delivery surface
    # the obligation refers to. intended_use ``ai-input`` is the real function
    # paper-satellites permits.
    _, item = buy_through_broker(
        compose_stack, res.uri, agent_id=res.buyer_agent_id, key_path=res.buyer_key_path
    )
    signed_url = item.retrieval_endpoint
    assert signed_url, (
        f"prerequisite — the agent must first receive a delivered URL — failed: {item}"
    )

    # (2) Tamper — flip a sig byte. (3) The edge refuses it with 403 and serves
    # nothing.
    tampered_url, original_sig = _tamper_sig(signed_url)
    # A content fetch dials the URL it is given, so the Exchange this client is
    # addressed at plays no part; it is the buyer's own signer that matters.
    buyer = sdk_exchange_client(
        compose_stack,
        EXCHANGE_A_DOMAIN,
        stack_offer_verifier(),
        agent_id=res.buyer_agent_id,
        key_path=res.buyer_key_path,
    )
    with closing(buyer), pytest.raises(CallError) as caught:
        buyer.fetch(tampered_url)

    refusal = caught.value
    assert refusal.status == 403, (
        f"PAID-008: a tampered signature must be refused with 403, got {refusal}; "
        f"original sig={original_sig!r}; tampered url={tampered_url!r}"
    )
    # The obligation is "the content is NOT SERVED", which the status alone does
    # not establish: an edge answering 403 while still writing the publisher body
    # would satisfy a status-only assertion and violate the obligation. The edge's
    # refusal token is read from the body, so it is there only when the body is
    # the edge's refusal rather than the publisher's bytes.
    assert refusal.reason == _SIGNATURE_MISMATCH, (
        f"PAID-008: the edge's refusal must name {_SIGNATURE_MISMATCH!r}, got {refusal}"
    )
