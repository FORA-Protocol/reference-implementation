"""Publisher self-service onboarding, driven through the console as a person would.

This is the only test that exercises both halves at once: the console's
add-a-domain wizard and the Exchange's registration routes, with a real
signature between them, a real login provider in front, and a real origin
answering for the domain being added.

What it proves: the document the console hands a publisher names the Exchange
and the contributor the running deployment is configured with, and the
Exchange's check of a document that does not match reaches the publisher as
the console's own repair sentences.

It also proves that the last button keeps what the publisher entered when the
checks do not pass: the domain is saved and listed as not verified, with the
Finish setup link that reopens its setup.

What it does not prove: that a matching document verifies a domain. The origin
here serves a fixed file for another payee, and its read-only mount cannot
serve the file this test is handed, so no domain is verified in this suite. A
verified domain, read back afterwards, is covered by the console's integration
tests, which run the Exchange's real registration surface in-process.
"""

from __future__ import annotations

import io
import json
import os
import tomllib
import zipfile
from collections.abc import Iterator
from http import HTTPStatus

import pytest
from wire.models import Role

from .conftest import StackURLs
from .console_client import SavedDomain, Wizard, unsigned_registration_status
from .console_dashboard import registered_sites
from .console_login import ConsoleBrowser, sign_in
from .seed import SeededFixture

# The domain the wizard adds. It is aliased to the stack's origin, because the
# first step works out which network serves a domain by reading what it answers
# with -- a hostname that resolves to nothing never gets past it.
#
# That origin serves its own discovery document
# (tests/e2e/mock-publisher/content/.well-known/fora.json), which names another
# payee and another catalog contributor, so the document check here fails the
# way a publisher's does before they publish theirs.
SITE_DOMAIN = "stoa.example"

# The domain the last button saves. A second alias of the same origin: saving
# holds a hostname for the whole deployment, so the test that saves one uses a
# hostname the other tests never look up.
SAVED_DOMAIN = "harmonia.example"

# What the console says about that document, in the order the Exchange
# reports the two rules it breaks: the payee is someone else, and this
# deployment's contributor is not authorised. Held to the whole sentences, so
# a reason code that reaches the page, or a different reason, fails here.
PAYEE_MISMATCH = "The file names a different payee than your account. Use the one below, unchanged."
CONTRIBUTOR_NOT_LISTED = (
    "The file does not authorise us to submit your catalogue. Use the one below, unchanged."
)


# Signing in to the console goes through the login provider, and the provider
# names itself by its in-network address, which does not resolve from the host.
# A mark rather than a skip inside the fixture: marks are evaluated before any
# fixture is set up, so a host run skips here instead of seeding the whole stack
# first for a test that cannot run.
in_network_only = pytest.mark.skipif(
    os.environ.get("FORA_E2E_IN_NETWORK") != "1",
    reason="the console sign-in reaches the login provider only in-network (FORA_E2E_IN_NETWORK=1)",
)


@pytest.fixture
def console(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # The Exchange's own seat must exist before Register.
) -> Iterator[ConsoleBrowser]:
    """A browser holding a signed-in console session.

    Registration refuses every publisher until the deployment's own seat row
    exists, and on a fresh stack only the seed writes it. Without ``seeded``
    this suite passes only when another suite has seeded first.
    """
    browser = sign_in(compose_stack.console)
    yield browser
    browser.close()


def _ensure_publisher(wizard: Wizard) -> None:
    """Register the business, for a run that has not registered one yet.

    Every later step acts for that business. A stack an earlier test or run
    already used has it registered, and the wizard then skips the step.
    """
    if not wizard.registered:
        assert wizard.register_publisher("Stoa Press").startswith("pub_")


@in_network_only
def test_a_publisher_walks_the_wizard_and_gets_a_document_naming_this_deployment(
    compose_stack: StackURLs, console: ConsoleBrowser
) -> None:
    """Sign in, walk the steps, and read back what each one produced.

    The assertion that matters is the document: what the running console hands
    a publisher names this stack's Exchange and the contributor the stack
    configures, and the edge package it hands over beside it names the same
    two. A console deployed with settings that disagree with its Exchange fails
    here, which neither service's own tests can see. This test does not submit
    the document to the Exchange; see the module docstring.

    The second assertion is the ordering. Every call below is made before the
    domain exists, and the domain still does not exist when they are done: the
    publisher has to publish the document first, so the document has to be
    produced for a domain that has no row to produce it from.
    """
    wizard = Wizard(console, compose_stack.console)

    # Step one, for a run that has not registered a business yet.
    _ensure_publisher(wizard)

    # Step two: the network. The origin announces itself as one the console
    # supports, so the wizard goes on.
    lookup = wizard.lookup(SITE_DOMAIN)
    assert lookup.supported is True, lookup
    assert lookup.provider == "cloudflare", lookup
    assert lookup.taken is False, lookup

    # Step three: the file, built for a domain that is not registered.
    files = wizard.files(SITE_DOMAIN, cdn=lookup.provider, content_paths="/articles/")
    # The deployment ships edge packages, so the publisher is handed the
    # Cloudflare package in place of the settings file it carries.
    assert set(files) == {"fora.json", "fora-cloudflare.zip"}, files

    document = files["fora.json"].manifest()
    assert document.role is Role.ROLE_PUBLISHER, document
    assert document.domain == SITE_DOMAIN, document
    exchanges = document.exchanges or []
    assert len(exchanges) == 1, document
    owner = (exchanges[0].ext or {}).get("resource_owner_id", "")
    assert str(owner).startswith("pub_"), document
    contributors = [c.domain for c in document.catalog_contributors or []]
    assert contributors == ["catalog-contributor-e2e"], document

    # The link beside it resolves and hands over the same bytes.
    assert wizard.download(files["fora.json"].url).decode() == files["fora.json"].body

    # The package is what the publisher deploys, so it has to point their edge
    # at the same Exchange and contributor the document names. Read it the way
    # they get it: downloaded through the console, then unpacked.
    package = wizard.download(files["fora-cloudflare.zip"].url)
    with zipfile.ZipFile(io.BytesIO(package)) as archive:
        names = set(archive.namelist())
        assert names == {"fora-worker.mjs", "wrangler.toml"}, names
        project = tomllib.loads(archive.read("wrangler.toml").decode())
    settings = project["vars"]
    assert settings["PROVIDER"] == SITE_DOMAIN, settings
    [edge_exchange] = json.loads(settings["EXCHANGES_JSON"])
    assert edge_exchange["domain"] == exchanges[0].domain, (edge_exchange, document)
    assert edge_exchange["endpoint"] == exchanges[0].endpoint, (edge_exchange, document)
    assert settings["EXCHANGE_URL"] == exchanges[0].endpoint, settings
    edge_contributors = [c["domain"] for c in json.loads(settings["CATALOG_CONTRIBUTORS_JSON"])]
    assert edge_contributors == contributors, (edge_contributors, document)

    # Nothing has been registered by any of it.
    home = wizard.home_page()
    assert SITE_DOMAIN not in home, home[:600]


