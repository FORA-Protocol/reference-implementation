"""The publisher console's add-a-domain wizard, driven the way its page script drives it.

A signed-in browser (``console_login.sign_in``) makes the calls. Each method
here makes one of the wizard's JSON calls or one of the page reads a publisher
makes, requires the answer the console gives a working call, and returns it
parsed into a typed result. A test asserts on those results and never on a raw
response. The document the wizard hands over is a protocol manifest, so it is
checked by the SDK's strict check and parsed into the generated ``WellKnownManifest``.

For an account that has not named its business the wizard opens on its business
step: :meth:`Wizard.register_publisher` takes that step the way its Next does,
and the console answers a second one, with another name, with the business the
account already holds.

:func:`unsigned_registration_status` is the other side of the same flow: the
Exchange's console-only registration route, called without the console's
signature, as anyone on the network could call it. :func:`foreign_signed_list_status`
is the same side with a signature, from a signer whose key directory is not the
console's.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import UTC, datetime, time, timedelta
from http import HTTPStatus
from typing import Any

import httpx
from fora_sdk.signing_transport import SigningTransport
from wire.models import WellKnownManifest

from .console_dashboard import Period
from .console_login import NEW_DOMAIN_PATH, ConsoleBrowser, home_page, wizard_boot
from .protocol_documents import parse_document

#: The Exchange route the console registers a publisher through.
_PUBLISHER_REGISTER_PATH = "/exchange/v1/console/publishers/register"
#: The Exchange route the console reads a publisher's transaction list through.
_TRANSACTIONS_LIST_PATH = "/exchange/v1/console/transactions/list"


@dataclass(frozen=True)
class NamedBusiness:
    """What the business step's Next answered: the payee the Exchange holds for the
    account, and the name it holds the business under."""

    publisher_id: str
    display_name: str


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
class SavedDomain:
    """What the domain step's Next saved: the domain, its licensed paths, and the page
    that finishes its setup.

    The domain is held for the publisher from here on and listed as not verified,
    with no terms yet. A domain saved before is answered as it was saved.
    """

    domain: str
    content_paths: str
    setup: str


@dataclass(frozen=True)
class Refusal:
    """A call the console refused: its status, and the sentence the publisher reads."""

    status: HTTPStatus
    message: str


@dataclass(frozen=True)
class FileTask:
    """The last step's file task: whether the site serves the discovery document the
    publisher was handed, what the step says about it, and each repair it names."""

    passed: bool
    state: str
    status: str
    reasons: list[str]


@dataclass(frozen=True)
class PackageTask:
    """The last step's package task: whether the package answers in front of the site."""

    passed: bool
    state: str


@dataclass(frozen=True)
class ConnectionCheck:
    """What one check of the last step found: both tasks, and whether the domain is
    verified now.

    The console asks the Exchange to verify the domain on the check that finds
    both tasks done, so a check either leaves the domain not verified or answers
    that it is.
    """

    verified: bool
    file: FileTask
    package: PackageTask


