"""A per-test publisher site: a fresh domain and the files it serves.

A domain can be registered in the console once, so a publisher test that must
pass twice on one stack needs a domain of its own each time. Each test claims a
fresh host under :data:`SITE_ZONE`; the stack's wildcard-DNS sidecar resolves
every name in that zone to the ``sites`` service (``tests/e2e/sites/server.py``),
which serves whatever files the test registered for the host.

A site here is what the publisher journey needs a site to be:

* a home page, which the console's CDN lookup reads;
* the discovery document the console handed the publisher, published at
  ``/.well-known/fora.json`` byte for byte as the console produced it, which the
  Exchange reads before it activates the listing and at every catalog push;
* a sitemap, which the catalog importer walks, listing two pages inside the
  licensed path and one outside it;
* those three pages;
* optionally, the site's own Web Bot Auth key directory, for a test in which the
  publisher signs its own catalog calls as its domain (:func:`mint_site_key`);
* the publisher's edge package in front of the licensed path, which the console
  asks about before it verifies a saved domain. The sites service answers an AI
  agent there the way the package does.

The files are registered over HTTP; nothing is written to a volume. A test that
changes what the site publishes registers the site again with the new files.
"""

from __future__ import annotations

import json
import secrets
import time
from dataclasses import dataclass
from typing import Any

import httpx
from fora_sdk import to_wire
from fora_sdk.directory_signature import sign_directory_response
from fora_sdk.identity import directory_document, generate_key
from fora_sdk.signing_transport import SigningTransport
from wire.models import WBAFile, WellKnownManifest

from .fresh_agent import DIRECTORY_PATH
from .httpsig_signer import signing_transport
from .stack_urls import StackURLs

#: The DNS zone every per-test site is minted in. The stack's wildcard-DNS
#: sidecar resolves the whole zone to the ``sites`` service;
#: ``test_guards_publisher_sites.py`` fails when the compose file and this value
#: disagree. ``.example`` is reserved (RFC 6761), so no site can name a real party.
SITE_ZONE = "sites.example"

#: The one path scope every per-test site licenses, as the console's wizard
#: takes it (``content_paths``).
LICENSED_PATH = "/articles/"

#: Where a site's discovery document is published.
MANIFEST_PATH = "/.well-known/fora.json"

#: The media type the Web Bot Auth profile serves a key directory with.
_DIRECTORY_TYPE = "application/http-message-signatures-directory+json"
#: A directory's response signature: open a minute in the past, valid for a day.
_SIGNATURE_BACKDATE_SEC = 60
_SIGNATURE_LIFETIME_SEC = 24 * 60 * 60

_REGISTER_PATH = "/sites/"
_TIMEOUT_SEC = 10.0
_HTML = "text/html; charset=utf-8"


@dataclass(frozen=True)
class Site:
    """One test's site: its domain and the page addresses its sitemap lists.

    ``licensed`` are inside :data:`LICENSED_PATH` and ``unlicensed`` outside it;
    both are absolute ``http`` URLs, the scheme every service in this stack
    answers on and the scheme the Exchange stores catalog addresses with here.
    """

    domain: str
    licensed: tuple[str, ...]
    unlicensed: tuple[str, ...]

    @property
    def origin(self) -> str:
        return f"http://{self.domain}"

    @property
    def sitemap_url(self) -> str:
        return f"{self.origin}/sitemap.xml"

    @property
    def pages(self) -> tuple[str, ...]:
        return self.licensed + self.unlicensed


def mint_site() -> Site:
    """A site under a fresh host in :data:`SITE_ZONE`, with two licensed pages and one not."""
    label = f"pub{secrets.token_hex(6)}"
    origin = f"http://{label}.{SITE_ZONE}"
    return Site(
        domain=f"{label}.{SITE_ZONE}",
        licensed=(
            f"{origin}{LICENSED_PATH}first-story.html",
            f"{origin}{LICENSED_PATH}second-story.html",
        ),
        unlicensed=(f"{origin}/members/briefing.html",),
    )


def html_page(title: str) -> dict[str, str]:
    """An HTML page whose ``<title>`` and headline are ``title``, as a site file."""
    body = f"<!doctype html><html><head><title>{title}</title></head><body><h1>{title}</h1></body></html>"
    return {"type": _HTML, "body": body}


