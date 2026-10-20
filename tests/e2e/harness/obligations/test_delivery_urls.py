"""Obligations DELIVERY-001, -002 and -013: the delivery URLs an Exchange issues.

The contract is the signed delivery URL of ``fora.proto`` in the pinned module
``github.com/FORA-Protocol/protocol``, with the edge-function and threat-model
pages of its documentation:

* the Exchange signs ``"GET\\n<canonical URL>"`` with Ed25519, and the URL verifies
  under the Exchange's published key;
* the URL's ``kid`` is the RFC 7638 thumbprint of the tenant's Ed25519 key, which
  the edge resolves against the Exchange's key directory;
* the Exchange issues short expiries, five minutes in the reference flows.

Every purchase is the SDK's ``ExecuteTransaction``, sent directly to the Exchange
that sells the resource, as the test's own funded agent (``fresh_agent``). The URL
is read off the item the SDK returns. The Exchange's keys are read through the
SDK's ``read_wba_directory``, and the URL is judged by the SDK's signed-URL
verifier, which is not code the Exchange runs.

The two-tenant half of DELIVERY-002 (URLs from two tenants of one Exchange carry
different kids) is not driven: every Exchange in this stack runs one seat.
"""

from __future__ import annotations

import time
from urllib.parse import parse_qs, urlsplit

import pytest
from fora_sdk.b64 import b64url_decode_strict
from fora_sdk.signedurl import verify_ed25519_signed_url
from fora_sdk.thumbprint import thumbprint

from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, exchange_url
from ..fresh_agent import FreshAgent
from ..money_path import ISSUER, PAPER_SATELLITES, SCI_FI_DOOR, THALES, buy_at_issuer
from ..obligation_registry import Obligation, obligation
from ..protocol_documents import wba_directory_at

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The longest a delivery URL may stay valid after its purchase, in seconds.
_SHORT_EXPIRY_S = 300

#: The verdict for a URL that does not verify under the key it is checked with.
_SIGNATURE_MISMATCH = "signature_mismatch"

#: The query parameter a URL's expiry is written in, per signing scheme: ``exp`` on
#: an Ed25519 delivery URL, ``Expires`` on a CloudFront canned-policy URL.
_EXPIRY_PARAMS = ("exp", "Expires")


def _query(url: str) -> dict[str, str]:
    """The URL's query parameters; each must occur once."""
    parsed = parse_qs(urlsplit(url).query, keep_blank_values=True)
    repeated = {name: values for name, values in parsed.items() if len(values) != 1}
    assert not repeated, f"{url} repeats {repeated}"
    return {name: values[0] for name, values in parsed.items()}


def _published_keys(agent: FreshAgent, exchange: str) -> dict[str, bytes]:
    """The raw Ed25519 keys ``exchange`` publishes in its directory, by RFC 7638 thumbprint."""
    directory = wba_directory_at(exchange_url(agent.stack, exchange)).message
    raw = [b64url_decode_strict(key.x or "") for key in directory.keys or []]
    assert raw, f"{exchange} publishes no key in its directory"
    return {thumbprint(key): key for key in raw}


@obligation(Obligation.DELIVERY_001, Obligation.DELIVERY_002)
@pytest.mark.parametrize(
    ("uri", "other_exchange"),
    [(THALES, EXCHANGE_B_DOMAIN), (PAPER_SATELLITES, EXCHANGE_A_DOMAIN)],
    ids=["exchange-a", "exchange-b"],
)
def test_a_delivery_url_verifies_under_the_key_its_kid_names_in_the_issuers_directory(
    fresh_agent: FreshAgent, uri: str, other_exchange: str
) -> None:
    """The kid is a thumbprint the issuer publishes, and the URL verifies under that key.

    DELIVERY-002: the URL's ``kid`` is the thumbprint of a key in the issuing
    Exchange's directory. DELIVERY-001: the SDK's verifier accepts the URL under
    the key that kid selects, now, bound to the buyer's thumbprint. The control
    shows the verifier tells keys apart: the same URL checked under another
    Exchange's key is refused ``signature_mismatch``.
    """
    url = str(buy_at_issuer(fresh_agent, uri).retrieval_endpoint)
    issued = _published_keys(fresh_agent, ISSUER[uri])
    kid = _query(url).get("kid", "")
    assert kid in issued, (
        f"the URL's kid {kid!r} is not the thumbprint of a key {ISSUER[uri]} publishes: "
        f"{sorted(issued)}"
    )

    verdict = verify_ed25519_signed_url(
        url, now=int(time.time()), resolve_key=lambda k: issued.get(k or "")
    )
    assert verdict.valid, f"the URL does not verify under {ISSUER[uri]}'s key: {verdict}"
    assert verdict.kid == kid, verdict
    assert verdict.agent_id == fresh_agent.thumbprint, (
        f"the URL is bound to {verdict.agent_id!r}, want the buyer's {fresh_agent.thumbprint!r}"
    )

    foreign = _published_keys(fresh_agent, other_exchange)
    assert not set(foreign) & set(issued), "two Exchanges publish the same key"
    wrong = verify_ed25519_signed_url(
        url, now=int(time.time()), resolve_key=lambda _k: next(iter(foreign.values()))
    )
    assert not wrong.valid and wrong.reason == _SIGNATURE_MISMATCH, (
        f"the URL verified under {other_exchange}'s key: {wrong}"
    )


@obligation(Obligation.DELIVERY_013)
@pytest.mark.parametrize(
    "uri",
    [THALES, PAPER_SATELLITES, SCI_FI_DOOR],
    ids=["ed25519-exchange-a", "ed25519-exchange-b", "cloudfront-exchange-c"],
)
def test_a_delivery_url_expires_at_most_five_minutes_after_its_purchase(
    fresh_agent: FreshAgent, uri: str
) -> None:
    """expires_at and the URL's own expiry agree, and fall within five minutes of the purchase.

    The purchase happens between ``before`` and ``after`` on this clock, which the
    stack's containers share. Both the item's ``expires_at`` and the expiry signed
    into the URL must be later than ``before`` and no later than ``after`` plus
    five minutes. The URL carries whole seconds, so the two agree when they are
    less than a second apart.
    """
    before = time.time()
    item = buy_at_issuer(fresh_agent, uri)
    after = time.time()

    assert item.expires_at is not None, f"a bought item carries no expires_at: {item}"
    expires_at = item.expires_at.timestamp()
    query = _query(str(item.retrieval_endpoint))
    signed = [query[name] for name in _EXPIRY_PARAMS if name in query]
    assert len(signed) == 1 and signed[0].isdigit(), (
        f"the URL states its expiry in exactly one of {_EXPIRY_PARAMS}: {item.retrieval_endpoint}"
    )
    url_expiry = int(signed[0])

    assert abs(expires_at - url_expiry) < 1, (
        f"expires_at {item.expires_at} and the URL's expiry {url_expiry} disagree"
    )
    for label, value in (("expires_at", expires_at), ("the URL's expiry", url_expiry)):
        assert before < value <= after + _SHORT_EXPIRY_S, (
            f"{label} is {value - after:.0f}s after the purchase; want later than the purchase "
            f"and at most {_SHORT_EXPIRY_S}s after it"
        )
