"""Publishers and MCP developers sign in through separate Zitadel instances.

The stack's one Zitadel runs two instances: the developers' at ``zitadel``,
where the identity service signs people in for MCP, and the publishers' at
``zitadel-publishers``, the only issuer the console trusts. Each has its own
accounts, browser session and pages. These suites show it through what a person
uses -- the identity service's sign-up, the console's sign-in, the provider's
pages and the mail it sends:

  * a developer's session at the provider signs nobody in at the console, also
    with the developer host's cookies copied to the publishers' host, and the
    developer's login name does not exist there;
  * one address holds a developer account and a publisher account, each with a
    password of its own, each signed up through its own instance's email;
  * each instance's pages carry its side's name, in English whatever language
    the browser asks for, and the publishers' pages the FORA website colours.

That the console refuses an ID token the developers' instance issued is proven
below the browser, in the console's Go integration tests: only a provider that
signs whatever the test chooses can hand the console such a token. A code the
developers' instance issued would prove nothing here: a code works only for the
client it was issued to, so any token endpoint refuses it at the console.

The provider's pages are read through the readers in ``provider_login.py`` and
``console_login.py``, the harness modules coupled to that HTML; this module reads
no page itself.

In-network only: the provider names itself by its in-network address. Every run
signs up a new address, because the runner reuses the stack.
"""

from __future__ import annotations

import os
import re
import secrets
import uuid
from collections.abc import Callable
from urllib.parse import urlsplit

import pytest

from .conftest import StackURLs
from .console_login import home_page, redirects_to_sign_in, sign_in
from .identity_signup import BOB_USER, provider_login_page, provision_agent
from .mailbox import Mailbox
from .provider_login import (
    Browser,
    ProviderLoginError,
    Registration,
    asks_for_password,
    asks_to_accept_terms,
    asset_url,
    is_login_page,
    page_host,
    page_language,
    page_title,
    registration_page,
    submit_login_name,
)
from .seed import SeededFixture

pytestmark = pytest.mark.skipif(
    os.environ.get("FORA_E2E_IN_NETWORK") != "1",
    reason="the provider is reachable only in-network (FORA_E2E_IN_NETWORK=1)",
)

DEVELOPERS = "FORA Developers"
PUBLISHERS = "FORA Publishers"
# The publisher palette documented in deploy/zitadel/CONFIGURATION.md, as Login
# V1 renders the base shade in both colour sets; and the text colour. The
# background differs per set: the light set is the email palette (the console's
# panels), the dark set is the sign-in page.
PALETTE = {
    "primary": "rgb(79, 109, 245)",
    "warn": "rgb(255, 138, 128)",
}
BACKGROUND = {":root": "rgb(18, 18, 26)", ".lgn-dark-theme": "rgb(10, 10, 15)"}
TEXT_COLOUR = "#e8e8f0"
# The provider's answer to a wrong password, as the shared driver reports it.
REFUSED_PASSWORD = "the password submit did not redirect"


@pytest.fixture
def mailbox(compose_stack: StackURLs) -> Mailbox:
    return Mailbox(compose_stack.mailpit)


def _new_address() -> str:
    return f"person-{uuid.uuid4().hex[:12]}@publisher.example"


def _new_password(side: str) -> str:
    # The provider's password policy wants all four character classes.
    return f"{side}-{secrets.token_hex(6)}-Aa1!"


def _netloc(url: str) -> str:
    return urlsplit(url).netloc


