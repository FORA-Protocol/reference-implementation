"""Obligations ADMIN-001 and ADMIN-003 to ADMIN-007: the Exchange's operator plane.

The contract is ``fora.admin.v1.AdminService`` in ``admin.proto`` of the pinned
module ``github.com/FORA-Protocol/protocol``: the service is not on the public
listener, each setter is a full replace that answers with what it persisted, and
each field rule of the payload is enforced. ADMIN-007 is a DECISION quoting the
reference Exchange's code, because the protocol names no answer for a tenant the
Exchange does not hold. ADMIN-002, the address allowlist, is proved by
``harness/test_admin_plane.py``.

Every call is the SDK's own ``AdminClient`` (``harness.admin_plane``); a refusal
that needs a malformed payload is a declared ``malformed()`` mutation in raw mode
beside its control. What a policy did is read where it takes effect: the
reporting obligation of a purchase made after it, bought as the test's own agent
at exchange-d, whose lecture notes mint an obligation on every purchase. Every
policy and fee rate a test sets is put back when the test ends.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.client import CallError
from wire.models import ReportingObligation, ReportingPolicy, TenantFeeRate

from ..admin_plane import (
    admin_url,
    answer,
    fee_rate_call,
    policy_call,
    replace_reporting_policy,
    reporting_policy,
    require_admin_plane,
    tenant_fee_rate,
)
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_D_DOMAIN, exchange_url, seat_tenant_id
from ..fresh_agent import FreshAgent
from ..malformed import Alteration, Mutation, malformed
from ..money_path import LECTURE_NOTES, assert_bought, discover_offer, only_item
from ..obligation_registry import Obligation, obligation
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_SEAT = EXCHANGE_D_DOMAIN
_TENANT = seat_tenant_id(_SEAT)
_ONE_YEAR = 31_536_000
_REQUIRED = "billing_id"


@pytest.fixture
def buyer(fresh_agent: FreshAgent) -> FreshAgent:
    """The test's own agent, funded at exchange-d, on a run that reaches the admin plane."""
    require_admin_plane()
    fresh_agent.fund(_SEAT)
    return fresh_agent


def owed(agent: FreshAgent) -> ReportingObligation:
    """The reporting obligation a purchase of the lecture notes at exchange-d mints now."""
    offer = discover_offer(agent, LECTURE_NOTES, _SEAT)
    item = only_item(agent.exchange(_SEAT).execute(offer), LECTURE_NOTES)
    assert_bought(item, "the lecture notes")
    assert item.reporting_obligation is not None, f"the purchase minted no obligation: {item}"
    return item.reporting_obligation


def _window(obligation_: ReportingObligation) -> str | None:
    return obligation_.window


def _edit_policy(edit: Callable[[dict[str, Any]], None]) -> Callable[[dict[str, Any]], dict]:
    """A raw-mode alteration that edits the policy inside a SetReportingPolicyRequest."""

    def alter(body: dict[str, Any]) -> dict[str, Any]:
        edit(body["policy"])
        return body

    return alter


@obligation(Obligation.ADMIN_001, negative=True)
def test_admin_service_is_not_served_on_the_public_listener(
    compose_stack: StackURLs, buyer: FreshAgent
) -> None:
    """SetReportingPolicy at the public listener is refused and changes nothing.

    The same call at the admin listener is answered with the policy it persisted.
    """
    default = _window(owed(buyer))
    public = exchange_url(compose_stack, _SEAT)
    try:
        outcome = answer(policy_call(public, ReportingPolicy(tenant_id=_TENANT, window_seconds=77)))
        assert isinstance(outcome, CallError), f"the public listener served AdminService: {outcome}"
        assert _window(owed(buyer)) == default, "a policy sent to the public listener took effect"
    finally:
        replace_reporting_policy(_SEAT, ReportingPolicy(tenant_id=_TENANT))
    with reporting_policy(_SEAT, window_seconds=77) as echoed:
        assert echoed.window_seconds == 77, f"the admin listener echoed {echoed}"
        assert _window(owed(buyer)) == "77s", "the admin listener's policy did not take effect"


@obligation(Obligation.ADMIN_003)
def test_each_setter_answers_with_what_it_persisted() -> None:
    """The fee rate echoes its rate and note, then no note; the policy echoes fields and window."""
    require_admin_plane()
    url = admin_url(_SEAT)
    noted_rate = TenantFeeRate(tenant_id=_TENANT, fee_rate_bps=250, notes="set by the e2e suite")
    bare_rate = TenantFeeRate(tenant_id=_TENANT, fee_rate_bps=300)
    with tenant_fee_rate(_SEAT, 250):
        noted = answer(fee_rate_call(url, noted_rate))
        assert not isinstance(noted, CallError), noted
        assert noted.rate == noted_rate, f"echoed {noted.rate}, want {noted_rate}"
        bare = answer(fee_rate_call(url, bare_rate))
        assert not isinstance(bare, CallError), bare
        assert bare.rate == bare_rate, f"echoed {bare.rate}, want {bare_rate}: the note was kept"
    with reporting_policy(_SEAT, window_seconds=600, required_fields=(_REQUIRED,)) as echoed:
        assert echoed.tenant_id == _TENANT, echoed
        assert echoed.window_seconds == 600, echoed
        assert list(echoed.required_fields or []) == [_REQUIRED], echoed


