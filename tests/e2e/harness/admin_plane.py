"""Each Exchange's admin listener: the operator setters and the evidence read.

Every Exchange runs ``fora.admin.v1.AdminService`` on a second, internal
listener, and serves the operator evidence read (``GET
/ops/transaction-evidence``) on the same one. The listener has no request
signing and no operator identity. Its address allowlist (``ADMIN_ALLOWED_CIDRS``)
is the only gate (ADR-022), and in this stack each Exchange's allowlist admits
the runner's address and no other (``x-runner-address`` in
``docker-compose.e2e.yml``). So a test reaches the admin plane only from inside
the compose network, running as the runner; :func:`require_admin_plane` skips
anywhere else.

Three things come from here:

* the reporting policy, set through the SDK's own ``AdminClient``
  (``SetReportingPolicy``). The policy is per seat and the seat is shared, so
  :func:`reporting_window` puts the default back when the test ends;
* the seat's fee rate, set the same way (``SetTenantFeeRate``), and put back by
  :func:`tenant_fee_rate` when the test ends;
* the evidence one transaction left at the Exchange that served it
  (:func:`transaction_evidence`). It is not a protocol RPC, so no SDK verb
  serves it; it is read as the plain JSON GET it is, and only the fields a test
  asserts on are taken out of it;
* the setters as control requests (:func:`policy_call`, :func:`fee_rate_call`)
  for a test that sends one to another address, alters one through
  :func:`harness.malformed.malformed`, or reads the refusal: :func:`answer`
  sends one and returns the typed answer or the SDK's ``CallError``.
"""

from __future__ import annotations

import base64
import json
import os
from collections.abc import Callable, Iterator
from contextlib import closing, contextmanager
from dataclasses import dataclass
from datetime import datetime
from http import HTTPStatus
from typing import Any, TypeVar

import httpx
import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import AdminClient
from wire.models import (
    ReportingPolicy,
    SetReportingPolicyRequest,
    SetReportingPolicyResponse,
    SetTenantFeeRateRequest,
    SetTenantFeeRateResponse,
    TenantFeeRate,
)

from .exchanges import seat_tenant_id
from .malformed import Request, SeamHTTP
from .sdk_client import BeforeSign, sdk_admin_client

T = TypeVar("T")

#: The port every Exchange's admin listener binds: the binary's ``ADMIN_ADDR``
#: default, which ``docker-compose.e2e.yml`` leaves unset.
ADMIN_PORT = 8082

#: The operator evidence read on the admin listener.
TRANSACTION_EVIDENCE_PATH = "/ops/transaction-evidence"

_READ_TIMEOUT_SEC = 10.0


def require_admin_plane() -> None:
    """Skip unless this run is the runner inside the compose network.

    The admin listener publishes no host port, and its allowlist admits only the
    runner's address, so from anywhere else every call would be refused.
    """
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip("the admin listeners admit only the runner, inside the compose network")


def admin_url(exchange_domain: str) -> str:
    """The admin listener of the Exchange whose identity is ``exchange_domain``.

    In-network an Exchange's identity is its service host and public port, and
    the admin listener is that service on :data:`ADMIN_PORT`, reached by its
    ``-admin`` network alias. The service's own name resolves to the capture
    proxy in the runner (x-through-capture-proxy in docker-compose.e2e.yml), and
    a request the proxy forwards arrives from the proxy's address, which the
    listener's allowlist does not admit; the alias is in no hosts file, so the
    runner's admin calls arrive from the runner's own address.
    """
    host = exchange_domain.rsplit(":", 1)[0]
    return f"http://{host}-admin:{ADMIN_PORT}"


def _admin_at(base_url: str) -> Callable[..., AdminClient]:
    """A :class:`harness.malformed.ClientFactory` for the AdminService at ``base_url``."""

    def build(
        *, signer: SigningTransport | None, before_sign: BeforeSign | None, http: SeamHTTP
    ) -> AdminClient:
        assert signer is None, "the admin plane takes no request signature"
        return sdk_admin_client(base_url, before_sign=before_sign, http=http)

    return build


def policy_call(
    base_url: str, policy: ReportingPolicy
) -> Request[AdminClient, SetReportingPolicyResponse]:
    """SetReportingPolicy of ``policy``, addressed to ``base_url``, as a control request."""
    return Request(
        client=_admin_at(base_url),
        verb=lambda c: c.set_reporting_policy,
        message=SetReportingPolicyRequest(ver=ProtocolVersion, policy=policy),
    )


