"""The ways a signed request is broken, and the reason each refusal must carry.

Every RPC that verifies an agent's RFC 9421 signature refuses a broken one with
the same three collapsed reasons (``request_auth_failure``): MISSING (sign the
request), INVALID (fix the signature) and STALE (sign again now). The cases are
the same at every verifier, so they are written once here, and each suite that
drives one verifier (an Exchange's DiscoverResources, the Broker's Resolve) says
only which control request it alters and what each case proves there.

Each case alters the control in exactly one declared way, through the seams of
``harness.malformed``: the after-signing transport, or a different signer or
signing window for the same agent. The agent is the test's own fresh agent, so
the case names its directory and its key.
"""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass

from .after_signing import drop_header, edit_body, strip_signature
from .fresh_agent import FreshAgent
from .httpsig_signer import unpublished_signer
from .identity_requests import SIGNATURE_INVALID, SIGNATURE_MISSING, SIGNATURE_STALE, signer_for
from .malformed import Alteration

_HOUR_S = 3600


@dataclass(frozen=True)
class SignatureCase:
    """One way of breaking a signed request, and the reason the verifier must give."""

    reason: str
    why: str
    alter: Callable[[FreshAgent], Alteration]


SIGNATURE_CASES: dict[str, SignatureCase] = {
    "unsigned": SignatureCase(
        SIGNATURE_MISSING,
        "no Signature or Signature-Input header",
        lambda _agent: Alteration(after_signing=strip_signature),
    ),
    "unknown-key": SignatureCase(
        SIGNATURE_INVALID,
        "signed by a key the agent's directory does not publish",
        lambda agent: Alteration(signer=unpublished_signer(agent.host)),
    ),
    "tampered-body": SignatureCase(
        SIGNATURE_INVALID,
        "the body differs from the signed content digest",
        lambda _agent: Alteration(
            after_signing=edit_body(lambda body: body.replace(b"socrates", b"epicurus"))
        ),
    ),
    "covered-header-removed": SignatureCase(
        SIGNATURE_INVALID,
        "a header the signature covers is removed",
        lambda _agent: Alteration(after_signing=drop_header("content-digest")),
    ),
    "expired": SignatureCase(
        SIGNATURE_STALE,
        "the signature was created an hour ago, and its window closed before it was sent",
        lambda agent: Alteration(
            signer=signer_for(agent, agent.ed25519, created_offset_s=-_HOUR_S)
        ),
    ),
    "created-in-future": SignatureCase(
        SIGNATURE_STALE,
        "the signature is created an hour from now",
        lambda agent: Alteration(signer=signer_for(agent, agent.ed25519, created_offset_s=_HOUR_S)),
    ),
    "replayed": SignatureCase(
        SIGNATURE_STALE,
        "the same signed bytes are sent twice",
        lambda _agent: Alteration(sends=2),
    ),
}
