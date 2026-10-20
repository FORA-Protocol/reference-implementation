"""Meta-guard — the unresolvable-reference gate must never pass without reading.

``scripts/check-published-refs.sh`` is the only thing standing between the
published tree and pointers a public reader cannot open. It is wired into
``make quality``, so a green quality run is taken as evidence that no such
pointer exists.

That evidence is only worth anything if the gate can fail. It once could not:
``grep``'s "No such file or directory" went to a muted stderr and its non-zero
exit was swallowed by ``|| true``, so a renamed top-level directory silently
deleted the gate's coverage and it reported PASS having scanned nothing at all.
The same shape had already cost the publish tool two broken gates, one of which
returned success at the exact moment it found a stray file.

A guard that can go blind needs a test that watches it, and this is that test.
The four outcomes it has to get right, each driven by one or more cases below:

    clean tree           -> PASS          (it does not cry wolf)
    planted violation    -> FAIL          (it can still see)
    missing search root  -> FAIL loudly   (it refuses to guess)
    waiver on the line   -> PASS          (the documented escape hatch works)

The third row is the one that matters, and the reason is narrower than it looks:
none of the other three ever removes a search root, so the gate could be blind to
a missing one and every case in those rows would still pass. Only the third
builds the tree that makes the blindness visible.

The planted-violation row carries several cases, because the gate has more than
one pattern family and more than one search root, and a case is added whenever
either set grows. The held-back family adds a fifth outcome, a file the gate must
not read at all: a path the publish holds back never ships, so nothing in it is
a pointer a public reader could follow. Its complement has cases too: a file
whose name only extends a held-back entry's ships, so the gate reads it.

Unlike its sibling guards, the subject here is a shell script rather than Python
source, so there is no pure detector function to feed inline snippets to. The
cases build synthetic trees under ``tmp_path`` and run the real script against
them through its ``--root`` argument — the seam exists for exactly this, and
testing the shipped artifact rather than a reimplementation of it is the point.
An argument rather than an environment variable, because a variable that selects
what a gate reads can redirect it in production too.

Pure subprocess work: no stack, no Docker, no database. The marks come from
``guard_harness.guard_marks``, whose docstring says why the isolation one is
there.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from .conftest import REPO_ROOT
from .guard_harness import guard_marks, hold_back, run_gate
from .published_paths import first_entry as _first_entry
from .published_paths import shared_array as _shared_array

# REPO_ROOT rather than a parents[N] walk: inside the runner container the
# harness sits at /runner/harness, which has fewer parents than the host layout,
# and computing the walk here raises at import — killing collection for the whole
# tier rather than this one module.
_GATE = REPO_ROOT / "scripts" / "check-published-refs.sh"

# The skip covers the container leg: scripts/ is not COPYed into the runner image
# and not bind-mounted, so there is nothing to drive there. The host `test-fast`
# target runs this module on every commit, so the skip costs no coverage.
pytestmark = guard_marks(skip_when=not _GATE.is_file())

# One reference of each kind the gate exists to reject. All are resolvable in
# this repository and unresolvable in the published one, which is the whole
# distinction under test.
# published-ref-allow: the fixture a gate is tested with has to be a real violation
_FULL_PATH_VIOLATION = "# see docs/design/design-exchange.md for the layering\n"
# An unpublished doc that exists only in the fixture tree. The gate derives its
# bare-basename patterns from whatever docs/*.md it finds outside
# docs/architecture, so the name the violation cites has to be a name the fixture
# actually creates — hence one constant used by both.
_UNPUBLISHED_DOC = "unpublished-note.md"
# The same class of target named WITHOUT the docs/ prefix. This is the form a
# path search misses, and it is caught only by that derived basename list — a
# half of the gate no case reached while the fixture contained no docs/*.md
# outside docs/architecture.
_BARE_NAME_VIOLATION = f"# see {_UNPUBLISHED_DOC} for the layering\n"
# published-ref-allow: as above
_CLAUDE_MD_VIOLATION = "# the repository conventions in CLAUDE.md require this\n"
_WAIVED_VIOLATION = (
    # published-ref-allow: as above — this one also demonstrates the waiver itself
    "# published-ref-allow: illustrating the escape hatch\n"
    "# see docs/design/design-exchange.md for the layering\n"
)
# The protocol file, cited four ways. The pattern for the retired in-tree copy
# must reject the in-tree path, alone or beside a module citation, and must
# accept the path qualified by the pinned module, because naming the module is
# what lets a public reader open it. A versioned module path is not the accepted
# form, so it stays flagged. The module-qualified line is not a violation and
# carries no waiver.
_PROTOCOL_MODULE_CITATION = (
    "# wire types: github.com/FORA-Protocol/protocol/proto/fora/v1/fora.proto\n"
)
# A page of the protocol's own website docs, cited through the module. Its path
# matches the unpublished-docs pattern, and it is resolvable all the same because
# the module ships it.
_PROTOCOL_MODULE_DOC_CITATION = (
    "# see github.com/FORA-Protocol/protocol/website/src/content/docs/protocol/authentication.mdx\n"
)
_MODULE_DOC_AND_IN_REPO_DOC_VIOLATION = (
    "# see github.com/FORA-Protocol/protocol/website/src/content/docs/protocol/a.mdx"
    # published-ref-allow: the fixture a gate is tested with has to be a real violation
    " and docs/protocol/a.mdx\n"
)
# published-ref-allow: the fixture a gate is tested with has to be a real violation
_PROTOCOL_IN_TREE_VIOLATION = "# wire types: proto/fora/v1/fora.proto\n"
_PROTOCOL_BOTH_FORMS_VIOLATION = (
    # published-ref-allow: as above
    "# github.com/FORA-Protocol/protocol/proto/fora/v1/fora.proto, copied to proto/fora/v1/fora.proto\n"
)
_PROTOCOL_VERSIONED_VIOLATION = (
    # published-ref-allow: as above
    "# wire types: github.com/FORA-Protocol/protocol@v1.0.8/proto/fora/v1/fora.proto\n"
)
# There is deliberately NO constant here for the doc the publish stages out of
# this tree. The two cases that need it read it from the shared definition
# inside their own bodies, and both halves of that matter: transcribing the name
# would keep the cases passing after the entry had been renamed, and reading it
# at module scope would fail at import wherever scripts/ is absent, which pytest
# answers by aborting the whole session.
#
# The gate also subtracts INHERIT_FROM_MAIN when deriving bare basenames, so a
# citation of an inherited doc is resolvable too. That half had its own case
# while the retired aws-demo handoff gave INHERIT_FROM_MAIN a docs/-shaped
# entry; with the array down to LICENSE and README.md the subtraction has no
# member the basename sweep can see, so there is nothing real for a case to
# pin. If a docs/ entry ever returns to INHERIT_FROM_MAIN, reinstate the case:
# cite the entry from a published file and assert the gate accepts it.


def _build_tree(root: Path) -> None:
    """Materialise the minimum tree the gate considers a complete search set.

    Every directory, docs/ file, root file and allowlisted script named by the
    shared definition has to exist, because a missing one is itself a failure the
    gate reports — that is the behaviour the missing-root case asserts.

    The unpublished doc is not decoration. The gate derives the bare-basename
    half of its pattern set from whatever ``docs/*.md`` exists outside
    ``docs/architecture``; with none present that half compiles to nothing and
    never runs, which is how four separate ways of breaking the gate once passed
    this suite.
    """
    for directory in _shared_array("ALLOW_DIRS"):
        (root / directory).mkdir(parents=True, exist_ok=True)
    for doc in _shared_array("ALLOW_DOC_FILES"):
        (root / doc).parent.mkdir(parents=True, exist_ok=True)
        (root / doc).touch()
    for name in _shared_array("ALLOW_ROOT_FILES"):
        (root / name).touch()
    for script in _shared_array("ALLOW_SCRIPTS"):
        dest = root / script
        dest.parent.mkdir(parents=True, exist_ok=True)
        src = REPO_ROOT / script
        # Every allowlisted script that exists here is copied verbatim. The gate
        # and the definition it sources have to be the real ones; the rest are
        # real too, so a pattern that would flag a line in a shipped script turns
        # the clean-tree case red rather than passing unnoticed.
        dest.write_bytes(src.read_bytes() if src.is_file() else b"")

    (root / "docs" / _UNPUBLISHED_DOC).write_text("# not a published doc\n")
    for inherited in _shared_array("INHERIT_FROM_MAIN"):
        target = root / inherited
        target.parent.mkdir(parents=True, exist_ok=True)
        target.touch()


def _run_gate(root: Path) -> subprocess.CompletedProcess[str]:
    """This gate's parameters. The mechanics are shared, and only these differ."""
    return run_gate(root, _GATE.name)


def test_gate_passes_on_a_clean_tree(tmp_path: Path) -> None:
    """A tree with no dangling pointers is reported clean."""
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text("# ordinary configuration\n")

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "PASS" in proc.stdout, proc.stdout


def test_gate_flags_a_reference_in_a_published_root_file(tmp_path: Path) -> None:
    """A pointer into an unpublished docs/ path fails, naming file and line.

    ``docker-compose.yml`` is deliberate: the root build files ship, and were
    once outside the gate's search set entirely, so two live pointers passed
    clean for as long as that list was maintained by hand.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(_FULL_PATH_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "docker-compose.yml:1:" in proc.stdout, proc.stdout


def test_gate_fails_loudly_when_a_search_root_is_absent(tmp_path: Path) -> None:
    """A missing search root aborts — it must never be read as "nothing found".

    This is the regression the whole file exists for. The gate previously
    reported PASS here, so every other assertion in this module could hold while
    it scanned an empty tree.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(_FULL_PATH_VIOLATION)
    # Rename a top-level directory the way an ordinary refactor would.
    (tmp_path / "src").rename(tmp_path / "src_moved")

    proc = _run_gate(tmp_path)

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, f"gate passed with a missing search root:\n{combined}"
    assert "search root" in combined, combined
    assert "PASS" not in proc.stdout, proc.stdout


def test_gate_flags_a_bare_filename(tmp_path: Path) -> None:
    """A doc named without its docs/ prefix is the same violation.

    This is the form a path search misses, and the only case that exercises the
    basename list the gate derives from the unpublished docs on disk. Without it,
    that derivation can be deleted outright and this suite stays green.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(_BARE_NAME_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "docker-compose.yml:1:" in proc.stdout, proc.stdout


def test_gate_accepts_a_citation_of_an_allowlisted_doc(tmp_path: Path) -> None:
    """A docs/ file the publish stages out of this tree is resolvable too.

    The basename derivation sweeps in every ``docs/*.md`` outside
    ``docs/architecture``, and an ALLOW_DOC_FILES entry is one of them — it lives
    here and travels, unlike the design notes around it. Subtracting that array
    as well as the inherited one is what keeps citing it legal. Without this case
    the ALLOW_DOC_FILES half of the subtraction can be deleted and the suite
    still passes, because the inherited case covers only the other half.
    """
    _build_tree(tmp_path)
    allowlisted_doc = _first_entry("ALLOW_DOC_FILES")
    (tmp_path / "docker-compose.yml").write_text(f"# the operator guide is in {allowlisted_doc}\n")

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


def test_gate_scans_allowlisted_docs(tmp_path: Path) -> None:
    """A violation inside a published docs/ file is caught.

    Those files travel, so they are search roots like any published script. A
    docs/ entry that was published but never scanned would carry pointers into
    the design notes it was extracted from, which is the likeliest way one of
    these documents goes wrong.
    """
    _build_tree(tmp_path)
    allowlisted_doc = _first_entry("ALLOW_DOC_FILES")
    (tmp_path / allowlisted_doc).write_text(_FULL_PATH_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert f"{allowlisted_doc}:1:" in proc.stdout, proc.stdout


def test_gate_flags_a_reference_to_the_conventions_file(tmp_path: Path) -> None:
    # published-ref-allow: naming the file under test is unavoidable here
    """The conventions file does not travel, so naming it is a dangling pointer.

    One case per pattern family the gate carries, so deleting a family from the
    pattern list cannot pass unnoticed.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(_CLAUDE_MD_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


def test_gate_scans_published_scripts(tmp_path: Path) -> None:
    """A violation inside an allowlisted script is caught.

    The published scripts are search roots in their own right. Before that, they
    were reached through a prefix denylist standing in for "not published", which
    skipped any published script whose name happened to match one of its
    prefixes.
    """
    _build_tree(tmp_path)
    (tmp_path / "scripts" / "devstack.sh").write_text(_FULL_PATH_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "scripts/devstack.sh:1:" in proc.stdout, proc.stdout


def test_waiver_comment_suppresses_a_deliberate_reference(tmp_path: Path) -> None:
    """The documented per-line escape hatch works, on the preceding line."""
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(_WAIVED_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "PASS" in proc.stdout, proc.stdout


@pytest.mark.parametrize(
    "line",
    [_PROTOCOL_MODULE_CITATION, _PROTOCOL_MODULE_DOC_CITATION],
    ids=["fora-proto", "website-doc-page"],
)
def test_gate_accepts_the_protocol_file_cited_through_its_module(tmp_path: Path, line: str) -> None:
    """A path cited through the protocol module is resolvable, so it passes.

    The in-tree pattern also matches inside this path. If the gate does not
    remove the module-qualified occurrence before it judges the line, every
    registry entry citing the protocol fails the gate.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(line)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "PASS" in proc.stdout, proc.stdout


@pytest.mark.parametrize(
    "line",
    [
        _PROTOCOL_IN_TREE_VIOLATION,
        _PROTOCOL_BOTH_FORMS_VIOLATION,
        _PROTOCOL_VERSIONED_VIOLATION,
        _MODULE_DOC_AND_IN_REPO_DOC_VIOLATION,
    ],
    ids=[
        "in-tree-path",
        "module-and-in-tree-on-one-line",
        "versioned-module-path",
        "module-doc-and-in-repo-doc-on-one-line",
    ],
)
def test_gate_flags_the_protocol_file_cited_any_other_way(tmp_path: Path, line: str) -> None:
    """The in-tree path is flagged alone, beside a module citation, and under a version.

    The second case is the one that proves the module-qualified exemption is
    narrow: the gate removes only the exact module-qualified path and still sees
    the in-tree path left on the line.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(line)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "docker-compose.yml:1:" in proc.stdout, proc.stdout


# Held-back paths, in the shapes DENY_PATHS takes: a directory (trailing slash),
# a file, and a Python module, which a citation can name by its import path. They
# are synthetic and live only in the fixture, so the real gate never sees them as
# entries and this module needs no waiver for writing them.
_HELD_DIR = "deploy/kept/held-back-dir/"
_HELD_FILE = "deploy/kept/held-back-guide.md"
_HELD_MODULE = "tests/e2e/harness/held_back_module.py"


def _hold_back(root: Path, *entries: str) -> None:
    """Add ``entries`` to DENY_PATHS in the fixture's own copy of the definition."""
    hold_back(root / "scripts" / "published-paths.sh", *entries)


@pytest.mark.parametrize(
    "citation",
    [
        "# see deploy/kept/held-back-dir/main.tf\n",
        "# see kept/held-back-dir/main.tf\n",
        "# copied from ../kept/held-back-dir.\n",
        "# see deploy/kept/held-back-guide.md\n",
        "# the steps are in held-back-guide.md#first-deployment\n",
        "from harness.held_back_module import Step\n",
        "# reads tests/e2e/harness/held_back_module.py\n",
    ],
    ids=[
        "directory-by-full-path",
        "directory-by-its-last-two-components",
        "directory-at-the-end-of-a-sentence",
        "file-by-full-path",
        "file-by-its-name-alone",
        "module-by-its-import-path",
        "module-by-full-path",
    ],
)
def test_gate_flags_a_citation_of_a_held_back_path(tmp_path: Path, citation: str) -> None:
    """Every form a published file can cite a held-back path in is refused.

    A held-back path is absent from the public tree, so a citation of it is the
    same dangling pointer as one into an unpublished doc. A directory is matched
    by its last two components, the shortest form a relative citation takes; a
    file by its name alone, the form a bare doc filename takes; a Python module by
    its import path too, because a dotted import names the file without its
    extension.
    """
    _build_tree(tmp_path)
    _hold_back(tmp_path, _HELD_DIR, _HELD_FILE, _HELD_MODULE)
    (tmp_path / "docker-compose.yml").write_text(citation)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "docker-compose.yml:1:" in proc.stdout, proc.stdout


@pytest.mark.parametrize(
    "line",
    [
        "# see deploy/kept/held-back-dir-sibling/main.tf\n",
        "# see deploy/kept/held-back-guide.md.orig\n",
        "# see deploy/kept/pre-held-back-guide.md\n",
        "from harness.held_back_module_extra import Step\n",
        "from harness.extra_held_back_module import Step\n",
        "# the held-back-dir variable defaults to null\n",
    ],
    ids=[
        "directory-sibling",
        "file-sibling",
        "file-whose-name-ends-like-it",
        "module-sibling",
        "module-whose-name-ends-like-it",
        "bare-directory-name",
    ],
)
def test_gate_accepts_a_name_that_only_resembles_a_held_back_path(
    tmp_path: Path, line: str
) -> None:
    """A different path whose name starts like an entry's is not a citation of it.

    The patterns are bounded on both sides, so a name that extends an entry's,
    at either end, is a different file; and a directory is never matched by its
    last component alone, because a bare directory name is as often an ordinary
    word.
    """
    _build_tree(tmp_path)
    _hold_back(tmp_path, _HELD_DIR, _HELD_FILE, _HELD_MODULE)
    (tmp_path / "docker-compose.yml").write_text(line)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


def test_gate_does_not_scan_a_held_back_file(tmp_path: Path) -> None:
    """What a held-back file says never reaches a public reader, so it is not read.

    A held-back directory's files cite each other and the rest of what is held
    back as a matter of course, and none of them ship. The planted file carries
    one violation of each family — a held-back sibling and an unpublished doc —
    and the gate must report neither.
    """
    _build_tree(tmp_path)
    _hold_back(tmp_path, _HELD_DIR, _HELD_FILE)
    inside = tmp_path / _HELD_DIR / "notes.md"
    inside.parent.mkdir(parents=True)
    inside.write_text(f"see held-back-guide.md and {_FULL_PATH_VIOLATION}")
    (tmp_path / _HELD_FILE).write_text(_FULL_PATH_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "PASS" in proc.stdout, proc.stdout


@pytest.mark.parametrize(
    ("held", "neighbour"),
    [
        (f"{_HELD_DIR}notes.md", "deploy/kept/held-back-dir-sibling/notes.md"),
        (_HELD_FILE, f"{_HELD_FILE}.orig"),
    ],
    ids=["directory-sibling", "file-sibling"],
)
def test_gate_scans_a_published_neighbour_of_a_held_back_path(
    tmp_path: Path, held: str, neighbour: str
) -> None:
    """A file whose name only extends a held-back entry's ships, so it is read.

    The complement of the case above. The skip is bounded the way the patterns
    are: a directory entry covers what sits under its slash, a file entry that one
    file. Without this case, dropping either bound skips the neighbour too, and
    every other case in this module still passes. The held-back file carries the
    same violation as its neighbour and stays unreported, so one run shows both
    sides of the bound.
    """
    _build_tree(tmp_path)
    _hold_back(tmp_path, _HELD_DIR, _HELD_FILE)
    for relative in (held, neighbour):
        planted = tmp_path / relative
        planted.parent.mkdir(parents=True, exist_ok=True)
        planted.write_text(_FULL_PATH_VIOLATION)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert f"{neighbour}:1:" in proc.stdout, proc.stdout
    assert f"{held}:1:" not in proc.stdout, proc.stdout


def test_gate_honours_a_waiver_on_the_same_line(tmp_path: Path) -> None:
    """The waiver works on the line it sits on, which is the form a shell array entry takes.

    The preceding-line form has its own case above; this one is what every entry
    of DENY_PATHS relies on, because each carries its waiver on its own line.
    """
    _build_tree(tmp_path)
    _hold_back(tmp_path, _HELD_FILE)
    (tmp_path / "docker-compose.yml").write_text(
        "# see deploy/kept/held-back-guide.md  # published-ref-allow: the case needs one\n"
    )

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


def test_gate_flags_a_local_only_file(tmp_path: Path) -> None:
    """A file that stays on the operator's machine is named only at the cost of a dangling pointer.

    This pattern family is derived from LOCAL_ONLY_FILES, the array closest in
    shape to DENY_PATHS, and it had no case of its own. The name is read from the
    shared definition inside the body, for the reason ``first_entry`` gives.
    """
    _build_tree(tmp_path)
    local_only = _first_entry("LOCAL_ONLY_FILES")
    (tmp_path / "docker-compose.yml").write_text(f"# run {local_only} first\n")

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "docker-compose.yml:1:" in proc.stdout, proc.stdout


def test_gate_does_not_scan_python_build_metadata(tmp_path: Path) -> None:
    """A package's build metadata lists every module by path, held-back ones included.

    uv writes it into tests/e2e on every sync, gitignored like the other build
    output the scan skips, and it never ships. Reading it would make the gate
    fail on any checkout that has run the harness once.
    """
    _build_tree(tmp_path)
    _hold_back(tmp_path, _HELD_MODULE)
    sources = tmp_path / "tests" / "e2e" / "fixture_e2e.egg-info" / "SOURCES.txt"
    sources.parent.mkdir(parents=True)
    sources.write_text("harness/held_back_module.py\n")

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


def _citation_of(entry: str) -> str:
    """The shortest form a published file can cite ``entry`` in and still be refused."""
    if entry.endswith("/"):
        parts = entry.rstrip("/").split("/")
        return "/".join(parts[-2:]) + "/main.tf"
    return entry.rsplit("/", 1)[-1]


def test_gate_flags_a_citation_of_every_real_held_back_path(tmp_path: Path) -> None:
    """Every path the publish really holds back is a refused citation.

    The synthetic cases above prove the pattern forms; this one proves the real
    entries produce them, in the shortest form each can be cited in. It raises
    on an empty array, which would otherwise pass having checked nothing.
    """
    entries = _shared_array("DENY_PATHS")
    if not entries:
        raise AssertionError("DENY_PATHS is empty, so this case would assert nothing")
    _build_tree(tmp_path)
    lines = [f"# see {_citation_of(entry)}\n" for entry in entries]
    (tmp_path / "docker-compose.yml").write_text("".join(lines))

    proc = _run_gate(tmp_path)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    for number, entry in enumerate(entries, start=1):
        assert f"docker-compose.yml:{number}:" in proc.stdout, f"{entry} was not flagged"


def test_gate_accepts_the_bare_name_of_a_real_held_back_directory(tmp_path: Path) -> None:
    """A held-back directory's last component alone is not a citation, by design.

    Published scripts pass a held-back stack's name as an argument, and a
    component's bare name is as often an ordinary word, so the gate leaves those
    to review. This case pins that choice: making bare names a pattern would
    refuse those uses, and has to be decided rather than slipped in.
    """
    directories = [entry for entry in _shared_array("DENY_PATHS") if entry.endswith("/")]
    if not directories:
        raise AssertionError("DENY_PATHS holds no directory, so this case would assert nothing")
    _build_tree(tmp_path)
    names = [entry.rstrip("/").rsplit("/", 1)[-1] for entry in directories]
    (tmp_path / "docker-compose.yml").write_text("".join(f"# the {name} name\n" for name in names))

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


@pytest.mark.parametrize(
    "line",
    [
        'onboarding_image = "registry.example/fora/onboarding@sha256:0"\n',
        "# the publisher onboarding page answers from the demo\n",
        "# ghcr.io/fora-protocol/onboarding was never published\n",
        "# reaches http://onboarding:8084 inside the network\n",
    ],
    ids=["variable", "prose", "image-name", "in-network-url"],
)
def test_gate_accepts_an_ordinary_use_of_a_held_back_directorys_name(
    tmp_path: Path, line: str
) -> None:
    """The real entries flag a held-back path, never the word it is named after.

    "onboarding" names a held-back directory, and it is also a variable prefix, an
    image name, a compose service and plain prose in files that ship.
    """
    _build_tree(tmp_path)
    (tmp_path / "docker-compose.yml").write_text(line)

    proc = _run_gate(tmp_path)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
