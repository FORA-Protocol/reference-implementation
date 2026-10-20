"""The obligation registry, the ``obligation`` claim, and the report (ADR-028 D1).

Each fact about an obligation is written in exactly one place:

* the obligation itself in ``tests/e2e/obligations/<area>.yaml``, loaded here;
* a test's link to it on the test, as ``@obligation(Obligation.RELAY_004)``;
* its state nowhere: the report computes it from the claiming tests at the end of
  every session, and never changes the exit status.

``Obligation`` is built from the registry when this module is imported, one member
per live entry, so a mistyped member raises AttributeError at import. A retired
entry has no member, so no test can claim it.

``check_quotes`` checks each quote against the file its cite names. A cite that
starts with the pinned protocol module's path reads that module's source, which
go resolves on the host; any other cite reads this repository.
"""

from __future__ import annotations

import enum
import json
import re
import shutil
import subprocess
import tempfile
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Any

import pytest
import yaml
from _pytest.skipping import evaluate_xfail_marks
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from ._compose import REPO_ROOT

MARKER = "obligation"
PROTOCOL_MODULE = "github.com/FORA-Protocol/protocol"
#: ``tests/e2e/obligations`` on the host and ``/runner/obligations`` in the runner.
REGISTRY_DIR = Path(__file__).resolve().parent.parent / "obligations"
REPORT_FILE = "obligation-report.json"


class Level(enum.Enum):
    MUST = "MUST"
    SHOULD = "SHOULD"
    MAY = "MAY"
    DECISION = "DECISION"


class Surface(enum.Enum):
    SDK = "sdk"
    MCP = "mcp"
    # The publisher console, driven as a signed-in publisher (ADR-028 D2).
    CONSOLE = "console"
    # A plain JSON API the protocol SDK has no client for, driven through the one
    # harness module that reads it (ADR-028 D2).
    HTTP = "http"


class _Strict(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True)


class Source(_Strict):
    cite: str
    quote: str


class Negative(_Strict):
    """A refusal with a typed error response, never an answer that only leaves something out."""

    code: str
    reason: str
    side_effect: str


class Entry(_Strict):
    id: str = Field(pattern=r"^[A-Z]+-[0-9]{3}$")
    title: str
    level: Level
    actor: str
    surface: Surface
    source: Source
    positive: str
    negative: list[Negative]
    question: str | None = None
    retired: bool = False


class RegistryError(Exception):
    """The registry files are missing or invalid."""


def load_registry(directory: Path) -> dict[str, Entry]:
    """Read every ``*.yaml`` area file under ``directory``, each a list of entries."""
    if not directory.is_dir():
        msg = f"obligation registry directory {directory} does not exist"
        raise RegistryError(msg)
    registry: dict[str, Entry] = {}
    origin: dict[str, str] = {}
    for path in sorted(directory.glob("*.yaml")):
        raw = yaml.safe_load(path.read_text()) or []
        if not isinstance(raw, list):
            msg = f"{path.name}: an area file holds a list of entries"
            raise RegistryError(msg)
        for index, item in enumerate(raw):
            try:
                entry = Entry.model_validate(item)
            except ValidationError as exc:
                msg = f"{path.name}: entry {index}: {exc}"
                raise RegistryError(msg) from exc
            if entry.id in origin:
                msg = f"duplicate obligation {entry.id} in {origin[entry.id]} and {path.name}"
                raise RegistryError(msg)
            registry[entry.id] = entry
            origin[entry.id] = path.name
    return registry


REGISTRY = load_registry(REGISTRY_DIR)

if TYPE_CHECKING:
    # The members exist only at runtime, so the checker is told any member name is an
    # Obligation. What it still refuses is anything that is not one, such as a str.
    class _ObligationMeta(enum.EnumMeta):
        def __getattr__(cls, name: str) -> Obligation: ...

    class Obligation(enum.Enum, metaclass=_ObligationMeta):
        pass

else:
    Obligation = enum.Enum(
        "Obligation",
        {oid.replace("-", "_"): oid for oid, entry in REGISTRY.items() if not entry.retired},
    )


def obligation(*members: Obligation, negative: bool = False) -> pytest.MarkDecorator:
    """Claim the obligations a test proves. Only ``Obligation`` members are accepted.

    ``negative=True`` says the test drives at least one of the ``negative:`` cases
    each claimed entry lists, and asserts the typed refusal. A "must not" the
    entry's ``positive:`` states is not a negative and takes no flag. A test that drives both a
    success and a negative of different obligations stacks two claims, one with the
    flag and one without. An entry that lists negatives counts as tested only when
    a claiming test sets the flag; claiming one that lists none with the flag set is
    refused when the session collects it.
    """
    for member in members:
        if not isinstance(member, Obligation):
            msg = f"obligation() takes Obligation members, not {member!r}"
            raise TypeError(msg)
    mark = getattr(pytest.mark, MARKER)
    return mark(*members, negative=True) if negative else mark(*members)


