"""Publisher self-service onboarding, driven through the console as a person would.

This exercises both halves at once: the console's add-a-domain wizard and the
Exchange's registration routes, with a real signature between them, a real login
provider in front, and a real site answering for the domain being added.

Each test signs a fresh publisher in -- a login of its own at the stack's
provider -- and a test that adds a domain adds a fresh one under sites.example,
so a second run on the same stack never meets a domain the first one saved: the
wizard's domain step saves the domain, and saving holds a hostname for the whole
deployment.

What it proves: the document the console hands a publisher names the Exchange
and the contributor the running deployment is configured with, and the package
beside it points at the same Exchange; a domain is kept after each step's Next,
listed as not verified, and its setup reopens where it stands; the Exchange's
check of a document that does not match reaches the publisher as the console's
own repair sentences; and before the business is named every page of the
console opens the wizard on its business step, while after it the console
opens empty until the first domain is saved. The journey that ends with a
verified domain is the publisher journey (obligations/test_publisher_journey.py).
"""

from __future__ import annotations

import io
import os
import tomllib
import zipfile
from http import HTTPStatus

import pytest
from wire.models import Role

from .conftest import StackURLs
from .console_client import (
    SavedDomain,
    Wizard,
    setup_step,
    unsigned_registration_status,
)
from .console_dashboard import (
    CATALOG_PATH,
    DOMAINS_PATH,
    INCOME_PATH,
    OVERVIEW_PATH,
    TRANSACTIONS_PATH,
    ConsolePage,
    console_page,
    registered_sites,
)
from .console_login import NEW_DOMAIN_PATH
from .publisher_accounts import PublisherFactory, PublisherSession
from .publisher_sites import LICENSED_PATH, Site, for_another_payee, mint_site, publish

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

