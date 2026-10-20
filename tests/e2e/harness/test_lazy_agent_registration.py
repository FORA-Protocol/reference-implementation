"""Lazy agent registration (ADR-009 D2) through the real Agent -> Broker -> Exchange chain.

A fresh agent — one the seed never introduces to any Exchange, serving its own
Web Bot Auth key directory on the ``lazy-agent-e2e`` network alias (== its
keyID/domain), and never registered for an account — resolves an offer at the Broker and buys it
through the Broker, with the SDK's own ``BrokerClient``. The Broker verifies the
agent's request signature against the key in that directory; the Exchange the
Broker re-packages the purchase to resolves the agent's acceptance key from the
same directory, and LAZILY records the agent's identity. The host also serves a
ROLE_AGENT fora.json, but nothing on either path reads it — the overlay carries
no keys.

Every other buyer in the suite is learned the same way, at its first signed
call, which is its Register. This is the only test whose agent is first seen on
a purchase relayed by the Broker, and the only one that never registers.

Whether an Exchange holds an agent's identity is read back through the agent's
own GetAccountStatus at that Exchange. An identity the Exchange holds, with no
account, is answered not_found ("not registered for paid content yet"); a key the
Exchange cannot resolve is refused unauthenticated before any account is read.
"""

from __future__ import annotations

from contextlib import closing
from pathlib import Path

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError

from .broker_purchase import only_item, resolve_offer
from .conftest import StackURLs
from .exchanges import EXCHANGE_A_DOMAIN, exchange_url
from .sdk_client import sdk_account_client, sdk_broker_client
from .seed import DEMO_PHILOSOPHY_DOMAIN, SeededFixture

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_FIXTURES = Path(__file__).resolve().parent / "fixtures"

# Fresh agent: published directory on the `lazy-agent-e2e` alias, and never
# registered for an account (the seed's Register calls leave it out on purpose).
LAZY_AGENT_ID = "lazy-agent-e2e"
LAZY_KEY_PATH = _FIXTURES / "agent_lazy_e2e_key.json"

# Ghost agent: a valid signing key but NO well-known host, so its key can be
# resolved nowhere — the negative control for the per-agent resolver.
GHOST_AGENT_ID = "ghost-agent-e2e"
GHOST_KEY_PATH = _FIXTURES / "agent_ghost_e2e_key.json"

# epicurus: PER_UNIT 0.0001/characters USD — a PAID resource. A lazily registered
# agent has an identity row but NO billing account (D1 / ADR-021), so the
# paid purchase is denied ACCOUNT_NOT_REGISTERED while the lazy-registration side
# effect still fires. Buying paid content requires an explicit Register first.
_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"

_ACCOUNT_NOT_REGISTERED = "DENIAL_REASON_ACCOUNT_NOT_REGISTERED"


def _account_read_refusal(stack: StackURLs, *, agent_id: str, key_path: Path) -> CallError:
    """The refusal exchange-a answers the agent's own GetAccountStatus with.

    Neither agent here holds an account at exchange-a, so the read is always
    refused; what the refusal says is the observation. ``not_found`` means the
    Exchange resolved the agent's identity and found no account for it.
    ``unauthenticated`` means it could not resolve the agent's key at all.
    """
    client = sdk_account_client(
        exchange_url(stack, EXCHANGE_A_DOMAIN), agent_id=agent_id, key_path=key_path
    )
    with closing(client), pytest.raises(CallError) as refused:
        client.get_account_status({"ver": ProtocolVersion, "exchange": EXCHANGE_A_DOMAIN})
    return refused.value


