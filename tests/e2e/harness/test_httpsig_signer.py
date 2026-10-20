"""The harness's request signer keeps identical fixed-window requests unique.

Every signer the harness builds is the SDK's ``SigningTransport``
(``httpsig_signer.py``). Ed25519 is deterministic and the signature timestamps
have one-second resolution, so two identical requests signed in one second share
a signature unless each carries a fresh nonce, and the receiver's replay store
then refuses the second. The e2e bursts cannot promise to catch a signer that
only advanced ``expires``: how many seconds a burst spans depends on the
machine. Holding the window fixed here can, because the nonce is then the only
thing left that can make the two signatures differ.

Reads the signer's own output, so it needs no stack and no network.
"""

from __future__ import annotations

import pytest
from fora_sdk import sfv
from fora_sdk.identity import generate_key

from .httpsig_signer import signing_transport

pytestmark = pytest.mark.stack_isolation("isolated")


def test_identical_requests_get_fresh_nonces_with_a_fixed_window() -> None:
    """Fixed timestamps rule out clock changes masking a missing or reused nonce."""
    key, _thumbprint = generate_key()
    created, expires = 1_700_000_000, 1_700_000_030
    transport = signing_transport("https://agent.example", key, window=lambda: (created, expires))
    signed = [
        transport.sign_outbound(
            method="POST",
            url="https://exchange.example/fora.v1.ExchangeService/DiscoverResources",
            body=b"{}",
            authorization="",
        ).headers
        for _ in range(2)
    ]

    nonces = []
    for headers in signed:
        members = list(sfv.parse_dictionary(headers["signature-input"]).values())
        assert len(members) == 1, headers["signature-input"]
        member = members[0]
        assert isinstance(member, sfv.InnerList), member
        params = member.params
        assert (params.get("created"), params.get("expires")) == (created, expires), params
        nonce = params.get("nonce")
        assert isinstance(nonce, str) and nonce, (
            f"the signer emitted no nonce: {headers['signature-input']}"
        )
        nonces.append(nonce)

    assert nonces[0] != nonces[1], "the signer reused its nonce"
    assert signed[0]["signature"] != signed[1]["signature"], "the signer reused its signature"