# What the console says about a document that names another payee and no
# catalog contributor, in the order the Exchange reports the two rules it
# breaks. Held to the whole sentences, so a reason code that reaches the page,
# or a different reason, fails here.
PAYEE_MISMATCH = "The file names a different payee than your account. Use the fora.json from this page, unchanged."
CONTRIBUTOR_NOT_LISTED = (
    "The file does not authorise us to submit your catalogue. "
    "Use the fora.json from this page, unchanged."
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


def _saved_domain(
    stack: StackURLs, publisher: PublisherSession
) -> tuple[Wizard, Site, SavedDomain]:
    """Name the business on the wizard's first step, then take its domain step for a fresh site.

    The site has the package in front of it and no discovery document yet, as a
    site has before its publisher puts the console's file up.
    """
    site = mint_site()
    publish(stack, site, manifest=None)
    wizard = Wizard(publisher.browser, stack.console)
    assert wizard.register_publisher("Stoa Press").publisher_id.startswith("pub_")
    lookup = wizard.lookup(site.domain)
    # The site announces itself as a network the console supports, so the
    # wizard goes on.
    assert lookup.supported is True, lookup
    assert lookup.provider == "cloudflare", lookup
    assert lookup.taken is False, lookup
    saved = wizard.save_domain(site.domain, cdn=lookup.provider, content_paths=LICENSED_PATH)
    return wizard, site, saved


@in_network_only
def test_a_publisher_saves_a_domain_and_gets_a_document_naming_this_deployment(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """Sign in, save the domain, and read back the files the last step hands over.

    The assertion that matters is the document: what the running console hands
    a publisher names this stack's Exchange and the contributor the stack
    configures, and the edge package it hands over beside it names the same
    Exchange. A console deployed with settings that disagree with its Exchange
    fails here, which neither service's own tests can see.
    """
    wizard, site, saved = _saved_domain(compose_stack, fresh_publisher())
    assert saved == SavedDomain(
        domain=site.domain, content_paths=LICENSED_PATH, setup=f"/domains/{site.domain}/setup"
    ), saved

    files = wizard.files(site.domain)
    # The deployment ships edge packages, so the publisher is handed the
    # Cloudflare package in place of the settings file it carries.
    assert set(files) == {"fora.json", "fora-cloudflare.zip"}, files

    document = files["fora.json"].manifest()
    assert document.role is Role.ROLE_PUBLISHER, document
    assert document.domain == site.domain, document
    exchanges = document.exchanges or []
    assert len(exchanges) == 1, document
    owner = (exchanges[0].ext or {}).get("resource_owner_id", "")
    assert str(owner).startswith("pub_"), document
    contributors = [c.domain for c in document.catalog_contributors or []]
    assert contributors == ["catalog-contributor-e2e"], document

    # The link beside it resolves and hands over the same bytes.
    assert wizard.download(files["fora.json"].url).decode() == files["fora.json"].body

    # The package is what the publisher deploys, so it has to point their edge
    # at the same Exchange the document names. It carries none of the settings
    # that once made a discovery document at the edge: the document above is the
    # publisher's to serve from its own site. Read it the way they get it:
    # downloaded through the console, then unpacked.
    package = wizard.download(files["fora-cloudflare.zip"].url)
    with zipfile.ZipFile(io.BytesIO(package)) as archive:
        names = set(archive.namelist())
        assert names == {"fora-worker.mjs", "wrangler.toml"}, names
        project = tomllib.loads(archive.read("wrangler.toml").decode())
    settings = project["vars"]
    assert settings["EXCHANGE_URL"] == exchanges[0].endpoint, settings
    retired = {"PROVIDER", "EXCHANGES_JSON", "CATALOG_CONTRIBUTORS_JSON", "RSL_BODY"}
    assert not retired & set(settings), settings


@in_network_only
def test_before_the_first_domain_the_console_opens_empty_after_the_business_step(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """The business comes first, and then the console, empty until the first domain.

    Before the business is named, My domains and every section open the wizard on
    its business step: that step is left only through its Next. Once it is named
    -- the wizard's Finish later on the domain step goes to My domains -- My
    domains and every section answer at their own address inside the console,
    their own section marked, each showing that it is empty, read from the running
    Exchange for a publisher that holds no domain.
    """
    publisher = fresh_publisher()
    base = compose_stack.console
    pages = (DOMAINS_PATH, OVERVIEW_PATH, TRANSACTIONS_PATH, INCOME_PATH, CATALOG_PATH)

    for path in pages:
        landed = console_page(publisher.browser, base, path)
        assert landed == ConsolePage(path=NEW_DOMAIN_PATH, current="", empty=False), (path, landed)
    wizard = Wizard(publisher.browser, base)
    assert wizard.steps[0] == "name", wizard.steps

    wizard.register_publisher("Stoa Press")
    for path in pages:
        landed = console_page(publisher.browser, base, path)
        assert landed == ConsolePage(path=path, current=path, empty=True), landed
    home = console_page(publisher.browser, base, "/")
    assert home == ConsolePage(path="/", current=DOMAINS_PATH, empty=True), home
    assert registered_sites(publisher.browser, base) == []


@in_network_only
def test_a_domain_is_kept_after_each_step_and_reopens_where_it_stands(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """Finish later after either step loses nothing.

    After the domain step the domain is listed as not verified, with the Finish
    setup link, and its setup reopens on the licensing step. After the licensing
    step it reopens on the last one.
    """
    publisher = fresh_publisher()
    wizard, site, saved = _saved_domain(compose_stack, publisher)
    base = compose_stack.console

    listed = {s.domain: s for s in registered_sites(publisher.browser, base)}
    assert site.domain in listed, listed
    assert listed[site.domain].verified is False, listed[site.domain]
    # The link is drawn only on the row of a domain that is not verified.
    assert listed[site.domain].setup_link == saved.setup, listed[site.domain]
    assert setup_step(publisher.browser, base, saved.setup) == "terms"
    # Typing the domain again is answered with the domain already held, and the
    # same page, rather than a second save.
    again = wizard.lookup(site.domain)
    assert again.yours and not again.verified and again.link == saved.setup, again

    terms = {"pricing_model": "free", "permitted_functions": ["search", "ai-input"]}
    wizard.set_terms(site.domain, terms, sitemap_url=site.sitemap_url)
    assert setup_step(publisher.browser, base, saved.setup) == "connect"


@in_network_only
def test_the_last_step_names_the_repair_for_a_file_that_does_not_match(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """A document that does not match names the repair, not a bare failure.

    The site serves a document naming another payee and no catalog contributor.
    The package answers, so the package task is done; the file task names both
    rules the document breaks, in the console's own sentences, and the domain is
    not verified.
    """
    wizard, site, _ = _saved_domain(compose_stack, fresh_publisher())
    wizard.set_terms(
        site.domain,
        {"pricing_model": "free", "permitted_functions": ["search"]},
        sitemap_url=site.sitemap_url,
    )
    handed_over = wizard.files(site.domain)["fora.json"].manifest()
    publish(compose_stack, site, manifest=for_another_payee(handed_over))

    checked = wizard.check(site.domain)
    assert checked.verified is False, checked
    assert checked.package.passed is True and checked.package.state == "deployed", checked
    assert checked.file.passed is False and checked.file.state == "mismatch", checked
    assert checked.file.reasons == [PAYEE_MISMATCH, CONTRIBUTOR_NOT_LISTED], checked
    assert checked.file.status == "We read the file at your site and it does not match yet.", (
        checked
    )


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
