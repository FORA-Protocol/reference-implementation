"""Sign in to the publisher console through the real login provider, headlessly.

The console's sign-in is an ordinary OIDC redirect dance in a browser: it sends
the person to the provider, the provider sends them back with a code, and the
console seals a session cookie. This driver does exactly that, so the run
exercises the real flow rather than a short-circuit around it.

The provider's login form, the per-host cookie handling and the redirect
following are shared with the identity service's sign-up driver and are
defined in ``provider_login.py``. What is here is the console's own side: its
landing page and the wizard's JSON calls.
"""

from __future__ import annotations

import json
import re
from html import unescape
from urllib.parse import urlsplit

import httpx

from .provider_login import (
    ALICE_PASSWORD,
    ALICE_USER,
    Browser,
    Login,
    ProviderLoginError,
    Registration,
    is_login_page,
    pass_provider,
)

# The add-a-domain page is one page driven by a script, so it carries its
# starting state as an escaped attribute rather than as form fields. The
# browser un-escapes it through dataset; unescape does the same job here.
_BOOT_RE = re.compile(r'data-boot="([^"]*)"')


class ConsoleLoginError(ProviderLoginError):
    """A sign-in did not complete, with the page that stopped it."""


class ConsoleBrowser(Browser):
    """A browser that can also make the wizard's JSON calls."""

    def post_json(self, url: str, body: dict) -> httpx.Response:
        """POST JSON to one of the console's own calls.

        These routes never redirect: a call that arrives without a session is
        refused in the shape the caller asked for, precisely so a browser
        script does not read a sign-in page as its answer.
        """
        return self.request("POST", url, json=body)


def sign_in(
    console_base: str,
    *,
    login_name: str = ALICE_USER,
    password: str = ALICE_PASSWORD,
    registration: Registration | None = None,
) -> ConsoleBrowser:
    """Return a browser holding a signed-in console session.

    The provider is passed as ``login_name`` with ``password``, or, given a
    ``registration``, by signing up a new account there. The sign-in counts
    only when it ends on the console's home page, which needs a session whose
    address the provider verified. A step the provider refuses raises with the
    provider driver's own message, a refused password among them. Any other page
    that answers 200 -- the console's unverified-address page, say -- is a
    sign-in that did not complete.

    The caller owns the browser and should close it.
    """
    browser, page = _walk(
        console_base, Login(login_name, password, registration), accept_next_step=False
    )
    if not landed_home(console_base, page):
        browser.close()
        raise ConsoleLoginError(
            f"sign-in ended at {page.request.url} with {page.status_code}, "
            f"not the console's home page: {page.text[:400]}"
        )
    return browser


def attempt_sign_in(
    console_base: str, *, login_name: str, password: str
) -> tuple[ConsoleBrowser, httpx.Response]:
    """Walk the console's sign-in as ``login_name``; return the browser and the page it ended on.

    Nothing is required of the page: a caller that expects the sign-in to stop
    short of the console reads where it stopped, which may be a further step the
    provider asks for after the password. The caller owns the browser and should
    close it.
    """
    return _walk(console_base, Login(login_name, password), accept_next_step=True)


def _walk(
    console_base: str, login: Login, *, accept_next_step: bool
) -> tuple[ConsoleBrowser, httpx.Response]:
    """Open the console's sign-in and get past the provider as ``login`` says.

    Returns the browser and the page the walk ended on. ``accept_next_step`` is
    :func:`provider_login.pass_provider`'s.
    """
    browser = ConsoleBrowser()
    try:
        page = browser.get(f"{console_base}/login")
        if is_login_page(page):
            page, _ = pass_provider(browser, page, login, accept_next_step=accept_next_step)
    except Exception:
        browser.close()
        raise
    return browser, page


def home_page(browser: Browser, console_base: str) -> str:
    """The console's home page, as the session the browser holds sees it."""
    response = browser.get(f"{console_base}/")
    assert response.status_code == httpx.codes.OK, response.text[:400]
    return response.text


def redirects_to_sign_in(browser: Browser, console_base: str) -> bool:
    """Whether the console's home page sends browser to the console's sign-in,
    as it does a browser that holds no console session."""
    home = browser.request("GET", f"{console_base}/")
    return (
        home.status_code in (httpx.codes.FOUND, httpx.codes.SEE_OTHER)
        and urlsplit(home.headers.get("location", "")).path == "/login"
    )


def landed_home(console_base: str, page: httpx.Response) -> bool:
    """Whether ``page`` is the console's home page, answered 200."""
    landed = urlsplit(str(page.request.url))
    home = urlsplit(console_base)
    return page.status_code == httpx.codes.OK and (landed.netloc, landed.path) == (home.netloc, "/")


def wizard_boot(html: str) -> dict:
    """Read the add-a-domain page's starting state, the way its script does."""
    match = _BOOT_RE.search(html)
    if not match:
        raise ConsoleLoginError(f"no wizard state in the page: {html[:400]}")
    return json.loads(unescape(match.group(1)))
