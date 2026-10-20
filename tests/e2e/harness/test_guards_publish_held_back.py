"""Meta-guard — a path the publish holds back, or no longer publishes, must not reach the public tree.

``scripts/published-paths.sh`` publishes whole subtrees, and its DENY_PATHS array
names the paths inside them that stay here: a deployment's own production stack,
or a component that is not part of the reference implementation. The publish
tool removes every entry from the staged tree, and its forbidden-path scan
refuses any entry that is still there. A push cannot be taken back, so each
layer is tested on its own, and so is every check that keeps an entry honest:

    entries planted, tool as shipped     -> held back; near misses still travel
    entries planted, hold-back removed   -> the scan refuses them on its own
    an entry the source ref lacks        -> refused before any work
    an entry of the wrong shape          -> refused before any work
    an entry that is also a route        -> refused before any work
    a definition unlike the source ref's -> refused before any work

A misspelled or mis-shaped entry holds back nothing, so the path it was written
to keep here would ship without a word — which is why those refusals have cases
of their own rather than being left to review. The last row covers every array,
not DENY_PATHS alone: the tool reads the definition from the checkout it runs in
and stages the source ref's tree, so a checkout whose copy differs would act on
one set of arrays while the public tree carried another.

Every case drives the real script against the fixture from ``publish_harness``.
The entries are synthetic: a case adds them to the fixture's own copy of the
shared definition, which is the copy the tool sources, so they are read exactly
as the real ones are. A case that needs the checkout and the source ref to
disagree changes one of the two copies and leaves the other alone.
"""

from __future__ import annotations

import re
from collections.abc import Callable
from pathlib import Path

import pytest

from .guard_harness import commit_all, hold_back, run_git
from .published_paths import first_entry, shared_array
from .publish_harness import (
    PUBLISH_GUARD_MARKS,
    branch_sha,
    edit_fixture_definition,
    hold_back_in_fixture,
    publish_without_hold_back,
    run_publish,
)

pytestmark = PUBLISH_GUARD_MARKS

# A held-back directory and a held-back file, under a directory that ships.
_HELD_DIR = "deploy/kept/held-back-dir/"
_HELD_FILE = "deploy/kept/held-back-guide.md"
# Names that extend an entry's. They are different paths and must still travel:
# a directory entry is a prefix only up to its trailing slash, and a file entry
# matches only itself.
_DIR_SIBLING = "deploy/kept/held-back-dir-sibling/inner.txt"
_FILE_SIBLING = "deploy/kept/held-back-guide.md.orig"
# A neighbour of the held-back directory, standing in for the stacks that ship
# beside the one a deployment keeps to itself.
_NEIGHBOUR = "deploy/kept/shipping-stack/main.tf"


def _plant(source: Path, *paths: str) -> None:
    """Write each path into the fixture source repository and commit them."""
    for relative in paths:
        target = source / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(f"# {relative}\n")
    commit_all(source, "plant paths for a held-back case")


def _additions(stdout: str) -> set[str]:
    """The paths the dry run lists as new on the public repository, one per line.

    Compared as whole lines: the file entry is a prefix of its own near miss, so
    a substring test would report the sibling as the entry.
    """
    return {line.strip()[2:] for line in stdout.splitlines() if line.strip().startswith("+ ")}


def test_held_back_paths_leave_the_staged_tree(publish_fixture: tuple[Path, Path, Path]) -> None:
    """Both entry shapes are removed, and nothing beside them is.

    The staged worktree is the primary evidence, because the dry run stops short
    of tearing it down; the additions listing is the operator's view of the same
    tree, and the only place a held-back path would show up during review.
    """
    source, bare, worktree = publish_fixture
    _plant(source, f"{_HELD_DIR}inner.txt", _HELD_FILE, _DIR_SIBLING, _FILE_SIBLING, _NEIGHBOUR)
    hold_back_in_fixture(source, _HELD_DIR, _HELD_FILE)

    proc = run_publish(source, bare, worktree)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    added = _additions(proc.stdout)
    assert not (worktree / _HELD_DIR).exists(), proc.stdout
    assert not (worktree / _HELD_FILE).exists(), proc.stdout
    assert f"{_HELD_DIR}inner.txt" not in added, proc.stdout
    assert _HELD_FILE not in added, proc.stdout
    for kept in (_DIR_SIBLING, _FILE_SIBLING, _NEIGHBOUR):
        assert (worktree / kept).is_file(), f"{kept} did not travel:\n{proc.stdout}"
        assert kept in added, f"{kept} is missing from the additions listing:\n{proc.stdout}"


