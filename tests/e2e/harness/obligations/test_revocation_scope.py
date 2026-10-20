"""Obligations KEYS-008, -009, -015, -016 and -017: revocation is scoped to the signer.

The relayed purchase signed by a revoked key also proves the revoked-key half of
KEYS-007: an acceptance is verified only against keys currently valid in the
requester's directory. Its expired-key half is in ``test_key_windows.py``.

The protocol author's decision: each party's revocation list covers only its own
keys. The Broker's list covers the Broker's keys, an agent's list covers that
agent's keys, and the Broker can never invalidate an agent's signature. Each
signature is verified against its own signer's directory. A relayed purchase
carries the agent's acceptances and the Broker's request signature, and the
failure of either refuses the whole purchase; a direct request never depends on
the Broker's directory.

Surface: the Exchange's DiscoverResources and the Broker's Resolve and
ExecuteTransaction, each through the agent's own SDK client. Every refusal is read
through the SDK's typed error; every "not bought" through the typed purchase answer
and the buyer's balance, read back through GetAccountStatus. No leg reads a
datastore.

The cases, in the owner's numbering:

1. The Broker's list names an agent's key that the agent's own directory still
   publishes unrevoked: the agent's signature verifies (KEYS-015).
2. A direct discovery while the Broker's directory is unreachable is answered
   (KEYS-016).
3. A purchase through the Broker while the Broker's own list names the Broker's
   signing key is not bought, and moves no money (KEYS-017).
4. An agent revokes one of its own keys: a direct discovery and a Resolve signed by
   it are refused, and a purchase through the Broker signed by it is not bought
   (KEYS-008, KEYS-009).

How the state is arranged. An agent's revocation list is served by the stack's
agent-directory service on the agent's own host (``FreshAgent.publish`` and
``publish_revocations``), published before the agent's first signed call, because
a verifier reads a host's list when it first fetches the host's directory. The
Broker's list is the file the Broker serves (``harness.broker_revocations``),
withdrawn with a newer empty list on exit. The Broker is made unreachable by
stopping its container for the length of one block (``_compose.stopped_service``)
and starting it again. Cases 2 and 3 disturb every test running at the same moment;
the e2e runner runs one test at a time, and each case waits, before it returns,
until every Exchange verifies the Broker's fan-out again.

Cases 1 and 2 cannot wait for a refusal, because the correct outcome is no
refusal. Each instead asks again for a bounded time that covers the moments the
defect needs: the Exchange re-reads the Broker's list every second in this stack,
and forces a fetch of the Broker's directory for an unknown key at most once every
five seconds. A refusal at any point is the defect; an answer every time for the
whole window is the obligation holding.
"""

from __future__ import annotations

import os

import pytest
from fora_sdk.client import CallError
from fora_sdk.identity import generate_key

from .._compose import stopped_service
from ..broker_revocations import broker_list_naming, broker_thumbprints, served_thumbprints
from ..conftest import StackURLs
from ..exchanges import EXCHANGE_C_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent, publish_revocations
from ..identity_requests import (
    PRICED_URI,
    ROUTED_URIS,
    SIGNATURE_INVALID,
    answered_with_offer,
    assert_auth_refusal,
    bought,
    buy_through_broker,
    discover_outcome,
    discovery_by,
    every_exchange_verifies_the_broker,
    offer_for,
    resolve_by,
    signer_for,
)
from ..malformed import Alteration, Mutation, malformed
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: How long cases 1 and 2 keep asking: past several of the Exchange's one-second
#: polls of the Broker's list, and past two of its five-second windows for a
#: forced directory fetch.
_STEADY_WINDOW_S = 15.0
#: How long a revocation, or its withdrawal, may take to reach every Exchange.
_PROPAGATION_S = 30.0
#: How long the Broker may take to answer again after its container restarts.
_RESTART_S = 90.0