class Wizard:
    """The add-a-domain page's calls, made the way its script makes them.

    For an account that has not named its business the page opens on the business
    step (:meth:`register_publisher`); for one that has, on the domain step.
    """

    def __init__(self, browser: ConsoleBrowser, base: str) -> None:
        self._browser = browser
        self._base = base
        page = browser.get(f"{base}{NEW_DOMAIN_PATH}")
        assert page.status_code == httpx.codes.OK, page.text[:400]
        self._boot = wizard_boot(page.text)

    @property
    def registered(self) -> bool:
        """Whether the account had named its business when the page opened."""
        return bool(self._boot["registered"])

    @property
    def steps(self) -> list[str]:
        """The keys of the steps the page opened with, in order."""
        return [str(step["key"]) for step in self._boot["steps"]]

    def register_publisher(self, display_name: str) -> NamedBusiness:
        """The business step's Next: register the business under ``display_name``."""
        named = self._call("/api/publisher", {"display_name": display_name})
        return NamedBusiness(
            publisher_id=str(named["publisher_id"]), display_name=str(named["display_name"])
        )

    def lookup(self, domain: str) -> DomainLookup:
        """The domain step's lookup: which network serves ``domain``."""
        found = self._call("/api/domain/lookup", {"domain": domain})
        return DomainLookup(
            supported=found["supported"],
            provider=found["provider"],
            taken=found["taken"],
            yours=found["yours"],
            verified=found["verified"],
            link=found.get("link", ""),
        )

    def save_domain(self, domain: str, *, cdn: str, content_paths: str) -> SavedDomain:
        """The domain step's Next: save ``domain`` behind ``cdn``, with no terms yet."""
        saved = self._call("/api/domain", _domain_request(domain, cdn, content_paths))
        return SavedDomain(
            domain=saved["domain"], content_paths=saved["content_paths"], setup=saved["setup"]
        )

    def save_domain_refused(self, domain: str, *, cdn: str, content_paths: str) -> Refusal:
        """The domain step's Next, for a domain the console refuses to save; the refusal.

        A refusal is the console's answer to a save it will not pass on, such as a
        domain another publisher holds. It is required here, so a domain that was
        saved fails the call.
        """
        response = self._browser.post_json(
            f"{self._base}/api/domain", _domain_request(domain, cdn, content_paths)
        )
        assert response.status_code != httpx.codes.OK, response.text[:400]
        return Refusal(HTTPStatus(response.status_code), response.json().get("error", ""))

    def set_terms(self, domain: str, terms: dict[str, Any], *, sitemap_url: str) -> None:
        """The licensing step's Next: save ``terms`` on the saved ``domain``."""
        self._call(
            "/api/domain/terms", {"domain": domain, "terms": terms, "sitemap_url": sitemap_url}
        )

    def files(self, domain: str) -> dict[str, SetupFile]:
        """The last step's files for the saved ``domain``, by name."""
        built = self._call("/api/domain/files", {"domain": domain})
        return {
            f["name"]: SetupFile(name=f["name"], body=f["body"], url=f["url"])
            for f in built["files"]
        }

    def check(self, domain: str) -> ConnectionCheck:
        """The last step's check of ``domain``: both tasks, and the verification."""
        out = self._call("/api/domain/check", {"domain": domain})
        file, pkg = out["file"], out["package"]
        return ConnectionCheck(
            verified=out["verified"],
            file=FileTask(
                passed=file["passed"],
                state=file["state"],
                status=file["status"],
                reasons=list(file["reasons"]),
            ),
            package=PackageTask(passed=pkg["passed"], state=pkg["state"]),
        )

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


def setup_step(browser: ConsoleBrowser, base: str, setup: str) -> str:
    """The step the page at ``setup`` reopens a saved domain's wizard on.

    The console decides it from what the Exchange holds -- the licensing step for
    a domain with no terms, the last step after that -- and renders it into the
    page's starting state, which is where this reads it.
    """
    page = browser.get(f"{base}{setup}")
    assert page.status_code == httpx.codes.OK, page.text[:400]
    resume = wizard_boot(page.text).get("resume") or {}
    return str(resume.get("step", ""))


def _domain_request(domain: str, cdn: str, content_paths: str) -> dict[str, Any]:
    """The domain step's save, as the page sends it."""
    return {"domain": domain, "cdn": cdn, "content_paths": content_paths}


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
    response = _post_signed_json(f"{exchange_url}{_TRANSACTIONS_LIST_PATH}", body, signer=signer)
    return HTTPStatus(response.status_code)


def _post_signed_json(
    url: str, body: dict[str, Any], *, signer: SigningTransport
) -> httpx.Response:
    """POST ``body`` as JSON to ``url``, signed by the SDK's ``signer``.

    For the console's transactions-list read, a console route of the Exchange that
    is not a protocol RPC, so no SDK verb sends it. The SDK's signer signs the exact
    bytes sent (RFC 9421 over the Content-Digest of this serialization), so nothing
    changes the body after it is built here.
    """
    payload = json.dumps(body, separators=(",", ":")).encode()
    signed = signer.sign_outbound(method="POST", url=url, body=payload, authorization="")
    headers = {**signed.headers, "Content-Type": "application/json"}
    return httpx.post(url, content=payload, headers=headers, timeout=30.0)