def test_the_forbidden_scan_refuses_a_held_back_path_on_its_own(
    publish_fixture: tuple[Path, Path, Path], tmp_path: Path
) -> None:
    """With the hold-back step gone, the scan still refuses the tree.

    The two layers derive from one array, so no edit of the definition can put a
    held-back path in front of the scan while the hold-back step works. The case
    therefore runs a copy of the tool with that one line removed — the shape a
    broken hold-back would leave — and asserts that the scan alone refuses both
    entry shapes, naming each path it found.
    """
    source, bare, worktree = publish_fixture
    _plant(source, f"{_HELD_DIR}inner.txt", _HELD_FILE)
    hold_back_in_fixture(source, _HELD_DIR, _HELD_FILE)

    proc = run_publish(source, bare, worktree, script=publish_without_hold_back(tmp_path))

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, combined
    assert "forbidden paths present in staged tree" in combined, combined
    assert f"{_HELD_DIR}inner.txt" in combined, combined
    assert _HELD_FILE in combined, combined


@pytest.mark.parametrize(
    "missing",
    ["deploy/kept/never-written/", "/etc/", "../outside/"],
    ids=["misspelled", "absolute", "outside-the-repository"],
)
def test_an_entry_the_source_ref_does_not_carry_is_refused(
    publish_fixture: tuple[Path, Path, Path], missing: str
) -> None:
    """An entry that matches nothing is a typo, and a typo ships what it meant to keep.

    An absolute entry and one that climbs out of the repository match nothing
    either, so the same check refuses them; there is no separate rule for their
    shape. The check runs before the staging worktree is created, so a run
    stopped here did no work at all.
    """
    source, bare, worktree = publish_fixture
    hold_back_in_fixture(source, missing)

    proc = run_publish(source, bare, worktree)

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, combined
    assert missing in combined, combined
    assert "DENY_PATHS entries above do not match" in combined, combined
    assert not worktree.exists(), combined


@pytest.mark.parametrize(
    ("planted", "entry", "expected"),
    [
        (f"{_HELD_DIR}inner.txt", _HELD_DIR.rstrip("/"), "expected a blob"),
        (_HELD_FILE, f"{_HELD_FILE}/", "expected a tree"),
    ],
    ids=["directory-without-its-slash", "file-with-a-slash"],
)
def test_an_entry_of_the_wrong_shape_is_refused(
    publish_fixture: tuple[Path, Path, Path], planted: str, entry: str, expected: str
) -> None:
    """The trailing slash has to say what the source ref holds there.

    A directory entered without it would become an exact-match rule in the
    forbidden-path scan, which never matches the files inside the directory, so
    only the hold-back step would stand between them and the public tree.
    """
    source, bare, worktree = publish_fixture
    _plant(source, planted)
    hold_back_in_fixture(source, entry)

    proc = run_publish(source, bare, worktree)

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, combined
    assert expected in combined, combined
    assert not worktree.exists(), combined


@pytest.mark.parametrize("route", ["an-allowlisted-script", "a-directory-holding-a-route"])
def test_an_entry_that_is_also_a_route_onto_the_tree_is_refused(
    publish_fixture: tuple[Path, Path, Path], route: str
) -> None:
    """A path cannot both ship and be held back.

    The first form holds back a file an allowlist publishes by name. The second
    holds back a directory with a published subtree inside it, so the hold-back
    would quietly delete what the allowlist promises. Both are refused before the
    remote is contacted. The entry is read from the shared definition inside the
    test body, never at import, for the reason ``published_paths.first_entry``
    gives.
    """
    source, bare, worktree = publish_fixture
    entry = first_entry("ALLOW_SCRIPTS") if route == "an-allowlisted-script" else "docs/"
    hold_back_in_fixture(source, entry)

    proc = run_publish(source, bare, worktree)

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, combined
    assert "is also a route onto the public tree" in combined, combined
    assert not worktree.exists(), combined


