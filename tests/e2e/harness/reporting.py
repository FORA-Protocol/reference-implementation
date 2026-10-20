"""The usage report a suite sends, built once.

A report is five required fields plus two that depend on the obligation, and it
was written out at every call site. It is built here as the SDK's own
``UsageReport`` message, so a field the protocol does not define cannot be sent.
When the protocol adds a required field, a builder is one edit and four
hand-written bodies are four.

The recipient is the exchange that ISSUED the offer, which is not always the
exchange a suite is otherwise talking to. It is required, and an exchange
refuses a report naming anybody else — before the obligation is read, so a
misaddressed report leaves no record to inspect afterwards. Use
``exchanges.recipient_of`` on the URL the report is posted to.
"""

from __future__ import annotations

import uuid
from contextlib import closing
from pathlib import Path
import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from wire.models import Usage, UsageReport

from .e2e_catalog import USD_AGENT_ID, USD_AGENT_KEY_PATH
from .exchanges import SERVICE_IDENTITIES
from .sdk_client import sdk_exchange_client, stack_offer_verifier
from .stack_urls import StackURLs


def report_body(
    *,
    exchange: str,
    transaction_id: str,
    consumed_quantity: int = 0,
    function: list[str] | None = None,
    billing_id: str | None = None,
    idempotency_key: str | None = None,
) -> UsageReport:
    """A ``UsageReport`` addressed to ``exchange``, built as the SDK's own message type.

    ``consumed_quantity`` defaults to 0. ``billing_id`` is left unset when absent
    rather than sent empty, which is what an obligation that carries none expects:
    the SDK renders only the fields a message sets. The report carries no
    requester: the agent it is filed for is the one whose signature it carries.
    """
    billing = {"billing_id": billing_id} if billing_id else {}
    return UsageReport(
        ver=ProtocolVersion,
        exchange=exchange,
        idempotency_key=idempotency_key or f"report-{uuid.uuid4().hex}",
        transaction_id=transaction_id,
        usage=Usage(consumed_quantity=consumed_quantity, function=list(function or ["ai_input"])),
        **billing,
    )


def readdressed(report: UsageReport, exchange: str) -> UsageReport:
    """The same report, naming ``exchange`` as its recipient instead."""
    return report.model_copy(update={"exchange": exchange})


def file_report(
    stack: StackURLs,
    report: UsageReport,
    *,
    agent_id: str = USD_AGENT_ID,
    key_path: Path = USD_AGENT_KEY_PATH,
) -> str:
    """File ``report`` as the agent ``key_path`` signs as; return the report_id it was answered with.

    The agent defaults to the seed's USD buyer. A test that bought as its own
    agent (``fresh_agent``) reports as that agent: an Exchange accepts a report
    only from the agent the transaction is recorded under.

    The SDK routes a report to the exchange the report names, through that
    exchange's own manifest, so the client is addressed at that exchange too.
    """
    exchange = str(report.exchange)
    client = sdk_exchange_client(
        stack, exchange, stack_offer_verifier(), agent_id=agent_id, key_path=key_path
    )
    with closing(client):
        report_id = client.report_usage(report).report_id
    assert report_id, f"{exchange} accepted the report and answered no report_id: {report}"
    return report_id


def assert_report_recorded_only_at(
    stack: StackURLs,
    report: UsageReport,
    report_id: str,
    *,
    agent_id: str = USD_AGENT_ID,
    key_path: Path = USD_AGENT_KEY_PATH,
) -> None:
    """The exchange ``report`` names recorded it under ``report_id``, and no other holds its sale.

    Every report is filed as the agent ``key_path`` signs as, the one that filed
    ``report`` first (see :func:`file_report`).

    Observed through ReportUsage itself, because the transaction ledger has no
    read RPC. Two observations:

    * the named exchange answers a replay of the same report (same idempotency
      key) with the report_id it returned first. A replay is answered from the
      stored, accepted report, so only an exchange that recorded it can;
    * every other exchange refuses the same report, readdressed to it, with
      not_found: it holds no transaction with that id, so the sale was recorded
      at the exchange that made it and nowhere else.
    """
    issuer = str(report.exchange)
    replayed = file_report(stack, report, agent_id=agent_id, key_path=key_path)
    assert replayed == report_id, (
        f"{issuer} answered a replay of the report with {replayed!r}, want the "
        f"{report_id!r} it returned first -- the report was not recorded there"
    )
    for other in SERVICE_IDENTITIES.values():
        if other == issuer:
            continue
        with pytest.raises(CallError) as absent:
            file_report(stack, readdressed(report, other), agent_id=agent_id, key_path=key_path)
        assert absent.value.code == "not_found", (
            f"{other} answered a report for transaction {report.transaction_id} with "
            f"{absent.value.code!r}; an exchange that never sold it holds no such "
            f"transaction and refuses not_found: {absent.value}"
        )
