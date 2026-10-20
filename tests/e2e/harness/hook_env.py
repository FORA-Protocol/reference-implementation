"""A stand-in for the developer's repository, and a git hook's environment aimed at it.

The pre-push hook runs ``make quality-ci``, and git hands a hook variables that
select a repository. From a linked worktree that is GIT_DIR, naming the
worktree's admin directory under the main repository's ``.git/worktrees/``. A
guard suite or a shipped gate that runs git under that environment, for what it
believes is a scratch repository, acts on the developer's repository instead. It
commits onto the pushed branch, deletes tracked files, and writes into the shared
``.git/config``. ``git init`` writes ``core.bare = true`` there.

The cases that pin this down need a repository to aim the variables at that is
not the real one. This module builds it: a main worktree with one commit, and a
linked worktree beside it, because the linked-worktree GIT_DIR is the form a hook
from a worktree actually inherits, and it is the form that flips ``core.bare``.
``snapshot`` records every byte under both, so a case can assert that a run left
the stand-in exactly as it found it.

Building and reading the stand-in goes through ``guard_harness``, whose git
commands scrub the inherited variables. So the stand-in is safe to build even when
the suite itself runs under a hook.
"""

from __future__ import annotations

import configparser
import hashlib
from dataclasses import dataclass
from pathlib import Path
from typing import NamedTuple

from .guard_harness import commit_all, init_scratch_repo, run_git

# The two shapes a hook environment takes, by what each one names.
#
# ``linked-worktree-admin-dir`` is what a hook run from a linked worktree inherits:
# GIT_DIR alone, pointing at .git/worktrees/<name>. ``git init <dir>`` under it
# re-initializes the main repository and sets core.bare.
#
# ``dir-and-work-tree`` names a repository and its work tree outright, the two
# variables a caller sets when it means to redirect git on purpose.
HOOK_SHAPES = ("linked-worktree-admin-dir", "dir-and-work-tree")


@dataclass(frozen=True)
class StandIn:
    """The stand-in repository: its main worktree and one linked worktree."""

    root: Path
    main: Path
    linked: Path

    def hook_env(self, shape: str) -> dict[str, str]:
        """The variables a hook of ``shape`` exports, all aimed at the stand-in."""
        if shape == "linked-worktree-admin-dir":
            return {"GIT_DIR": str(self.main / ".git" / "worktrees" / self.linked.name)}
        if shape == "dir-and-work-tree":
            return {"GIT_DIR": str(self.main / ".git"), "GIT_WORK_TREE": str(self.main)}
        raise ValueError(f"unknown hook shape {shape!r}; expected one of {HOOK_SHAPES}")


def build_stand_in(root: Path) -> StandIn:
    """A committed main worktree under ``root``, and a linked worktree beside it."""
    main = root / "main"
    init_scratch_repo(main, branch="main")
    (main / "tracked.txt").write_text("a tracked file the run must not delete\n")
    commit_all(main, "stand-in baseline")
    linked = root / "linked"
    run_git(main, "worktree", "add", "-q", "-b", "linked", str(linked))
    return StandIn(root=root, main=main, linked=linked)


class Snapshot(NamedTuple):
    """What ``assert_untouched`` compares against."""

    files: dict[str, str]
    bare: str | None


def snapshot(stand_in: StandIn) -> Snapshot:
    """Every file under the stand-in by content hash, and its ``core.bare``.

    Every byte rather than a few git queries: HEAD, the refs, the index, the
    object store, the shared config and both work trees are all files under
    ``root``, so a leak that wrote any of them shows up as a changed, added or
    removed path. Reading files needs no git, so the environment under test
    cannot redirect the reading.
    """
    files = {
        str(path.relative_to(stand_in.root)): hashlib.sha256(path.read_bytes()).hexdigest()
        for path in sorted(stand_in.root.rglob("*"))
        if path.is_file()
    }
    parser = configparser.ConfigParser(strict=False, interpolation=None)
    parser.read(stand_in.main / ".git" / "config")
    return Snapshot(files=files, bare=parser.get("core", "bare", fallback=None))


def assert_untouched(stand_in: StandIn, before: Snapshot) -> None:
    """Fail, naming what changed, if the stand-in differs from ``before``.

    ``core.bare`` is asserted on its own and first. It is the change that breaks a
    developer's checkout outright, so it is the one a reader most needs named.
    """
    after = snapshot(stand_in)
    assert after.bare == before.bare, (
        f"core.bare in the stand-in's shared config went from {before.bare!r} to "
        f"{after.bare!r}: a git init reached the repository GIT_DIR names"
    )
    changed = sorted(
        p for p in before.files.keys() & after.files.keys() if before.files[p] != after.files[p]
    )
    added = sorted(after.files.keys() - before.files.keys())
    removed = sorted(before.files.keys() - after.files.keys())
    assert (changed, added, removed) == ([], [], []), (
        "the run wrote into the repository its inherited GIT_DIR names:\n"
        f"  changed: {changed}\n  added ({len(added)}): {added[:20]}\n  removed: {removed}"
    )
