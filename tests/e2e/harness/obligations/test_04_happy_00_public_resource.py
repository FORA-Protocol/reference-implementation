"""Obligations PUBLIC-001, -002 and -003: an own-principal agent and a free resource.

The agent discovers and accepts a free offer and receives a signed URL
(PUBLIC-001), fetches it through the edge (PUBLIC-002), and reports its usage
for a transaction recorded under it (PUBLIC-003), while another agent's report
for the same transaction is refused. Which refusal that is, the one an unknown
transaction gets, is PUBLIC-005, in ``test_04_failure_04_unclaimed_usage_report``.

Driven against the demo ``socrates`` resource (FREE EUR, academic, EU) as the
test's own agent (``fresh_agent``), registered at exchange-a and operating as its
own principal: every Exchange call is the SDK's own, signed with the agent's own
Ed25519 key, and carries NO ``Authorization`` bearer and NO entitlement biscuit.
What the agent's client sent is read from a capture of its requests as they went
on the wire. The offer is FREE (price zero); accepting it yields a
signed URL whose fetch through the Cloudflare edge delivers content.

The recorded transaction carries the agent's identity. That is observed through
ReportUsage, whose authorization is the point: the Exchange accepts a report only
from the agent its transaction is recorded under, and refuses any other agent.
So a report for the transaction, signed by a second
agent of the test's own, also registered there, is refused, and the same report
signed by the buying agent is accepted. The refusal comes first: a replay probe runs before the authorization
check, so an accepted report would be answered to anyone presenting its key.

Every leg goes in through the Exchange's public RPCs and the edge, and is read
back through them as the SDK's typed results; no leg reads the deployment's
datastore.
"""

from __future__ import annotations

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from wire.models import ResourceQuery

from ..after_signing import Capture
from ..broker_purchase import one_group_per_uri
from ..constants import requester
from ..defects import FOREIGN_REPORT_NOT_HIDDEN
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import AgentFactory
from ..reporting import report_body
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from ..obligation_registry import Obligation, obligation

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/socrates.txt"

_FORBIDDEN_DELEGATION_HEADERS = ("authorization", "x-fora-entitlement-biscuit")


def _assert_no_delegation_credentials_on_wire(capture: Capture) -> None:
    """Fail if any request the agent's client sent carried a populated delegation header."""
    for name in _FORBIDDEN_DELEGATION_HEADERS:
        for url, value in capture.header_values(name):
            assert not value, (
                f"the PUBLIC-001 own-principal path expects no delegation/entitlement headers; "
                f"request to {url} carried {name!r}={value!r}"
            )


@obligation(Obligation.PUBLIC_001, Obligation.PUBLIC_002, Obligation.PUBLIC_003)
def test_agent_as_own_principal_lists_accepts_and_fetches_free_resource(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Agent-as-own-principal lists, accepts, and fetches a zero-price demo resource."""
    agent, other = fresh_agent_factory(), fresh_agent_factory()
    agent.fund(EXCHANGE_A_DOMAIN)
    other.fund(EXCHANGE_A_DOMAIN)
    capture = Capture()
    client = agent.exchange_client(EXCHANGE_A_DOMAIN, http=capture.client())

    result = client.discover(
        ResourceQuery(
            ver=ProtocolVersion,
            exchange=EXCHANGE_A_DOMAIN,
            uris=[_RESOURCE_URI],
            requester=requester(agent.agent_id, user_type="academic", geography="EU"),
        )
    )
    group = one_group_per_uri(result, [_RESOURCE_URI], "free-URI discovery")[_RESOURCE_URI]
    rejected = [(r.offer.get("offer_id"), r.reason) for r in group.result.rejected]
    assert not rejected, f"offers for the free URI failed verification: {rejected}"
    per_request = [v for v in group.result.verified if not v.offer.get("subscription_id")]
    assert per_request, f"free URI must surface a per-request offer, got {group}"
    chosen = per_request[0]
    # Price zero — assert on the canonical pricing.rate (the FREE term's rate is 0).
    # Money-as-string: pricing.rate rides as a decimal STRING on the wire
    # (discover.go FormatMoney: 0 → "0"); parse before comparing to zero.
    rate = (chosen.offer.get("pricing") or {}).get("rate")
    assert rate is not None, f"free resource offer carries no pricing.rate: {chosen.offer}"
    assert float(rate) == 0, f"free resource offer must carry price zero, got pricing.rate={rate!r}"

    # The SDK signs the agent acceptance with the agent's own key and sends the
    # single offer as a one-item batch.
    accepted_tx = client.execute(chosen)
    items = accepted_tx.items or []
    assert len(items) == 1, f"one offer bought, {len(items)} items answered: {accepted_tx}"
    item = items[0]
    transaction_id = item.transaction_id
    signed_url = item.retrieval_endpoint
    assert signed_url, f"retrieval_endpoint missing from the bought item: {item}"
    assert transaction_id, f"transaction_id missing from the bought item: {item}"

    # The demo domain resolves to its edge in-network; the SDK fetches the signed
    # URL directly, presenting the agent's proof of possession. Assert the ACTUAL
    # delivered socrates article (deploy/content/demo/stoa-press/.../socrates.txt).
    content = client.fetch(signed_url)
    assert "Socrates" in content.body.decode(), (
        f"delivered body missing expected marker 'Socrates': {content.body[:256]!r}"
    )

    # The recorded transaction carries the agent's identity, observed through
    # ReportUsage's authorization. Precondition: the item owes a report, so the
    # Exchange holds a record a report can be filed against.
    assert item.reporting_obligation is not None and item.reporting_obligation.required, (
        f"the free item must owe a usage report for this observation to apply: {item}"
    )

    # Another agent's report for this transaction is refused, because the
    # transaction is not recorded under that agent. Which refusal it is, the one an
    # unknown transaction gets, is PUBLIC-005's to assert
    # (test_04_failure_04_unclaimed_usage_report); here the refusal is the evidence
    # of whose transaction it is, so it must be an authorization refusal and not a
    # signature or transport failure: permission_denied while the defect
    # FOREIGN_REPORT_NOT_HIDDEN stands, not_found once it is fixed.
    foreign = report_body(
        exchange=EXCHANGE_A_DOMAIN,
        transaction_id=transaction_id,
    )
    with pytest.raises(CallError) as refused:
        other.exchange(EXCHANGE_A_DOMAIN).report_usage(foreign)
    assert refused.value.code in ("permission_denied", "not_found"), (
        f"another agent's report was refused {refused.value.code!r}, not as an authorization "
        f"refusal ({FOREIGN_REPORT_NOT_HIDDEN.reason}): {refused.value}"
    )

    # Control: the same report from the agent itself is accepted.
    own = report_body(
        exchange=EXCHANGE_A_DOMAIN,
        transaction_id=transaction_id,
    )
    report = client.report_usage(own)
    assert report.report_id, f"accepted report has no report_id: {report}"

    # Discovery, purchase, fetch and report: none carried a delegation credential.
    _assert_no_delegation_credentials_on_wire(capture)
