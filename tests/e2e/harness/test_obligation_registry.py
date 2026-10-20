"""The obligation registry's module API: the enum, the decorator, the parser and quotes.

Host tier: no compose stack, no database. Each group names the surface it goes
through.

* **The enum and the decorator** are graded in a subprocess that imports a copy
  of the shipped module beside a temporary ``obligations/`` directory, because the
  module builds ``Obligation`` from that directory when it is imported. A
  mistyped member must fail at import; a string passed to ``obligation()`` must
  fail at import. That mypy also refuses the string is checked by ``make
  typecheck``, through ``typecheck_probes/check.py``, where mypy already runs.
* **``load_registry``** is a parser, a permitted unit-test category. An invalid
  entry or a duplicated ID must fail naming the file and the field. The registry
  holds no ``tests`` and no ``status`` field: each obligation fact is written
  once, and a test's link to its obligation lives only on the test.
* **``check_quotes``** is pure logic over a ``read_cited(cite) -> str | None``
  reader, where ``None`` means the cite resolves to no file. Comment leaders
  (``//``, ``#``, ``*``) and whitespace are normalised on both sides.
* **The live quote check** resolves cites for real on the host: a cite naming the
  pinned protocol module reads the module go resolves, any other cite reads this
  repository. It skips only when the go binary is absent, which is the runner
  image.
"""

from __future__ import annotations

import json
import re
import shutil
import sys
import textwrap
from pathlib import Path

import pytest

from .obligation_builders import (
    ADR_CITE,
    ADR_QUOTE,
    PROTOCOL_FILE_CITE,
    PROTOCOL_MODULE,
    PROTOCOL_QUOTE,
    entry_fields,
    plant_harness,
    plant_registry,
    registry_module,
    write_area,
)

pytest_plugins = ["pytester"]

# No compose stack and no database. Declaring `isolated` keeps the autouse
# cleanup chain, which needs a live Postgres, a no-op.
pytestmark = pytest.mark.stack_isolation("isolated")

_GO_ABSENT = "the go binary is absent (the runner image), so the pinned module cannot be found"

# The registry every enum and decorator probe imports: two live obligations and
# one retired one, which must get no member.
_PROBE_ENTRIES = [
    entry_fields("RELAY-004"),
    entry_fields("MONEY-012", surface="mcp"),
    entry_fields("RELAY-005", retired=True),
]


def _plant_probe_tree(pytester: pytest.Pytester) -> None:
    plant_harness(pytester.path)
    plant_registry(pytester.path, *_PROBE_ENTRIES)


def _run_python(pytester: pytest.Pytester, module: str, source: str) -> pytest.RunResult:
    """Write ``<root>/<module>.py`` and import it in a fresh interpreter."""
    (pytester.path / f"{module}.py").write_text(textwrap.dedent(source))
    return pytester.run(sys.executable, "-c", f"import {module}")


# ---------------------------------------------------------------------------
# The Obligation enum and the obligation() decorator, through a real import
# ---------------------------------------------------------------------------


def test_enum_has_one_member_per_live_entry_named_from_its_id(pytester: pytest.Pytester) -> None:
    _plant_probe_tree(pytester)
    result = _run_python(
        pytester,
        "probe_members",
        """
        import json
        from harness.obligation_registry import Obligation
        print(json.dumps(sorted([m.name, m.value] for m in Obligation)))
        """,
    )
    assert result.ret == 0, result.stderr.str()
    members = json.loads(result.stdout.lines[-1])
    assert members == [["MONEY_012", "MONEY-012"], ["RELAY_004", "RELAY-004"]]


def test_a_claim_on_a_registered_member_imports(pytester: pytest.Pytester) -> None:
    """Positive control for the typo tests below: the same module, correctly spelt."""
    _plant_probe_tree(pytester)
    result = _run_python(
        pytester,
        "claims_valid",
        """
        from harness.obligation_registry import Obligation, obligation

        @obligation(Obligation.RELAY_004)
        def test_claim() -> None:
            pass
        """,
    )
    assert result.ret == 0, result.stderr.str()


