"""The CatalogService calls and the catalog reads the catalog obligation tests share.

A catalog test sends what a real publisher or contributor sends: the SDK's own
``CatalogClient`` (``harness.sdk_client.sdk_catalog_client``), signed as the
caller's keyfile. :class:`Catalog` builds each call as a control request
(``harness.malformed.Request``), so the same object is either sent as it is
(:func:`answer`, :func:`refusal`) or altered once through
:func:`harness.malformed.malformed`.

What a push stored is read back the way an agent reads it: ``DiscoverResources``
through the SDK's Exchange client, signed as the test's own agent
(:func:`offers`, :func:`stored`). A refusal is read through the SDK's typed
``CallError``: its Connect code and the ``catalog_rejection`` block
(:func:`rejection`), never the message text.
"""

from __future__ import annotations

from collections.abc import Callable
from contextlib import closing
from dataclasses import dataclass
from pathlib import Path
from typing import Any, TypeVar

import uuid

import httpx
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from fora_sdk.core import DiscoveryResult, OfferGroupResult, VerifiedOffer
from fora_sdk.signing_transport import SigningTransport
from fora_sdk.sync import CatalogClient
from wire.models import (
    PushResourcesResponse,
    RefreshCatalogResponse,
    RemoveResourcesResponse,
)

from . import after_signing
from .broker_purchase import one_group_per_uri, resource_query
from .catalog_push import (
    PRICING_MODEL_FREE,
    PRICING_MODEL_PER_UNIT,
    CatalogEntry,
    license_term,
)
from .defects import CATALOG_REFUSAL_UNTYPED, CATALOG_TENANT_MISMATCH_UNTYPED
from .e2e_catalog import CONTRIBUTOR_KEY_PATH, DEMO_PHILOSOPHY_DOMAIN, SEAT_OF_PHILOSOPHY
from .exchanges import EXCHANGE_A_DOMAIN, recipient_of
from .fresh_agent import FreshAgent
from .httpsig_signer import load_keypair, signing_transport
from .malformed import Request, patch_body
from .sdk_client import BeforeSign, sdk_catalog_client
from .stack_urls import StackURLs

T = TypeVar("T")

#: How long one catalog call may take. A push fetches the publisher's manifest and
#: the caller's key directory before it answers.
_CALL_TIMEOUT_SEC = 10.0

#: The reason a malformed entry is refused with: the envelope and term rules.
MALFORMED_ENTRY = "CATALOG_REJECTION_REASON_MALFORMED_ENTRY"
NOT_CATALOG_CONTRIBUTOR = "CATALOG_REJECTION_REASON_NOT_CATALOG_CONTRIBUTOR"
TENANT_MISMATCH = "CATALOG_REJECTION_REASON_TENANT_MISMATCH"
DOMAIN_NOT_VERIFIED = "CATALOG_REJECTION_REASON_DOMAIN_NOT_VERIFIED"
UNKNOWN_VOCAB_TOKEN = "CATALOG_REJECTION_REASON_UNKNOWN_VOCAB_TOKEN"
URI_UNAVAILABLE = "CATALOG_REJECTION_REASON_URI_UNAVAILABLE"
EXCHANGE_NOT_LISTED = "CATALOG_REJECTION_REASON_EXCHANGE_NOT_LISTED"
UNKNOWN_CRITICAL_EXTENSION = "CATALOG_REJECTION_REASON_UNKNOWN_CRITICAL_EXTENSION"


#: The per-access rate of the term an entry that declares none is pushed with. An
#: entry with no LicenseTerm yields no offer, so an entry that only needs "an offer
#: exists" leaves its terms empty and gets this one.
DEFAULT_ENTRY_RATE = 0.05


def entry_wire(e: CatalogEntry) -> dict[str, Any]:
    """One ``ResourceEntry`` under its canonical snake_case wire names."""
    terms = e.terms or (
        license_term(
            model=PRICING_MODEL_PER_UNIT,
            rate=DEFAULT_ENTRY_RATE,
            unit="accesses",
            estimated_quantity=1,
        ),
    )
    entry: dict[str, Any] = {
        "domain": e.domain,
        "path": e.path,
        "content_id": e.content_id,
        "terms": [dict(t) for t in terms],
    }
    if e.estimated_quantity is not None:
        entry["estimated_quantity"] = e.estimated_quantity
    if e.ext is not None:
        entry["ext"] = dict(e.ext)
    if e.ext_critical:
        entry["ext_critical"] = list(e.ext_critical)
    if e.fields:
        entry.update(e.fields)
    return entry


def keyfile_signer(key_path: Path) -> SigningTransport:
    """The SDK's request signer over the keyfile at ``key_path``."""
    return signing_transport(*load_keypair(key_path))


