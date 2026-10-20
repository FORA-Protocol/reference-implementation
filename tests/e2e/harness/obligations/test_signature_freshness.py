"""Obligations IDENTITY-016 and IDENTITY-025: how fresh a request signature has to be.

Surface: the Exchange's DiscoverResources through a fresh agent's own SDK client,
read back as the SDK's typed answer or typed refusal (``request_auth_failure``).

* IDENTITY-016: ``created`` and ``expires`` are both required and a signature
  outside its window is refused. The protocol author asked for the numbers to be
  stated from what the SDK verifier enforces: ``created`` may sit up to 300
  seconds ahead of the verifier's clock. A signature created one minute ahead is
  answered; one created more than 300 seconds ahead is refused as STALE. The
  signing window is moved through the SDK signer's own window setting.
* IDENTITY-025: the same signed bytes sent again are a replay, and the verifier
  refuses them. The reference verifier's replay store remembers a signature for
  five minutes, while the SDK signs with a ten-minute window by default, and no
  protocol mount caps a signature's lifetime. So the bytes are sent once and
  answered, sent again at once and refused as STALE, and then sent again every few
  seconds until six minutes have passed, all before the signature expires. Any
  answer is the replay forgotten inside the window. This test takes about five
  minutes when the defect is present and six when it is fixed; the wait cannot be
  shortened, because the five minutes are the replay store's own memory.
"""

from __future__ import annotations

import pytest
from fora_sdk.client import CallError

from ..after_signing import Capture, after_signing, resend
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..identity_requests import (
    SDK_SIGNING_TTL_S,
    SIGNATURE_STALE,
    answered_with_offer,
    assert_auth_refusal,
    discovery_by,
    probe_query,
    signer_for,
)
from ..malformed import Alteration, Mutation, malformed
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: How far ahead of the verifier's clock ``created`` may sit.
_SKEW_S = 300
#: How long the replay is resent: almost the whole lifetime of the signature, which
#: the Web Bot Auth profile caps at five minutes, stopping short of its expiry.
_RESEND_FOR_S = SDK_SIGNING_TTL_S - 30
_RESEND_EVERY_S = 10.0


@obligation(Obligation.IDENTITY_016)
def test_a_signature_created_within_the_skew_is_answered(fresh_agent: FreshAgent) -> None:
    """``created`` one minute ahead of the Exchange's clock: answered with an offer."""
    client = fresh_agent.exchange_client(
        EXCHANGE_A_DOMAIN, signer=signer_for(fresh_agent, fresh_agent.ed25519, created_offset_s=60)
    )
    answered_with_offer()(client.discover(probe_query()))


@obligation(Obligation.IDENTITY_016, negative=True)
def test_a_signature_created_beyond_the_skew_is_refused_as_stale(fresh_agent: FreshAgent) -> None:
    """``created`` 400 seconds ahead, past the 300-second skew: refused as STALE."""
    refusal = malformed(
        Mutation.SIGNATURE,
        f"created {_SKEW_S + 100} seconds ahead of the verifier's clock",
        control=discovery_by(fresh_agent),
        alter=Alteration(
            signer=signer_for(fresh_agent, fresh_agent.ed25519, created_offset_s=_SKEW_S + 100)
        ),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_STALE)


@obligation(Obligation.IDENTITY_025, negative=True)
def test_a_replay_is_refused_for_as_long_as_its_signature_is_valid(fresh_agent: FreshAgent) -> None:
    """The same signed bytes stay a replay for as long as the signature is valid."""
    assert _RESEND_FOR_S < SDK_SIGNING_TTL_S, "the resends must end before the signature expires"
    capture = Capture()
    first = fresh_agent.exchange_client(
        EXCHANGE_A_DOMAIN,
        signer=signer_for(fresh_agent, fresh_agent.ed25519),
        http=capture.client(),
    )
    answered_with_offer()(first.discover(probe_query()))
    original = capture.last()

    def send_again() -> CallError | None:
        client = fresh_agent.exchange_client(
            EXCHANGE_A_DOMAIN, http=after_signing(resend(original))
        )
        try:
            client.discover(probe_query())
        except CallError as refusal:
            return refusal
        return None

    immediate = send_again()
    assert immediate is not None, "the same signed bytes sent at once were answered"
    assert_auth_refusal(immediate, SIGNATURE_STALE)

    answered_at: list[int] = []

    def answered() -> bool:
        refusal = send_again()
        if refusal is None:
            answered_at.append(1)
            return True
        assert_auth_refusal(refusal, SIGNATURE_STALE)
        return False

    try:
        poll_until(
            answered,
            _RESEND_FOR_S,
            failure=lambda: "refused throughout",
            interval_s=_RESEND_EVERY_S,
        )
    except TimeoutError:
        return
    assert not answered_at, "the replayed bytes were answered while the signature was still valid"