@obligation(Obligation.ADMIN_004)
def test_a_policy_that_names_nothing_puts_the_exchange_defaults_back(buyer: FreshAgent) -> None:
    """A window and a required field reach the next purchase; the next policy clears both."""
    before = owed(buyer)
    with reporting_policy(_SEAT, window_seconds=120, required_fields=(_REQUIRED,)):
        named = owed(buyer)
        assert named.window == "120s", f"the named window did not apply: {named}"
        assert list(named.required_fields or []) == [_REQUIRED], named
        replace_reporting_policy(_SEAT, ReportingPolicy(tenant_id=_TENANT))
        cleared = owed(buyer)
        assert cleared.window == before.window, f"window {cleared.window}, want {before.window}"
        assert list(cleared.required_fields or []) == list(before.required_fields or []), (
            f"required fields {cleared.required_fields}, want {before.required_fields}"
        )


@obligation(Obligation.ADMIN_005, negative=True)
def test_a_policy_naming_a_required_field_twice_is_refused(buyer: FreshAgent) -> None:
    """billing_id once is persisted; twice is refused, and the policy in force stays."""
    control = policy_call(
        admin_url(_SEAT),
        ReportingPolicy(tenant_id=_TENANT, window_seconds=300, required_fields=[_REQUIRED]),
    )

    def twice(policy: dict[str, Any]) -> None:
        policy["required_fields"] = [_REQUIRED, _REQUIRED]
        policy["window_seconds"] = 900

    with reporting_policy(_SEAT):
        refusal = malformed(
            Mutation.MESSAGE,
            "required_fields names billing_id twice",
            control=control,
            alter=Alteration(raw=_edit_policy(twice)),
        )
        assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
        assert _window(owed(buyer)) == "300s", "the refused policy changed the window in force"


@obligation(Obligation.ADMIN_006, negative=True)
def test_a_reporting_window_is_positive_and_at_most_a_year(buyer: FreshAgent) -> None:
    """One year is persisted; zero and one second more are refused, and one year stays."""
    control = policy_call(
        admin_url(_SEAT), ReportingPolicy(tenant_id=_TENANT, window_seconds=_ONE_YEAR)
    )
    with reporting_policy(_SEAT):
        for window in (0, _ONE_YEAR + 1):

            def out_of_range(policy: dict[str, Any], seconds: int = window) -> None:
                policy["window_seconds"] = seconds

            refusal = malformed(
                Mutation.MESSAGE,
                f"a reporting window of {window} seconds",
                control=control,
                alter=Alteration(raw=_edit_policy(out_of_range)),
            )
            assert refusal.code == "invalid_argument", f"{window}: refused as {refusal.code!r}"
        assert _window(owed(buyer)) == f"{_ONE_YEAR}s", "the window in force is not the year"


@obligation(Obligation.ADMIN_007, Obligation.TENANT_001, negative=True)
def test_a_setter_naming_a_tenant_the_exchange_does_not_hold_is_refused_not_found(
    buyer: FreshAgent,
) -> None:
    """exchange-a's admin listener, asked for exchange-d's seat: not_found, exchange-d unchanged.

    Control: the same policy naming exchange-a's own seat is persisted there. The
    refusal is ADMIN-007's; that exchange-d's purchases keep their own window is
    TENANT-001's, one seat's admin plane changing nothing of another seat's.
    """
    default = _window(owed(buyer))
    home = seat_tenant_id(EXCHANGE_A_DOMAIN)
    control = policy_call(
        admin_url(EXCHANGE_A_DOMAIN), ReportingPolicy(tenant_id=home, window_seconds=45)
    )

    def other_seat(policy: dict[str, Any]) -> None:
        policy["tenant_id"] = _TENANT

    with reporting_policy(EXCHANGE_A_DOMAIN):
        refusal = malformed(
            Mutation.IDENTITY,
            "the policy names exchange-d's seat and reaches exchange-a",
            control=control,
            alter=Alteration(raw=_edit_policy(other_seat)),
        )
    assert refusal.code == "not_found", f"refused as {refusal.code!r}: {refusal}"
    assert _window(owed(buyer)) == default, "exchange-d's window moved"