@dataclass(frozen=True)
class Catalog:
    """One caller's CatalogService at one Exchange: who signs, and where the calls go."""

    exchange_url: str
    signer: SigningTransport

    @property
    def recipient(self) -> str:
        """The bare host every request names in ``exchange``."""
        return recipient_of(self.exchange_url)

    def client(
        self,
        *,
        signer: SigningTransport | None = None,
        before_sign: BeforeSign | None = None,
        http: httpx.Client | None = None,
    ) -> CatalogClient:
        """The SDK Catalog client, with the seams ``malformed`` alters a call through."""
        return sdk_catalog_client(
            self.exchange_url,
            signer if signer is not None else self.signer,
            call_timeout_sec=_CALL_TIMEOUT_SEC,
            before_sign=before_sign,
            http=http,
        )

    def push(
        self, tenant_id: str, entries: list[CatalogEntry]
    ) -> Request[CatalogClient, PushResourcesResponse]:
        """PushResources of ``entries`` for ``tenant_id``, signed by the caller's key.

        ``ver`` is stamped from ``fora_sdk.ProtocolVersion`` rather than left to the
        SDK, so a wrong or missing value cannot hide behind the SDK filling it in.
        ``exchange`` is the recipient's bare host. The body still carries the
        deprecated caller_id, the signer's directory, because the reference
        Exchange refuses a push without it (CATALOG-024 proves the push without it).
        """
        message: dict[str, Any] = {
            "ver": ProtocolVersion,
            "exchange": self.recipient,
            "tenant_id": tenant_id,
            "caller_id": self.signer.signature_agent,
            "entries": [entry_wire(e) for e in entries],
        }
        return Request(client=self.client, verb=lambda c: c.push_resources, message=message)

    def remove(
        self, tenant_id: str, entries: list[CatalogEntry]
    ) -> Request[CatalogClient, RemoveResourcesResponse]:
        """RemoveResources of ``entries`` for ``tenant_id``, each named by domain and path."""
        message: dict[str, Any] = {
            "ver": ProtocolVersion,
            "exchange": self.recipient,
            "tenant_id": tenant_id,
            "resources": [{"domain": e.domain, "path": e.path} for e in entries],
        }
        return Request(client=self.client, verb=lambda c: c.remove_resources, message=message)

    def refresh(self, tenant_id: str) -> Request[CatalogClient, RefreshCatalogResponse]:
        """RefreshCatalog for ``tenant_id``."""
        message: dict[str, Any] = {
            "ver": ProtocolVersion,
            "exchange": self.recipient,
            "tenant_id": tenant_id,
        }
        return Request(client=self.client, verb=lambda c: c.refresh_catalog, message=message)


def answer(
    request: Request[CatalogClient, T],
    *,
    signer: SigningTransport | None = None,
    after: after_signing.Alteration | None = None,
) -> T | CallError:
    """Send ``request`` once; its typed answer, or the SDK's refusal.

    ``signer`` signs it instead of the caller's own key, and ``after`` alters the
    signed request before it is sent (``harness.after_signing``), for a signature
    negative whose message is well formed and whose entries are its own, so what
    it did not store can be read back.
    """
    with after_signing.after_signing(after) as http:
        client = request.client(signer=signer, before_sign=None, http=http)
        with closing(client):
            try:
                return request.verb(client)(request.message)
            except CallError as exc:
                return exc


def refusal(
    request: Request[CatalogClient, T],
    why: str,
    *,
    signer: SigningTransport | None = None,
    after: after_signing.Alteration | None = None,
) -> CallError:
    """Send ``request`` once, as :func:`answer` does, and return the refusal ``why`` expects."""
    outcome = answer(request, signer=signer, after=after)
    assert isinstance(outcome, CallError), f"{why}: the call was answered: {outcome!r}"
    return outcome


def accepted(request: Request[CatalogClient, PushResourcesResponse]) -> PushResourcesResponse:
    """Send a push once and require it stored whole: answered, ``accepted`` its own size."""
    outcome = answer(request)
    assert not isinstance(outcome, CallError), f"the push was refused: {outcome}"
    sent = len(request.message["entries"])
    assert outcome.accepted == sent, f"accepted {outcome.accepted} of {sent}: {outcome!r}"
    return outcome


def rejection(error: CallError) -> tuple[str | None, list[str]]:
    """The refusal's ``catalog_rejection``: (reason, rejected_paths); (None, []) without one."""
    block = error.detail.catalog_rejection if error.detail is not None else None
    if block is None:
        return None, []
    return block.reason.value, list(block.rejected_paths or [])


def discover(agent: FreshAgent, exchange: str, uris: list[str], **fields: Any) -> DiscoveryResult:
    """DiscoverResources for ``uris`` at ``exchange`` as ``agent``; ``fields`` add query fields."""
    query = resource_query(exchange, uris, **fields)
    return agent.exchange(exchange).discover(query)


def group(agent: FreshAgent, exchange: str, uri: str, **fields: Any) -> OfferGroupResult:
    """The one offer group ``exchange`` answers ``agent`` for ``uri``, every offer verified."""
    found = one_group_per_uri(discover(agent, exchange, [uri], **fields), [uri], uri)[uri]
    rejected = [(r.offer.get("offer_id"), r.reason) for r in found.result.rejected]
    assert not rejected, f"offers for {uri} failed verification: {rejected}"
    return found