@pytest.mark.parametrize(
    "member",
    ["RELAY_04", "RELAY_005", "relay_004"],
    ids=["mistyped", "retired", "lowercase"],
)
def test_a_claim_on_an_unregistered_member_fails_at_import(
    pytester: pytest.Pytester, member: str
) -> None:
    _plant_probe_tree(pytester)
    result = _run_python(
        pytester,
        "claims_typo",
        f"""
        from harness.obligation_registry import Obligation, obligation

        @obligation(Obligation.{member})
        def test_claim() -> None:
            pass
        """,
    )
    assert result.ret != 0
    stderr = result.stderr.str()
    assert "AttributeError" in stderr
    assert member in stderr


def test_obligation_accepts_members_and_refuses_strings_at_import(
    pytester: pytest.Pytester,
) -> None:
    """A string fails even without mypy: the decorator raises TypeError when applied.

    Only TypeError is caught, so any other failure (an AttributeError from reading
    ``.value`` on a string, say) crashes the probe and fails the test.
    """
    _plant_probe_tree(pytester)
    result = _run_python(
        pytester,
        "probe_decorator",
        """
        import json
        from harness.obligation_registry import Obligation, obligation

        verdicts = {}
        for label, args in [
            ("string", ("RELAY-004",)),
            ("member_then_string", (Obligation.RELAY_004, "RELAY-005")),
        ]:
            try:
                obligation(*args)
                verdicts[label] = "accepted"
            except TypeError:
                verdicts[label] = "TypeError"
        mark = obligation(Obligation.RELAY_004, Obligation.MONEY_012).mark
        verdicts["mark"] = [mark.name, [a.value for a in mark.args]]
        print(json.dumps(verdicts))
        """,
    )
    assert result.ret == 0, result.stderr.str()
    assert json.loads(result.stdout.lines[-1]) == {
        "string": "TypeError",
        "member_then_string": "TypeError",
        "mark": ["obligation", ["RELAY-004", "MONEY-012"]],
    }


# ---------------------------------------------------------------------------
# load_registry (parser)
# ---------------------------------------------------------------------------


def test_load_registry_reads_every_area_file_into_typed_entries(tmp_path: Path) -> None:
    mod = registry_module()
    plant_registry(tmp_path)
    write_area(tmp_path / "obligations", "relay", entry_fields("RELAY-004"))
    write_area(
        tmp_path / "obligations",
        "money",
        entry_fields("MONEY-001", level="DECISION", surface="mcp", question="Which party pays?"),
        entry_fields("MONEY-002", level="SHOULD", retired=True),
    )

    entries = mod.load_registry(tmp_path / "obligations")

    assert sorted(entries) == ["MONEY-001", "MONEY-002", "RELAY-004"]
    relay = entries["RELAY-004"]
    assert relay.id == "RELAY-004"
    assert relay.level == mod.Level("MUST")
    assert relay.surface == mod.Surface("sdk")
    assert relay.actor == "exchange"
    assert relay.source.cite == PROTOCOL_FILE_CITE
    assert relay.source.quote == PROTOCOL_QUOTE
    assert [(n.code, n.reason, n.side_effect) for n in relay.negative] == [
        ("invalid_argument", "EXCHANGE_MISMATCH", "no transaction is recorded")
    ]
    assert relay.question is None
    assert relay.retired is False
    assert entries["MONEY-001"].level == mod.Level("DECISION")
    assert entries["MONEY-001"].surface == mod.Surface("mcp")
    assert entries["MONEY-001"].question == "Which party pays?"
    assert entries["MONEY-002"].retired is True


def test_load_registry_of_an_empty_directory_is_empty(tmp_path: Path) -> None:
    """A directory holding no area file, only a placeholder that is not YAML."""
    registry = plant_registry(tmp_path)
    assert registry_module().load_registry(registry) == {}


def test_load_registry_refuses_a_missing_directory_naming_it(tmp_path: Path) -> None:
    """A missing directory means the build or the runner COPY broke; it must not read as empty."""
    mod = registry_module()
    missing = tmp_path / "obligations"
    with pytest.raises(mod.RegistryError) as excinfo:
        mod.load_registry(missing)
    assert str(missing) in str(excinfo.value)


