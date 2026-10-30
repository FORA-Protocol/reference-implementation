"""Obligations SUBSCRIPTION-001 to SUBSCRIPTION-006: a subscription-gated term is reached only
through a verified delegation chain.

The pinned protocol module ``github.com/FORA-Protocol/protocol`` gates a licensing
term that carries a ``subscription:`` scope on ``Requester.delegation``: a
holder-bound JWT chain the Exchange verifies against the root keys of the
subscription record that owns the scope, bound to the key the request proved.
``Requester.scopes`` never satisfies it (``test_04_failure_03_scope_not_covered.py``).

Driven against the demo ``parmenides`` resource, which exchange-a sells under one
FREE term gated on ``subscription:premium``. The suite makes exchange-a's seat
trust the test's own issuer key for that scope through the e2e control service
(:func:`harness.e2e_controls.subscription_record`), mints the chains with
:mod:`harness.delegation`, and drives every leg through the SDK:

* direct discovery with a verified chain presents the gated offer; plaintext
  ``Delegation`` mirrors wider than the chain change nothing;
* the Broker's Resolve with the same chain hides the term: the Exchange cannot
  bind a chain to the agent's key on a query the Broker signs (interim rule);
* a direct purchase of the gated offer succeeds, and so does the same purchase
  relayed through the Broker, bound to the agent's acceptance key;
* a chain bound to another key, an expired one and one rooted in an untrusted
  key are each denied per item, ``DENIAL_REASON_DELEGATION_INVALID``, nothing sold;
* a relayed batch whose gated item lost its acceptance is answered with that item
  denied ``DELEGATION_INVALID`` and its siblings bought, and a public item without
  an acceptance is refused by the Broker before anything is sent;
* a push whose term carries two subscription scopes, or a wildcard one, is refused
  at ingest.
"""

from __future__ import annotations

import json
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey, Ed25519PublicKey
from fora_sdk.b64 import b64url_decode
from fora_sdk.client import CallError
from fora_sdk.identity import generate_key
from fora_sdk.core import DiscoveryResult, VerifiedOffer
from wire.models import Requester, TransactionResultItem

from ..broker_purchase import (
    one_group_per_uri,
    only_verified_offer,
    resolve_request,
    resource_query,
)
from ..catalog_calls import (
    HOME_SEAT,
    MALFORMED_ENTRY,
    assert_typed_refusal,
    contributor,
    fresh_entry,
    refusal,
)
from ..catalog_push import PRICING_MODEL_FREE, license_term
from ..constants import requester
from ..defects import CATALOG_REFUSAL_UNTYPED
from ..delegation import Chain, delegation_wire, mint, public_key_b64url
from ..e2e_catalog import DEMO_PHILOSOPHY_DOMAIN
from ..e2e_controls import subscription_record
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..malformed import patch_body
from ..money_path import assert_bought, assert_denied, only_item
from ..obligation_registry import Obligation, obligation
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SCOPE = "subscription:premium"
_ISSUER = "issuer.example"
_GATED = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/parmenides.txt"
_PUBLIC = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/socrates.txt"
DELEGATION_INVALID = "DENIAL_REASON_DELEGATION_INVALID"


def _fresh_key() -> Ed25519PrivateKey:
    """A key no directory publishes, minted by the SDK: an issuer's root, or a stranger's."""
    key, _thumbprint = generate_key()
    return key


#: The key the Broker signs its relayed purchases with, as the stack mounts it.
_BROKER_RELAY_KEY = Path(__file__).resolve().parents[1] / "fixtures" / "broker_relay_key.json"


def _broker_public_key() -> Ed25519PublicKey:
    """The Broker's own transport key: a chain bound to it must grant nothing."""
    raw = b64url_decode(json.loads(_BROKER_RELAY_KEY.read_text())["public_key"])
    return Ed25519PublicKey.from_public_bytes(raw)


