"""The publisher console's add-a-domain wizard, driven the way its page script drives it.

A signed-in browser (``console_login.sign_in``) makes the calls. Each method
here makes one of the wizard's JSON calls or one of the page reads a publisher
makes, requires the answer the console gives a working call, and returns it
parsed into a typed result. A test asserts on those results and never on a raw
response. The document the wizard hands over is a protocol manifest, so it is
checked by the SDK's strict check and parsed into the generated ``WellKnownManifest``.

:func:`unsigned_registration_status` is the other side of the same flow: the
Exchange's console-only registration route, called without the console's
signature, as anyone on the network could call it. :func:`foreign_signed_list_status`
is the same side with a signature, from a signer whose key directory is not the
console's.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import UTC, datetime, time, timedelta
from http import HTTPStatus
from typing import Any

import httpx
from fora_sdk.signing_transport import SigningTransport
from wire.models import WellKnownManifest

from .console_dashboard import Period
from .console_login import ConsoleBrowser, home_page, wizard_boot
from .protocol_documents import parse_document
from .sdk_client import post_signed_json

#: The Exchange route the console registers a publisher through.
_PUBLISHER_REGISTER_PATH = "/exchange/v1/console/publishers/register"
#: The Exchange route the console reads a publisher's transaction list through.
_TRANSACTIONS_LIST_PATH = "/exchange/v1/console/transactions/list"


@dataclass(frozen=True)
class DomainLookup:
    """What the wizard learned about a domain: which network serves it, whether it
    is supported and still free to save, and whether the person already holds it.

    ``yours`` is set when the domain is already one of the person's; ``verified``
    then says whether it is verified, and ``link`` is where to go instead of adding
    it again: its page when it is verified, the page that finishes its setup when
    it is not.
    """

    supported: bool
    provider: str
    taken: bool
    yours: bool
    verified: bool
    link: str


@dataclass(frozen=True)
class SetupFile:
    """One file the wizard hands a publisher: its name, its body and its download link."""

    name: str
    body: str
    url: str

    def manifest(self) -> WellKnownManifest:
        """The file read as the protocol manifest it is (``fora.json`` only)."""
        return parse_document(WellKnownManifest, self.body)


@dataclass(frozen=True)
class ManifestCheck:
    """The console's verdict on a domain's published document, with its repair sentences."""

    passed: bool
    reasons: list[str]


@dataclass(frozen=True)
class SavedDomain:
    """What the last button did: the domain it saved, and whether it is verified now.

    The console saves a domain whatever its checks find, and verifies it when it
    has terms, the Exchange accepts the published document, and the site's
    package answers.
    """

    domain: str
    verified: bool


class Wizard:
    """The add-a-domain page's calls, made the way its script makes them."""

    def __init__(self, browser: ConsoleBrowser, base: str) -> None:
        self._browser = browser
        self._base = base
        page = browser.get(f"{base}/domains/new")
        assert page.status_code == httpx.codes.OK, page.text[:400]
        self._boot = wizard_boot(page.text)

    @property
    def registered(self) -> bool:
        """Whether the signed-in account had already registered a business."""
        return bool(self._boot["registered"])

    def register_publisher(self, display_name: str) -> str:
        """Step one: register the business; returns its publisher id."""
        return str(self._call("/api/publisher", {"display_name": display_name})["publisher_id"])

    def lookup(self, domain: str) -> DomainLookup:
        """Step two: which network serves ``domain``."""
        found = self._call("/api/domain/lookup", {"domain": domain})
        return DomainLookup(
            supported=found["supported"],
            provider=found["provider"],
            taken=found["taken"],
            yours=found["yours"],
            verified=found["verified"],
            link=found.get("link", ""),
        )

    def files(self, domain: str, *, cdn: str, content_paths: str) -> dict[str, SetupFile]:
        """Step three: the files built for ``domain``, by name."""
        built = self._call(
            "/api/domain/files", {"domain": domain, "cdn": cdn, "content_paths": content_paths}
        )
        return {
            f["name"]: SetupFile(name=f["name"], body=f["body"], url=f["url"])
            for f in built["files"]
        }

    def check_manifest(self, domain: str) -> ManifestCheck:
        """Ask the console whether ``domain`` publishes the document it was handed."""
        return _check(self._call("/api/domain/check-manifest", {"domain": domain}))

    def add_domain(self, request: dict[str, Any]) -> SavedDomain:
        """The last button: save the domain, and verify it when its checks pass."""
        saved = self._call("/api/domain", request)
        return SavedDomain(domain=saved["domain"], verified=saved["verified"])

    def add_domain_refused(self, request: dict[str, Any]) -> HTTPStatus:
        """The last button, for a registration the console refuses outright; its status.

        A refusal is the console's answer to a request it will not pass on, such
        as a domain another publisher holds. It is required here, so a domain that
        was saved fails the call.
        """
        response = self._browser.post_json(f"{self._base}/api/domain", request)
        assert response.status_code != httpx.codes.OK, response.text[:400]
        return HTTPStatus(response.status_code)

    def download(self, link: str) -> bytes:
        """Follow a download link the wizard handed over; the bytes it serves."""
        response = self._browser.get(f"{self._base}{link}")
        assert response.status_code == httpx.codes.OK, response.text[:400]
        return response.content

    def home_page(self) -> str:
        """The console's home page, as the signed-in publisher sees it."""
        return home_page(self._browser, self._base)

    def _call(self, path: str, body: dict[str, Any]) -> dict[str, Any]:
        response = self._browser.post_json(f"{self._base}{path}", body)
        assert response.status_code == httpx.codes.OK, (
            path,
            response.status_code,
            response.text[:400],
        )
        answer = response.json()
        assert isinstance(answer, dict), (path, answer)
        return answer