@pytest.mark.parametrize(
    ("field", "value", "named"),
    [
        ("tests", ["suite/test_offers.py::test_refused"], ["tests"]),
        ("status", "tested", ["status"]),
        ("owner", "the operator", ["owner"]),
        ("title", None, ["title"]),
        ("id", "relay-4", ["id"]),
        ("id", "RELAY-04", ["id"]),
        ("id", "RELAY_004", ["id"]),
        ("level", "OUGHT", ["level"]),
        ("surface", "smtp", ["surface"]),
        ("source", {"cite": PROTOCOL_FILE_CITE}, ["source", "quote"]),
        ("negative", [{"code": "invalid_argument", "reason": "X"}], ["negative", "side_effect"]),
        ("retired", "sometimes", ["retired"]),
    ],
    ids=[
        "tests-field",
        "status-field",
        "unknown-field",
        "missing-title",
        "id-lowercase",
        "id-two-digits",
        "id-underscore",
        "level",
        "surface",
        "source-without-quote",
        "negative-without-side-effect",
        "retired-not-bool",
    ],
)
def test_load_registry_refuses_an_invalid_entry_naming_file_and_field(
    tmp_path: Path, field: str, value: object, named: list[str]
) -> None:
    """The invalid entry is the second in its file, behind a valid one."""
    mod = registry_module()
    invalid = entry_fields("RELAY-002")
    if value is None:
        del invalid[field]
    else:
        invalid[field] = value
    registry = plant_registry(tmp_path)
    write_area(registry, "relay", entry_fields("RELAY-001"), invalid)

    with pytest.raises(mod.RegistryError) as excinfo:
        mod.load_registry(registry)

    message = str(excinfo.value)
    assert "relay.yaml" in message
    for name in named:
        assert re.search(rf"\b{name}\b", message), f"{name!r} not named in: {message}"


def test_load_registry_refuses_a_duplicate_id_naming_both_files(tmp_path: Path) -> None:
    mod = registry_module()
    registry = plant_registry(tmp_path)
    write_area(registry, "relay", entry_fields("AREA-001"))
    write_area(registry, "money", entry_fields("AREA-002"), entry_fields("AREA-001"))

    with pytest.raises(mod.RegistryError) as excinfo:
        mod.load_registry(registry)

    message = str(excinfo.value)
    assert "AREA-001" in message
    assert "relay.yaml" in message
    assert "money.yaml" in message


def test_load_registry_refuses_a_duplicate_id_within_one_file(tmp_path: Path) -> None:
    mod = registry_module()
    registry = plant_registry(tmp_path)
    write_area(registry, "relay", entry_fields("AREA-001"), entry_fields("AREA-001"))

    with pytest.raises(mod.RegistryError) as excinfo:
        mod.load_registry(registry)

    assert "AREA-001" in str(excinfo.value)
    assert "relay.yaml" in str(excinfo.value)


# ---------------------------------------------------------------------------
# check_quotes (pure)
# ---------------------------------------------------------------------------

_GO_SOURCE = """\
package offers

// An Exchange MUST refuse an offer whose signature does not
// verify against its published key, and MUST NOT record a
//   transaction for it.
func refuse() {}
"""
_SHELL_SOURCE = """\
#!/bin/sh
# The operator rotates the signing key
#    before the old one expires.
exit 0
"""
_BLOCK_SOURCE = """\
/*
 * A Broker SHOULD route each request to
 * the Exchange that issued the offer.
 */
"""
_GO_QUOTE = (
    "An Exchange MUST refuse an offer whose signature does not verify against its "
    "published key, and MUST NOT record a transaction for it."
)


def _source(cite: str, quote: str) -> dict[str, str]:
    return {"cite": cite, "quote": quote}