def test_a_developer_session_signs_nobody_in_at_the_console(identity_ready: StackURLs) -> None:
    stack = identity_ready
    with Browser() as browser:
        provision_agent(stack.identity, login_name=BOB_USER, browser=browser)

        page = browser.get(f"{stack.console}/login")
        assert is_login_page(page), f"the console signed bob in: {page_host(page)}"
        assert page_host(page) == _netloc(stack.zitadel_publishers)
        assert page_title(page) == PUBLISHERS

        # The developer host's cookies, sent to the publishers' host, change nothing.
        browser.copy_cookies(stack.zitadel, stack.zitadel_publishers)
        page = browser.get(f"{stack.console}/login")
        assert is_login_page(page), f"copied cookies signed bob in: {page_host(page)}"

        # bob exists only in the developers' instance.
        _, answer = submit_login_name(browser, page, BOB_USER)
        assert is_login_page(answer) and not asks_for_password(answer), (
            "the publishers' instance knows bob"
        )

        assert redirects_to_sign_in(browser, stack.console)
        # Each instance's cookies stay on the host that set them.
        for base in (stack.zitadel, stack.zitadel_publishers):
            host = urlsplit(base).hostname or ""
            domains = browser.cookie_domains(base)
            assert domains <= {"", host}, domains


def test_one_address_holds_a_developer_and_a_publisher_account(
    identity_ready: StackURLs,
    mailbox: Mailbox,
    # The console's home page reads the Exchange's own seat, which the seed writes.
    seeded: SeededFixture,
) -> None:
    stack = identity_ready
    address = _new_address()
    developer_password, publisher_password = _new_password("Dev"), _new_password("Pub")

    def code(subject: str) -> Callable[[], str]:
        return lambda: mailbox.wait_for(address, subject).code()

    developer = Registration(address, developer_password, code(f"Welcome to {DEVELOPERS}"))
    assert provision_agent(stack.identity, registration=developer)
    publisher = Registration(address, publisher_password, code(f"Welcome to {PUBLISHERS}"))
    with sign_in(stack.console, registration=publisher) as console:
        assert address in home_page(console, stack.console)
        # The console's own cookies are host-only.
        assert console.cookie_domains(stack.console) == {""}

    # Each password opens its own side only: the other side knows the address
    # and refuses the password.
    with pytest.raises(ProviderLoginError, match=REFUSED_PASSWORD):
        sign_in(stack.console, login_name=address, password=developer_password).close()
    with pytest.raises(ProviderLoginError, match=REFUSED_PASSWORD):
        provision_agent(stack.identity, login_name=address, password=publisher_password)
    # And each instance sent exactly its own sign-up email.
    assert [m.subject for m in mailbox.messages(address)] == [
        f"Welcome to {DEVELOPERS}",
        f"Welcome to {PUBLISHERS}",
    ]


@pytest.mark.parametrize("language", [None, "de-DE,de;q=0.9"])
def test_each_instance_shows_its_own_side(identity_ready: StackURLs, language: str | None) -> None:
    stack = identity_ready
    with Browser(language=language) as browser:
        developer_login = provider_login_page(stack.identity, browser)
        publisher_login = browser.get(f"{stack.console}/login")
        for side, login in ((DEVELOPERS, developer_login), (PUBLISHERS, publisher_login)):
            assert is_login_page(login), f"{side}: {page_host(login)}"
            assert page_language(login) == "en", f"{side} answered {language} in another language"
            assert page_title(login) == side
            _, signup = registration_page(browser, login)
            assert page_title(signup) == side
            # No terms of service or privacy policy to accept.
            assert not asks_to_accept_terms(signup), side

        stylesheet = asset_url(publisher_login, "variables.css")
        css = browser.fetch(stylesheet, follow_redirects=True).text
        for theme, background in BACKGROUND.items():
            block = re.search(rf"(?s){re.escape(theme)}\s*{{(.*?)}}", css)
            assert block, f"no {theme} block in the publishers' colours: {css[:400]}"
            for name, value in {**PALETTE, "background": background}.items():
                assert f"--zitadel-color-{name}-500: {value}" in block.group(1), (theme, name)
        assert TEXT_COLOUR in css

        logo = browser.fetch(asset_url(publisher_login, "policy/label/logo", attribute="src"))
        assert logo.status == 200 and logo.content_type == "image/png", logo.content_type
