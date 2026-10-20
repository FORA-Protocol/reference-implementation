"""Obligations KEYS-004, -005, -006 and -007: key validity windows and rotation.

The contract is the authentication page of the pinned module,
``github.com/FORA-Protocol/protocol/website/src/content/docs/protocol/authentication.mdx``,
and the key directory of its ``fora.proto``:

* "The window is **half-open** ``[not_before, not_after)`` — a key is valid at
  ``not_before`` and invalid **at and after** ``not_after``." (KEYS-004);
* rotation: "Publish the new key alongside the old key (both valid)", and a key
  removed from the directory stops verifying once the verifier has read it again
  (KEYS-005);
* "consumers poll on a 300s cadence (±10% jitter) and sync-fetch on an unknown
  thumbprint", so a newly published key verifies without waiting for the cache
  (KEYS-006);
* "The envelope carries no ``keyid``, so the verifier tries the currently-valid
  Ed25519 keys of that directory — rotation overlap keeps that set small."
  (KEYS-007).

THE STATE. Each test owns a fresh agent (ADR-028 D7). Its key directory is served
by the stack's agent-directory service on the agent's own host, and the test
republishes it (``FreshAgent.publish``) to add a key, remove one, or give one a
window that opens in the future or has already closed. No product control is
involved: the directory is the agent's own, as it is in production.

THE SURFACE. A discovery at exchange-a, and a Resolve and a purchase at the Broker,
each through the agent's own SDK client. A refusal is read as the SDK's typed
error; a purchase that is not bought is read from the typed answer and the buyer's
balance, read back through GetAccountStatus.

THE RE-READ. A verifier caches an agent's directory, for an hour by default in the
SDK resolver both services use, and fetches it again at once when a request names
a key it has not seen. KEYS-005 therefore makes the Exchange re-read the directory
the way the protocol provides: after republishing without the old key, the agent
signs once with a key that only the new directory publishes.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

import pytest
from fora_sdk.client import CallError
from fora_sdk.identity import generate_key

from ..defects import AGENT_PINNED_TO_ONE_KEY
from ..exchanges import EXCHANGE_C_DOMAIN
from ..fresh_agent import FreshAgent, KeyWindow, thumbprint_of
from ..identity_requests import (
    PRICED_URI,
    SIGNATURE_INVALID,
    answered_with_offer,
    assert_auth_refusal,
    bought,
    buy_through_broker,
    discover_outcome,
    discovery_by,
    offer_for,
    signer_for,
)
from ..malformed import Alteration, Mutation, malformed
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: How long the Exchange may take to re-read a directory after it first meets a key
#: that only the new directory publishes. The SDK resolver forces at most one such
#: fetch per host every five seconds.
_REREAD_S = 30.0


def _outside_window(case: str) -> KeyWindow:
    """A window that does not hold now: one opening in an hour, or one closed an hour ago."""
    now = datetime.now(UTC)
    if case == "not-yet-valid":
        return KeyWindow(not_before=now + timedelta(hours=1), not_after=now + timedelta(days=1))
    return KeyWindow(not_before=now - timedelta(days=1), not_after=now - timedelta(hours=1))


@obligation(Obligation.KEYS_004, negative=True)
@pytest.mark.parametrize("case", ["not-yet-valid", "expired"])
def test_a_discovery_signed_by_a_key_outside_its_window_is_refused(
    fresh_agent: FreshAgent, case: str
) -> None:
    """A key the directory publishes, but whose window does not hold now, verifies nothing.

    The agent's directory publishes its own key, valid now, and a second key whose
    window opens an hour from now or closed an hour ago. The control is the
    discovery signed by the first key. The same discovery signed by the second is
    refused unauthenticated, SIGNATURE_INVALID, and returns no offer.
    """
    outside, _ = generate_key()
    fresh_agent.publish(
        [fresh_agent.ed25519, outside], windows={thumbprint_of(outside): _outside_window(case)}
    )
    refusal = malformed(
        Mutation.SIGNATURE,
        f"signed by a published key whose validity window is {case}",
        control=discovery_by(fresh_agent),
        alter=Alteration(signer=signer_for(fresh_agent, outside)),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.KEYS_005, negative=True)
def test_both_keys_verify_during_an_overlap_and_a_removed_key_then_does_not(
    fresh_agent: FreshAgent,
) -> None:
    """Old and new both verify while both are published; once the old is removed, it does not.

    The directory publishes the new key beside the old one, and a discovery signed
    by each is answered. Then the directory is republished with the new key and a
    third, without the old one. The agent signs with the third key until the
    Exchange answers, which shows it has read the new directory. The control is the
    discovery signed by the new key; the same discovery signed by the old key is
    refused unauthenticated, SIGNATURE_INVALID.
    """
    agent = fresh_agent
    new, _ = generate_key()
    newest, _ = generate_key()
    agent.publish([new, agent.ed25519])
    for label, key in (("old", agent.ed25519), ("new", new)):
        refusal = discover_outcome(agent, signer=signer_for(agent, key))
        assert refusal is None, f"during the overlap the {label} key was refused: {refusal}"

    agent.publish([newest, new])
    last: list[CallError | None] = []

    def reread() -> bool:
        last.append(discover_outcome(agent, signer=signer_for(agent, newest)))
        return last[-1] is None

    poll_until(
        reread,
        _REREAD_S,
        failure=lambda: (
            f"the Exchange never verified a key only the new directory publishes: {last[-1]}"
        ),
    )
    refusal = malformed(
        Mutation.SIGNATURE,
        "signed by the old key, which the directory no longer publishes",
        control=discovery_by(agent.signing_as(new)),
        alter=Alteration(signer=signer_for(agent, agent.ed25519)),
        accepted=answered_with_offer(),
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.KEYS_006)
def test_a_key_published_after_first_contact_verifies_at_once(fresh_agent: FreshAgent) -> None:
    """The Exchange has cached the agent's directory; a key added to it verifies on first use.

    The first discovery makes the Exchange fetch and cache the directory, which
    then publishes one key. The agent adds a second key and signs the next
    discovery with it. That one discovery must be answered: the Exchange meets a
    key it has not seen and fetches the directory again, rather than refusing until
    its cached copy expires an hour later.
    """
    assert discover_outcome(fresh_agent) is None, "control: the first discovery was refused"
    added, _ = generate_key()
    fresh_agent.publish([added, fresh_agent.ed25519])
    refusal = discover_outcome(fresh_agent, signer=signer_for(fresh_agent, added))
    assert refusal is None, f"a key published after first contact was refused: {refusal}"


@obligation(Obligation.KEYS_007)
@AGENT_PINNED_TO_ONE_KEY.mark
def test_a_relayed_purchase_accepted_by_the_old_key_during_an_overlap_is_bought(
    fresh_agent: FreshAgent,
) -> None:
    """While the directory publishes both keys, acceptances signed by the old one buy.

    The directory publishes a newer key first and the agent's first key after it.
    The agent opens its account at the seller with the newer key. Then it signs the
    Broker request and every acceptance with the old key. The Exchange verifies the
    acceptances against the keys currently valid in that directory, which include
    the old key, so the item is bought and the buyer's balance moves.
    """
    new, _ = generate_key()
    fresh_agent.publish([new, fresh_agent.ed25519])
    current = fresh_agent.signing_as(new)
    current.fund(EXCHANGE_C_DOMAIN)
    offer = offer_for(current, PRICED_URI)
    before = current.balances(EXCHANGE_C_DOMAIN)

    answer = buy_through_broker(fresh_agent, offer)

    AGENT_PINNED_TO_ONE_KEY.check(
        bought(answer), f"a purchase accepted by the old key during the overlap: {answer}"
    )
    assert current.balances(EXCHANGE_C_DOMAIN) != before, "the purchase moved no money"


@obligation(Obligation.KEYS_007, negative=True)
def test_a_relayed_purchase_accepted_by_a_key_past_its_window_is_not_bought(
    fresh_agent: FreshAgent,
) -> None:
    """Acceptances by a key whose not_after has passed buy nothing and move no money.

    The directory publishes the agent's key, valid now, and a second key whose
    window closed an hour ago. The control is the offer bought through the Broker
    with the first key. Then the expired key signs the request and every
    acceptance. Whichever party checks first, the Broker or the Exchange, no
    currently-valid key verifies them: either the call is refused unauthenticated
    or nothing is bought, and the balance must not move. The revoked-key half of
    this obligation is the purchase in ``test_revocation_scope.py``.
    """
    expired, _ = generate_key()
    fresh_agent.publish(
        [fresh_agent.ed25519, expired], windows={thumbprint_of(expired): _outside_window("expired")}
    )
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    offer = offer_for(fresh_agent, PRICED_URI)
    before = fresh_agent.balances(EXCHANGE_C_DOMAIN)
    assert bought(buy_through_broker(fresh_agent, offer)), (
        "control: the valid key's purchase failed"
    )
    after_control = fresh_agent.balances(EXCHANGE_C_DOMAIN)
    assert after_control != before, "control: the purchase moved no money"

    answer = buy_through_broker(fresh_agent.signing_as(expired), offer)

    assert not bought(answer), f"a purchase accepted by an expired key was bought: {answer}"
    if isinstance(answer, CallError):
        assert answer.code == "unauthenticated", f"refused as {answer.code!r}: {answer}"
    assert fresh_agent.balances(EXCHANGE_C_DOMAIN) == after_control, (
        "a refused purchase moved money"
    )