@in_network_only
def test_the_console_reports_why_an_unpublished_domain_is_not_confirmed(
    compose_stack: StackURLs, console: ConsoleBrowser
) -> None:
    """A domain whose file does not match names the repair, not a bare failure.

    Every first-time setup passes through this state, so the wizard has to tell
    the publisher which file to put where. The check writes nothing: the domain
    is saved only by the last button, which this test does not press.
    """
    wizard = Wizard(console, compose_stack.console)
    _ensure_publisher(wizard)

    wizard.lookup(SITE_DOMAIN)
    checked = wizard.check_manifest(SITE_DOMAIN)
    assert checked.passed is False, checked
    # The reason is the console's sentence for the Exchange's code, not the
    # code, for each rule the origin's document breaks.
    assert checked.reasons == [PAYEE_MISMATCH, CONTRIBUTOR_NOT_LISTED], checked

    assert SITE_DOMAIN not in wizard.home_page()


@in_network_only
def test_a_domain_saved_before_its_checks_pass_is_listed_not_verified(
    compose_stack: StackURLs, console: ConsoleBrowser
) -> None:
    """The last button keeps a domain whose checks do not pass, as not verified.

    The origin serves a document naming another payee and has no package in
    front of it, so the domain cannot be verified. The publisher still keeps
    what they entered: the domain is listed, marked not verified, with the
    Finish setup link that reopens its setup. On a stack that already holds it
    from an earlier run, the lookup answers that the domain is already this
    publisher's, and the same button answers the same.
    """
    wizard = Wizard(console, compose_stack.console)
    _ensure_publisher(wizard)
    setup_link = f"/domains/{SAVED_DOMAIN}/setup"

    lookup = wizard.lookup(SAVED_DOMAIN)
    if lookup.yours:
        assert lookup.verified is False, lookup
        assert lookup.link == setup_link, lookup
        # Nothing about the network is looked up for a domain already held;
        # the earlier run asserted it is this one.
        cdn = "cloudflare"
    else:
        # The alias puts the origin behind this name, as a supported network.
        assert lookup.supported is True, lookup
        assert lookup.provider == "cloudflare", lookup
        assert lookup.taken is False, lookup
        cdn = lookup.provider

    finished = wizard.add_domain(
        {
            "domain": SAVED_DOMAIN,
            "cdn": cdn,
            "content_paths": "/articles/",
            "sitemap_url": "",
            "terms": {
                "pricing_model": "free",
                "permitted_functions": ["search", "ai-input"],
                "attribution_required": False,
            },
        },
    )
    assert finished == SavedDomain(domain=SAVED_DOMAIN, verified=False), finished

    listed = {s.domain: s for s in registered_sites(console, compose_stack.console)}
    assert SAVED_DOMAIN in listed, listed
    assert listed[SAVED_DOMAIN].verified is False, listed[SAVED_DOMAIN]
    # The link is drawn only on the row of a domain that is not verified.
    assert listed[SAVED_DOMAIN].setup_link == setup_link, listed[SAVED_DOMAIN]


def test_the_exchange_refuses_a_caller_that_is_not_the_console(
    compose_stack: StackURLs,
) -> None:
    """The registration routes are closed to anything but the configured console.

    This is the property the whole actor assertion rests on. An unsigned POST
    carrying a well-formed actor must be refused, or anyone who can reach the
    Exchange could act as any publisher.
    """
    status = unsigned_registration_status(
        compose_stack.exchange,
        {
            "actor": {
                "oidc_issuer": "https://login.example",
                "oidc_subject": "intruder",
                "email": "intruder@elsewhere.example",
                "email_verified": True,
            },
            "display_name": "Not Ours",
        },
    )
    assert status == HTTPStatus.UNAUTHORIZED, status
