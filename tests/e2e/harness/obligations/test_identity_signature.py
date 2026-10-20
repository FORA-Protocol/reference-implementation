"""Obligations IDENTITY-002 to -005 and IDENTITY-024: what a request signature binds.

Surface: the Exchange's DiscoverResources through a fresh agent's own SDK client.
Every negative is a declared mutation (``harness.malformed``): the agent's own
discovery is sent first and answered with a verified offer, then the same
discovery is sent signed or altered in exactly one way. A refusal is read through
the SDK's typed error, code ``unauthenticated`` with ``request_auth_failure``
SIGNATURE_INVALID, and the call raising is the absence of the side effect: no
offers came back.

* IDENTITY-002: the SDK covers ``authorization`` and sends it empty, and that
  request is answered. Removing the empty Authorization line, or the
  Signature-Agent line, after signing is refused.
* IDENTITY-003: a second field line beside a covered header changes its value, so
  a second Authorization line carrying a bearer, or a second Signature-Agent line
  naming another agent's directory, is refused.
* IDENTITY-004: the verifier selects the key whose thumbprint equals the keyid.
  An agent whose directory publishes two keys is answered when it signs with the
  second under the second's thumbprint; signing with the second under the first's
  thumbprint, or under a free label, is refused.
* IDENTITY-005: the key comes from the covered Signature-Agent, never from
  requester.domain. A discovery naming the agent in requester.domain, signed with
  the agent's key under another agent's directory, is refused.
* IDENTITY-024: a scope-gated term is presented on the scopes in the signed
  request. A scope added to requester.scopes after signing is refused, so the gated
  term is never presented on a scope the signature does not cover.
"""

from __future__ import annotations

import json
from collections.abc import Callable

import pytest
from fora_sdk.identity import generate_key

from ..after_signing import (
    Capture,
    append_field_line,
    drop_header,
    edit_body,
    second_signature_agent_line,
)
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..httpsig_signer import mislabelled_signer, signing_transport
from ..identity_requests import (
    SIGNATURE_INVALID,
    answered_with_offer,
    assert_auth_refusal,
    discovery_by,
    probe_query,
)
from ..malformed import Alteration, Mutation, malformed
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The scope exchange-a gates one demo term on. Stated in the signed request it
#: presents that term, which PUBLIC-004's test shows.
_SCOPE = "subscription:premium"


def _refused(agent: FreshAgent, why: str, alter: Alteration) -> None:
    """``agent``'s discovery is answered, and the same discovery altered by ``alter`` refused."""
    refusal = malformed(
        Mutation.SIGNATURE,
        why,
        control=discovery_by(agent),
        alter=alter,
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.IDENTITY_002)
def test_a_covered_header_sent_empty_is_accepted(fresh_agent: FreshAgent) -> None:
    """The SDK covers ``authorization`` and sends it empty; the discovery is answered."""
    capture = Capture()
    client = fresh_agent.exchange_client(EXCHANGE_A_DOMAIN, http=capture.client())
    answered_with_offer()(client.discover(probe_query()))
    sent = capture.header_values("signature-input")
    assert all('"authorization"' in value for _, value in sent), f"not covered: {sent}"
    assert all(value == "" for _, value in capture.header_values("authorization"))


@pytest.mark.parametrize("header", ["authorization", "signature-agent"])
@obligation(Obligation.IDENTITY_002, negative=True)
def test_a_covered_header_removed_after_signing_is_refused(
    fresh_agent: FreshAgent, header: str
) -> None:
    """The covered ``header`` is removed after signing: refused."""
    _refused(
        fresh_agent,
        f"the covered {header} line is removed",
        Alteration(after_signing=drop_header(header)),
    )


@obligation(Obligation.IDENTITY_003, negative=True)
def test_a_second_authorization_line_is_refused(fresh_agent: FreshAgent) -> None:
    """A second Authorization line carrying a bearer, beside the signed empty one: refused."""
    _refused(
        fresh_agent,
        "a second Authorization line carries a bearer token",
        Alteration(after_signing=append_field_line("authorization", "Bearer e2e-not-a-token")),
    )


@obligation(Obligation.IDENTITY_003, negative=True)
def test_a_second_signature_agent_line_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """A second Signature-Agent line giving the signed member another agent's directory: refused."""
    agent, other = fresh_agent_factory(), fresh_agent_factory()
    _refused(
        agent,
        "a second Signature-Agent line gives the signed member another agent's directory",
        Alteration(after_signing=second_signature_agent_line(f"https://{other.host}")),
    )


def _two_key_agent(factory: AgentFactory) -> tuple[FreshAgent, FreshAgent]:
    """An agent publishing two keys: the agent signing with the first, and with the second."""
    agent = factory()
    second, _thumbprint = generate_key()
    agent.publish([agent.ed25519, second])
    return agent, agent.signing_as(second)


@obligation(Obligation.IDENTITY_004)
def test_the_second_published_key_signs_under_its_own_thumbprint(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The directory publishes two keys; a discovery signed by the second is answered."""
    _agent, second = _two_key_agent(fresh_agent_factory)
    answered_with_offer()(second.exchange(EXCHANGE_A_DOMAIN).discover(probe_query()))


_KEYIDS: dict[str, Callable[[FreshAgent], str]] = {
    "another-published-key": lambda agent: agent.thumbprint,
    "free-label": lambda _agent: "agent-key-1",
}


@pytest.mark.parametrize("keyid", list(_KEYIDS))
@obligation(Obligation.IDENTITY_004, negative=True)
def test_a_keyid_that_is_not_the_signing_keys_thumbprint_is_refused(
    fresh_agent_factory: AgentFactory, keyid: str
) -> None:
    """Signed by the second key under the first key's thumbprint, or a label: refused."""
    agent, second = _two_key_agent(fresh_agent_factory)
    signer = mislabelled_signer(agent.host, second.ed25519, _KEYIDS[keyid](agent))
    _refused(
        agent, f"the keyid is {keyid}, not the signing key's thumbprint", Alteration(signer=signer)
    )


@obligation(Obligation.IDENTITY_005, negative=True)
def test_the_key_is_never_looked_up_in_requester_domain(
    fresh_agent_factory: AgentFactory,
) -> None:
    """requester.domain names the agent; Signature-Agent names a directory without its key."""
    agent, other = fresh_agent_factory(), fresh_agent_factory()
    _refused(
        agent,
        "Signature-Agent names another agent's directory, which does not publish the signing key",
        Alteration(signer=signing_transport(other.host, agent.ed25519)),
    )


def _with_scope(body: bytes) -> bytes:
    """The discovery body with ``_SCOPE`` added to requester.scopes."""
    message = json.loads(body)
    message["requester"]["scopes"] = [_SCOPE]
    return json.dumps(message, separators=(",", ":")).encode()


@obligation(Obligation.IDENTITY_024, negative=True)
def test_a_scope_added_after_signing_presents_nothing(fresh_agent: FreshAgent) -> None:
    """A scope added to the signed discovery after signing: refused, so no gated offer."""
    _refused(
        fresh_agent,
        "requester.scopes gains the gated term's scope after the request was signed",
        Alteration(after_signing=edit_body(_with_scope)),
    )
