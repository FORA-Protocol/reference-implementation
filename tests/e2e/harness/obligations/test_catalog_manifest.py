"""Catalog obligations the publisher's own manifest decides: the resource owner and its version.

CATALOG-006 (ADR-026: the manifest fetched for a push must list this seat with
the registered publisher's id; a resource-owner mismatch is refused
NOT_CATALOG_CONTRIBUTOR, and a manifest that does not list this Exchange
EXCHANGE_NOT_LISTED) and CATALOG-008 (``fora.proto``: a manifest of an
unrecognised major version is refused, an unrecognised minor accepted; a
manifest the Exchange cannot read refuses the push NOT_CATALOG_CONTRIBUTOR).

Each test onboards a fresh publisher through the console (``onboard``), so the
domain is registered and listed on exchange-a and serves the manifest the console
built. It then pushes a page of that domain directly, as the operator's catalog
contributor the manifest authorizes, through the SDK's ``CatalogClient``, and
reads the page back through ``DiscoverResources`` as the test's own agent. A
manifest change is made by republishing the site's files (``publish``); the
sites service answers ``Cache-Control: no-store``, so the Exchange reads the new
document on the next push.
"""

from __future__ import annotations

import json
from typing import Any

import pytest
from fora_sdk import to_wire
from fora_sdk.client import CallError

from ..catalog_calls import (
    EXCHANGE_NOT_LISTED,
    HOME_EXCHANGE,
    HOME_SEAT,
    NOT_CATALOG_CONTRIBUTOR,
    accepted,
    assert_typed_refusal,
    contributor,
    fresh_entry,
    refusal,
    stored,
)
from ..defects import CATALOG_REFUSAL_UNTYPED
from ..exchanges import EXCHANGE_B_DOMAIN
from ..fresh_agent import FreshAgent
from ..obligation_registry import Obligation, obligation
from ..publisher_accounts import PublisherFactory
from ..publisher_journey import onboard
from ..publisher_sites import Site, mint_site, publish
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _onboarded(stack: StackURLs, factory: PublisherFactory) -> tuple[Site, dict[str, Any]]:
    """A fresh publisher's registered site, and the manifest it serves, as its wire dict."""
    site = mint_site()
    registration = onboard(stack, factory(), site)
    assert registration.registered.verified, registration.registered
    return site, to_wire(registration.manifest)


def _republish(stack: StackURLs, site: Site, manifest: dict[str, Any]) -> None:
    publish(stack, site, manifest=json.dumps(manifest))


def _with(manifest: dict[str, Any], **changes: Any) -> dict[str, Any]:
    edited = json.loads(json.dumps(manifest))
    edited.update(changes)
    return edited


@obligation(Obligation.CATALOG_006, Obligation.CATALOG_008)
def test_a_registered_publishers_page_is_pushed_and_a_new_minor_version_is_read(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory, fresh_agent: FreshAgent
) -> None:
    """The console's manifest admits the push; so does the same manifest at version 1.7."""
    site, manifest = _onboarded(compose_stack, fresh_publisher)
    catalog = contributor(compose_stack)
    first = fresh_entry(site.domain)
    accepted(catalog.push(HOME_SEAT, [first]))
    assert stored(fresh_agent, HOME_EXCHANGE, first.uri), (
        "the registered publisher's page was not stored"
    )

    _republish(compose_stack, site, _with(manifest, ver="1.7"))
    second = fresh_entry(site.domain)
    accepted(catalog.push(HOME_SEAT, [second]))
    assert stored(fresh_agent, HOME_EXCHANGE, second.uri), "a 1.7 manifest refused the push"


def _refused(
    stack: StackURLs, site: Site, served: dict[str, Any], why: str, agent: FreshAgent
) -> tuple[CallError, str]:
    """Serve ``served`` as the site's manifest, push one page, require it refused and unstored."""
    _republish(stack, site, served)
    entry = fresh_entry(site.domain)
    error = refusal(contributor(stack).push(HOME_SEAT, [entry]), why)
    assert not stored(agent, HOME_EXCHANGE, entry.uri), f"{why}: the page was stored"
    return error, entry.path


@obligation(Obligation.CATALOG_006, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_manifest_naming_another_resource_owner_refuses_the_push_not_catalog_contributor(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory, fresh_agent: FreshAgent
) -> None:
    """The manifest names another publisher id as this Exchange's resource owner.

    The signer may not push for a resource owner it is not authorized for, so
    the push is refused NOT_CATALOG_CONTRIBUTOR, naming the page.
    """
    site, manifest = _onboarded(compose_stack, fresh_publisher)
    other_owner = json.loads(json.dumps(manifest.get("exchanges") or []))
    assert other_owner, f"the console's manifest lists no Exchange: {manifest}"
    for listed in other_owner:
        listed.setdefault("ext", {})["resource_owner_id"] = "pub_someone_else"
    error, path = _refused(
        compose_stack,
        site,
        _with(manifest, exchanges=other_owner),
        "another publisher id as resource owner",
        fresh_agent,
    )
    assert_typed_refusal(error, NOT_CATALOG_CONTRIBUTOR, [path])


@obligation(Obligation.CATALOG_006, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_manifest_that_does_not_list_this_exchange_refuses_the_push_exchange_not_listed(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory, fresh_agent: FreshAgent
) -> None:
    """The manifest lists only another Exchange: refused EXCHANGE_NOT_LISTED, naming the page."""
    site, manifest = _onboarded(compose_stack, fresh_publisher)
    elsewhere = json.loads(json.dumps(manifest.get("exchanges") or []))
    assert elsewhere, f"the console's manifest lists no Exchange: {manifest}"
    for listed in elsewhere:
        listed["domain"] = EXCHANGE_B_DOMAIN
    error, path = _refused(
        compose_stack,
        site,
        _with(manifest, exchanges=elsewhere),
        "only another Exchange listed",
        fresh_agent,
    )
    assert_typed_refusal(error, EXCHANGE_NOT_LISTED, [path])


@obligation(Obligation.CATALOG_008, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_manifest_of_an_unknown_major_version_refuses_the_push_not_catalog_contributor(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory, fresh_agent: FreshAgent
) -> None:
    """The manifest carries ver 2.0: the push is refused NOT_CATALOG_CONTRIBUTOR, naming the page.

    The Exchange cannot read a manifest of an unknown major version, so the
    manifest authorizes no contributor: the push is refused NOT_CATALOG_CONTRIBUTOR.
    The protocol has no separate reason for an unsupported manifest.
    """
    site, manifest = _onboarded(compose_stack, fresh_publisher)
    error, path = _refused(
        compose_stack, site, _with(manifest, ver="2.0"), "manifest version 2.0", fresh_agent
    )
    assert_typed_refusal(error, NOT_CATALOG_CONTRIBUTOR, [path])
