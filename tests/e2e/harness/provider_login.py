"""Drive the stack's login provider (Zitadel's server-rendered login UI) headlessly.

Two drivers sign a person in through the real provider: the identity service's
sign-up (``identity_signup.py``) and the publisher console's sign-in
(``console_login.py``). Both use this module, and it is the only code in the
harness coupled to the provider's login HTML. A provider upgrade surfaces here
as a parse error naming the page, not as a hang.

Cookies are threaded by hand, per host. Zitadel binds the auth request to a
cookie whose Domain is the single-label service name ``zitadel``, and Python's
cookie policy silently refuses to send a cookie scoped to a dot-less domain.
httpx's own jar therefore drops it on the very next hop and the provider
answers "User Agent does not correspond". Keeping one jar per host also means
one service's session cookie is never sent to another.

The stack runs two instances of the provider: the developers' one at
``zitadel``, where the identity service signs people in, and the publishers'
one at ``zitadel-publishers``, the console's. The same pages and the same
drivers serve both.
"""

from __future__ import annotations

import re
from collections.abc import Callable
from dataclasses import dataclass
from html import unescape
from http.cookies import SimpleCookie
from urllib.parse import urljoin, urlsplit

import httpx

# The local logins the bootstrap creates (deploy/zitadel/bootstrap/bootstrap.sh),
# sharing one password. ALICE exists in both instances; BOB and CAROL only in
# the developers' one, so the publishers' instance does not know them. A
# sign-up is idempotent per login: it returns the same agent subdomain every
# time. ALICE is the agent the account-tool suites register at exchanges; BOB is
# never registered anywhere, so a suite that needs an agent with no billing
# account signs up as BOB and can assert that it has none. CAROL is registered
# and spends only in the balance suite, so no other suite moves the balance that
# suite measures.
ALICE_USER = "alice@acme.local"
BOB_USER = "bob@acme.local"
CAROL_USER = "carol@acme.local"
ALICE_PASSWORD = "Alice12345!"

# The provider's login pages carry their own token and the request id as hidden
# inputs, and name where to submit in the form's action.
_INPUT_RE = re.compile(r"(?s)<input\b[^>]*>")
_NAME_RE = re.compile(r'name="([^"]+)"')
_VALUE_RE = re.compile(r'value="([^"]*)"')
_ACTION_RE = re.compile(r'(?s)<form\b[^>]*\baction="([^"]+)"')
_TITLE_RE = re.compile(r"(?s)<title>(.*?)</title>")
_LANG_RE = re.compile(r'<html\b[^>]*\blang="([^"]*)"')

REDIRECT_STATUSES = frozenset({301, 302, 303, 307, 308})
# A redirect chain longer than this is a loop, not a sign-in.
MAX_HOPS = 20
# One bound for every request either driver makes.
TIMEOUT_SECONDS = 30.0


class ProviderLoginError(RuntimeError):
    """A login leg did not produce the page or redirect it should have."""


@dataclass(frozen=True)
class Registration:
    """A new account to sign up with.

    read_code returns the code the provider emails to the address. The caller
    reads the mail, so it can also check what the mail says.
    """

    email: str
    password: str
    read_code: Callable[[], str]
    first_name: str = "Pat"
    last_name: str = "Tester"


@dataclass(frozen=True)
class Login:
    """How a driver gets past the provider: as an existing login with its
    password, or, with a registration, by signing up a new account."""

    login_name: str = ALICE_USER
    password: str = ALICE_PASSWORD
    registration: Registration | None = None


@dataclass(frozen=True)
class Asset:
    """One file a page links to, as fetched: its status, media type and text."""

    status: int
    content_type: str
    text: str


@dataclass(frozen=True)
class LoginForm:
    """What a provider login page asks to be sent back."""

    csrf: str
    request_id: str
    action: str


def is_redirect(resp: httpx.Response) -> bool:
    return resp.status_code in REDIRECT_STATUSES


