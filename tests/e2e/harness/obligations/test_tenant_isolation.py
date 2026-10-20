"""Obligations TENANT-001 to TENANT-003: what one tenant can read or change of another.

From the multi-tenant page of the pinned protocol module
``github.com/FORA-Protocol/protocol``: no tenant can access another tenant's
catalog, keys or transaction records; offer signatures are per tenant; and the
catalog calls carry the push's authentication, which refuses a tenant the caller
may not act for with ``permission_denied``.

TENANT-001 to TENANT-003 take two seats on two Exchanges: every test makes a
call at one seat's Exchange that names the other seat's data. TENANT-004 and
TENANT-005 take two seats on ONE Exchange: exchange-a also holds a second seat,
which it does not sell as, and a call at exchange-a names it. The two publishers
on one seat, TENANT-006 to TENANT-009, are in ``test_tenant_publishers.py``.

Every call is an SDK verb as the test's own agent, as the operator's catalog
contributor or as the operator on an admin listener; what the call left is read
back through the seat that owns the data. The admin-plane case for two
Exchanges, a policy naming another Exchange's seat, is in
``test_admin_plane_rules.py`` beside the rule it shares with ADMIN-007.
"""

from __future__ import annotations

from contextlib import closing

import pytest
from fora_sdk.client import CallError
from wire.models import UsageReport

from ..admin_plane import reporting_policy, require_admin_plane, tenant_fee_rate
from ..catalog_calls import (
    HOME_EXCHANGE,
    HOME_SEAT,
    TENANT_MISMATCH,
    accepted,
    answer,
    assert_push_naming_tenant_refused,
    contributor,
    fresh_entry,
    rejection,
    stored,
)
from ..broker_purchase import discover_group, resource_query
from ..defects import (
    CATALOG_TENANT_MISMATCH_UNTYPED,
    REFRESH_CATALOG_UNIMPLEMENTED,
    REMOVE_RESOURCES_UNIMPLEMENTED,
    USAGE_REPORT_REFUSAL_UNTYPED,
)
from ..e2e_catalog import SEAT_OF_SFX
from ..exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_D_DOMAIN,
    SECOND_SEAT_OF_EXCHANGE_A,
)
from ..fresh_agent import FreshAgent
from ..money_path import LECTURE_NOTES, THALES, assert_bought, discover_offer, only_item
from ..obligation_registry import Obligation, obligation
from ..reporting import report_body
from ..sdk_client import offer_verifier, sdk_exchange_client
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_NOT_FOUND = "USAGE_REPORT_REJECTION_REASON_TRANSACTION_NOT_FOUND"


_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"


