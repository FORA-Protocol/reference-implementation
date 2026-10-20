"""The publisher console's dashboard, read the way a signed-in publisher reads it.

Each reader here fetches one page or file the console serves a signed-in
publisher, requires the answer a working page gives, and returns it as a typed
result: the income report as a file (``/income.csv``), the transaction list, one
transaction with the signed evidence its Verify button checks, the catalog, and
the publisher's list of domains. A test asserts on those results and never on a
raw response or on markup.

Pages are parsed structurally, with the standard library's HTML parser, into a
small element tree (:class:`Element`), and each reader picks out the elements
the console's templates (``src/console/internal/web/templates``) render its
values into: a table row's cells, a ``data-status`` badge, the ``data-evidence``
attribute, a definition list's terms. A change to the templates surfaces here as
a reader that finds nothing, named, rather than as a wrong value.

:func:`page_answer` is the other side of the same pages: the status a page
answers a publisher with, for the negatives that ask for something that is not
theirs.
"""

from __future__ import annotations

import base64
import binascii
import csv
import io
import json
from collections.abc import Callable, Iterator
from dataclasses import dataclass, field
from datetime import UTC, date, datetime, timedelta
from decimal import Decimal, InvalidOperation
from html.parser import HTMLParser
from http import HTTPStatus
from urllib.parse import quote, unquote, urlencode

import httpx

from .console_login import ConsoleBrowser

# The console's dashboard routes (src/console/internal/dashboard/paths.go).
DOMAINS_PATH = "/domains"
TRANSACTIONS_PATH = "/transactions"
CATALOG_PATH = "/catalog"
INCOME_CSV_PATH = "/income.csv"

