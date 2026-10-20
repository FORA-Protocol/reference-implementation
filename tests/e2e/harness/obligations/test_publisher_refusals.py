"""The publisher journey's refusals that need no sale, each beside its control.

* A person whose address the login provider has not verified gets no publisher
  functionality, while a verified one signs in to the console's home page
  (PUB-005).
* A domain one publisher registered cannot be registered by a second account,
  while it stays verified for the first (PUB-007).
* A publisher that removes the operator's contributor from its published file
  stops the next import, and its catalog keeps the pages the import before had
  put there (PUB-012).
* The Exchange answers RequestDomainVerification ``unimplemented``, while the same
  agent's signed call to an implemented RPC at the same Exchange is answered
  (PUB-017).

The journey's refusals that are only meaningful once a page has sold (another
publisher's transaction, a signer from another directory) are in
``test_publisher_journey.py``, after the sale they are about.

Every test claims its own logins, domain and agent, so each passes again on a
stack it has already run on (ADR-028 D7).
"""

from __future__ import annotations

from contextlib import closing
from urllib.parse import urlsplit

import pytest
from fora_sdk import ProtocolVersion
from fora_sdk.client import CallError
from wire.models import DomainVerificationRequest

from ..conftest import StackURLs
from ..console_client import Wizard, wizard_call_status
from ..console_dashboard import catalog, registered_sites
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..importer import COMPLETE, run_import
from ..obligation_registry import Obligation, obligation
from ..publisher_accounts import PublisherFactory
from ..publisher_journey import DISPLAY_NAME, onboard, registration_request
from ..publisher_sites import mint_site, publish, without_contributors
from ..sdk_client import sdk_domain_verification_client

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The wizard's business step, the first call that would mint a publisher.
_PUBLISHER_CALL = "/api/publisher"

#: The login provider's step for a person whose address it has not verified, as
#: the path its form submits to. The provider asks for the code it mails to the
#: address before it lets the person back to any client, so an unverified login
#: never returns to the console with a session.
_VERIFY_STEP = "/ui/login/user/init"


@obligation(Obligation.PUB_005, negative=True)
def test_an_unverified_address_gets_no_publisher_functionality(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """The provider has not verified the address, so the person never acts as a publisher.

    Control: a login whose address is verified signs in to the console's home page
    and its business step is answered.
    """
    verified = fresh_publisher(verified=True)
    assert verified.signed_in, verified.landed
    assert Wizard(verified.browser, compose_stack.console).register_publisher(DISPLAY_NAME)

    unverified = fresh_publisher(verified=False)
    assert not unverified.signed_in, f"an unverified address reached {unverified.landed}"
    # The provider stopped at its own step for an address it has not verified, which
    # it asks for before the password, so the person never reached the console with
    # a session.
    # The console's own gate for an unverified address (its verify-email page) is
    # behind this one and is not reached with this provider.
    console_host = urlsplit(compose_stack.console).netloc
    assert urlsplit(unverified.landed).netloc != console_host, unverified.landed
    assert any(_VERIFY_STEP in action for action in unverified.form_actions), (
        f"the sign-in stopped at {unverified.form_actions}, not at the address check: "
        f"{unverified.page[:400]}"
    )
    refused = wizard_call_status(
        unverified.browser, compose_stack.console, _PUBLISHER_CALL, {"display_name": DISPLAY_NAME}
    )
    assert refused == 401, refused


@obligation(Obligation.PUB_007, negative=True)
def test_a_second_account_cannot_register_a_registered_domain(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """The domain is the first publisher's; the second is told so and registers nothing."""
    console = compose_stack.console
    first = fresh_publisher()
    site = mint_site()
    owned = onboard(compose_stack, first, site)
    assert owned.registered.verified, owned.registered

    second = fresh_publisher()
    wizard = Wizard(second.browser, console)
    wizard.register_publisher(f"Not {DISPLAY_NAME}")
    lookup = wizard.lookup(site.domain)
    assert lookup.taken is True, lookup
    refused = wizard.add_domain_refused(registration_request(site, lookup.provider))
    assert refused == 400, refused

    assert registered_sites(second.browser, console) == [], "the domain reached the second account"
    mine = {s.domain: s.verified for s in registered_sites(first.browser, console)}
    assert mine == {site.domain: True}, mine


@obligation(Obligation.PUB_012, negative=True)
def test_removing_the_contributor_stops_the_import_and_keeps_the_catalog(
    compose_stack: StackURLs, fresh_publisher: PublisherFactory
) -> None:
    """The publisher takes the operator's authorization back; the next import is refused whole.

    Control: the run before the change, with the contributor authorized, completes.
    """
    console = compose_stack.console
    publisher = fresh_publisher()
    site = mint_site()
    registration = onboard(compose_stack, publisher, site)
    assert registration.registered.verified, registration.registered
    authorized = run_import(site.domain)
    assert (authorized.outcome, authorized.accepted) == (COMPLETE, len(site.licensed)), authorized
    before = catalog(publisher.browser, console)
    assert sorted(row.uri for row in before) == sorted(site.licensed), before

    publish(compose_stack, site, manifest=without_contributors(registration.manifest))
    revoked = run_import(site.domain)
    assert revoked.outcome == "failed" and revoked.accepted == 0, revoked
    assert catalog(publisher.browser, console) == before


@obligation(Obligation.PUB_017, negative=True)
def test_request_domain_verification_is_unimplemented(
    compose_stack: StackURLs, fresh_agent: FreshAgent
) -> None:
    """The HTTP-01 pair is not this Exchange's proof of control (ADR-026 D3).

    Control: the same agent, signing as itself, reads its account at the same
    Exchange, so a refusal here is the RPC's and not the signature gate's.
    """
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    assert fresh_agent.balances(EXCHANGE_A_DOMAIN)[EXCHANGE_A_DOMAIN], "no account answered"
    site = mint_site()
    request = DomainVerificationRequest(
        ver=ProtocolVersion, domain=site.domain, exchange=EXCHANGE_A_DOMAIN
    )
    client = sdk_domain_verification_client(
        compose_stack, EXCHANGE_A_DOMAIN, key_path=fresh_agent.key_path
    )
    with closing(client), pytest.raises(CallError) as refused:
        client.request_domain_verification(request)
    assert refused.value.code == "unimplemented", (
        f"refused as {refused.value.code!r}: {refused.value}"
    )