def fee_rate_call(
    base_url: str, rate: TenantFeeRate
) -> Request[AdminClient, SetTenantFeeRateResponse]:
    """SetTenantFeeRate of ``rate``, addressed to ``base_url``, as a control request."""
    return Request(
        client=_admin_at(base_url),
        verb=lambda c: c.set_tenant_fee_rate,
        message=SetTenantFeeRateRequest(ver=ProtocolVersion, rate=rate),
    )


def answer(request: Request[AdminClient, T]) -> T | CallError:
    """Send ``request`` once, unaltered; its typed answer, or the SDK's refusal."""
    with (
        httpx.Client() as http,
        closing(request.client(signer=None, before_sign=None, http=http)) as c,
    ):
        try:
            return request.verb(c)(request.message)
        except CallError as exc:
            return exc


def replace_reporting_policy(exchange_domain: str, policy: ReportingPolicy) -> ReportingPolicy:
    """Replace the seat's reporting policy; return the policy the Exchange echoes back."""
    echoed = answer(policy_call(admin_url(exchange_domain), policy))
    assert not isinstance(echoed, CallError), f"{exchange_domain} refused {policy}: {echoed}"
    assert echoed.policy is not None, f"{exchange_domain} echoed no policy for {policy}"
    return echoed.policy


@contextmanager
def reporting_policy(
    exchange_domain: str,
    *,
    window_seconds: int | None = None,
    required_fields: tuple[str, ...] = (),
    quantity_tolerance: float | None = None,
    tenant_id: str | None = None,
) -> Iterator[ReportingPolicy]:
    """Set the reporting policy of ``exchange_domain``'s seat for the block; yield the echo.

    ``tenant_id`` names another seat the same Exchange holds, set through that
    Exchange's admin listener, instead of the seat it sells as.

    ``SetReportingPolicy`` replaces the whole policy, so every field left out falls
    back to the Exchange's default. On the way out the policy is replaced by one
    that states nothing, which is the default the seed leaves every seat with. That
    runs however the block ends, so a failing test does not leave the seat's later
    purchases under this policy.

    The policy is read when a transaction executes, and each obligation keeps the
    window and required fields it was minted with.
    """
    tenant = tenant_id or seat_tenant_id(exchange_domain)
    echoed = replace_reporting_policy(
        exchange_domain,
        ReportingPolicy(
            tenant_id=tenant,
            window_seconds=window_seconds,
            required_fields=list(required_fields) or None,
            quantity_tolerance=quantity_tolerance,
        ),
    )
    try:
        yield echoed
    finally:
        replace_reporting_policy(exchange_domain, ReportingPolicy(tenant_id=tenant))


@contextmanager
def reporting_window(exchange_domain: str, seconds: int) -> Iterator[ReportingPolicy]:
    """Shorten the reporting window of ``exchange_domain``'s seat for the block; yield the echo.

    Only obligations minted while the window is short get the short deadline;
    see :func:`reporting_policy`.
    """
    with reporting_policy(exchange_domain, window_seconds=seconds) as echoed:
        yield echoed


#: The fee rate every seat in this stack sells at when no test has set one: the
#: column default of ``fora.tenants.fee_rate_bps``
#: (``src/exchange/internal/db/migrations/000019_tenant_fee_rate.up.sql``). The seed
#: sets no rate, and the admin plane has no read of the rate to restore from, so
#: this default is what :func:`tenant_fee_rate` puts back.
DEFAULT_FEE_RATE_BPS = 0


def _set_fee_rate(exchange_domain: str, rate: TenantFeeRate) -> TenantFeeRate:
    """Replace the seat's fee rate; return the rate the Exchange echoes back."""
    echoed = answer(fee_rate_call(admin_url(exchange_domain), rate))
    assert not isinstance(echoed, CallError), f"{exchange_domain} refused {rate}: {echoed}"
    assert echoed.rate is not None, f"{exchange_domain} echoed no fee rate for {rate}"
    return echoed.rate


@contextmanager
def tenant_fee_rate(
    exchange_domain: str, bps: int, *, tenant_id: str | None = None
) -> Iterator[TenantFeeRate]:
    """Set the fee rate of ``exchange_domain``'s seat to ``bps`` for the block; yield the echo.

    ``tenant_id`` names another seat the same Exchange holds, as for
    :func:`reporting_policy`.

    ``SetTenantFeeRate`` replaces the rate and its note. On the way out the rate is
    replaced by :data:`DEFAULT_FEE_RATE_BPS` with no note, however the block ends,
    so a failing test does not leave the seat's later sales charged at this rate.
    A sale takes the rate in force when it executes, so only sales made inside the
    block are charged at ``bps``.
    """
    tenant = tenant_id or seat_tenant_id(exchange_domain)
    echoed = _set_fee_rate(exchange_domain, TenantFeeRate(tenant_id=tenant, fee_rate_bps=bps))
    try:
        yield echoed
    finally:
        _set_fee_rate(
            exchange_domain, TenantFeeRate(tenant_id=tenant, fee_rate_bps=DEFAULT_FEE_RATE_BPS)
        )