def test_lazy_registration_fires_but_paid_needs_register(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests the demo catalog
) -> None:
    """A never-seen agent is lazily registered (identity), but a PAID buy needs Register.

    Round-trip: resolve at the Broker, signed with the agent's own key, which the
    Broker resolves from http://lazy-agent-e2e/.well-known/http-message-signatures-directory;
    then buy the winning offer through the Broker. Lazy registration (ADR-009 D2)
    records the agent's IDENTITY from the verified directory — enough for free
    crawling, but NOT a billing account. Per ADR-021 D1, an agent with no
    billing_ref cannot buy paid content, so the PAID epicurus item is denied
    in-body with DENIAL_REASON_ACCOUNT_NOT_REGISTERED (the agent must call
    Register first).

    The denial is itself the evidence that the Exchange accepted the agent's
    identity from its directory: an agent whose key resolves nowhere is refused
    unauthenticated before any per-item decision (see the ghost test below).

    Then the agent reads its own account at exchange-a: the Exchange answers
    not_found, so it holds the agent's identity and no account. That leg is the
    agent's own signed call, through the Exchange's public RPC. It cannot show
    that the identity was recorded BY the purchase rather than by the read
    itself, because an unknown agent's first signed call of any kind is what
    records it, and the agent is a fixed identity that a stack the suite has
    already run against holds from the first run.
    """
    buyer = sdk_broker_client(compose_stack, agent_id=LAZY_AGENT_ID, key_path=LAZY_KEY_PATH)
    with closing(buyer) as client:
        offer = resolve_offer(client, _RESOURCE_URI)
        item = only_item(client.execute([offer]), "the lazy agent's paid purchase")

    # A lazily registered identity is not a registered billing account, so the
    # paid purchase is refused in-body.
    assert item.denial_reason is not None and item.denial_reason.value == _ACCOUNT_NOT_REGISTERED, (
        f"a paid buy by an identity-only lazy agent must be denied {_ACCOUNT_NOT_REGISTERED} "
        f"(Register mints the account first): {item}"
    )
    assert not item.retrieval_endpoint, f"a denied paid item must carry no signed URL: {item}"

    # The Exchange holds the agent's identity and, the paid charge having been
    # refused, still no account for it.
    held = _account_read_refusal(compose_stack, agent_id=LAZY_AGENT_ID, key_path=LAZY_KEY_PATH)
    assert held.code == "not_found", (
        f"exchange-a must answer the lazily registered agent's account read not_found "
        f"(identity held, no account); it answered {held.code!r}: {held}"
    )


def test_unresolvable_agent_refused_before_execute(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed ingests the demo catalog
) -> None:
    """Negative control: a fresh agent with NO well-known host is refused; not registered.

    Control first: the lazy agent, which differs only in having a directory,
    resolves the same URI. Then the ghost agent, which holds a valid signing key
    that no directory publishes, sends the same resolve. Directories are the ONLY
    path a verifier learns a key by, so the Broker cannot verify the signature and
    refuses the call with request_auth_failure. The refusal lands on the first
    signed leg, so the ghost never reaches an Exchange and lazy registration never
    fires. The ghost's own account read at exchange-a is refused unauthenticated:
    the Exchange cannot resolve its key either, so it holds no identity for it.
    """
    control = sdk_broker_client(compose_stack, agent_id=LAZY_AGENT_ID, key_path=LAZY_KEY_PATH)
    with closing(control) as client:
        resolve_offer(client, _RESOURCE_URI)

    ghost = sdk_broker_client(compose_stack, agent_id=GHOST_AGENT_ID, key_path=GHOST_KEY_PATH)
    with closing(ghost) as client, pytest.raises(CallError) as refused:
        resolve_offer(client, _RESOURCE_URI)

    error = refused.value
    assert error.code == "unauthenticated", f"refused as {error.code!r}: {error}"
    block = error.detail.request_auth_failure if error.detail is not None else None
    assert block is not None, f"the refusal carries no request_auth_failure reason: {error}"
    assert block.reason.value == "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID", block
    unknown = _account_read_refusal(compose_stack, agent_id=GHOST_AGENT_ID, key_path=GHOST_KEY_PATH)
    assert unknown.code == "unauthenticated", (
        f"exchange-a must refuse the ghost agent's account read unauthenticated (no key "
        f"to resolve, so no identity to hold); it answered {unknown.code!r}: {unknown}"
    )
