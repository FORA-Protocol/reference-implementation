"""The steps of a publisher's onboarding, as the publisher tests take them.

A publisher registers in the console in a fixed order: the business, then the
domain and what serves it, then the files built for it, which the publisher
publishes on the domain, then the check of the published file, then the last
button. :func:`onboard` takes those steps for one fresh publisher and one fresh
site and returns what each step answered, so a test asserts on every answer and
a negative test starts from a registration that worked.

Nothing is asserted here beyond what the next step needs to run at all: a test
that asks for a registration and gets a refusal reads the refusal in the result.
"""

from __future__ import annotations

from dataclasses import dataclass

from wire.models import WellKnownManifest

from .console_client import DomainLookup, ManifestCheck, SavedDomain, SetupFile, Wizard
from .publisher_accounts import PublisherSession
from .publisher_sites import LICENSED_PATH, Site, publish
from .stack_urls import StackURLs

#: The manifest file the wizard hands a publisher, among its setup files.
MANIFEST_FILE = "fora.json"

#: The business name every fresh publisher registers.
DISPLAY_NAME = "Stoa Press"

#: The price every fresh publisher charges per access, in the stack's denomination
#: (USD, per fetch). Eight decimals, the ledger's scale, with a last digit that a
#: 1000-basis-point fee does not divide evenly, so a fee that rounded instead of
#: flooring would show.
RATE = "0.12345679"


@dataclass(frozen=True)
class Onboarding:
    """What each onboarding step answered, in order."""

    publisher_id: str
    publisher_id_again: str
    lookup: DomainLookup
    files: dict[str, SetupFile]
    manifest: WellKnownManifest
    check: ManifestCheck
    registered: SavedDomain


def registration_request(site: Site, cdn: str) -> dict[str, object]:
    """The last wizard step's body for ``site``: charged per access at :data:`RATE`."""
    return {
        "domain": site.domain,
        "cdn": cdn,
        "content_paths": LICENSED_PATH,
        "sitemap_url": site.sitemap_url,
        "terms": {
            "pricing_model": "per_unit",
            "rate": RATE,
            "permitted_functions": ["ai-input"],
            "attribution_required": False,
        },
    }


def onboard(stack: StackURLs, publisher: PublisherSession, site: Site) -> Onboarding:
    """Take every onboarding step for ``publisher`` and ``site``; return what each answered.

    The business step is taken twice, as a double submit would, so the second
    answer can be compared with the first. The site is published first without a
    discovery document, as a site is before its publisher puts the console's file
    up, and again with the file the wizard built, byte for byte.
    """
    publish(stack, site, manifest=None)
    wizard = Wizard(publisher.browser, stack.console)
    publisher_id = wizard.register_publisher(DISPLAY_NAME)
    publisher_id_again = wizard.register_publisher(DISPLAY_NAME)
    lookup = wizard.lookup(site.domain)
    files = wizard.files(site.domain, cdn=lookup.provider, content_paths=LICENSED_PATH)
    assert MANIFEST_FILE in files, f"the wizard built no {MANIFEST_FILE}: {sorted(files)}"
    publish(stack, site, manifest=files[MANIFEST_FILE].body)
    return Onboarding(
        publisher_id=publisher_id,
        publisher_id_again=publisher_id_again,
        lookup=lookup,
        files=files,
        manifest=files[MANIFEST_FILE].manifest(),
        check=wizard.check_manifest(site.domain),
        registered=wizard.add_domain(registration_request(site, lookup.provider)),
    )
