"""E2E: the money path through the Broker, read off each Exchange's ledger.

Every purchase here is ``BrokerService.ExecuteTransaction`` through the SDK's
own ``BrokerClient``, with offers the same client resolved at the Broker and
verified against each issuing Exchange's published key. Every balance is read
through ``GetAccountStatus`` at the Exchange that holds it (``ledger``). Nothing
here reads a database, builds a request body or signs anything itself.

The contract under test is the protocol's, in ``fora.proto`` of the pinned
module ``github.com/FORA-Protocol/protocol`` (``BrokerService.ExecuteTransaction``,
``BrokerTransactionResponse``, ``ExchangeOutcome``, ``UpstreamRefusal``,
``TransactionResultItem``):

* items come back one per request item, in request order;
* ``exchanges`` holds one ``ExchangeOutcome`` per Exchange, in the order each
  first appears among the items, each listing its items' offer ids in order,
  with the thumbprint of the agent's key its delivery URLs are bound to;
* ``totals`` holds one entry per currency, summed exactly, never converted,
  over the charged items only, and is empty when nothing was charged;
* an Exchange's denial of one item rides on that item as ``denial_reason``,
  unchanged, and a provider that does not accept relayed purchases is one such
  denial;
* the Broker's own refusals are Connect errors and buy nothing anywhere.

Each negative is a declared mutation (``harness.malformed``): it runs its
control first, in the same test, with the same offers, so the alteration under
test is the only difference between the accepted call and the refused one. Each
negative also reads every affected balance after the control and after the
refusal, and asserts it did not move.

The resources are flat-priced and free ones (``harness.money_path``), so a test
here fails for its own reason and not for a metered-settlement one.

Buyer: each test's own agent (``fresh_agent``), funded at the Exchanges the test
buys at, so it holds that Exchange's welcome credit in USD and in EUR and nothing
any other test spent. Every balance claim is still about the change around one
call: the claim is what the call did, whatever the account held before it.
"""

from __future__ import annotations

from decimal import Decimal
from typing import Any

import pytest
from fora_sdk.core import VerifiedOffer
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import Client
from wire.models import BrokerTransactionResponse, TransactionResponse, TransactionResultItem

from .after_signing import edit_body
from .defects import RELAY_OPT_OUT_REFUSES_WHOLE_SUBREQUEST
from .exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_C_DOMAIN,
    EXCHANGE_D_DOMAIN,
    RELAY_REFUSING_EXCHANGES,
)
from .fresh_agent import AgentFactory, FreshAgent
from .httpsig_signer import unpublished_signer
from .ledger import Balances
from .malformed import Alteration, Mutation, Request, SeamHTTP, malformed, patch_body
from .money_path import (
    ARISTOTLE,
    ISSUER,
    LECTURE_NOTES,
    PAPER_SATELLITES,
    RAIN,
    SCI_FI_DOOR,
    THALES,
    assert_auth_refusal,
    assert_bought,
    assert_denied,
    assert_moved,
    assert_not_sold,
    broker_purchase,
    charged,
    only_item,
    resolve_offers,
    spent_per_exchange,
)
from .obligation_registry import Obligation, obligation
from .sdk_client import BeforeSign

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The Broker's own Web Bot Auth directory: BROKER_DOMAIN in docker-compose.e2e.yml,
#: the Signature-Agent the Broker signs every re-packaged sub-request with.
_BROKER_DIRECTORY = "broker:8082"

_RELAY_NOT_ACCEPTED = "DENIAL_REASON_RELAY_NOT_ACCEPTED"
_ACCOUNT_NOT_REGISTERED = "DENIAL_REASON_ACCOUNT_NOT_REGISTERED"


def _expected_totals(items: list[TransactionResultItem]) -> list[tuple[str, Decimal]]:
    """The totals the contract defines: one per currency, in first-charged order, exact."""
    totals: dict[str, Decimal] = {}
    for item in items:
        if item.refusal is not None or item.denial_reason is not None:
            continue
        currency, amount = charged(item)
        totals[currency] = totals.get(currency, Decimal(0)) + amount
    return list(totals.items())