@obligation(Obligation.TENANT_001)
def test_a_resource_pushed_to_one_seat_is_not_offered_by_the_other(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Pushed to exchange-a's seat: exchange-a offers it, exchange-b offers nothing for it."""
    entry = fresh_entry()
    accepted(contributor(compose_stack).push(HOME_SEAT, [entry]))
    assert stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the owning seat does not offer it"
    other = discover_group(
        fresh_agent.exchange(EXCHANGE_B_DOMAIN), EXCHANGE_B_DOMAIN, entry.uri, "exchange-b"
    )
    assert not other.result.verified, f"exchange-b offers exchange-a's resource: {other}"
    assert other.absence_reason == _NOT_IN_CATALOG, f"exchange-b answered {other}"


@obligation(Obligation.TENANT_001, negative=True)
@USAGE_REPORT_REFUSAL_UNTYPED.mark
def test_a_report_at_one_seat_for_the_other_seats_transaction_is_refused(
    fresh_agent: FreshAgent,
) -> None:
    """Bought at exchange-d, reported at exchange-a: not_found; exchange-d then takes the report."""
    fresh_agent.fund(EXCHANGE_D_DOMAIN)
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    offer = discover_offer(fresh_agent, LECTURE_NOTES, EXCHANGE_D_DOMAIN)
    item = only_item(fresh_agent.exchange(EXCHANGE_D_DOMAIN).execute(offer), LECTURE_NOTES)
    assert_bought(item, "the lecture notes at exchange-d")

    def report(exchange: str) -> UsageReport:
        return report_body(
            exchange=exchange, transaction_id=str(item.transaction_id), billing_id=item.billing_id
        )

    with pytest.raises(CallError) as refused:
        fresh_agent.exchange(EXCHANGE_A_DOMAIN).report_usage(report(EXCHANGE_A_DOMAIN))
    refusal = refused.value
    assert refusal.code == "not_found", f"refused as {refusal.code!r}: {refusal}"
    accepted_id = fresh_agent.exchange(EXCHANGE_D_DOMAIN).report_usage(report(EXCHANGE_D_DOMAIN))
    assert accepted_id.report_id, "the selling seat did not take the report as the first"

    block = refusal.detail.usage_report_rejection if refusal.detail else None
    USAGE_REPORT_REFUSAL_UNTYPED.check(block is not None, f"refused {refusal.code} untyped")
    assert block is not None and block.reason.value == _NOT_FOUND, f"rejected as {block}"


@obligation(Obligation.TENANT_002)
def test_an_offer_verifies_under_its_own_seats_key_and_no_other(
    compose_stack: StackURLs, fresh_agent: FreshAgent
) -> None:
    """exchange-a's offer verifies under exchange-a's key and is rejected under exchange-b's.

    The second verifier files exchange-b's published key under exchange-a's name,
    so the only thing it changes is which seat's key checks the signature.
    """
    verifiers = {
        "exchange-a's own key": offer_verifier([EXCHANGE_A_DOMAIN]),
        "exchange-b's key": offer_verifier(
            [EXCHANGE_A_DOMAIN], dial={EXCHANGE_A_DOMAIN: EXCHANGE_B_DOMAIN}
        ),
    }
    query = resource_query(EXCHANGE_A_DOMAIN, [THALES])
    outcomes: dict[str, tuple[int, int]] = {}
    for label, verifier in verifiers.items():
        client = sdk_exchange_client(
            compose_stack,
            EXCHANGE_A_DOMAIN,
            verifier,
            agent_id=fresh_agent.host,
            key_path=fresh_agent.key_path,
        )
        with closing(client):
            groups = client.discover(query).groups
        outcomes[label] = (
            sum(len(g.result.verified) for g in groups),
            sum(len(g.result.rejected) for g in groups),
        )
    own_verified, own_rejected = outcomes["exchange-a's own key"]
    assert own_verified >= 1 and own_rejected == 0, f"under its own key: {outcomes}"
    other_verified, other_rejected = outcomes["exchange-b's key"]
    assert other_verified == 0 and other_rejected == own_verified, (
        f"exchange-b's key verified an exchange-a offer: {outcomes}"
    )


def _removal_naming(stack: StackURLs, agent: FreshAgent, tenant_id: str) -> None:
    """A removal at exchange-a naming ``tenant_id``: refused, and the entry still offered.

    Control: the same removal naming exchange-a's own seat removes another entry.
    """
    catalog = contributor(stack)
    kept, removed = fresh_entry(), fresh_entry()
    accepted(catalog.push(HOME_SEAT, [kept, removed]))

    outcome = answer(catalog.remove(tenant_id, [kept]))
    REMOVE_RESOURCES_UNIMPLEMENTED.check(
        not (isinstance(outcome, CallError) and outcome.code == "unimplemented"),
        f"answered {outcome}",
    )
    assert isinstance(outcome, CallError), f"a removal naming another seat was answered: {outcome}"
    assert stored(agent, HOME_EXCHANGE, kept.uri), "the mismatched removal removed the entry"
    reason, _paths = rejection(outcome)
    assert (outcome.code, reason) == ("permission_denied", TENANT_MISMATCH), outcome

    control = answer(catalog.remove(HOME_SEAT, [removed]))
    assert not isinstance(control, CallError), f"the control removal was refused: {control}"
    assert not stored(agent, HOME_EXCHANGE, removed.uri), "the control did not remove"


def _refresh_naming(stack: StackURLs, tenant_id: str) -> None:
    """RefreshCatalog at exchange-a naming ``tenant_id``: permission_denied, no refresh."""
    outcome = answer(contributor(stack).refresh(tenant_id))
    REFRESH_CATALOG_UNIMPLEMENTED.check(
        not (isinstance(outcome, CallError) and outcome.code == "unimplemented"),
        f"answered {outcome}",
    )
    assert isinstance(outcome, CallError), f"a refresh naming another seat was answered: {outcome}"
    reason, _paths = rejection(outcome)
    assert (outcome.code, reason) == ("permission_denied", TENANT_MISMATCH), outcome


@obligation(Obligation.TENANT_003, negative=True)
@REMOVE_RESOURCES_UNIMPLEMENTED.mark
def test_a_removal_naming_another_seat_is_refused_and_the_entry_stays(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """tenant_id naming exchange-c's seat in a removal at exchange-a: refused, entry still offered."""
    _removal_naming(compose_stack, fresh_agent, SEAT_OF_SFX)


@obligation(Obligation.TENANT_003, negative=True)
@REFRESH_CATALOG_UNIMPLEMENTED.mark
def test_a_refresh_naming_another_seat_is_refused(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
) -> None:
    """RefreshCatalog at exchange-a naming exchange-c's seat: permission_denied, no refresh."""
    _refresh_naming(compose_stack, SEAT_OF_SFX)


# ── Two seats on one Exchange ──────────────────────────────────────────────


@obligation(Obligation.TENANT_004, negative=True)
@CATALOG_TENANT_MISMATCH_UNTYPED.mark
def test_a_push_naming_the_second_seat_on_the_same_exchange_is_refused(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed writes the second seat
    fresh_agent: FreshAgent,
) -> None:
    """tenant_id naming exchange-a's second seat in a push to exchange-a: refused, nothing stored.

    The second seat is in exchange-a's own database, so the refusal is the seat
    check against a tenant the Exchange holds, not a lookup that finds nothing.
    Control: the same caller's push naming the seat exchange-a sells as is stored.
    """
    assert_push_naming_tenant_refused(compose_stack, fresh_agent, SECOND_SEAT_OF_EXCHANGE_A)


@obligation(Obligation.TENANT_004, negative=True)
@REMOVE_RESOURCES_UNIMPLEMENTED.mark
def test_a_removal_naming_the_second_seat_on_the_same_exchange_is_refused(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed writes the second seat
    fresh_agent: FreshAgent,
) -> None:
    """tenant_id naming exchange-a's second seat in a removal at exchange-a: entry still offered."""
    _removal_naming(compose_stack, fresh_agent, SECOND_SEAT_OF_EXCHANGE_A)


@obligation(Obligation.TENANT_004, negative=True)
@REFRESH_CATALOG_UNIMPLEMENTED.mark
def test_a_refresh_naming_the_second_seat_on_the_same_exchange_is_refused(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed writes the second seat
) -> None:
    """RefreshCatalog at exchange-a naming its second seat: permission_denied, no refresh."""
    _refresh_naming(compose_stack, SECOND_SEAT_OF_EXCHANGE_A)


#: The reporting window and fee rate the second seat is given. Neither is a value
#: any seat in this stack carries otherwise, so seeing one at exchange-a's own
#: seat could only mean the setting reached the wrong seat.
_SECOND_SEAT_WINDOW_S = 77
_SECOND_SEAT_FEE_BPS = 250


def _window_at_exchange_a(agent: FreshAgent) -> str | None:
    """The reporting window a purchase of Thales at exchange-a carries now."""
    item = only_item(
        agent.exchange(EXCHANGE_A_DOMAIN).execute(discover_offer(agent, THALES)), THALES
    )
    assert_bought(item, "Thales at exchange-a")
    assert item.reporting_obligation is not None, f"the purchase minted no obligation: {item}"
    return item.reporting_obligation.window


@obligation(Obligation.TENANT_005)
def test_an_operator_setting_naming_the_second_seat_reaches_that_seat_alone(
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed writes the second seat
    fresh_agent: FreshAgent,
) -> None:
    """exchange-a's admin listener sets its second seat's policy and rate; its own seat keeps its window.

    Each setter answers with the payload as persisted, which names the second seat.
    A purchase at exchange-a made while the second seat has its own window carries
    the window of the seat exchange-a sells as, the same one a purchase made
    before the setting carries. Both settings are put back when the test ends.
    """
    require_admin_plane()
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    before = _window_at_exchange_a(fresh_agent)
    assert before != f"{_SECOND_SEAT_WINDOW_S}s", f"exchange-a's own window is already {before}"
    with (
        reporting_policy(
            EXCHANGE_A_DOMAIN,
            window_seconds=_SECOND_SEAT_WINDOW_S,
            tenant_id=SECOND_SEAT_OF_EXCHANGE_A,
        ) as policy,
        tenant_fee_rate(
            EXCHANGE_A_DOMAIN, _SECOND_SEAT_FEE_BPS, tenant_id=SECOND_SEAT_OF_EXCHANGE_A
        ) as rate,
    ):
        assert (policy.tenant_id, policy.window_seconds) == (
            SECOND_SEAT_OF_EXCHANGE_A,
            _SECOND_SEAT_WINDOW_S,
        ), policy
        assert (rate.tenant_id, rate.fee_rate_bps) == (
            SECOND_SEAT_OF_EXCHANGE_A,
            _SECOND_SEAT_FEE_BPS,
        ), rate
        during = _window_at_exchange_a(fresh_agent)
    assert during == before, f"exchange-a's own window moved from {before} to {during}"