#: Elements HTML closes without an end tag.
_VOID = frozenset(
    {"area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "source", "wbr"}
)

#: A paged list is a handful of pages in any one test; more is a pager that loops.
_MAX_PAGES = 20


@dataclass
class Element:
    """One HTML element: its tag, its attributes, and what it contains, in order."""

    tag: str
    attrs: dict[str, str]
    children: list[Element | str] = field(default_factory=list)

    def iter(self) -> Iterator[Element]:
        """This element and every element inside it, in document order."""
        yield self
        for child in self.children:
            if isinstance(child, Element):
                yield from child.iter()

    def find_all(
        self, tag: str, predicate: Callable[[Element], bool] = lambda _: True
    ) -> list[Element]:
        """Every element inside this one (itself included) with ``tag`` that ``predicate`` accepts."""
        return [e for e in self.iter() if e.tag == tag and predicate(e)]

    def text(self) -> str:
        """The element's text, its runs of whitespace collapsed to single spaces."""
        parts: list[str] = []
        for child in self.children:
            parts.append(child if isinstance(child, str) else child.text())
        return " ".join(" ".join(parts).split())

    def classes(self) -> set[str]:
        return set(self.attrs.get("class", "").split())


class _TreeBuilder(HTMLParser):
    """Builds an :class:`Element` tree. An end tag closes the nearest open element it names."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.root = Element("#document", {})
        self._open = [self.root]

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        element = Element(tag, {name: value or "" for name, value in attrs})
        self._open[-1].children.append(element)
        if tag not in _VOID:
            self._open.append(element)

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        self._open[-1].children.append(Element(tag, {name: value or "" for name, value in attrs}))

    def handle_endtag(self, tag: str) -> None:
        for depth in range(len(self._open) - 1, 0, -1):
            if self._open[depth].tag == tag:
                del self._open[depth:]
                return

    def handle_data(self, data: str) -> None:
        self._open[-1].children.append(data)


def parse_html(text: str) -> Element:
    """``text`` as an element tree, rooted at a ``#document`` element."""
    builder = _TreeBuilder()
    builder.feed(text)
    builder.close()
    return builder.root


@dataclass(frozen=True)
class Period:
    """A stretch of whole UTC days, both ends inclusive, as the dashboard's address carries it."""

    first: date
    last: date

    def query(self) -> dict[str, str]:
        return {"from": self.first.isoformat(), "to": self.last.isoformat()}

    @classmethod
    def around_today(cls) -> Period:
        """The day before today to the day after, in UTC, so a sale near midnight is inside."""
        today = datetime.now(UTC).date()
        return cls(first=today - timedelta(days=1), last=today + timedelta(days=1))


def _get(browser: ConsoleBrowser, url: str) -> httpx.Response:
    response = browser.get(url)
    assert response.status_code == httpx.codes.OK, (url, response.status_code, response.text[:400])
    return response


def _page(browser: ConsoleBrowser, url: str) -> Element:
    return parse_html(_get(browser, url).text)


@dataclass(frozen=True)
class PageAnswer:
    """How the console answered a page: its status, and the page it served with it."""

    status: HTTPStatus
    html: str


def page_answer(browser: ConsoleBrowser, base: str, path: str) -> PageAnswer:
    """How the console answers ``path`` for this browser's publisher, whatever the status."""
    response = browser.get(f"{base}{path}")
    return PageAnswer(status=HTTPStatus(response.status_code), html=response.text)


def transaction_path(transaction_id: str) -> str:
    """Where one transaction's page is, as the list links to it."""
    return f"{TRANSACTIONS_PATH}/{quote(transaction_id, safe='')}"


# --- the income report -----------------------------------------------------------------------


@dataclass(frozen=True)
class IncomeRow:
    """One line of the income file: a day, or the period's total (``label == "Total"``).

    Amounts are the Exchange's money strings read as exact decimals, or None for a
    figure the file carries without an amount.
    """

    label: str
    transactions: int
    gross: Decimal | None
    fee: Decimal | None
    net: Decimal | None
    currency: str


_INCOME_HEADER = ["date", "transactions", "gross", "exchange_fee", "net", "currency"]


def _amount(raw: str) -> Decimal | None:
    try:
        return Decimal(raw)
    except InvalidOperation:
        return None


def income_total(browser: ConsoleBrowser, base: str, period: Period) -> IncomeRow:
    """The total line of the income file the console exports for ``period``.

    Every day line above it is parsed too, so a malformed line fails here.
    """
    response = _get(browser, f"{base}{INCOME_CSV_PATH}?{urlencode(period.query())}")
    rows = list(csv.reader(io.StringIO(response.text)))
    assert rows and rows[0] == _INCOME_HEADER, f"the income file's header is {rows[:1]}"
    lines = [
        IncomeRow(
            label=row[0],
            transactions=int(row[1]),
            gross=_amount(row[2]),
            fee=_amount(row[3]),
            net=_amount(row[4]),
            currency=row[5],
        )
        for row in rows[1:]
    ]
    assert lines and lines[-1].label == "Total", f"the income file ends without its total: {rows}"
    return lines[-1]


# --- paged lists --------------------------------------------------------------------------------


def _next_link(page: Element) -> str:
    links = page.find_all("a", lambda a: a.attrs.get("rel") == "next")
    return links[0].attrs["href"] if links else ""


def _paged_rows(browser: ConsoleBrowser, base: str, first: str) -> list[Element]:
    """The table rows of every page of a list, following its Next links from ``first``."""
    rows: list[Element] = []
    address = first
    for _ in range(_MAX_PAGES):
        page = _page(browser, f"{base}{address}")
        rows.extend(page.find_all("tr", lambda tr: "row-link" in tr.classes()))
        address = _next_link(page)
        if not address:
            return rows
    raise AssertionError(f"the list at {first} kept offering a next page past {_MAX_PAGES}")


def _cell(row: Element, label: str) -> Element:
    cells = row.find_all("td", lambda td: td.attrs.get("data-label") == label)
    assert len(cells) == 1, f"a row without exactly one {label!r} cell: {row.text()}"
    return cells[0]


def _row_link(row: Element) -> str:
    links = row.find_all("a", lambda a: "stretched-link" in a.classes())
    assert len(links) == 1, f"a row without its link: {row.text()}"
    return links[0].attrs["href"]


@dataclass(frozen=True)
class TransactionRow:
    """One row of the transaction list."""

    transaction_id: str
    resource_uri: str
    agent: str
    unit_price: str
    status: str


def transactions(browser: ConsoleBrowser, base: str, period: Period) -> list[TransactionRow]:
    """Every row of the transaction list for ``period``, newest first, across its pages."""
    out: list[TransactionRow] = []
    for row in _paged_rows(browser, base, f"{TRANSACTIONS_PATH}?{urlencode(period.query())}"):
        link = _row_link(row)
        assert link.startswith(f"{TRANSACTIONS_PATH}/"), f"a row linking elsewhere: {link}"
        badges = _cell(row, "Report").find_all("span", lambda s: "data-status" in s.attrs)
        assert len(badges) == 1, f"a row without its report status: {row.text()}"
        out.append(
            TransactionRow(
                transaction_id=unquote(link.removeprefix(f"{TRANSACTIONS_PATH}/")),
                resource_uri=_cell(row, "Resource").attrs.get("title", ""),
                agent=_cell(row, "Agent").text(),
                unit_price=_cell(row, "Unit price").text(),
                status=badges[0].attrs["data-status"],
            )
        )
    return out


@dataclass(frozen=True)
class CatalogRow:
    """One row of the catalog: the page's address and its own page."""

    uri: str
    link: str


def catalog(browser: ConsoleBrowser, base: str) -> list[CatalogRow]:
    """Every row of the publisher's catalog, across its pages."""
    return [
        CatalogRow(
            uri=_cell(row, "Title").attrs.get("title", ""),
            link=_row_link(row),
        )
        for row in _paged_rows(browser, base, CATALOG_PATH)
    ]


# --- one transaction ----------------------------------------------------------------------------


@dataclass(frozen=True)
class Evidence:
    """The signed evidence a transaction page hands its Verify button.

    Each signature, the exact bytes it signs and the key it verifies under, as the
    page carries them in its ``data-evidence`` attribute: bytes and keys decoded
    from standard base64, signatures as the hex the Exchange serves.
    """

    offer_canonical_bytes: bytes
    offer_sig: str
    exchange_signing_public_key: bytes
    agent_acceptance_canonical_bytes: bytes
    agent_acceptance_signature: str
    agent_public_key: bytes


@dataclass(frozen=True)
class TransactionDetail:
    """One transaction's page: who bought, its report status, its evidence, and its text.

    ``html`` is the page as served, for a test that asserts something is absent
    from everything the page says.
    """

    transaction_id: str
    agent: str
    status: str
    evidence: Evidence
    html: str


def _b64(raw: object, name: str) -> bytes:
    assert isinstance(raw, str), f"evidence field {name} is not a string: {raw!r}"
    try:
        return base64.b64decode(raw, validate=True)
    except binascii.Error as exc:
        raise AssertionError(f"evidence field {name} is not standard base64: {raw!r}") from exc


def _evidence(raw: str) -> Evidence:
    fields = json.loads(raw)
    assert isinstance(fields, dict), f"the page's evidence is not an object: {raw[:200]}"
    signatures = {name: fields.get(name) for name in ("offer_sig", "agent_acceptance_signature")}
    for name, value in signatures.items():
        assert isinstance(value, str) and value, f"evidence field {name} is empty: {fields}"
    return Evidence(
        offer_canonical_bytes=_b64(fields.get("offer_canonical_bytes"), "offer_canonical_bytes"),
        offer_sig=str(signatures["offer_sig"]),
        exchange_signing_public_key=_b64(
            fields.get("exchange_signing_public_key"), "exchange_signing_public_key"
        ),
        agent_acceptance_canonical_bytes=_b64(
            fields.get("agent_acceptance_canonical_bytes"), "agent_acceptance_canonical_bytes"
        ),
        agent_acceptance_signature=str(signatures["agent_acceptance_signature"]),
        agent_public_key=_b64(fields.get("agent_public_key"), "agent_public_key"),
    )


def _facts(section: Element) -> dict[str, str]:
    """A ``dl``'s terms and their descriptions, in order of appearance."""
    out: dict[str, str] = {}
    term = ""
    for element in section.iter():
        if element.tag == "dt":
            term = element.text()
        elif element.tag == "dd" and term:
            out[term] = element.text()
            term = ""
    return out


def transaction(browser: ConsoleBrowser, base: str, transaction_id: str) -> TransactionDetail:
    """The page of one of the publisher's transactions."""
    response = _get(browser, f"{base}{transaction_path(transaction_id)}")
    page = parse_html(response.text)
    evidence = page.find_all("section", lambda s: "data-evidence" in s.attrs)
    assert len(evidence) == 1, "the transaction page carries no evidence section"
    sale = page.find_all("section", lambda s: s.attrs.get("aria-labelledby") == "sale-heading")
    assert len(sale) == 1, "the transaction page carries no sale section"
    facts = _facts(sale[0])
    badges = page.find_all("span", lambda s: "data-status" in s.attrs)
    assert badges, "the transaction page carries no report status"
    return TransactionDetail(
        transaction_id=facts.get("Transaction id", ""),
        agent=facts.get("Agent", ""),
        status=badges[0].attrs["data-status"],
        evidence=_evidence(evidence[0].attrs["data-evidence"]),
        html=response.text,
    )


# --- the domains page ---------------------------------------------------------------------------


@dataclass(frozen=True)
class SiteSummary:
    """One domain on the domains page: its name, its CDN, whether it is verified,
    and the link that finishes its setup, which only a domain that is not
    verified carries ("" otherwise)."""

    domain: str
    cdn: str
    verified: bool
    setup_link: str


def registered_sites(browser: ConsoleBrowser, base: str) -> list[SiteSummary]:
    """The domains the domains page lists for the publisher, in its order.

    The console's home page shows the same list only until the publisher has a
    verified domain; from then on it opens the overview instead. Each row holds
    the link to the domain's page, with its name and network, and its badge; a
    domain that is not verified also carries the link that finishes its setup.
    """
    page = _page(browser, f"{base}{DOMAINS_PATH}")
    out: list[SiteSummary] = []
    for item in page.find_all("div", lambda d: "list-group-item" in d.classes()):
        names = item.find_all("span", lambda s: "fw-semibold" in s.classes())
        cdns = item.find_all("span", lambda s: "d-block" in s.classes())
        badges = item.find_all("span", lambda s: "badge" in s.classes())
        setups = item.find_all("a", lambda a: a.attrs.get("href", "").endswith("/setup"))
        assert len(names) == 1 and len(cdns) == 1 and len(badges) == 1, item.text()
        assert len(setups) <= 1, item.text()
        out.append(
            SiteSummary(
                domain=names[0].text(),
                cdn=cdns[0].text(),
                verified="text-bg-success" in badges[0].classes(),
                setup_link=setups[0].attrs["href"] if setups else "",
            )
        )
    return out
