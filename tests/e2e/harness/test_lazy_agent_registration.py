"""Lazy agent registration (ADR-009 D2) through the real Agent -> Broker -> Exchange chain.

Each test signs as an agent of its own (``fresh_agent``): a new key, published in
a Web Bot Auth directory under a new host in the agent zone, that no Exchange
has seen and that never calls Register. Whatever an Exchange holds for that
agent afterwards was put there by this test's own calls, on every run and on any
stack.

An Exchange records an agent's identity when a call it serves resolves the
caller: a purchase (direct, or relayed by the Broker with the agent as the
requester), a usage report, a Register or an account read. It fetches the
agent's directory then, and records the identity row it publishes. A discovery
verifies the signature against the same directory and records nothing, and a
Broker Resolve reaches an Exchange only as the Broker's own discovery.

So a relayed purchase is the agent's first call that an Exchange records, and
its answer is the proof that it did. An Exchange decides
``ACCOUNT_NOT_REGISTERED`` only after it has resolved the requester's row,
creating it when the agent is new, and the Broker never writes that reason
itself. The account read that follows answers not_found: an identity held with
no account. That read is consistent with the purchase having recorded the agent,
but it proves nothing on its own, because it would record a new identity itself.
"""

from __future__ import annotations

from contextlib import closing

import pytest
from fora_sdk.client import CallError

from .broker_purchase import resolve_and_buy, resolve_offer
from .conftest import StackURLs
from .exchanges import EXCHANGE_A_DOMAIN
from .fresh_agent import FreshAgent, new_host
from .httpsig_signer import unpublished_signer
from .identity_requests import SIGNATURE_INVALID, assert_auth_refusal
from .sdk_client import no_offer_verifier, sdk_broker_client, sdk_exchange_client
from .seed import DEMO_PHILOSOPHY_DOMAIN
from .seed_accounts import has_account

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

# epicurus: PER_UNIT 0.0001/characters USD, sold by exchange-a — a PAID resource.
# An identity with no account cannot buy it (ADR-021: the identity row and the
# billing account are different things, and Register opens the account), so the
# purchase is denied in the body.
_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"

_ACCOUNT_NOT_REGISTERED = "DENIAL_REASON_ACCOUNT_NOT_REGISTERED"


def test_lazy_registration_fires_but_paid_needs_register(fresh_agent: FreshAgent) -> None:
    """A relayed purchase records a never-seen agent, but a PAID item needs Register.

    The agent resolves epicurus at the Broker and buys the winner through the
    Broker, signed with its own key. Before the purchase, nothing the agent sends
    reaches exchange-a except the Broker's discovery. exchange-a answers the item
    in the body with ACCOUNT_NOT_REGISTERED, which it decides only after
    resolving the requester's row, so the purchase is what recorded the agent.
    The agent's own account read at exchange-a then answers not_found: the
    identity is held, and there is no account.
    """
    item = resolve_and_buy(fresh_agent, _RESOURCE_URI)

    assert item.denial_reason is not None and item.denial_reason.value == _ACCOUNT_NOT_REGISTERED, (
        f"a paid buy by an agent with no account must be denied {_ACCOUNT_NOT_REGISTERED} "
        f"(Register opens the account first): {item}"
    )
    assert not item.retrieval_endpoint, f"a denied paid item must carry no signed URL: {item}"
    assert not has_account(fresh_agent.exchange(EXCHANGE_A_DOMAIN), EXCHANGE_A_DOMAIN), (
        "exchange-a reports an account for an agent that never registered"
    )


def test_unresolvable_agent_refused_before_execute(
    compose_stack: StackURLs, fresh_agent: FreshAgent
) -> None:
    """Negative control: an agent whose key no directory publishes is refused and not recorded.

    Control first: the test's own agent, which differs only in having a
    directory, resolves the same URI. Then a ghost sends the same resolve: a valid
    key, signing under a new host in the agent zone that no directory publishes.
    Directories are the only path a verifier learns a key by, so the Broker
    cannot verify the signature and refuses the call unauthenticated with
    request_auth_failure SIGNATURE_INVALID. The ghost's own account read at
    exchange-a is refused unauthenticated too: the Exchange cannot resolve the
    key either, so it records no identity for it.
    """
    resolve_offer(fresh_agent.broker, _RESOURCE_URI)

    host = new_host()
    ghost = unpublished_signer(host)
    broker = sdk_broker_client(compose_stack, agent_id=host, signer=ghost)
    with closing(broker), pytest.raises(CallError) as refused:
        resolve_offer(broker, _RESOURCE_URI)
    assert_auth_refusal(refused.value, SIGNATURE_INVALID)

    account = sdk_exchange_client(
        compose_stack, EXCHANGE_A_DOMAIN, no_offer_verifier(), agent_id=host, signer=ghost
    )
    with closing(account), pytest.raises(CallError) as unknown:
        has_account(account, EXCHANGE_A_DOMAIN)
    assert unknown.value.code == "unauthenticated", (
        f"exchange-a must refuse the ghost's account read unauthenticated (no key to "
        f"resolve, so no identity to hold); it answered {unknown.value.code!r}: {unknown.value}"
    )