def _totals(response: BrokerTransactionResponse) -> list[tuple[str, Decimal]]:
    """The answer's totals as (currency, amount) pairs, in the order given."""
    return [(str(c.currency), Decimal(str(c.amount))) for c in response.totals or []]


def _assert_combined_answer(
    response: BrokerTransactionResponse, uris: list[str], offers: list[VerifiedOffer]
) -> None:
    """Items in request order, one outcome per Exchange in first-seen order, exact totals."""
    items = response.items or []
    want_ids = [offer.offer.get("offer_id") for offer in offers]
    assert [item.offer_id for item in items] == want_ids, (
        f"items must come back one per request item, in request order: "
        f"{[item.offer_id for item in items]} != {want_ids}"
    )

    order = list(dict.fromkeys(ISSUER[uri] for uri in uris))
    outcomes = response.exchanges or []
    assert [o.exchange for o in outcomes] == order, (
        f"one ExchangeOutcome per Exchange, in first-seen order: "
        f"{[o.exchange for o in outcomes]} != {order}"
    )
    for outcome in outcomes:
        mine = [
            oid for uri, oid in zip(uris, want_ids, strict=True) if ISSUER[uri] == outcome.exchange
        ]
        assert list(outcome.offer_ids or []) == mine, (
            f"{outcome.exchange} lists offer ids {outcome.offer_ids}, want {mine} in request order"
        )

    assert _totals(response) == _expected_totals(items), (
        f"totals {_totals(response)} are not the per-currency sums of the charged items "
        f"{_expected_totals(items)}"
    )
    assert all(c.unit_cost is None for c in response.totals or []), response.totals


@obligation(
    Obligation.RELAY_001,
    Obligation.RELAY_002,
    Obligation.RELAY_003,
    Obligation.RELAY_019,
    Obligation.RELAY_023,
)
def test_a_batch_across_exchanges_charges_each_ledger_its_own_items(
    fresh_agent: FreshAgent,
) -> None:
    """One purchase over three Exchanges: ordered items, per-Exchange outcomes, exact totals.

    The batch interleaves Exchanges on purpose: exchange-c's two items are not
    adjacent, so the outcome for exchange-c must gather them in request order,
    and exchange-c, first seen, must lead the outcomes. Two currencies are
    charged, so the totals must keep them apart. Each Exchange's ledger then
    drops by exactly its own items' cost, in their currency, and by nothing else,
    which is each item's signed price: the Broker adds no fee. Every outcome names
    the thumbprint of the agent's own key, never the Broker's, because that is the
    key each delivery URL is bound to.
    """
    uris = [SCI_FI_DOOR, ARISTOTLE, PAPER_SATELLITES, RAIN]
    exchanges = list(dict.fromkeys(ISSUER[uri] for uri in uris))
    fresh_agent.fund(*exchanges)
    offers = resolve_offers(fresh_agent, uris)

    before = fresh_agent.balances(*exchanges)
    response = fresh_agent.broker_client().execute(offers)
    after = fresh_agent.balances(*exchanges)

    _assert_combined_answer(response, uris, offers)
    items = response.items or []
    for uri, offer, item in zip(uris, offers, items, strict=True):
        assert_bought(item, uri)
        signed = offer.offer.get("pricing") or {}
        assert charged(item) == (str(signed.get("currency")), Decimal(str(signed.get("rate")))), (
            f"{uri}: charged {charged(item)}, the signed price is {signed}"
        )
    for outcome in response.exchanges or []:
        assert outcome.agent_identity_hash == fresh_agent.thumbprint, (
            f"{outcome.exchange} bound its URLs to {outcome.agent_identity_hash!r}, want the "
            f"agent's key thumbprint {fresh_agent.thumbprint!r}"
        )
    currencies = {currency for currency, _ in _expected_totals(items)}
    assert currencies == {"USD", "EUR"}, (
        f"the batch was meant to charge two currencies: {currencies}"
    )

    assert_moved(before, after, spent_per_exchange(uris, items))