class Subscriber:
    """The test's agent holding a grant for ``_SCOPE``: exchange-a's seat trusts the
    suite's issuer key for the scope, and the chains below are rooted in it."""

    def __init__(self, agent: FreshAgent) -> None:
        self.agent = agent
        self.root = _fresh_key()
        self.trust(self.root)

    def trust(self, *keys: Ed25519PrivateKey, issuer: str = _ISSUER) -> None:
        """Make the seat trust exactly ``keys``, under ``issuer``, for the scope."""
        subscription_record(EXCHANGE_A_DOMAIN, _SCOPE, issuer, [public_key_b64url(k) for k in keys])

    def chain(self, **changes: Any) -> Chain:
        """The happy-path chain, with ``changes`` applied."""
        fields: dict[str, Any] = {
            "keys": (self.root,),
            "holder": self.agent.ed25519.public_key(),
            "root_issuer": _ISSUER,
            "audience": EXCHANGE_A_DOMAIN,
            "scope": _SCOPE,
        }
        fields.update(changes)
        return Chain(**fields)

    def token(self, **changes: Any) -> str:
        return mint(self.chain(**changes))

    def who(self, token: str | None, **mirrors: Any) -> Requester:
        """The agent's requester carrying ``token`` (none when ``None``) and the mirrors."""
        fields = requester(self.agent.agent_id)
        if token is not None:
            fields["delegation"] = delegation_wire(token, **mirrors)
        return Requester.model_validate(fields)

    def discover(self, token: str | None, **mirrors: Any) -> DiscoveryResult:
        query = resource_query(EXCHANGE_A_DOMAIN, [_GATED], requester=self.who(token, **mirrors))
        return self.agent.exchange(EXCHANGE_A_DOMAIN).discover(query)

    def gated_offer(self) -> VerifiedOffer:
        """A fresh signed offer for the gated term, which only the verified chain reaches."""
        return self.gated_offer_with(self.token())

    def gated_offer_with(self, token: str) -> VerifiedOffer:
        """:meth:`gated_offer` reached with ``token`` instead of the happy-path chain."""
        group = one_group_per_uri(self.discover(token), [_GATED], "gated discovery")[_GATED]
        return only_verified_offer(group, "gated discovery")

    def with_delegation(self, token: str | None, *, strip: tuple[int, ...] = ()) -> Any:
        """A pre-signing edit: put ``token`` on the purchase's requester and drop the
        acceptance of the items at ``strip``. The acceptances cover the requester's id
        and domain only, and the request acceptance covers the offers, so both still
        verify over the edited body."""

        def patch(body: dict[str, Any]) -> None:
            if token is not None:
                body["requester"]["delegation"] = delegation_wire(token)
            for index in strip:
                body["items"][index].pop("agent_acceptance", None)

        return patch_body(patch)

    def buy_direct(
        self, offers: list[VerifiedOffer], token: str | None
    ) -> list[TransactionResultItem]:
        client = self.agent.exchange_client(
            EXCHANGE_A_DOMAIN, before_sign=self.with_delegation(token)
        )
        return list(client.execute(offers).items or [])

    def buy_relayed(
        self, offers: list[VerifiedOffer], token: str | None, *, strip: tuple[int, ...] = ()
    ) -> list[TransactionResultItem]:
        client = self.agent.broker_client(before_sign=self.with_delegation(token, strip=strip))
        return list(client.execute(offers).items or [])


@pytest.fixture
def subscriber(fresh_agent: FreshAgent) -> Subscriber:
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    return Subscriber(fresh_agent)


def _hidden(result: DiscoveryResult, leg: str) -> None:
    group = one_group_per_uri(result, [_GATED], leg)[_GATED]
    assert not group.result.verified and not group.result.rejected, (
        f"{leg}: the gated term was presented: {group.result}"
    )
    assert group.absence_reason is None, (
        f"{leg}: absence_reason = {group.absence_reason!r}, want none"
    )