class Browser:
    """An httpx client that threads cookies per host across redirects.

    It is not a general-purpose browser. It does what a sign-in needs: follow
    redirects between hosts, keep each host's cookies to itself, and let a
    caller GET and POST with those cookies attached.
    """

    def __init__(self, *, language: str | None = None) -> None:
        headers = {"Accept-Language": language} if language else None
        self._client = httpx.Client(
            timeout=TIMEOUT_SECONDS, follow_redirects=False, headers=headers
        )
        self._jars: dict[str, dict[str, str]] = {}
        # Every Set-Cookie header received, with the host that sent it.
        self.set_cookies: list[tuple[str, str]] = []

    def __enter__(self) -> Browser:
        return self

    def __exit__(self, *_: object) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()

    def get(self, url: str) -> httpx.Response:
        """GET url, following redirects to the page that answers."""
        resp, _ = self.get_until(url, stop_prefix=None)
        return resp

    def get_until(self, url: str, *, stop_prefix: str | None) -> tuple[httpx.Response, str]:
        """GET url and follow redirects; see :meth:`follow` for the result."""
        return self.follow(self.request("GET", url), stop_prefix=stop_prefix)

    def post(self, url: str, data: dict[str, str]) -> httpx.Response:
        """POST a form to url, then follow wherever it leads."""
        resp, _ = self.follow(self.request("POST", url, data=data), stop_prefix=None)
        return resp

    def request(
        self,
        method: str,
        url: str,
        *,
        data: dict[str, str] | None = None,
        json: object = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        """Send one request with this host's cookies, and keep what it sets."""
        merged = {**(headers or {}), **self._cookies(url)}
        resp = self._client.request(method, url, data=data, json=json, headers=merged)
        self._absorb(url, resp)
        return resp

    def follow(
        self, resp: httpx.Response, *, stop_prefix: str | None
    ) -> tuple[httpx.Response, str]:
        """Follow resp's redirects with GETs until a page answers.

        Returns the page and an empty string. With ``stop_prefix``, a redirect
        whose destination starts with it is not followed: the redirect is
        returned with that destination, for a caller that must deliver it
        itself. A redirect with no destination raises, wherever it happens.
        """
        for _ in range(MAX_HOPS):
            if not is_redirect(resp):
                return resp, ""
            here = str(resp.request.url)
            location = resp.headers.get("location", "")
            if not location:
                raise ProviderLoginError(f"a redirect from {here} carried no destination")
            target = urljoin(here, location)
            if stop_prefix and target.startswith(stop_prefix):
                return resp, target
            resp = self.request("GET", target)
        raise ProviderLoginError(f"the sign-in kept redirecting past {MAX_HOPS} hops")

    def fetch(self, url: str, *, follow_redirects: bool = False) -> Asset:
        """GET one file a page links to; with ``follow_redirects``, the file a
        redirect leads to."""
        resp = self.get(url) if follow_redirects else self.request("GET", url)
        return Asset(resp.status_code, resp.headers.get("content-type", ""), resp.text)

    def cookie_domains(self, url: str) -> set[str]:
        """The Domain attribute of every cookie the host of url has set ("" for a
        host-only cookie). Raises when that host set none: there is then nothing
        to check, and a check of nothing passes."""
        domains: set[str] = set()
        for host, raw in self.set_cookies:
            if host == _host(url):
                parsed = SimpleCookie()
                parsed.load(raw)
                domains |= {morsel["domain"] for morsel in parsed.values()}
        if not domains:
            raise ProviderLoginError(f"{_host(url)} set no cookie")
        return domains

    def copy_cookies(self, source: str, target: str) -> None:
        """Send target's host every cookie source's host has set, as a person
        copying cookies from one site to another would. Raises when source's
        host has set none: copying nothing would test nothing."""
        cookies = self._jars.get(_host(source), {})
        if not cookies:
            raise ProviderLoginError(f"{_host(source)} has set no cookie to copy")
        self._jars.setdefault(_host(target), {}).update(cookies)

    def _cookies(self, url: str) -> dict[str, str]:
        jar = self._jars.get(_host(url), {})
        return {"Cookie": "; ".join(f"{k}={v}" for k, v in jar.items())} if jar else {}

    def _absorb(self, url: str, resp: httpx.Response) -> None:
        jar = self._jars.setdefault(_host(url), {})
        for raw in resp.headers.get_list("set-cookie"):
            self.set_cookies.append((_host(url), raw))
            parsed = SimpleCookie()
            parsed.load(raw)
            for name, morsel in parsed.items():
                jar[name] = morsel.value


def is_login_page(page: httpx.Response) -> bool:
    """Whether page is the provider asking for a login name."""
    return 'name="loginName"' in page.text


def page_title(page: httpx.Response) -> str:
    """The page's <title>, which Login V1 renders from the same text as its heading."""
    found = _TITLE_RE.search(page.text)
    return unescape(found.group(1)).strip() if found else ""


def page_host(page: httpx.Response) -> str:
    """The host, with its port, that answered page."""
    return _host(str(page.request.url))


def page_language(page: httpx.Response) -> str:
    """The language page declares on its ``<html>`` element; "" when it declares none."""
    found = _LANG_RE.search(page.text)
    return found.group(1) if found else ""


def asks_for_password(page: httpx.Response) -> bool:
    """Whether page is the provider asking for a password."""
    return 'name="password"' in page.text


def asks_to_accept_terms(page: httpx.Response) -> bool:
    """Whether a registration page asks the person to accept terms of service or a
    privacy policy before signing up."""
    return "register-term-confirmation" in page.text


def asset_url(page: httpx.Response, name: str, *, attribute: str = "href") -> str:
    """The absolute address of the first file page links to, in ``attribute``,
    whose address contains name."""
    found = re.search(rf'{attribute}="([^"]*{re.escape(name)}[^"]*)"', page.text)
    if not found:
        raise ProviderLoginError(f"no {name} on {page.request.url}")
    return urljoin(str(page.request.url), unescape(found.group(1)))


def submit_login_name(
    browser: Browser,
    login_page: httpx.Response,
    login_name: str,
    extra: dict[str, str] | None = None,
) -> tuple[LoginForm, httpx.Response]:
    """Submit the login name, with any extra fields; return the login form and
    the page that answers.

    That page asks for the password when the provider knows the name, and is
    the login form again, with an error, when it does not.
    """
    first = parse_form(login_page, "login-name", want_request_id=True)
    page = browser.post(
        first.action,
        {
            "gorilla.csrf.Token": first.csrf,
            "authRequestID": first.request_id,
            "loginName": login_name,
            **(extra or {}),
        },
    )
    return first, page


def submit_credentials(
    browser: Browser,
    login_page: httpx.Response,
    login_name: str,
    *,
    password: str = ALICE_PASSWORD,
    stop_prefix: str | None = None,
    accept_next_step: bool = False,
) -> tuple[httpx.Response, str]:
    """Submit the login name, then the password, then follow the way back.

    ``password`` is the password the bootstrap's local logins share, unless the
    caller minted a login of its own with another one.

    The request id comes from the FIRST page and is carried to the second. The
    password page does not restate it, so reading it there again is what makes
    the parse fail on a page that is otherwise correct.

    A login name the provider does not know gets no password page, and a
    password POST that does not redirect is a refused password (the provider
    re-renders its form with an error). Each raises with its own message
    rather than handing the caller a page that merely answered 200. The result
    is :meth:`Browser.follow`'s.

    The provider also answers with a page of its own when it asks for a further
    step before it lets the person back to the client, such as activating an
    address it has not verified, which it can ask for in place of the password or
    after it. ``accept_next_step`` returns any such page instead of raising -- that
    step, or the form again for a refused login name or password -- for a caller
    that reads which page it is.
    """
    first, password_page = submit_login_name(browser, login_page, login_name)
    if not asks_for_password(password_page) and accept_next_step:
        return password_page, ""
    if not asks_for_password(password_page):
        raise ProviderLoginError(
            f"the provider did not ask for a password: {page_title(password_page)!r}"
        )
    second = parse_form(password_page, "password", want_request_id=False)
    submitted = browser.request(
        "POST",
        second.action,
        data={
            "gorilla.csrf.Token": second.csrf,
            "authRequestID": first.request_id,
            "password": password,
        },
    )
    if not is_redirect(submitted) and accept_next_step:
        return submitted, ""
    if not is_redirect(submitted):
        raise ProviderLoginError(f"the password submit did not redirect: {submitted.text[:400]}")
    return browser.follow(submitted, stop_prefix=stop_prefix)


def registration_page(
    browser: Browser, login_page: httpx.Response
) -> tuple[LoginForm, httpx.Response]:
    """Open the registration form from the provider's login page: its register
    button, then, when the page offers a choice of how to register, the
    username-and-password one. Returns the login form, which carries the
    sign-in's request id, and the registration form."""
    first, page = submit_login_name(browser, login_page, "", {"register": "true"})
    if 'name="usernamepassword"' in page.text:
        option = parse_form(page, "registration option", want_request_id=False)
        page = browser.post(
            option.action,
            {
                "gorilla.csrf.Token": option.csrf,
                "authRequestID": first.request_id,
                "usernamepassword": "true",
            },
        )
    return first, page


def register(
    browser: Browser,
    login_page: httpx.Response,
    registration: Registration,
    *,
    stop_prefix: str | None = None,
) -> tuple[httpx.Response, str]:
    """Sign up from the provider's login page, the way a person does.

    The steps are Login V1's: the registration form (see
    :func:`registration_page`), the page that takes the emailed code, and the
    page confirming the activation, whose form continues the sign-in. The
    result is :meth:`Browser.follow`'s.
    """
    first, page = registration_page(browser, login_page)
    request = {"authRequestID": first.request_id}
    form = parse_form(page, "registration", want_request_id=False)
    page = browser.post(
        form.action,
        {
            "gorilla.csrf.Token": form.csrf,
            **request,
            "language": _hidden(page.text, "language") or "en",
            "firstname": registration.first_name,
            "lastname": registration.last_name,
            "email": registration.email,
            "register-password": registration.password,
            "register-password-confirmation": registration.password,
        },
    )
    if 'name="code"' not in page.text:
        raise ProviderLoginError(
            "the registration did not ask for the emailed code: "
            f"{page_title(page)!r} {page.text[:400]}"
        )
    code = parse_form(page, "code", want_request_id=False)
    fields = {
        name: _hidden(page.text, name) for name in ("userID", "orgID", "passwordSet", "loginName")
    }
    submitted = browser.request(
        "POST",
        code.action,
        data={
            "gorilla.csrf.Token": code.csrf,
            **request,
            **fields,
            "code": registration.read_code(),
            "resend": "false",
        },
    )
    page, matched = browser.follow(submitted, stop_prefix=stop_prefix)
    if matched or not _on_provider(page):
        return page, matched
    if 'name="code"' in page.text:
        raise ProviderLoginError(f"the emailed code was refused: {page.text[:400]}")
    done = parse_form(page, "activation", want_request_id=False)
    submitted = browser.request(
        "POST",
        done.action,
        data={"gorilla.csrf.Token": done.csrf, **request, "orgID": _hidden(page.text, "orgID")},
    )
    if not is_redirect(submitted):
        raise ProviderLoginError(
            f"the activation did not continue the sign-in: {submitted.text[:400]}"
        )
    return browser.follow(submitted, stop_prefix=stop_prefix)


def pass_provider(
    browser: Browser,
    login_page: httpx.Response,
    login: Login,
    *,
    stop_prefix: str | None = None,
    accept_next_step: bool = False,
) -> tuple[httpx.Response, str]:
    """Get past the provider from its login page, as login says: sign up with
    its registration, or sign in with its login name and password. The result
    is :meth:`Browser.follow`'s. ``accept_next_step`` is
    :func:`submit_credentials`'s and applies to a sign-in; a sign-up walks every
    step the provider shows it."""
    if login.registration:
        return register(browser, login_page, login.registration, stop_prefix=stop_prefix)
    return submit_credentials(
        browser,
        login_page,
        login.login_name,
        password=login.password,
        stop_prefix=stop_prefix,
        accept_next_step=accept_next_step,
    )


def _on_provider(page: httpx.Response) -> bool:
    """Whether page is one of the provider's login pages, rather than the
    client's page the sign-in ended on."""
    return urlsplit(str(page.request.url)).path.startswith("/ui/login/")


def parse_form(page: httpx.Response, which: str, *, want_request_id: bool) -> LoginForm:
    """Read a provider login page's token, request id and absolute form action."""
    html = page.text
    csrf = _hidden(html, "gorilla.csrf.Token")
    request_id = _hidden(html, "authRequestID") if want_request_id else ""
    action = _ACTION_RE.search(html)
    if not csrf or not action or (want_request_id and not request_id):
        raise ProviderLoginError(
            f"could not read the provider's {which} form "
            f"(csrf={bool(csrf)} action={bool(action)}): {html[:400]}"
        )
    return LoginForm(csrf, request_id, urljoin(str(page.request.url), action.group(1)))


def _hidden(html: str, name: str) -> str:
    """The value of the input named exactly ``name``, or empty."""
    for tag in _INPUT_RE.findall(html):
        found = _NAME_RE.search(tag)
        if found and found.group(1) == name:
            value = _VALUE_RE.search(tag)
            return value.group(1) if value else ""
    return ""


def _host(url: str) -> str:
    return urlsplit(url).netloc