@obligation(Obligation.RELAY_021, negative=True)
@RELAY_OPT_OUT_REFUSES_WHOLE_SUBREQUEST.mark
def test_a_provider_that_refuses_relays_has_only_its_items_denied(
    fresh_agent: FreshAgent,
) -> None:
    """exchange-d's provider takes no relayed purchases: its item is denied in the body.

    Control first: the same exchange-d offer, bought DIRECTLY at exchange-d with
    the SDK's ``Client``, succeeds and is charged. So what denies it below is the
    provider's relay policy, not the offer, the buyer or its money.

    Then a mixed batch through the Broker. The protocol decides a relay opt-out
    per item, by each offer's provider, in the body: exchange-d's item carries
    ``denial_reason`` RELAY_NOT_ACCEPTED and no ``refusal``, exchange-c's item is
    bought, and exchange-d's ledger does not move.
    """
    assert EXCHANGE_D_DOMAIN in RELAY_REFUSING_EXCHANGES, "the seed no longer opts exchange-d out"
    fresh_agent.fund(EXCHANGE_D_DOMAIN, EXCHANGE_C_DOMAIN)
    uris = [LECTURE_NOTES, SCI_FI_DOOR]
    offers = resolve_offers(fresh_agent, uris)

    direct_before = fresh_agent.balances(EXCHANGE_D_DOMAIN)
    control = only_item(
        fresh_agent.exchange(EXCHANGE_D_DOMAIN).execute(offers[0]),
        "control: direct purchase at exchange-d",
    )
    assert_bought(control, "control: direct purchase at exchange-d")
    currency, cost = charged(control)
    assert cost > 0, f"the control item was meant to be priced: {control}"
    assert_moved(
        direct_before,
        fresh_agent.balances(EXCHANGE_D_DOMAIN),
        {EXCHANGE_D_DOMAIN: {currency: cost}},
    )

    before = fresh_agent.balances(EXCHANGE_D_DOMAIN, EXCHANGE_C_DOMAIN)
    response = fresh_agent.broker_client().execute(offers)
    after = fresh_agent.balances(EXCHANGE_D_DOMAIN, EXCHANGE_C_DOMAIN)

    denied, other = response.items or []
    assert_bought(other, "exchange-c's item beside the relay-refusing provider's")
    RELAY_OPT_OUT_REFUSES_WHOLE_SUBREQUEST.check(
        denied.refusal is None,
        f"exchange-d answered the sub-request with refusal {denied.refusal}",
    )
    assert_denied(denied, _RELAY_NOT_ACCEPTED, "exchange-d's item through the Broker")
    _assert_combined_answer(response, uris, offers)
    assert_moved(before, after, spent_per_exchange(uris, response.items or []))


def _requester_names(directory: str) -> BeforeSign:
    """A pre-signing alteration naming ``directory`` as the requester's domain.

    It runs inside the SDK after the SDK's own requester check passed, and the
    SDK then signs the altered bytes. So the request on the wire is validly
    signed by the buyer and claims another agent's directory: exactly the
    mismatch the Broker must refuse, with nothing else wrong.
    """

    def name_directory(body: dict[str, Any]) -> None:
        body["requester"]["domain"] = directory

    return patch_body(name_directory)