@obligation(Obligation.SUBSCRIPTION_001)
def test_a_verified_chain_presents_the_gated_term_and_mirrors_change_nothing(
    subscriber: Subscriber,
) -> None:
    """Direct discovery with the chain: the offer. Wider plaintext mirrors: the same offer, nothing more."""
    offer = subscriber.gated_offer()
    assert offer.offer.get("terms", [{}])[0].get("scopes") == [_SCOPE]

    far = (datetime.now(timezone.utc) + timedelta(days=3650)).strftime("%Y-%m-%dT%H:%M:%SZ")
    mirrored = subscriber.discover(
        subscriber.token(), scopes=["*"], expires_at=far, principal_domain="attacker.example"
    )
    group = one_group_per_uri(mirrored, [_GATED], "wide mirrors")[_GATED]
    only_verified_offer(group, "wide mirrors: the chain still decides, the term is still offered")

    _hidden(subscriber.discover(None), "no chain")


@obligation(Obligation.SUBSCRIPTION_002)
def test_a_broker_originated_query_hides_the_gated_term(subscriber: Subscriber) -> None:
    """Resolve through the Broker with a valid chain: hidden, no reason (interim rule)."""
    result = subscriber.agent.broker.resolve(
        resolve_request([_GATED, _PUBLIC], requester=subscriber.who(subscriber.token()))
    )
    groups = one_group_per_uri(result, [_GATED, _PUBLIC], "Broker resolve with a chain")
    assert not groups[_GATED].result.verified, "the Broker-originated query reached the gated term"
    assert groups[_GATED].absence_reason is None
    only_verified_offer(groups[_PUBLIC], "Broker resolve, the public resource")


@obligation(Obligation.SUBSCRIPTION_003)
def test_the_gated_term_is_bought_directly_and_through_the_broker(subscriber: Subscriber) -> None:
    """A FREE gated term: bought at exchange-a with the chain, and bought relayed by the Broker."""
    token = subscriber.token()
    direct = only_item(
        _response(subscriber.buy_direct([subscriber.gated_offer()], token)), "direct"
    )
    assert_bought(direct, "direct purchase of the gated term")

    relayed = only_item(
        _response(subscriber.buy_relayed([subscriber.gated_offer()], token)), "relayed"
    )
    assert_bought(relayed, "relayed purchase of the gated term")


@obligation(Obligation.SUBSCRIPTION_004)
@pytest.mark.parametrize(
    "change",
    [
        pytest.param({"holder": _fresh_key().public_key()}, id="holder-mismatch"),
        pytest.param({"expires": datetime.now(timezone.utc) - timedelta(hours=2)}, id="expired"),
        pytest.param({"keys": (_fresh_key(),)}, id="untrusted-root"),
    ],
)
def test_a_chain_that_does_not_verify_denies_the_item(
    subscriber: Subscriber, change: dict[str, Any]
) -> None:
    """Each failure class is the same per-item DELEGATION_INVALID; nothing is sold."""
    offer = subscriber.gated_offer()
    item = only_item(
        _response(subscriber.buy_direct([offer], subscriber.token(**change))), "denied"
    )
    assert_denied(item, DELEGATION_INVALID, "direct purchase with a chain that does not verify")

    item = only_item(_response(subscriber.buy_direct([subscriber.gated_offer()], None)), "no chain")
    assert_denied(item, DELEGATION_INVALID, "direct purchase without a chain")


@obligation(Obligation.SUBSCRIPTION_004)
def test_a_chain_bound_to_the_brokers_key_is_refused_on_the_relayed_path(
    subscriber: Subscriber,
) -> None:
    """Relayed purchase, valid acceptance from the agent, chain bound to the Broker's own
    transport key: DELEGATION_INVALID. The holder is the acceptance key, never the relay's."""
    offer = subscriber.gated_offer()
    token = subscriber.token(holder=_broker_public_key())
    item = only_item(_response(subscriber.buy_relayed([offer], token)), "broker-bound")
    assert_denied(
        item, DELEGATION_INVALID, "relayed purchase with a chain bound to the Broker's key"
    )


