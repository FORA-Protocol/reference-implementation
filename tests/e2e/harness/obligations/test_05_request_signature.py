"""Obligations SIGNATURE-001 to -008: a valid signature is accepted, every invalid one refused.

Surface: the Exchange's DiscoverResources RPC through the test's own agent's SDK
client. Each negative is a declared mutation (``harness.malformed``): the SAME
discovery the control sends, signed by the SDK and then altered in exactly one
way, either by the after-signing transport or by giving the SDK a different
signer or signing window. ``malformed`` sends the control first in every case, so
a refusal cannot be mistaken for an Exchange that would have refused the request
anyway, and it checks that every altered body still passes the request's strict
schema, so the only thing wrong with it is its signature.

The cases are the shared table in ``harness.signature_cases``, the same cases the
Broker's Resolve is driven through.

A refusal is read only through the SDK's typed error: code ``unauthenticated``
and the ErrorDetail ``request_auth_failure`` reason. The reasons are
deliberately coarse, so a refusal never reveals which verification step failed:

* ``SIGNATURE_MISSING``: no signature at all; the remedy is to sign;
* ``SIGNATURE_INVALID``: the signature does not verify (an unknown key, a body
  that differs from the signed digest, a covered header removed);
* ``SIGNATURE_STALE``: outside its time window, or already used; the remedy is to
  sign again now.

Every negative also asserts the absence of its side effect: the call raised, so
no offers came back.

The collapsed reasons are the protocol's decision, SIGNATURE-008, which replaces
the earlier requirement that every failure carry a reason of its own. Every
negative proves SIGNATURE-006 to -008, and each case also proves the obligation
for its own failure, as ``_PROVES`` records.
"""

from __future__ import annotations

import pytest

from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..identity_requests import (
    answered_with_offer,
    assert_auth_refusal,
    discovery_by,
    probe_query,
)
from ..malformed import Mutation, malformed
from ..obligation_registry import Obligation, obligation
from ..signature_cases import SIGNATURE_CASES

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The obligation each case proves for its own failure, beyond SIGNATURE-006 to -008.
_PROVES: dict[str, tuple[Obligation, ...]] = {
    "unsigned": (Obligation.SIGNATURE_001,),
    "unknown-key": (Obligation.SIGNATURE_002,),
    "tampered-body": (Obligation.SIGNATURE_004,),
    "covered-header-removed": (),
    "expired": (Obligation.SIGNATURE_003,),
    "created-in-future": (Obligation.SIGNATURE_003,),
    "replayed": (Obligation.SIGNATURE_005,),
}


@obligation(Obligation.SIGNATURE_007)
def test_a_validly_signed_request_is_answered(fresh_agent: FreshAgent) -> None:
    """The control on its own: the agent's correctly signed discovery gets a verified offer."""
    answered_with_offer()(fresh_agent.exchange(EXCHANGE_A_DOMAIN).discover(probe_query()))


@pytest.mark.parametrize(
    "case",
    [
        pytest.param(name, id=name, marks=obligation(*_PROVES[name], negative=True))
        for name in SIGNATURE_CASES
    ],
)
@obligation(
    Obligation.SIGNATURE_006, Obligation.SIGNATURE_007, Obligation.SIGNATURE_008, negative=True
)
def test_a_request_with_a_broken_signature_is_refused(fresh_agent: FreshAgent, case: str) -> None:
    spec = SIGNATURE_CASES[case]
    refusal = malformed(
        Mutation.SIGNATURE,
        spec.why,
        control=discovery_by(fresh_agent),
        alter=spec.alter(fresh_agent),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, spec.reason)
