"""The TypeScript WBA oracle against the WG-00 vectors, and against the Python oracle.

The ``wba-oracle-ts`` sidecar runs Cloudflare's ``web-bot-auth`` library. This
suite checks it the way ``test_wba_oracle.py`` checks the Python oracle: it must
reproduce the Appendix E signatures byte for byte and accept them. Then each
oracle verifies what the other signs, including a signature that covers more
than the WG-00 minimum, and each rejects a tampered copy. Two implementations
written independently, in different languages, agreeing on the same bytes is
what lets the conformance tests treat either one's verdict on FORA as WG-00's.

The sidecar publishes no host port, so this runs only inside the compose
network (the ``runner`` service). It needs nothing else from the stack.
"""

from __future__ import annotations

import os

import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from . import wba_vectors
from .wba_oracle import (
    Check,
    Message,
    Require,
    fresh_key,
    message,
    private_jwk,
    public_jwk,
    sign,
    verify,
)
from .wba_oracle_ts import TSOracle

pytestmark = [
    # Marks rather than a skip inside a fixture: a host run leaves before any
    # fixture tries to reach a hostname that resolves only in-network.
    pytest.mark.skipif(
        os.environ.get("FORA_E2E_IN_NETWORK") != "1",
        reason="the wba-oracle-ts sidecar is reachable only in-network (FORA_E2E_IN_NETWORK=1)",
    ),
    # Reads and writes no stack state, so the per-test cleanup chain has nothing to do.
    pytest.mark.stack_isolation("shared-without-cleanup"),
]

URL = "https://publisher.example/articles/1?view=full"
AGENT = "https://agents.example"
# Covered beyond the WG-00 minimum, the components WG-00 Section 5.2 recommends
# for narrowing a signature, plus a covered body digest.
EXTRA = ("@method", "@path", "@query", "content-digest")
DIGEST = "sha-256=:X48E9qOokqqrvdts8nOJRJN3OWDUoyWxBf7kbu9DBPE=:"


@pytest.fixture(scope="module")
def agent_signer() -> Ed25519PrivateKey:
    """A fresh key, so nothing below depends on the published RFC 9421 test key."""
    return fresh_key()


def _request() -> Message:
    return message("GET", URL, headers={"Content-Digest": DIGEST})


@pytest.mark.parametrize("form", wba_vectors.FORMS)
def test_ts_sign_reproduces_appendix_e(ts_oracle: TSOracle, form: str) -> None:
    vector = wba_vectors.request_vector(form)
    headers = ts_oracle.sign(
        message(vector["method"], vector["url"]),
        wba_vectors.private_jwk(),
        vector["agent_origin"],
        label=vector["label"],
        agent_key=vector["agent_key"],
        nonce=vector["nonce"],
        created=vector["created"],
        expires=vector["expires"],
        legacy=vector["form"] == "legacy",
    )
    assert headers == vector["headers"]


@pytest.mark.parametrize("form", wba_vectors.FORMS)
def test_ts_verify_accepts_appendix_e(ts_oracle: TSOracle, form: str) -> None:
    vector = wba_vectors.request_vector(form)
    result = ts_oracle.verify(
        vector["headers"],
        vector["method"],
        vector["url"],
        wba_vectors.public_jwk(),
        now=vector["created"] + 60,
    )
    assert result.ok, result
    assert result.label == vector["label"]
    assert result.keyid == wba_vectors.vectors()["key"]["thumbprint"]
    assert result.signature_agent is not None
    assert result.signature_agent["uri"] == vector["agent_origin"]


def test_ts_verify_rejects_a_tampered_vector(ts_oracle: TSOracle) -> None:
    vector = wba_vectors.request_vector("dictionary")
    tampered = {**vector["headers"], "Signature-Agent": 'agent2="https://other.example"'}
    result = ts_oracle.verify(
        tampered, vector["method"], vector["url"], wba_vectors.public_jwk(), now=vector["created"]
    )
    assert not result.ok
    assert result.code == "VerificationFailed", result


def test_python_signs_and_ts_verifies(ts_oracle: TSOracle, agent_signer: Ed25519PrivateKey) -> None:
    request = _request()
    headers = {
        **dict(request.headers),
        **sign(request, agent_signer, AGENT, extra_components=EXTRA),
    }
    result = ts_oracle.verify(headers, "GET", URL, public_jwk(agent_signer))
    assert result.ok, result
    covered = [c["name"] for c in result.raw["components"]]
    assert covered == ["@authority", "signature-agent", *EXTRA]
    assert result.signature_agent == {"label": "sig1", "uri": AGENT, "type": "directory"}


def test_ts_signs_and_python_verifies(ts_oracle: TSOracle, agent_signer: Ed25519PrivateKey) -> None:
    request = _request()
    signed = ts_oracle.sign(request, private_jwk(agent_signer), AGENT, extra_components=EXTRA)
    headers = {**dict(request.headers), **signed}
    strict = Require(alg=True, nonce_bytes=64, member_key_is_label=True, components=EXTRA)
    result = verify(headers, "GET", URL, b"", public_jwk(agent_signer), require=strict)
    assert result.ok, result
    assert result.signature_agent == AGENT


def test_each_oracle_rejects_what_the_other_signed_once_moved(
    ts_oracle: TSOracle, agent_signer: Ed25519PrivateKey
) -> None:
    """A signature covering @path does not verify for another path, in either oracle."""
    moved = "https://publisher.example/articles/2?view=full"
    request = _request()
    by_python = {
        **dict(request.headers),
        **sign(request, agent_signer, AGENT, extra_components=EXTRA),
    }
    by_ts = {
        **dict(request.headers),
        **ts_oracle.sign(request, private_jwk(agent_signer), AGENT, extra_components=EXTRA),
    }
    ts_result = ts_oracle.verify(by_python, "GET", moved, public_jwk(agent_signer))
    assert not ts_result.ok
    assert ts_result.code == "VerificationFailed", ts_result
    py_result = verify(by_ts, "GET", moved, b"", public_jwk(agent_signer))
    assert py_result.failed is Check.SIGNATURE, py_result


def test_each_oracle_rejects_a_key_it_was_not_given(
    ts_oracle: TSOracle, agent_signer: Ed25519PrivateKey
) -> None:
    stranger = public_jwk(fresh_key())
    request = _request()
    by_python = {**dict(request.headers), **sign(request, agent_signer, AGENT)}
    assert not ts_oracle.verify(by_python, "GET", URL, stranger).ok
    by_ts = {**dict(request.headers), **ts_oracle.sign(request, private_jwk(agent_signer), AGENT)}
    assert verify(by_ts, "GET", URL, b"", stranger).failed is Check.KEYID