@obligation(Obligation.RELAY_005, negative=True)
def test_a_request_naming_another_agents_directory_is_refused(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The Broker refuses a purchase whose requester.domain is not the signer's directory.

    Control first: the same two offers, sent unaltered, are bought. Then the
    same purchase with requester.domain naming another real agent's directory,
    signed by the buyer over those exact bytes: the Broker answers
    request_auth_failure SIGNATURE_INVALID and sends no sub-request, so no ledger
    moves anywhere. The other agent is minted for this test too, so the directory
    it names is published and resolvable.
    """
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    uris = [SCI_FI_DOOR, THALES]
    exchanges = [EXCHANGE_C_DOMAIN, EXCHANGE_A_DOMAIN]
    buyer.fund(*exchanges)
    offers = resolve_offers(buyer, uris)
    after_control: dict[str, Balances] = {}

    def bought(response: BrokerTransactionResponse) -> None:
        for uri, item in zip(uris, response.items or [], strict=True):
            assert_bought(item, f"control: {uri}")
        after_control.update(buyer.balances(*exchanges))

    refusal = malformed(
        Mutation.IDENTITY,
        "requester.domain names another agent's directory",
        control=broker_purchase(buyer, offers),
        alter=Alteration(before_sign=_requester_names(other.host)),
        accepted=bought,
    )
    assert_auth_refusal(refusal)
    assert buyer.balances(*exchanges) == after_control, "a refused purchase moved money"


@obligation(Obligation.RELAY_004, negative=True)
def test_a_tampered_request_to_the_broker_is_refused(fresh_agent: FreshAgent) -> None:
    """A purchase whose body changed after the agent signed it is refused, and buys nothing.

    Control first: the same purchase through the same after-signing transport,
    with nothing altered, is bought. Then one space is appended to the signed
    body, which leaves it valid JSON and breaks only its signed digest.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    offers = resolve_offers(fresh_agent, [SCI_FI_DOOR])
    after_control: dict[str, Balances] = {}

    def bought(response: BrokerTransactionResponse) -> None:
        assert_bought(only_item(response, "control"), "control")
        after_control.update(fresh_agent.balances(EXCHANGE_C_DOMAIN))

    refusal = malformed(
        Mutation.SIGNATURE,
        "the body differs from the signed content digest",
        control=broker_purchase(fresh_agent, offers),
        alter=Alteration(after_signing=edit_body(lambda body: body + b" ")),
        accepted=bought,
    )
    assert_auth_refusal(refusal)
    assert fresh_agent.balances(EXCHANGE_C_DOMAIN) == after_control, (
        "a refused purchase moved money"
    )


def test_a_free_label_requester_id_still_buys_as_the_signer(fresh_agent: FreshAgent) -> None:
    """``requester.id`` is a free label: a non-host id buys, and the signer is charged.

    The buyer's identity is the directory its signature resolves from, not the
    id it writes in the request. So a purchase labelled ``urn:parse:probe`` and
    signed by this test's agent is bought, and that agent's own balance pays.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    offers = resolve_offers(fresh_agent, [SCI_FI_DOOR])

    before = fresh_agent.balances(EXCHANGE_C_DOMAIN)
    labelled = fresh_agent.broker_client(agent_id="urn:parse:probe")
    item = only_item(labelled.execute(offers), "purchase under a free-label requester id")
    after = fresh_agent.balances(EXCHANGE_C_DOMAIN)

    assert_bought(item, "purchase under a free-label requester id")
    currency, cost = charged(item)
    assert_moved(before, after, {EXCHANGE_C_DOMAIN: {currency: cost}})


def test_an_unrelated_key_presenting_the_brokers_directory_is_refused(
    fresh_agent: FreshAgent,
) -> None:
    """An Exchange refuses a purchase signed by a key the Broker's directory does not publish.

    Control first: the buyer's own direct purchase of the offer at exchange-c is
    bought. Then the same purchase, sent directly to exchange-c and signed by a
    fresh key that names the Broker's directory as its Signature-Agent: the key
    resolves from nowhere the Broker publishes, so exchange-c refuses it with
    request_auth_failure and no transaction moves the buyer's money.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    offer = resolve_offers(fresh_agent, [SCI_FI_DOOR])[0]
    after_control: dict[str, Balances] = {}

    def bought(response: TransactionResponse) -> None:
        assert_bought(only_item(response, "control"), "control: the buyer's own direct purchase")
        after_control.update(fresh_agent.balances(EXCHANGE_C_DOMAIN))

    def exchange_c(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> Client:
        return fresh_agent.exchange_client(
            EXCHANGE_C_DOMAIN, signer=signer, before_sign=before_sign, http=http
        )

    refusal = malformed(
        Mutation.SIGNATURE,
        "signed by a key the Broker's directory does not publish",
        control=Request(client=exchange_c, verb=lambda c: c.execute, message=offer),
        alter=Alteration(signer=unpublished_signer(_BROKER_DIRECTORY)),
        accepted=bought,
    )
    assert_auth_refusal(refusal)
    assert fresh_agent.balances(EXCHANGE_C_DOMAIN) == after_control, (
        "a refused purchase moved money"
    )


@obligation(Obligation.RELAY_011, negative=True)
@obligation(Obligation.RELAY_003)
def test_an_item_an_exchange_denies_is_passed_through_and_left_out_of_the_totals(
    fresh_agent: FreshAgent,
) -> None:
    """A per-item denial reaches the agent unchanged and adds nothing to the totals.

    The agent holds accounts at exchange-a and exchange-c and none at
    exchange-b, so exchange-b answers its item with its own denial,
    ACCOUNT_NOT_REGISTERED, inside a sub-request it otherwise accepts. The
    denied item is first and is priced in USD. The next item is charged in EUR,
    and two more in USD. So the totals must be EUR first, then USD, and the USD
    total must hold only the two charged items. If the Broker counted the denied
    item, USD would lead the totals and carry its price as well.

    Control: once the agent opens its account at exchange-b, the same resource
    is bought through the Broker and its price is in the totals. So the account
    is the only reason it was left out before.
    """
    uris = [PAPER_SATELLITES, ARISTOTLE, SCI_FI_DOOR, RAIN]
    fresh_agent.fund(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN)
    offers = resolve_offers(fresh_agent, uris)

    before = fresh_agent.balances(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN)
    response = fresh_agent.broker_client().execute(offers)
    after = fresh_agent.balances(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN)

    _assert_combined_answer(response, uris, offers)
    denied, *bought = response.items or []
    assert_denied(
        denied, _ACCOUNT_NOT_REGISTERED, "exchange-b's item, from an agent with no account"
    )
    for uri, item in zip(uris[1:], bought, strict=True):
        assert_bought(item, uri)
    eur = charged(bought[0])[1]
    usd = charged(bought[1])[1] + charged(bought[2])[1]
    assert _totals(response) == [("EUR", eur), ("USD", usd)], (
        f"totals {_totals(response)}: want EUR {eur} then USD {usd}, the charged items "
        f"only, in the order each currency is first charged"
    )
    assert_moved(before, after, spent_per_exchange(uris, response.items or []))

    fresh_agent.fund(EXCHANGE_B_DOMAIN)
    control = fresh_agent.broker.execute(resolve_offers(fresh_agent, [PAPER_SATELLITES]))
    item = only_item(control, "control: exchange-b's item once the agent has an account there")
    assert_bought(item, "control: exchange-b's item once the agent has an account there")
    assert _totals(control) == [charged(item)], control.totals


@obligation(Obligation.RELAY_003)
def test_a_purchase_whose_every_item_is_denied_has_no_totals(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Nothing charged means no totals at all, not a zero in some currency.

    The buyer has no account anywhere, so exchange-c denies its one item. The
    answer carries the denial and an empty totals list. Control: the same buyer
    then opens its account and buys the same resource, and the totals hold that
    item's price. The denial charged nothing even later: once both have opened
    their accounts, the buyer holds what a second new agent holds, less only the
    control purchase.
    """
    buyer, untouched = fresh_agent_factory(), fresh_agent_factory()
    uris = [SCI_FI_DOOR]
    purchases = buyer.broker_client()

    refused = purchases.execute(resolve_offers(buyer, uris))
    item = only_item(refused, "unfunded purchase")
    assert_denied(item, _ACCOUNT_NOT_REGISTERED, "unfunded purchase")
    assert_not_sold(item, "unfunded purchase")
    assert not refused.totals, f"nothing was charged and the totals say {refused.totals}"

    buyer.fund(EXCHANGE_C_DOMAIN)
    untouched.fund(EXCHANGE_C_DOMAIN)
    control = purchases.execute(resolve_offers(buyer, uris))
    bought = only_item(control, "control: the same purchase once funded")
    assert_bought(bought, "control: the same purchase once funded")
    currency, cost = charged(bought)
    assert _totals(control) == [(currency, cost)], control.totals
    assert_moved(
        untouched.balances(EXCHANGE_C_DOMAIN),
        buyer.balances(EXCHANGE_C_DOMAIN),
        {EXCHANGE_C_DOMAIN: {currency: cost}},
    )