def offers(agent: FreshAgent, exchange: str, uri: str, **fields: Any) -> list[VerifiedOffer]:
    """Every verified offer ``exchange`` makes ``agent`` for ``uri``, in its order."""
    return list(group(agent, exchange, uri, **fields).result.verified)


def stored(agent: FreshAgent, exchange: str, uri: str) -> bool:
    """Whether ``exchange`` offers ``uri`` at all: the read-back of what a push stored."""
    return bool(offers(agent, exchange, uri))


def entry_ext(offer: VerifiedOffer) -> dict[str, Any]:
    """The offer's ``ext`` object; an empty dict when the offer carries none."""
    ext = offer.offer.get("ext") or {}
    assert isinstance(ext, dict), f"the offer's ext is not an object: {offer.offer}"
    return ext


def only_term(offer: VerifiedOffer) -> dict[str, Any]:
    """The one term an offer sells."""
    terms = offer.offer.get("terms") or []
    assert len(terms) == 1, f"an offer sells exactly one term, this one {len(terms)}: {terms}"
    term = terms[0]
    assert isinstance(term, dict), term
    return term


def sole(values: list[T], what: str) -> T:
    """The only element of ``values``."""
    assert len(values) == 1, f"want one {what}, got {len(values)}: {values}"
    return values[0]


#: The home every catalog test pushes to unless it says otherwise: the philosophy
#: publisher's domain, listed on exchange-a's seat, whose manifest authorizes the
#: operator's catalog contributor.
HOME_DOMAIN = DEMO_PHILOSOPHY_DOMAIN
HOME_EXCHANGE = EXCHANGE_A_DOMAIN
HOME_SEAT = SEAT_OF_PHILOSOPHY


def contributor(stack: StackURLs) -> Catalog:
    """The operator's catalog contributor, pushing to exchange-a."""
    return Catalog(stack.exchange, keyfile_signer(CONTRIBUTOR_KEY_PATH))


def assert_push_naming_tenant_refused(stack: StackURLs, agent: FreshAgent, tenant_id: str) -> None:
    """The contributor's push to exchange-a naming ``tenant_id`` is refused and stores nothing.

    Control: the same caller's push naming exchange-a's own seat is stored, so the
    tenant id is the only difference between the push that is stored and the one
    that is refused. The refusal must be permission_denied with catalog_rejection
    TENANT_MISMATCH; the Exchange's untyped invalid_argument is the known defect.
    """
    catalog = contributor(stack)
    control = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [control]))
    assert stored(agent, HOME_EXCHANGE, control.uri), "the control push was not stored"

    entry = fresh_entry()
    error = refusal(catalog.push(tenant_id, [entry]), f"tenant_id names {tenant_id}")
    assert not stored(agent, HOME_EXCHANGE, entry.uri), "the mismatched push was stored"
    reason, _paths = rejection(error)
    CATALOG_TENANT_MISMATCH_UNTYPED.check(
        error.code == "permission_denied" and reason is not None,
        f"refused {error.code} with catalog_rejection {reason}",
    )
    assert reason == TENANT_MISMATCH, f"refused with {reason}, want {TENANT_MISMATCH}"


def free_term() -> dict[str, Any]:
    """A FREE USD term: an offer that needs no account to be discovered."""
    return license_term(model=PRICING_MODEL_FREE, currency="USD")


def fresh_entry(
    domain: str = HOME_DOMAIN, *, terms: tuple[dict[str, Any], ...] = (), **kw: Any
) -> CatalogEntry:
    """A resource no other test or pass has pushed: a fresh path and content id."""
    tag = uuid.uuid4().hex
    return CatalogEntry(
        domain=domain,
        path=f"/articles/probe/catalog-{tag}.txt",
        content_id=f"catalog-{tag}",
        terms=terms or (free_term(),),
        **kw,
    )


def added(*entries: dict[str, Any]) -> BeforeSign:
    """A pre-signing alteration that appends ``entries`` (wire dicts) to a push."""

    def add(body: dict[str, Any]) -> None:
        body["entries"].extend(entries)

    return patch_body(add)


def broken(entry: CatalogEntry, edit: Callable[[dict[str, Any]], None]) -> dict[str, Any]:
    """``entry`` on the wire, then changed by ``edit``: an entry no SDK check would send."""
    wire = entry_wire(entry)
    edit(wire)
    return wire


def assert_typed_refusal(error: CallError, reason: str, paths: list[str]) -> None:
    """The refusal carries catalog_rejection with ``reason`` and exactly ``paths``.

    A refusal with no catalog_rejection at all is the known defect; a typed
    refusal with the wrong content fails plainly.
    """
    got, rejected = rejection(error)
    CATALOG_REFUSAL_UNTYPED.check(
        got is not None, f"refused {error.code} with no catalog_rejection: {error.detail}"
    )
    assert got == reason, f"refused with {got}, want {reason}"
    assert sorted(rejected) == sorted(paths), f"rejected_paths {rejected}, want {paths}"
