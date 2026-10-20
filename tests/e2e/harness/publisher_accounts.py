"""A per-test publisher: a console login of the test's own (ADR-028 D7).

A publisher shared across tests carries state from one test into the next and
from one run into the next. On a seat an account holds exactly one publisher
(ADR-026, amendment of 2026-09-21, D), a domain is registered once, and every
sale lands in that one publisher's totals. A test that registers, sells and
reads the dashboard as the bootstrap's ``alice`` login is therefore reading
whatever the tests before it left behind, and cannot pass a second time on the
same stack. Each publisher test gets a login of its own instead.

A fresh publisher is a person the stack's login provider has never seen:

* **a login**, imported through the management API of the provider's
  publishers' instance, the only issuer the console trusts, with that
  instance's admin token (``deploy/zitadel/bootstrap/bootstrap.sh`` imports its
  demo users through the same call). The runner mounts the volume holding the
  token read-only. The address is under ``publisher.example`` and the password
  is new per login;
* **whether the provider has verified the address**, which the import states.
  The console admits a person to publisher functionality only with a verified
  address (``src/console/internal/auth/middleware.go``), so ``verified=False`` is
  how a test reaches the refusal;
* **a console session**, signed in through the provider's own login pages
  exactly as ``alice`` is (``console_login.attempt_sign_in``).

Nothing here touches a database: the login is created through the provider's
API, and everything the console and the Exchange then hold about this person
they created themselves, on the first sign-in and the first registration.
"""

from __future__ import annotations

import secrets
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit

import httpx

from .console_dashboard import parse_html
from .console_login import ConsoleBrowser, attempt_sign_in, landed_home
from .stack_urls import StackURLs

#: The admin token of the provider's developers' instance, which its first start
#: writes (``ZITADEL_PAT_FILE``'s default in ``deploy/zitadel/bootstrap/bootstrap.sh``)
#: on the volume the runner mounts at ``/zitadel-bootstrap``.
DEVELOPERS_TOKEN_FILE = Path("/zitadel-bootstrap/zadmin.pat")

#: The admin token of the provider's publishers' instance: the owner token
#: ``deploy/zitadel/bootstrap/instance.sh`` writes when it creates that instance
#: (the ``zitadel-publishers-init`` one-shot's ``ZITADEL_PAT_FILE``), on the volume
#: the runner mounts at ``/zitadel-publishers``.
PUBLISHERS_TOKEN_FILE = Path("/zitadel-publishers/bootstrap.pat")

#: The domain every fresh publisher's address is under. ``.example`` is reserved
#: (RFC 6761), so no address minted here can reach a real mailbox.
LOGIN_DOMAIN = "publisher.example"

#: The provider's management API route that creates a person with a password.
_IMPORT_PATH = "/management/v1/users/human/_import"

_TIMEOUT_SEC = 30.0


def _admin_token(token_file: Path) -> str:
    token = token_file.read_text().strip()
    assert token, f"the provider's admin token at {token_file} is empty"
    return token


def _new_password() -> str:
    """A password the provider's default complexity policy accepts, new per login.

    The policy asks for a lower-case letter, an upper-case letter, a digit and a
    symbol; the fixed prefix carries one of each, the random part makes it unique.
    """
    return f"Pw-1{secrets.token_urlsafe(12)}"


@dataclass(frozen=True)
class Login:
    """A login the provider holds: its name (also its address), its password, and its id.

    ``subject`` is the provider's id for the person, the ``sub`` claim the console
    keys the account on. ``issuer`` is the provider as the console names it.
    """

    name: str
    password: str
    subject: str
    issuer: str


def import_login(zitadel: str, token_file: Path, *, verified: bool) -> Login:
    """Create a login at the provider instance at ``zitadel``, its address verified or not.

    ``token_file`` holds that instance's admin token: :data:`DEVELOPERS_TOKEN_FILE`
    or :data:`PUBLISHERS_TOKEN_FILE`.
    """
    login = f"pub-{secrets.token_hex(6)}@{LOGIN_DOMAIN}"
    password = _new_password()
    response = httpx.post(
        f"{zitadel}{_IMPORT_PATH}",
        headers={"Authorization": f"Bearer {_admin_token(token_file)}"},
        json={
            "userName": login,
            "profile": {"firstName": "Fresh", "lastName": "Publisher", "displayName": login},
            "email": {"email": login, "isEmailVerified": verified},
            "password": password,
            "passwordChangeRequired": False,
        },
        timeout=_TIMEOUT_SEC,
    )
    assert response.status_code == httpx.codes.OK, (
        f"the provider refused to import {login}: {response.status_code} {response.text[:400]}"
    )
    subject = response.json().get("userId", "")
    assert subject, f"the provider imported {login} and named no id for it: {response.text[:400]}"
    # The issuer is the instance's own address on this network, the address this
    # call used: for the publishers' instance, CONSOLE_OIDC_ISSUER in
    # docker-compose.e2e.yml.
    return Login(name=login, password=password, subject=subject, issuer=zitadel)


@dataclass
class PublisherSession:
    """One fresh publisher's browser, after its sign-in.

    ``signed_in`` is True when the sign-in ended on the console's home page,
    which needs a session whose address the provider verified. ``landed`` is the
    address of the last request the sign-in made, and ``page`` the page it was
    answered with. ``form_actions`` are the paths the forms on that page submit
    to: a sign-in the provider stopped short of the console ends on one of its
    own forms, and the path says which step it is asking for.
    """

    login: Login
    browser: ConsoleBrowser
    signed_in: bool
    landed: str
    page: str
    form_actions: list[str]


@dataclass
class PublisherFactory:
    """Mints publishers for one test (``factory(verified=...)``) and closes their browsers.

    The ``fresh_publisher`` fixture is one of these. A verified publisher whose
    sign-in does not reach the console's home page fails the call, so a test that
    asks for one gets a working session or a failure naming where it stopped.
    """

    stack: StackURLs
    _sessions: list[PublisherSession] = field(default_factory=list, repr=False)

    def __call__(self, *, verified: bool = True) -> PublisherSession:
        login = import_login(
            self.stack.zitadel_publishers, PUBLISHERS_TOKEN_FILE, verified=verified
        )
        browser, page = attempt_sign_in(
            self.stack.console, login_name=login.name, password=login.password
        )
        session = PublisherSession(
            login=login,
            browser=browser,
            signed_in=landed_home(self.stack.console, page),
            landed=str(page.request.url),
            page=page.text,
            form_actions=[
                urlsplit(form.attrs.get("action", "")).path
                for form in parse_html(page.text).find_all("form")
            ],
        )
        self._sessions.append(session)
        assert session.signed_in or not verified, (
            f"the verified publisher {login.name} did not reach the console's home page: "
            f"the sign-in ended at {session.landed}: {session.page[:400]}"
        )
        return session

    def close(self) -> None:
        """Close every minted publisher's browser."""
        for session in self._sessions:
            session.browser.close()
        self._sessions.clear()
