"""Obligations IDENTITY-010 to IDENTITY-012: a request forwarded unchanged, one signature per party.

"Multi-hop forwarding" and "Whose list revokes whose key" on the authentication page
of the pinned module ``github.com/FORA-Protocol/protocol``, and
``ResourceQuery`` and ``WellKnownManifest.max_intermediary_hops`` in ``fora.proto``:
a party that forwards a request unchanged appends its own signature, under its own
label and its own ``Signature-Agent`` member, and MAY cover the signature before it.
The Exchange resolves each signature's key in the directory that signature's own
covered member names, never in a member the signature does not cover, verifies
every signature, refuses the request if any one of them fails, and counts them
against the ``max_intermediary_hops`` its manifest publishes.

Every request is the agent's discovery of a priced philosophy resource at
exchange-a, signed by the agent's own SDK client. Each forwarding party is another
fresh agent, with a key directory of its own that exchange-a was never configured
with, and appends its signature through the SDK's append mode
(``SigningTransport(append_only=True, cover_previous=True)``, built in
``harness.forwarding``). The negatives alter that control in one declared way
through ``harness.malformed``: a different signer for the agent's own signature,
or one more signature appended after signing.

* IDENTITY-010: forwarded through one party, the discovery is answered. Refused
  ``unauthenticated`` SIGNATURE_INVALID when the agent's own signature is made by a
  key its directory does not publish, though the forwarding party covers it with a
  valid signature, and when a further party appends a signature made by a key its
  own directory does not publish.
* IDENTITY-011: with as many signatures as exchange-a publishes in
  ``max_intermediary_hops``, the agent's and one per forwarding party, the discovery
  is answered; with one more it is refused ``resource_exhausted``. The field's
  comment counts every signature, the agent's included, and names that code and
  no typed reason.
* IDENTITY-012: the forwarding party's key is found in the party's own directory,
  which exchange-a never heard of, so the forwarded discovery is answered. A
  signature appended with the agent's own key, under a member naming a third
  party's directory and covering nothing before it, is refused SIGNATURE_INVALID:
  the agent's directory publishes that key, but the signature does not cover the
  agent's member, so a verifier must not look the key up there.
"""

from __future__ import annotations

import pytest
from fora_sdk.client import CallError

from ..exchanges import EXCHANGE_A_DOMAIN, exchange_url
from ..forwarding import forwarded_discovery, forwarder, hop, send, unpublished_forwarder
from ..fresh_agent import AgentFactory
from ..httpsig_signer import unpublished_signer
from ..identity_requests import (
    SIGNATURE_INVALID,
    answered_with_offer,
    assert_auth_refusal,
    discovery_by,
)
from ..malformed import Alteration, Mutation, malformed, malformed_outcome
from ..obligation_registry import Obligation, obligation
from ..protocol_documents import manifest_at

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


@obligation(Obligation.IDENTITY_010, Obligation.IDENTITY_012)
def test_a_discovery_forwarded_unchanged_by_an_unconfigured_party_is_answered(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The agent signs, a party exchange-a never heard of covers it: answered with an offer."""
    agent, party = fresh_agent_factory(), fresh_agent_factory()
    answered_with_offer()(send(forwarded_discovery(agent, [forwarder(party)])))


@obligation(Obligation.IDENTITY_010, negative=True)
def test_a_forwarded_discovery_whose_agent_signature_fails_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The agent's signature by an unpublished key, validly covered: refused SIGNATURE_INVALID."""
    agent, party = fresh_agent_factory(), fresh_agent_factory()
    refusal = malformed(
        Mutation.SIGNATURE,
        "the agent's signature is made by a key its directory does not publish, and the "
        "forwarding party covers it with a valid signature",
        control=forwarded_discovery(agent, [forwarder(party)]),
        alter=Alteration(signer=unpublished_signer(agent.host)),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.IDENTITY_010, negative=True)
def test_a_forwarded_discovery_with_a_failing_further_signature_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """A further party's signature by a key its directory does not publish: refused."""
    agent, party, further = (fresh_agent_factory() for _ in range(3))
    refusal = malformed(
        Mutation.SIGNATURE,
        "a further party appends a signature made by a key its own directory does not publish",
        control=forwarded_discovery(agent, [forwarder(party)]),
        alter=Alteration(after_signing=hop(unpublished_forwarder(further))),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


def _published_cap(agent_factory: AgentFactory) -> int:
    """The max_intermediary_hops exchange-a's fora.json publishes."""
    stack = agent_factory.stack
    manifest = manifest_at(exchange_url(stack, EXCHANGE_A_DOMAIN)).message
    cap = manifest.max_intermediary_hops
    assert cap is not None and cap >= 1, (
        f"exchange-a publishes max_intermediary_hops {cap!r}; the test needs a cap of at "
        f"least one signature"
    )
    return cap


@obligation(Obligation.IDENTITY_011)
def test_a_discovery_carrying_the_published_number_of_signatures_is_answered(
    fresh_agent_factory: AgentFactory,
) -> None:
    """As many signatures as max_intermediary_hops, each covering the last: answered."""
    cap = _published_cap(fresh_agent_factory)
    agent = fresh_agent_factory()
    hops = [forwarder(fresh_agent_factory()) for _ in range(cap - 1)]
    answered_with_offer()(send(forwarded_discovery(agent, hops)))


@obligation(Obligation.IDENTITY_011, negative=True)
def test_a_discovery_carrying_one_signature_over_the_published_cap_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """One signature more than max_intermediary_hops: refused resource_exhausted."""
    cap = _published_cap(fresh_agent_factory)
    agent = fresh_agent_factory()
    hops = [forwarder(fresh_agent_factory()) for _ in range(cap - 1)]
    outcome = malformed_outcome(
        Mutation.SIGNATURE,
        f"one forwarding party more than the {cap} signatures exchange-a tolerates",
        control=forwarded_discovery(agent, hops),
        alter=Alteration(after_signing=hop(forwarder(fresh_agent_factory()))),
        accepted=answered_with_offer(),
    )
    assert isinstance(outcome, CallError), (
        f"a discovery carrying {cap + 1} signatures was answered: {outcome!r}"
    )
    assert outcome.code == "resource_exhausted", f"refused as {outcome.code!r}: {outcome}"


@obligation(Obligation.IDENTITY_012, negative=True)
def test_a_signature_is_never_verified_against_a_member_it_does_not_cover(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The agent's key under a third party's member, covering nothing else: refused."""
    agent, third = fresh_agent_factory(), fresh_agent_factory()
    refusal = malformed(
        Mutation.SIGNATURE,
        "a signature made by the agent's key names a third party's directory, which does not "
        "publish that key, and does not cover the agent's member",
        control=discovery_by(agent),
        alter=Alteration(
            after_signing=hop(forwarder(third, using=agent.ed25519, cover_previous=False))
        ),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)