def test_check_quotes_normalises_comment_leaders_and_flags_edits_and_missing_files(
    tmp_path: Path,
) -> None:
    mod = registry_module()
    cited = tmp_path / "cited"
    for relative, text in [
        ("src/offers.go", _GO_SOURCE),
        ("scripts/rotate.sh", _SHELL_SOURCE),
        ("proto/routing.proto", _BLOCK_SOURCE),
    ]:
        (cited / relative).parent.mkdir(parents=True, exist_ok=True)
        (cited / relative).write_text(text)
    registry = plant_registry(tmp_path)
    write_area(
        registry,
        "quotes",
        entry_fields("QUOTE-001", source=_source("src/offers.go", _GO_QUOTE)),
        entry_fields(
            "QUOTE-002",
            source=_source(
                "scripts/rotate.sh",
                "The operator rotates the signing key before the old one expires.",
            ),
        ),
        entry_fields(
            "QUOTE-003",
            source=_source(
                "proto/routing.proto",
                "A Broker SHOULD route each request to  the Exchange that issued the offer.",
            ),
        ),
        entry_fields(
            "QUOTE-004",
            source=_source("src/offers.go", _GO_QUOTE.replace("MUST NOT", "SHOULD NOT")),
        ),
        entry_fields("QUOTE-005", source=_source("src/absent.go", _GO_QUOTE)),
    )

    def read_cited(cite: str) -> str | None:
        path = cited / cite
        return path.read_text() if path.is_file() else None

    violations = mod.check_quotes(mod.load_registry(registry), read_cited)

    assert len(violations) == 2, violations
    assert [v for v in violations if "QUOTE-004" in v] != []
    assert [v for v in violations if "QUOTE-005" in v] != []
    for passing in ("QUOTE-001", "QUOTE-002", "QUOTE-003"):
        assert not [v for v in violations if passing in v], violations


# ---------------------------------------------------------------------------
# Live quote check: real cites, the pinned module, the real registry
# ---------------------------------------------------------------------------


def test_pinned_protocol_dir_finds_the_module_go_resolves() -> None:
    if shutil.which("go") is None:
        pytest.skip(_GO_ABSENT)
    directory = registry_module().pinned_protocol_dir()
    assert directory is not None
    assert (Path(directory) / "proto" / "fora" / "v1" / "fora.proto").is_file()


def test_pinned_protocol_dir_is_none_without_go(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = registry_module()
    monkeypatch.setenv("PATH", "")
    assert mod.pinned_protocol_dir() is None


def test_pinned_protocol_dir_raises_with_gos_output_when_go_cannot_find_it(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch
) -> None:
    """With go present, a failed lookup raises; the host check never skips silently."""
    if shutil.which("go") is None:
        pytest.skip(_GO_ABSENT)
    mod = registry_module()
    monkeypatch.setenv("GOPROXY", "off")
    monkeypatch.setenv("GOMODCACHE", str(tmp_path / "empty-modcache"))
    monkeypatch.setenv("GOFLAGS", "-mod=mod")
    monkeypatch.setenv("GOWORK", "off")
    with pytest.raises(Exception) as excinfo:
        mod.pinned_protocol_dir()
    assert "GOPROXY=off" in str(excinfo.value)


def test_resolve_cite_reads_the_pinned_module_and_the_repository(tmp_path: Path) -> None:
    """Real quotes pass and edited ones fail, through resolve_cite as the reader."""
    if shutil.which("go") is None:
        pytest.skip(_GO_ABSENT)
    mod = registry_module()
    registry = plant_registry(tmp_path)
    write_area(
        registry,
        "live",
        entry_fields("LIVE-001", source=_source(PROTOCOL_FILE_CITE, PROTOCOL_QUOTE)),
        entry_fields("LIVE-002", source=_source(ADR_CITE, ADR_QUOTE)),
        entry_fields(
            "LIVE-003",
            source=_source(PROTOCOL_FILE_CITE, PROTOCOL_QUOTE.replace("MUST", "SHOULD")),
        ),
        entry_fields(
            "LIVE-004", source=_source(ADR_CITE, ADR_QUOTE.replace("one place", "two places"))
        ),
    )

    violations = mod.check_quotes(mod.load_registry(registry), mod.resolve_cite)

    assert len(violations) == 2, violations
    assert [v for v in violations if "LIVE-003" in v] != []
    assert [v for v in violations if "LIVE-004" in v] != []
    assert mod.resolve_cite(f"{PROTOCOL_MODULE}/proto/fora/v1/absent.proto") is None
    assert mod.resolve_cite("docs/architecture/absent.md") is None


def test_every_quote_in_the_real_registry_appears_at_its_cite() -> None:
    if shutil.which("go") is None:
        pytest.skip(_GO_ABSENT)
    mod = registry_module()
    assert mod.check_quotes(mod.REGISTRY, mod.resolve_cite) == []