def _check(answer: dict[str, Any]) -> ManifestCheck:
    return ManifestCheck(passed=answer["passed"], reasons=list(answer["reasons"]))


def unsigned_registration_status(exchange_url: str, request: dict[str, Any]) -> HTTPStatus:
    """POST ``request`` to the Exchange's console registration route with no signature."""
    response = httpx.post(f"{exchange_url}{_PUBLISHER_REGISTER_PATH}", json=request, timeout=10.0)
    return HTTPStatus(response.status_code)


def wizard_call_status(
    browser: ConsoleBrowser, base: str, path: str, body: dict[str, Any]
) -> HTTPStatus:
    """The status one of the wizard's JSON calls is answered with, whatever it is.

    For a browser that may not hold a working session, where :class:`Wizard`,
    which requires one to open, cannot be used.
    """
    return HTTPStatus(browser.post_json(f"{base}{path}", body).status_code)


@dataclass(frozen=True)
class Actor:
    """The person a console call acts for, as the console asserts it to the Exchange."""

    oidc_issuer: str
    oidc_subject: str
    email: str


def foreign_signed_list_status(
    exchange_url: str, signer: SigningTransport, *, actor: Actor, publisher_id: str, period: Period
) -> HTTPStatus:
    """The status the Exchange answers a transaction-list read with when ``signer`` signs it.

    The body is the read the console itself sends for ``actor``'s publisher over
    ``period``, so the signature is the only thing that differs from the console's
    own call.
    """
    start = datetime.combine(period.first, time(), tzinfo=UTC)
    end = datetime.combine(period.last + timedelta(days=1), time(), tzinfo=UTC)
    body = {
        "actor": {
            "oidc_issuer": actor.oidc_issuer,
            "oidc_subject": actor.oidc_subject,
            "email": actor.email,
            "email_verified": True,
        },
        "publisher_id": publisher_id,
        "period": {"start": start.isoformat(), "end": end.isoformat()},
    }
    response = post_signed_json(f"{exchange_url}{_TRANSACTIONS_LIST_PATH}", body, signer=signer)
    return HTTPStatus(response.status_code)