@obligation(Obligation.SUBSCRIPTION_004)
def test_rotating_the_issuer_withdraws_the_old_root(subscriber: Subscriber) -> None:
    """The seat's record is written again with a new issuer and key: a chain rooted in the
    old key is denied, and a chain from the new issuer buys. The second write replaces the
    stored issuer identity as well as the keys."""
    offer = subscriber.gated_offer()
    new_root = _fresh_key()
    subscriber.trust(new_root, issuer="rotated.example")
    item = only_item(_response(subscriber.buy_direct([offer], subscriber.token())), "old root")
    assert_denied(
        item, DELEGATION_INVALID, "direct purchase with a chain rooted in the withdrawn key"
    )

    subscriber.root = new_root
    rotated = subscriber.token(root_issuer="rotated.example")
    bought = only_item(
        _response(subscriber.buy_direct([subscriber.gated_offer_with(rotated)], rotated)),
        "new root",
    )
    assert_bought(bought, "direct purchase with a chain from the rotated issuer")


@obligation(Obligation.SUBSCRIPTION_005)
def test_a_relayed_batch_with_a_stripped_acceptance_denies_that_item_only(
    subscriber: Subscriber,
) -> None:
    """Gated item without acceptance: DELEGATION_INVALID. Its siblings: bought. A public item
    without acceptance: the Broker refuses the purchase."""
    token = subscriber.token()
    stripped, kept = subscriber.gated_offer(), subscriber.gated_offer()
    public = only_verified_offer(
        one_group_per_uri(
            subscriber.agent.exchange(EXCHANGE_A_DOMAIN).discover(
                resource_query(EXCHANGE_A_DOMAIN, [_PUBLIC])
            ),
            [_PUBLIC],
            "public",
        )[_PUBLIC],
        "public",
    )
    items = subscriber.buy_relayed([stripped, kept, public], token, strip=(0,))
    by_offer = {str(item.offer_id): item for item in items}
    assert_denied(
        by_offer[str(stripped.offer.get("offer_id"))], DELEGATION_INVALID, "stripped gated item"
    )
    assert_bought(by_offer[str(kept.offer.get("offer_id"))], "sibling gated item")
    assert_bought(by_offer[str(public.offer.get("offer_id"))], "sibling public item")

    retried = only_item(_response(subscriber.buy_relayed([stripped], token)), "retry")
    assert_bought(retried, "the agent's retry with the acceptance present")

    with pytest.raises(CallError) as refused:
        subscriber.buy_relayed([public, subscriber.gated_offer()], token, strip=(0,))
    assert refused.value.code == "invalid_argument", f"Broker answered {refused.value}"
    assert "agent_acceptance" in str(refused.value)


@obligation(Obligation.SUBSCRIPTION_006, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
@pytest.mark.parametrize(
    "scopes",
    [
        pytest.param(["subscription:a:x", "subscription:a:y"], id="two-subscription-scopes"),
        pytest.param(["subscription:a:*"], id="wildcard-subscription-scope"),
    ],
)
def test_a_term_with_malformed_subscription_scopes_is_refused_at_ingest(
    compose_stack: StackURLs, scopes: list[str]
) -> None:
    """One concrete subscription scope per term, or none; the push is refused and names the entry."""
    term = license_term(model=PRICING_MODEL_FREE, scopes=scopes)
    entry = fresh_entry(terms=(term,))
    error = refusal(
        contributor(compose_stack).push(HOME_SEAT, [entry]), "a malformed subscription scope"
    )
    # The refusal names the entry and the rule in its message whatever the typed
    # detail carries; the typed detail is the known untyped-refusal defect.
    assert entry.uri in str(error.detail) and "subscription scope" in str(error.detail), (
        f"the refusal does not name the entry and the subscription-scope rule: {error.detail}"
    )
    assert_typed_refusal(error, MALFORMED_ENTRY, [entry.path])


class _Items:
    """A one-item answer in the shape :func:`only_item` reads."""

    def __init__(self, items: list[TransactionResultItem]) -> None:
        self.items = items


def _response(items: list[TransactionResultItem]) -> Any:
    return _Items(items)