def test_every_real_held_back_path_leaves_the_staged_tree(
    publish_fixture: tuple[Path, Path, Path],
) -> None:
    """Each path the publish really holds back is absent from what it would push.

    The fixture creates every real entry on the source ref, so the tool meets
    them exactly as it meets this repository. It raises on an empty array, which
    would otherwise pass having checked nothing.
    """
    entries = shared_array("DENY_PATHS")
    if not entries:
        raise AssertionError("DENY_PATHS is empty, so this case would assert nothing")
    source, bare, worktree = publish_fixture

    proc = run_publish(source, bare, worktree)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    for entry in entries:
        assert not (worktree / entry).exists(), f"{entry} reached the staged tree"


def test_a_pinned_path_dropped_from_the_deny_list_is_refused(
    publish_fixture: tuple[Path, Path, Path],
) -> None:
    """Deleting a held-back entry stops the next publish instead of shipping the path.

    The tool keeps its own hand-written list of the paths that must stay held
    back, and refuses to run when one of them is missing from the shipped array.
    The entry is read from the definition inside the test body, for the reason
    ``published_paths.first_entry`` gives.
    """
    entry = first_entry("DENY_PATHS")
    source, bare, worktree = publish_fixture
    edit_fixture_definition(
        source, rf"^  {re.escape(entry)} +#.*\n", "", "drop a held-back path from the definition"
    )

    proc = run_publish(source, bare, worktree)

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, combined
    assert entry in combined, combined
    assert "pinned in MUST_HOLD_BACK but missing from DENY_PATHS" in combined, combined
    assert not worktree.exists(), combined


def _source_holds_back_more(source: Path) -> None:
    """The source ref holds back a path that a checkout cut before it does not name."""
    before = branch_sha(source, "source")
    _plant(source, f"{_HELD_DIR}inner.txt")
    hold_back_in_fixture(source, _HELD_DIR)
    run_git(source, "checkout", "-q", "-b", "older", before)


def _checkout_holds_back_more(source: Path) -> None:
    """The checkout holds back a path, in an edit the source ref does not carry.

    The path is planted first: ``_plant`` commits everything, so planting it
    after the edit would commit the edited definition onto the source ref too.
    """
    _plant(source, _HELD_FILE)
    hold_back(source / "scripts" / "published-paths.sh", _HELD_FILE)


def _source_publishes_less(source: Path) -> None:
    """The source ref stops publishing a script that a checkout cut before it still lists.

    The script stays in the source ref's tree, as it does after a decision to keep
    it here, so only the allowlist says it no longer ships. The entry is read from
    the shared definition inside the test body, for the reason
    ``published_paths.first_entry`` gives.
    """
    dropped = shared_array("ALLOW_SCRIPTS")[-1]
    before = branch_sha(source, "source")
    edit_fixture_definition(source, rf"^  {re.escape(dropped)}\n", "", f"stop publishing {dropped}")
    run_git(source, "checkout", "-q", "-b", "older", before)


@pytest.mark.parametrize(
    "diverge",
    [_source_holds_back_more, _checkout_holds_back_more, _source_publishes_less],
    ids=[
        "the-source-ref-holds-back-more",
        "the-checkout-holds-back-more",
        "the-source-ref-publishes-less",
    ],
)
def test_a_checkout_whose_definition_differs_from_the_source_ref_is_refused(
    publish_fixture: tuple[Path, Path, Path], diverge: Callable[[Path], None]
) -> None:
    """The arrays that decide a run have to be the ones that ship with its tree.

    The tool reads the definition from the checkout it runs in and stages the
    source ref's tree. A path only the source ref holds back, or one it stopped
    publishing, would pass every check that reads the checkout's arrays and ship.
    A path only the checkout holds back would be held back by a list the public
    tree does not carry, so no reference gate would refuse a citation of it.
    Each difference is refused before the staging worktree exists.
    """
    source, bare, worktree = publish_fixture
    diverge(source)

    proc = run_publish(source, bare, worktree)

    combined = proc.stdout + proc.stderr
    assert proc.returncode != 0, combined
    assert "differs from its copy on 'source'" in combined, combined
    assert not worktree.exists(), combined