class State(enum.Enum):
    OPEN = "open"
    UNTESTED = "untested"
    SKIPPED = "skipped"
    XFAIL = "xfail"
    TESTED = "tested"
    PARTIAL = "partial"


@dataclass(frozen=True)
class Claim:
    """One collected test case's claim on one obligation.

    ``skip`` is the reason the case skipped when it ran, or None when it did not
    skip. A case that never ran, as under ``--collect-only``, has no skip.
    """

    strict_xfail: bool
    negative: bool = False
    skip: str | None = None


#: The note a partial obligation carries when every claim proving it is positive-only.
NO_NEGATIVE = "no claiming test drives a listed negative"


@dataclass(frozen=True)
class Status:
    """An obligation's derived state, with the skip reasons and why it is partial."""

    state: State
    skips: tuple[str, ...] = ()
    note: str | None = None

    def as_json(self) -> dict[str, object]:
        out: dict[str, object] = {"state": self.state.value}
        if self.skips:
            out["skips"] = list(self.skips)
        if self.note is not None:
            out["note"] = self.note
        return out


def derive_states(
    entries: Mapping[str, Entry], claims: Mapping[str, Sequence[Claim]]
) -> dict[str, Status]:
    """Each live obligation's status, from the test cases that claim it.

    In precedence order: *open* when ``question`` is set; *untested* when no case
    claims it; *skipped* when every claiming case skipped at run time; then, over
    the cases that did not skip, *xfail* when every one is a strict xfail and
    *partial* when only some are. When none is, an entry that lists negatives is
    *partial* unless one of those cases claims it with ``negative=True``, and
    every other entry is *tested*.
    """
    statuses: dict[str, Status] = {}
    for oid, entry in entries.items():
        if entry.retired:
            continue
        cases = claims.get(oid, ())
        skips = tuple(c.skip for c in cases if c.skip is not None)
        ran = [c for c in cases if c.skip is None]
        if entry.question is not None:
            statuses[oid] = Status(State.OPEN)
        elif not cases:
            statuses[oid] = Status(State.UNTESTED)
        elif not ran:
            statuses[oid] = Status(State.SKIPPED, skips)
        elif all(c.strict_xfail for c in ran):
            statuses[oid] = Status(State.XFAIL, skips)
        elif any(c.strict_xfail for c in ran):
            statuses[oid] = Status(State.PARTIAL, skips)
        elif entry.negative and not any(c.negative for c in ran):
            statuses[oid] = Status(State.PARTIAL, skips, NO_NEGATIVE)
        else:
            statuses[oid] = Status(State.TESTED, skips)
    return statuses


def _skip_reason(report: pytest.TestReport) -> str:
    """The reason a skipped report gives, without pytest's ``Skipped: `` prefix."""
    longrepr = report.longrepr
    message = longrepr[2] if isinstance(longrepr, tuple) else str(longrepr)
    return message.removeprefix("Skipped: ")


class ObligationReport:
    """Writes each obligation's derived state to the terminal summary and a JSON file.

    The claims are read when the session finishes collecting; whether a claiming
    case skipped is known only once it runs, so the state is derived when the
    session finishes.
    """

    def __init__(self, entries: Mapping[str, Entry], json_path: Path) -> None:
        self._entries = entries
        self._json_path = json_path
        self._claimed: list[tuple[str, str, bool, bool]] | None = None
        self._skipped: dict[str, str] = {}
        self._statuses: dict[str, Status] = {}

    def pytest_configure(self, config: pytest.Config) -> None:
        config.addinivalue_line(
            "markers",
            f"{MARKER}(*members, negative=False): the obligations this test proves (ADR-028 D1)",
        )

    def pytest_collection_finish(self, session: pytest.Session) -> None:
        claimed: list[tuple[str, str, bool, bool]] = []
        for item in session.items:
            strict: bool | None = None
            for mark in item.iter_markers(MARKER):
                negative = self._negative_flag(item, mark)
                for member in mark.args:
                    self._check_member(item, member, negative)
                    if strict is None:
                        xfail = evaluate_xfail_marks(item)
                        strict = xfail is not None and xfail.strict
                    claimed.append((item.nodeid, member.value, strict, negative))
        self._claimed = claimed

    def _negative_flag(self, item: pytest.Item, mark: pytest.Mark) -> bool:
        unknown = set(mark.kwargs) - {"negative"}
        negative = mark.kwargs.get("negative", False)
        if unknown or not isinstance(negative, bool):
            msg = f"{item.nodeid}: claim keywords {mark.kwargs!r} are not negative=<bool>"
            raise pytest.UsageError(msg)
        return negative

    def _check_member(self, item: pytest.Item, member: object, negative: bool) -> None:
        if not isinstance(member, Obligation):
            msg = f"{item.nodeid}: claim {member!r} is not an Obligation member"
            raise pytest.UsageError(msg)
        if negative and not self._entries[member.value].negative:
            msg = f"{item.nodeid}: claims a negative of {member.value}, which lists none"
            raise pytest.UsageError(msg)

    def pytest_runtest_logreport(self, report: pytest.TestReport) -> None:
        # An xfailed case also reports as skipped, carrying ``wasxfail``; it is not a skip.
        if report.skipped and not hasattr(report, "wasxfail"):
            self._skipped.setdefault(report.nodeid, _skip_reason(report))

    def pytest_sessionfinish(self, session: pytest.Session) -> None:
        if self._claimed is None:
            return
        claims: dict[str, list[Claim]] = {}
        for nodeid, oid, strict, negative in self._claimed:
            claim = Claim(strict, negative, self._skipped.get(nodeid))
            claims.setdefault(oid, []).append(claim)
        self._statuses = derive_states(self._entries, claims)
        self._json_path.write_text(
            json.dumps({oid: s.as_json() for oid, s in self._statuses.items()}, indent=2)
        )

    def pytest_terminal_summary(self, terminalreporter: pytest.TerminalReporter) -> None:
        if not self._entries:
            return
        terminalreporter.section("obligations")
        for state in State:
            ids = sorted(oid for oid, s in self._statuses.items() if s.state is state)
            listed = "" if state is State.TESTED else " ".join(ids)
            terminalreporter.write_line(f"{state.value:<9} {len(ids):>4}  {listed}".rstrip())
        for oid in sorted(self._statuses):
            for reason in self._statuses[oid].skips:
                terminalreporter.write_line(f"  {oid} skipped: {reason}")


