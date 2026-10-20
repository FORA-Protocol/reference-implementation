"""Obligation MONEY-038: GetAccountStatus answers a known agent with no account NOT_FOUND.

``ExchangeService.GetAccountStatus`` and ``GetAccountStatusResponse.billing_ref`` in
``fora.proto`` of the pinned module ``github.com/FORA-Protocol/protocol``: an agent
the Exchange knows, its request signature verified, but that holds no account at
that Exchange is answered with the Connect code NOT_FOUND, and ``billing_ref`` is set
on every OK answer, never sent empty in place of that refusal.

The agent is the test's own, and it becomes known to exchange-a the way every agent
does, by a signed request: its discovery there is answered with a verified offer,
which shows the Exchange resolved its key and verified its signature. Its
GetAccountStatus is then refused not_found, and a second read is refused the same
way, so the first read opened nothing. The control registers the agent, and the
same read now answers OK with the billing_ref Register returned.

Every call is the agent's own, through its SDK client, read back as the SDK's typed
answer or typed refusal. No leg reads the deployment's datastore.
"""

from __future__ import annotations

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from wire.models import GetAccountStatusResponse

from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..identity_requests import answered_with_offer, probe_query
from ..obligation_registry import Obligation, obligation
from ..seed_accounts import open_active_account

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _status(agent: FreshAgent) -> GetAccountStatusResponse:
    """The agent's own GetAccountStatus at exchange-a."""
    return agent.exchange(EXCHANGE_A_DOMAIN).get_account_status(
        {"ver": ProtocolVersion, "exchange": EXCHANGE_A_DOMAIN}
    )


@obligation(Obligation.MONEY_038, negative=True)
def test_a_known_agent_without_an_account_is_answered_not_found_until_it_registers(
    fresh_agent: FreshAgent,
) -> None:
    """Known, no account: not_found, twice. Registered: OK with the billing_ref Register returned."""
    answered_with_offer()(fresh_agent.exchange(EXCHANGE_A_DOMAIN).discover(probe_query()))

    for attempt in ("first", "second"):
        with pytest.raises(CallError) as refused:
            _status(fresh_agent)
        assert refused.value.code == "not_found", (
            f"the {attempt} read by a known agent with no account was refused "
            f"{refused.value.code!r}: {refused.value}"
        )

    billing_ref = open_active_account(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN), EXCHANGE_A_DOMAIN, fresh_agent.agent_id
    )
    assert billing_ref, "control: Register returned no billing_ref"
    status = _status(fresh_agent)
    assert status.billing_ref == billing_ref, (
        f"the OK answer carries billing_ref {status.billing_ref!r}, Register returned "
        f"{billing_ref!r}"
    )