@dataclass(frozen=True)
class ObligationEvidence:
    """The reporting obligation one transaction minted, as the Exchange holds it.

    ``state`` is the stored word verbatim (``PENDING`` until a report is
    accepted). ``window_end`` is the obligation's deadline and ``created_at``
    when it was minted, so the difference is the window it was minted with.
    ``fulfilled_at`` is when a report was accepted, or None while none has been.
    ``consumed_quantity`` is the quantity the accepted report stated, as the
    decimal string the Exchange stores, or None while no report has been accepted.
    """

    state: str
    created_at: datetime
    window_end: datetime
    fulfilled_at: datetime | None
    consumed_quantity: str | None


@dataclass(frozen=True)
class TransactionEvidence:
    """The fields of one transaction's evidence that a test asserts on.

    ``request_id`` is the X-Request-ID the Exchange recorded for the request
    that executed the transaction. ``request_id_minted`` is True when the
    Exchange minted that id itself because the request carried none it could
    accept, and False when the id arrived on the request. ``created_at`` is when
    the Exchange recorded the transaction. ``offer`` is the offer the agent
    accepted, decoded from the exact signed bytes the Exchange verified and
    stored, so its resource (``identity.canonical_url``) and its pricing are the
    ones the sale was made on.
    """

    transaction_id: str
    requester_id: str
    request_id: str | None
    request_id_minted: bool | None
    obligation: ObligationEvidence | None
    created_at: datetime
    offer: dict[str, Any]


def _instant(raw: str | None) -> datetime | None:
    return datetime.fromisoformat(raw) if raw else None


def _obligation(raw: dict[str, Any] | None) -> ObligationEvidence | None:
    if raw is None:
        return None
    created_at, window_end = _instant(raw["created_at"]), _instant(raw["window_end"])
    assert created_at and window_end, f"an obligation without its own times: {raw}"
    return ObligationEvidence(
        state=raw["state"],
        created_at=created_at,
        window_end=window_end,
        fulfilled_at=_instant(raw.get("fulfilled_at")),
        consumed_quantity=raw.get("consumed_quantity"),
    )


def _offer(raw: str) -> dict[str, Any]:
    """The stored canonical offer: base64 in the JSON body, a JSON document inside."""
    offer = json.loads(base64.b64decode(raw))
    assert isinstance(offer, dict), f"the stored canonical offer is not a JSON object: {offer!r}"
    return offer


def _evidence_read(exchange_domain: str, transaction_id: str) -> httpx.Response:
    return httpx.get(
        f"{admin_url(exchange_domain)}{TRANSACTION_EVIDENCE_PATH}",
        params={"tx": transaction_id},
        timeout=_READ_TIMEOUT_SEC,
    )


def evidence_read_status(exchange_domain: str, transaction_id: str) -> HTTPStatus:
    """The status the evidence read for ``transaction_id`` is answered with, from this runner.

    For the allowlist test: past the address allowlist, a transaction id that is
    not one is answered 400 by the route, and nothing changes.
    """
    return HTTPStatus(_evidence_read(exchange_domain, transaction_id).status_code)


def transaction_evidence(exchange_domain: str, transaction_id: str) -> TransactionEvidence:
    """Read the evidence ``exchange_domain`` holds for ``transaction_id``, from its admin listener."""
    response = _evidence_read(exchange_domain, transaction_id)
    assert response.status_code == httpx.codes.OK, (
        f"{exchange_domain} answered the evidence read for {transaction_id} with "
        f"{response.status_code}: {response.text[:256]}"
    )
    body = response.json()
    evidence = body["evidence"]
    created_at = _instant(evidence["created_at"])
    assert created_at, f"evidence without its own time: {evidence}"
    return TransactionEvidence(
        transaction_id=evidence["transaction_id"],
        requester_id=evidence["requester_id"],
        request_id=evidence.get("request_id"),
        request_id_minted=evidence.get("request_id_minted"),
        obligation=_obligation(body.get("obligation_state")),
        created_at=created_at,
        offer=_offer(evidence["offer_canonical_bytes"]),
    )