def pytest_configure(config: pytest.Config) -> None:
    config.pluginmanager.register(ObligationReport(REGISTRY, config.rootpath / REPORT_FILE))


def check_quotes(
    entries: Mapping[str, Entry], read_cited: Callable[[str], str | None]
) -> list[str]:
    """One message per entry whose quote is not in the file its cite names."""
    violations: list[str] = []
    for oid, entry in entries.items():
        text = read_cited(entry.source.cite)
        if text is None:
            violations.append(f"{oid}: cite {entry.source.cite} resolves to no file")
        elif _normalise(entry.source.quote) not in _normalise(text):
            violations.append(f"{oid}: quote not found in {entry.source.cite}")
    return violations


_COMMENT_LEADER = re.compile(r"^\s*(?://|#|\*)\s?")


def _normalise(text: str) -> str:
    """Drop each line's comment leader, then collapse every run of whitespace.

    A quote taken from a source comment spans several lines, each opening with
    ``//``, ``#`` or ``*``; without the leaders it reads as one sentence.
    """
    lines = (_COMMENT_LEADER.sub("", line) for line in text.splitlines())
    return " ".join(" ".join(lines).split())


def resolve_cite(cite: str) -> str | None:
    """The text of the file ``cite`` names, or None when no such file exists."""
    prefix = f"{PROTOCOL_MODULE}/"
    if cite.startswith(prefix):
        module = pinned_protocol_dir()
        if module is None:
            return None
        path = module / cite.removeprefix(prefix)
    else:
        path = REPO_ROOT / cite
    return path.read_text() if path.is_file() else None


def pinned_protocol_dir() -> Path | None:
    """The pinned protocol module's source directory, or None when go is absent.

    The pinned version is read from this repository without writing anything. The
    download then runs inside a throwaway module, because ``go mod download``
    records what it fetched in the calling module's go.sum and a read-only check
    must not edit a tracked file. With go present, any failure raises with go's own
    output, so the host check cannot pass by not running.
    """
    go = shutil.which("go")
    if go is None:
        return None
    version = _go_json(go, ["list", "-m", "-json", PROTOCOL_MODULE], REPO_ROOT).get("Version")
    with tempfile.TemporaryDirectory() as scratch:
        _go(go, ["mod", "init", "obligationquotes"], Path(scratch))
        module = _go_json(
            go, ["mod", "download", "-json", f"{PROTOCOL_MODULE}@{version}"], Path(scratch)
        )
    directory = module.get("Dir", "")
    if not directory:
        msg = f"go reported no directory for {PROTOCOL_MODULE}@{version}: {module}"
        raise RuntimeError(msg)
    return Path(directory)


def _go(go: str, args: list[str], cwd: Path) -> str:
    done = subprocess.run([go, *args], cwd=cwd, capture_output=True, text=True, check=False)
    if done.returncode != 0:
        msg = f"go {' '.join(args)} failed: {done.stdout}{done.stderr}"
        raise RuntimeError(msg)
    return done.stdout


def _go_json(go: str, args: list[str], cwd: Path) -> dict[str, Any]:
    parsed: dict[str, Any] = json.loads(_go(go, args, cwd))
    return parsed