def _revoked_second_key(factory: AgentFactory) -> tuple[FreshAgent, FreshAgent]:
    """An agent whose directory publishes two keys and whose own list revokes the second.

    Returns the agent signing with its first key, and the same agent signing with
    the second. Both are published before the agent's first signed call.
    """
    agent = factory()
    second, second_thumbprint = generate_key()
    agent.publish([agent.ed25519, second], revocations=True)
    publish_revocations(agent.stack, agent.host, [second_thumbprint])
    return agent, agent.signing_as(second)


def _await_broker_verified(agent: FreshAgent, why: str) -> None:
    """Wait until every Exchange in ROUTED_URIS verifies the Broker's fan-out again."""
    poll_until(
        lambda: every_exchange_verifies_the_broker(agent),
        _RESTART_S,
        failure=lambda: (
            f"{why}: the Broker's Resolve did not bring back a verified offer from each of "
            f"{sorted(ROUTED_URIS)} within {_RESTART_S:.0f}s, so later tests would run "
            f"against a Broker the Exchanges do not verify"
        ),
    )


@obligation(Obligation.KEYS_008, negative=True)
def test_a_discovery_signed_by_a_key_its_agent_revoked_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The agent's own list names its second key: a discovery signed by it is refused.

    The control is the same discovery signed by the agent's first key, from the
    same directory, which names the same revocation list.
    """
    agent, revoked = _revoked_second_key(fresh_agent_factory)
    refusal = malformed(
        Mutation.SIGNATURE,
        "signed by a key the agent's own revocation list names, which its directory still publishes",
        control=discovery_by(agent),
        alter=Alteration(signer=signer_for(agent, revoked.ed25519)),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.KEYS_009, negative=True)
def test_a_resolve_signed_by_a_key_its_agent_revoked_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The agent's own list names its second key: the Broker refuses a Resolve signed by it."""
    agent, revoked = _revoked_second_key(fresh_agent_factory)
    refusal = malformed(
        Mutation.SIGNATURE,
        "signed by a key the agent's own revocation list names, which its directory still publishes",
        control=resolve_by(agent),
        alter=Alteration(signer=signer_for(agent, revoked.ed25519)),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.KEYS_008, Obligation.KEYS_007, negative=True)
def test_a_relayed_purchase_signed_by_a_key_its_agent_revoked_is_not_bought(
    fresh_agent_factory: AgentFactory,
) -> None:
    """A purchase through the Broker, signed and accepted by the revoked key, buys nothing.

    The control is the same offer bought through the Broker by the agent's first key.
    The revoked view signs the request and every acceptance with the second key, so
    whichever party checks first, the Broker or the Exchange, the purchase carries no
    signature a verifier may accept. Either a refused call or an answer with nothing
    bought is the whole request refused; the balance must not move.
    """
    agent, revoked = _revoked_second_key(fresh_agent_factory)
    agent.fund(EXCHANGE_C_DOMAIN)
    offer = offer_for(agent, PRICED_URI)
    before = agent.balances(EXCHANGE_C_DOMAIN)
    assert bought(buy_through_broker(agent, offer)), (
        "control: the first key's purchase was not bought"
    )
    after_control = agent.balances(EXCHANGE_C_DOMAIN)
    assert after_control != before, "control: the purchase moved no money"

    answer = buy_through_broker(revoked, offer)

    assert not bought(answer), f"a purchase signed by a revoked key was bought: {answer}"
    if isinstance(answer, CallError):
        assert answer.code == "unauthenticated", f"refused as {answer.code!r}: {answer}"
    assert agent.balances(EXCHANGE_C_DOMAIN) == after_control, "a refused purchase moved money"