def _sitemap(site: Site) -> dict[str, str]:
    urls = "".join(f"<url><loc>{page}</loc></url>" for page in site.pages)
    body = (
        '<?xml version="1.0" encoding="UTF-8"?>'
        f'<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">{urls}</urlset>'
    )
    return {"type": "application/xml", "body": body}


@dataclass(frozen=True)
class SiteKey:
    """A key the site's publisher signs its own catalog calls with, as its domain.

    ``signer`` is the SDK's request signer under the site's domain, and
    ``directory`` the Web Bot Auth document publishing the key, serialized, for
    :func:`publish` to serve on the site. ``directory_headers`` is the response
    signature the profile requires of that document, made by the key for the
    site's host through the SDK, which the site serves with it.
    """

    signer: SigningTransport
    directory: str
    directory_headers: dict[str, str]


def mint_site_key(site: Site) -> SiteKey:
    """A fresh key for ``site``'s publisher, signing as the site's own domain.

    A catalog push signed this way is the publisher's own: the Exchange fetches
    the key from the directory at the domain the signer names, and that domain is
    the one the entries live on. The directory reaches the site only when
    :func:`publish` is given it.
    """
    key, _thumbprint = generate_key()
    document = WBAFile.model_validate(directory_document([key.public_key()]))
    body = json.dumps(to_wire(document))
    now = int(time.time())
    signature = sign_directory_response(
        site.domain,
        body.encode(),
        [key.private_bytes_raw()],
        now - _SIGNATURE_BACKDATE_SEC,
        now + _SIGNATURE_LIFETIME_SEC,
    )
    return SiteKey(signing_transport(site.domain, key), body, signature.headers())


def publish(
    stack: StackURLs, site: Site, *, manifest: str | None, key: SiteKey | None = None
) -> None:
    """Register every file ``site`` serves, replacing what it served before.

    ``manifest`` is the discovery document to publish, verbatim; None publishes
    none, which is the state of a site before the publisher has put the
    console's file up. ``key`` is the site's own key, whose signed directory the
    site serves (:attr:`SiteKey.directory`); None publishes none. The site always
    has the edge package in front of :data:`LICENSED_PATH`, sending refused agents
    to the stack's Exchange, the one the console registers domains at.
    """
    files: dict[str, dict[str, Any]] = {"/": html_page(f"Home of {site.domain}")}
    files["/sitemap.xml"] = _sitemap(site)
    for page in site.pages:
        files[page.removeprefix(site.origin)] = html_page(page.rsplit("/", 1)[-1])
    if manifest is not None:
        files[MANIFEST_PATH] = {"type": "application/json", "body": manifest}
    if key is not None:
        files[DIRECTORY_PATH] = {
            "type": _DIRECTORY_TYPE,
            "body": key.directory,
            "headers": key.directory_headers,
        }
    register_files(
        stack,
        site.domain,
        files,
        package={"guards": [LICENSED_PATH], "exchange_origin": stack.exchange},
    )


def register_files(
    stack: StackURLs,
    domain: str,
    files: dict[str, dict[str, Any]],
    *,
    package: dict[str, object] | None = None,
) -> None:
    """Make the sites service serve exactly ``files`` for ``domain``, replacing what it served.

    ``files`` maps an absolute path to ``{"type": <media type>, "body": <text>}``,
    with an optional ``"headers"`` map served with the file.
    ``domain`` must be one label under :data:`SITE_ZONE`; the service refuses
    anything else. ``package``, ``{"guards": [<prefix>], "exchange_origin":
    <origin>}``, puts the publisher's edge package in front of those path
    prefixes; None serves the site with no package.
    """
    assert stack.sites, "this stack runs no sites service to publish a site at"
    body: dict[str, object] = {"files": files}
    if package is not None:
        body["package"] = package
    response = httpx.put(f"{stack.sites}{_REGISTER_PATH}{domain}", json=body, timeout=_TIMEOUT_SEC)
    assert response.status_code == httpx.codes.NO_CONTENT, (
        f"the sites service refused {domain}: {response.status_code} {response.text}"
    )


def without_contributors(manifest: WellKnownManifest) -> str:
    """``manifest`` with its catalog contributors removed, serialized by the SDK.

    What a publisher serves after revoking the operator's catalog contributor:
    the same document, naming no one who may push its catalog.
    """
    revoked = manifest.model_copy(update={"catalog_contributors": []})
    return json.dumps(to_wire(revoked))
