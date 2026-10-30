"""The steps of a publisher's onboarding, as the publisher tests take them.

A publisher registers in the console in a fixed order, all of it in the
add-a-domain wizard the first sign-in opens: the business, on the step the
wizard opens on while the business is unnamed; the domain, which its Next saves;
the terms, which its Next saves on the domain; and the last step, which hands
over the files and checks the site.
The site's package is in front of it from the start, so the last step's first
check finds the package answering and no file; the publisher then publishes the
file the step handed over, byte for byte, and the next check verifies the
domain. :func:`onboard` takes those steps for one fresh publisher and one fresh
site and returns what each step answered, so a test asserts on every answer and
a negative test starts from a registration that worked.

Nothing is asserted here beyond what the next step needs to run at all: a test
that asks for a registration and gets a refusal reads the refusal in the result.
"""

from __future__ import annotations

from dataclasses import dataclass

from wire.models import WellKnownManifest

from .console_client import (
    ConnectionCheck,
    DomainLookup,
    NamedBusiness,
    SavedDomain,
    SetupFile,
    Wizard,
)
from .console_dashboard import Business, business_named
from .publisher_accounts import PublisherSession
from .publisher_sites import LICENSED_PATH, Site, publish
from .stack_urls import StackURLs

#: The manifest file the wizard hands a publisher, among its setup files.
MANIFEST_FILE = "fora.json"

#: The business name every fresh publisher registers.
DISPLAY_NAME = "Stoa Press"

#: The name the business step is sent again with, as a second tab would send it.
#: It must change nothing: the business keeps its first name.
REENTERED_NAME = f"Not {DISPLAY_NAME}"

#: The price every fresh publisher charges per access, in the stack's denomination
#: (USD, per fetch). Eight decimals, the ledger's scale, with a last digit that a
#: 1000-basis-point fee does not divide evenly, so a fee that rounded instead of
#: flooring would show.
RATE = "0.12345679"


@dataclass(frozen=True)
class Onboarding:
    """What each onboarding step answered, in order.

    ``opened_on`` is the step the wizard opened on. ``business`` is the business
    step's answer, ``business_again`` its answer to the step sent again with
    another name, and ``asks_again`` whether the wizard, opened again after it,
    still has the business step. ``saved`` is the domain step's save.
    ``manifest_before`` is the discovery
    document the last step built between the two business answers, and ``files``
    and ``manifest`` what it built after the second, which is what the publisher
    publishes. ``unpublished`` is the last step's check before the file is
    published, ``connected`` its check after. ``shown`` is the business the
    domains page names once the domain is verified.
    """

    opened_on: str
    business: NamedBusiness
    lookup: DomainLookup
    saved: SavedDomain
    manifest_before: WellKnownManifest
    business_again: NamedBusiness
    asks_again: bool
    files: dict[str, SetupFile]
    manifest: WellKnownManifest
    unpublished: ConnectionCheck
    connected: ConnectionCheck
    shown: Business

    @property
    def publisher_id(self) -> str:
        """The payee id the business step answered with: the one the Exchange minted."""
        return self.business.publisher_id


#: The terms every fresh publisher sets on the licensing step: charged per
#: fetch at :data:`RATE`, for AI input.
TERMS: dict[str, object] = {
    "pricing_model": "per_unit",
    "rate": RATE,
    "permitted_functions": ["ai-input"],
    "attribution_required": False,
}


def onboard(stack: StackURLs, publisher: PublisherSession, site: Site) -> Onboarding:
    """Take every onboarding step for ``publisher`` and ``site``; return what each answered.

    The business step is taken first, and again after the last step's files are
    built, with another name, as a second tab would send it; the files are built
    again after it, so the payee each build names can be compared, and the wizard
    is opened again to see whether it still asks for the business. The
    site is published first without a discovery document, as a site is before
    its publisher puts the console's file up, and again with the file the wizard
    built last, byte for byte.
    """
    publish(stack, site, manifest=None)
    wizard = Wizard(publisher.browser, stack.console)
    opened_on = wizard.steps[0]
    business = wizard.register_publisher(DISPLAY_NAME)
    lookup = wizard.lookup(site.domain)
    saved = wizard.save_domain(site.domain, cdn=lookup.provider, content_paths=LICENSED_PATH)
    wizard.set_terms(site.domain, TERMS, sitemap_url=site.sitemap_url)
    before = _manifest_file(wizard.files(site.domain))
    business_again = wizard.register_publisher(REENTERED_NAME)
    asks_again = not Wizard(publisher.browser, stack.console).registered
    files = wizard.files(site.domain)
    manifest = _manifest_file(files)
    unpublished = wizard.check(site.domain)
    publish(stack, site, manifest=manifest.body)
    return Onboarding(
        opened_on=opened_on,
        business=business,
        lookup=lookup,
        saved=saved,
        manifest_before=before.manifest(),
        business_again=business_again,
        asks_again=asks_again,
        files=files,
        manifest=manifest.manifest(),
        unpublished=unpublished,
        connected=wizard.check(site.domain),
        shown=business_named(publisher.browser, stack.console),
    )


def _manifest_file(files: dict[str, SetupFile]) -> SetupFile:
    """The discovery document among the files the wizard built."""
    assert MANIFEST_FILE in files, f"the wizard built no {MANIFEST_FILE}: {sorted(files)}"
    return files[MANIFEST_FILE]