@obligation(Obligation.KEYS_015)
def test_the_brokers_list_cannot_revoke_an_agent_key(
    compose_stack: StackURLs, fresh_agent: FreshAgent
) -> None:
    """The Broker's list names the agent's key; the agent's discovery is still answered.

    The agent's own directory publishes the key and names no revocation list. The
    control is the agent's discovery before the Broker's list names it. Once the
    Broker serves the list, the discovery is asked again for the whole steady window.
    """
    assert discover_outcome(fresh_agent) is None, "control: the agent's discovery was refused"
    with broker_list_naming(compose_stack.broker, [fresh_agent.thumbprint]):
        assert fresh_agent.thumbprint in served_thumbprints(compose_stack.broker)
        refused: list[CallError] = []

        def refused_once() -> bool:
            refusal = discover_outcome(fresh_agent)
            if refusal is not None:
                refused.append(refusal)
            return refusal is not None

        try:
            poll_until(refused_once, _STEADY_WINDOW_S, failure=lambda: "answered throughout")
        except TimeoutError:
            return
    raise AssertionError(f"refused while the Broker's list named the agent's key: {refused[-1]}")


@obligation(Obligation.KEYS_016)
def test_a_direct_request_does_not_need_the_brokers_directory(
    compose_stack: StackURLs, fresh_agent_factory: AgentFactory
) -> None:
    """With the Broker stopped, direct discoveries by new agents are answered.

    The control is one agent's discovery while the Broker runs. Then the Broker's
    container is stopped, and a new agent, with a key no verifier has seen, sends a
    direct discovery once a second for the whole steady window. Each new key is one
    the Exchange would look up in the Broker's directory if it still did.
    """
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip("stopping a service needs the runner's Docker socket (FORA_E2E_IN_NETWORK=1)")
    witness = fresh_agent_factory()
    assert discover_outcome(witness) is None, "control: a discovery was refused with the Broker up"
    refused: list[CallError] = []

    def refused_once() -> bool:
        refusal = discover_outcome(fresh_agent_factory())
        if refusal is not None:
            refused.append(refusal)
        return refusal is not None

    try:
        with stopped_service("broker"):
            poll_until(refused_once, _STEADY_WINDOW_S, failure=lambda: "answered throughout")
    except TimeoutError:
        pass
    finally:
        _await_broker_verified(witness, "after the Broker restarted")
    assert not refused, f"a direct discovery was refused while the Broker was down: {refused[:1]}"


@obligation(Obligation.KEYS_017, negative=True)
def test_a_relayed_purchase_is_refused_while_the_broker_key_is_revoked(
    compose_stack: StackURLs, fresh_agent: FreshAgent
) -> None:
    """The Broker's own list names its keys: a purchase through it buys nothing.

    The control is the same offer bought through the Broker before the revocation.
    The offer is resolved before the Broker's list names its key, because the
    Broker's fan-out is signed by the same key. After the Broker serves the list the
    purchase is retried until the Exchange holds the new list: a purchase bought
    before then is the old list still in force, and the first one not bought must
    move no money. Then the revocation is withdrawn, and the test waits until every
    Exchange verifies the Broker again.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    offer = offer_for(fresh_agent, PRICED_URI)
    assert bought(buy_through_broker(fresh_agent, offer)), "control: the purchase was not bought"
    broker_keys = broker_thumbprints(compose_stack.broker)
    outcome: dict[str, object] = {}

    def refused_without_charge() -> bool:
        before = fresh_agent.balances(EXCHANGE_C_DOMAIN)
        answer = buy_through_broker(fresh_agent, offer)
        if bought(answer):
            return False
        outcome["answer"] = answer
        outcome["moved"] = fresh_agent.balances(EXCHANGE_C_DOMAIN) != before
        return True

    try:
        with broker_list_naming(compose_stack.broker, broker_keys):
            poll_until(
                refused_without_charge,
                _PROPAGATION_S,
                failure=lambda: "every purchase was bought while the Broker's key was revoked",
            )
    finally:
        _await_broker_verified(fresh_agent, "after the Broker's key was un-revoked")
    assert outcome["moved"] is False, f"the refused purchase moved money: {outcome['answer']}"
