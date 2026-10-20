"""Obligation IDENTITY-013: the Broker refuses a Resolve whose signature it cannot accept.

At Resolve the Broker is the request's final recipient, and it verifies the
agent's RFC 9421 signature before it fans the discovery out. A refusal carries one
of the three collapsed reasons the protocol defines for every RPC: MISSING (sign
the request), INVALID (fix the signature) or STALE (sign again now).

The cases are the shared table in ``harness.signature_cases``, the same cases an
Exchange's DiscoverResources is driven through.

Surface: the Broker's Resolve through a fresh agent's own SDK client. Each
negative is a declared mutation (``harness.malformed``): the agent's Resolve is
sent first and answered with a verified offer, then the same Resolve is sent with
its signature broken in one way. The refusal is read through the SDK's typed
error, and the call raising is the absence of the side effect: no offers came
back. Whether the Broker sent a fan-out query for the refused Resolve is not
observed here; that needs a capture of the Broker's outbound requests.
"""

from __future__ import annotations

import pytest

from ..defects import BROKER_UNSIGNED_RESOLVE_UNTYPED, Defect
from ..fresh_agent import FreshAgent
from ..identity_requests import answered_with_offer, assert_auth_refusal, auth_reason, resolve_by
from ..malformed import Mutation, malformed
from ..obligation_registry import Obligation, obligation
from ..signature_cases import SIGNATURE_CASES

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The product defect that breaks a case at the Broker, for the cases one breaks.
_DEFECTS: dict[str, Defect] = {"unsigned": BROKER_UNSIGNED_RESOLVE_UNTYPED}


@obligation(Obligation.IDENTITY_013, negative=True)
@pytest.mark.parametrize(
    "case",
    [
        pytest.param(name, id=name, marks=[_DEFECTS[name].marker] if name in _DEFECTS else [])
        for name in SIGNATURE_CASES
    ],
)
def test_a_resolve_with_a_broken_signature_is_refused(fresh_agent: FreshAgent, case: str) -> None:
    spec = SIGNATURE_CASES[case]
    refusal = malformed(
        Mutation.SIGNATURE,
        spec.why,
        control=resolve_by(fresh_agent),
        alter=spec.alter(fresh_agent),
        accepted=answered_with_offer(),
    )
    assert refusal.code == "unauthenticated", f"refused as {refusal.code!r}: {refusal}"
    defect = _DEFECTS.get(case)
    if defect is not None:
        defect.check(auth_reason(refusal) == spec.reason, f"the refusal carries: {refusal}")
    assert_auth_refusal(refusal, spec.reason)
