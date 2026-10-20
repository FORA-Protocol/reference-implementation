"""Scaffolding every structural guard suite needs.

Six suites in this directory assert that something shipped in this repository
refuses what it says it refuses. Five of them do it by driving a shipped script
against a synthetic tree; the sixth reads the real workflow files and drives
nothing. What they need overlaps rather than coincides — all six declare the same
marks, four build a scratch git repository, three run a gate script, two plant a
private key — and every piece of it was written once per suite until this module
existed, in copies that had already started to differ. One gate runner grew an
``env`` parameter and a longer timeout; the other two did not.

None of it is publish-specific. ``publish_harness`` sits on top of this module and
keeps only what belongs to driving the publish tool: the miniature published tree,
the stand-in remote, and the invocation. A suite that guards some other script has
this module to import from, and no reason to reach into that one.

The guards that drive no script at all -- the ones that only read source under
``tests/e2e/`` and report a shape -- share ``ast_scan`` instead: the tree walk, the
exclusion set and the dict-key reader. Nothing here is useful to them, so a
source-scan helper belongs there rather than in this module.

A module of its own rather than something in ``conftest``: it takes no fixture
arguments, and several modules import it at import time to build their marks
before any test runs. It also imports nothing from this package, so ``conftest``
imports it at module top like anything else.

That is true of ``publish_harness`` too, and it did not used to be.
``conftest`` owned ``REPO_ROOT``, ``publish_harness`` read it from there, and
``conftest`` could therefore only import ``publish_harness`` from inside a
function body. ``REPO_ROOT`` now lives in ``_compose``; both modules read it
from there and neither imports ``conftest`` at all. Test modules still import
``conftest`` freely — they are leaves, and nothing imports them back.
"""

from __future__ import annotations

import functools
import os
import shutil
import subprocess
from collections.abc import Mapping
from pathlib import Path

import pytest
from cryptography.hazmat.primitives.asymmetric import ed25519
from cryptography.hazmat.primitives.serialization import (
    Encoding,
    NoEncryption,
    PrivateFormat,
)

from ._compose import REPO_ROOT

# Fixture git calls are small and local. A minute is generous for the slowest of
# them and short enough that a hung call fails the suite rather than the job.
GIT_TIMEOUT = 60

# The gates are offline source scans and finish in well under this. The secret
# gate is the exception and raises it at the call site, because gitleaks does
# real work over the whole tree.
GATE_TIMEOUT = 120

# One identity for every scratch repository, and .invalid can never resolve (RFC
# 6761). It reaches git as environment variables on each command, built by
# ``fixture_env``, and never as a ``git config`` write: a config write goes to
# whichever repository git resolves, and under an inherited GIT_DIR that is the
# developer's own repository rather than the fixture.
COMMITTER_NAME = "guard fixture"
COMMITTER_EMAIL = "guard@example.invalid"

# Identity by environment, which git reads ahead of every config file. A shipped
# script that commits inside a fixture gets the same identity this way.
_FIXTURE_IDENTITY = {
    "GIT_AUTHOR_NAME": COMMITTER_NAME,
    "GIT_AUTHOR_EMAIL": COMMITTER_EMAIL,
    "GIT_COMMITTER_NAME": COMMITTER_NAME,
    "GIT_COMMITTER_EMAIL": COMMITTER_EMAIL,
}

# The secret scanner resolves its ruleset from this file in the tree it scans, so
# a fixture that leaves it empty turns the scan into a no-op that reports clean.
SECRET_SCAN_CONFIG = ".gitleaks.toml"

# scripts/ is not copied into the e2e runner image, so on that tier a gate suite
# has nothing to drive. Every suite that runs a script under scripts/ skips for
# this reason, in these words.
GATE_ABSENT_REASON = "scripts/ is absent from the e2e runner image; this guard runs on the host"

# The argument parser four of the gates source. It defines `die` and the --root
# loop, so a gate that finds it missing aborts before it reads anything.
GATE_SEAM = "gate-args.sh"


def install_gate(root: Path, gate: str, *extra: str) -> Path:
    """Copy a gate, the seam it sources, and any other file it reads, into ``root``.

    The seam goes in unconditionally. A scratch tree that carries a gate and not
    the file the gate sources fails every case in the suite with one message about
    a missing file, which reads as a broken suite rather than a missing fixture
    input -- and one gate here does not source it, for which the cost is a spare
    file no case opens. ``deployment_docs`` made this same argument for the
    document list its gate reads; ``extra`` is where that kind of file goes now.
    """
    scripts = root / "scripts"
    scripts.mkdir(parents=True, exist_ok=True)
    for name in (gate, GATE_SEAM, *extra):
        shutil.copy2(REPO_ROOT / "scripts" / name, scripts / name)
    return scripts / gate


def hold_back(definition: Path, *entries: str) -> None:
    """Append ``entries`` to DENY_PATHS in a fixture's copy of the shared definition.

    One line per entry, each carrying the reference gate's waiver on that line.
    The copy is a search root of that gate in every fixture that holds it, and a
    line naming a held-back path is exactly what the gate refuses, so an entry
    without the waiver would fail a case for a reason it is not testing.
    """
    with definition.open("a") as out:
        for entry in entries:
            out.write(
                f"DENY_PATHS+=( {entry} )  # published-ref-allow: a held-back path a case adds\n"
            )


# One instance, shared by every suite that needs it — the secret-gate suite reads
# it directly, and the three publish suites get it through PUBLISH_GUARD_MARKS.
# Applying a MarkDecorator does not mutate it, so one object serves all four.
GITLEAKS_ABSENT = pytest.mark.skipif(
    shutil.which("gitleaks") is None,
    reason="gitleaks is not installed, and the gate under test refuses to run without it",
)


@functools.cache
def repository_selecting_git_vars() -> frozenset[str]:
    """The variables that tell git WHICH repository to act on.

    This is git's own list, from ``git rev-parse --local-env-vars``: the
    variables git itself clears before it crosses into another repository, for a
    submodule for example. It is read from git rather than copied here, so it
    cannot fall behind the installed git.

    A git hook exports some of these. The pre-push hook runs ``make quality-ci``,
    and from a linked worktree git hands that hook a GIT_DIR naming the
    worktree's admin directory. ``-C`` does not override it, because an absolute
    GIT_DIR wins over the directory git starts in. Every fixture command run
    under it acts on the real repository: ``git -C <fixture> init`` re-initializes
    it and writes ``core.bare = true`` into its shared config, ``commit`` lands on
    the pushed branch, ``rm`` deletes tracked files, and ``config`` writes into
    its ``.git/config``.

    The list needs no repository, and git prints it whatever GIT_DIR says.
    """
    done = subprocess.run(
        ["git", "rev-parse", "--local-env-vars"],
        check=True,
        capture_output=True,
        text=True,
        timeout=GIT_TIMEOUT,
    )
    return frozenset(done.stdout.split())


def fixture_env(env: Mapping[str, str] | None = None) -> dict[str, str]:
    """The environment for any process that runs git against a fixture.

    ``env`` is a complete environment, ``os.environ`` when it is omitted. The
    result is a copy with every repository-selecting variable removed and the
    fixture identity set, so git finds the fixture from the directory it runs in
    and from nothing else. This is the one place the scrubbing happens: every
    process a guard suite starts that runs git, directly or through a shipped
    script, takes its environment from here. The exceptions are deliberate: the
    cases built on ``hook_env`` hand a hook's variables to a child on purpose, to
    show the child keeps out of the repository they name.

    A caller that needs a variable of its own builds the full environment and
    passes it in: ``fixture_env({**os.environ, "TMPDIR": scratch})``. Variables
    that only tune git, such as ``GIT_TERMINAL_PROMPT``, pass through.
    """
    source = os.environ if env is None else env
    selecting = repository_selecting_git_vars()
    scrubbed = {name: value for name, value in source.items() if name not in selecting}
    scrubbed.update(_FIXTURE_IDENTITY)
    return scrubbed


def git(*args: str) -> subprocess.CompletedProcess[str]:
    """Run git for a fixture, and hand back what it printed.

    It RETURNS the result rather than discarding it. The callers that need stdout
    used to hand-roll their own ``subprocess.run`` for exactly that reason, and
    so did the two that create a bare repository, which have no repository to
    ``-C`` into and so cannot go through ``run_git``.

    ``text=True`` is for those readers: they want ``.stdout`` as a string. It is
    not a better failure message — ``CalledProcessError`` renders only the
    command and the exit status, never the captured output.

    The environment comes from ``fixture_env``, so an inherited GIT_DIR cannot
    turn a fixture command into a command on the real repository.
    """
    return subprocess.run(
        ["git", *args],
        check=True,
        capture_output=True,
        text=True,
        timeout=GIT_TIMEOUT,
        env=fixture_env(),
    )


def run_git(repo: Path, *args: str) -> subprocess.CompletedProcess[str]:
    """``git`` inside ``repo``."""
    return git("-C", str(repo), *args)


def commit_all(repo: Path, message: str) -> None:
    """Stage everything in ``repo``, including deletions, and commit it."""
    run_git(repo, "add", "-A")
    run_git(repo, "commit", "-qm", message)


def init_scratch_repo(root: Path, *, branch: str) -> None:
    """Create ``root`` and make it an empty repository.

    The branch name is a parameter because every caller depends on its own: the
    publish tool is told which ref to read and which to write, and a fixture
    built on the wrong branch drives nothing.

    No identity is written into the repository. ``fixture_env`` passes it to
    every command, so committing needs no ``git config`` call.
    """
    root.mkdir(parents=True, exist_ok=True)
    run_git(root, "init", "-q", "-b", branch)


def run_gate(
    root: Path,
    gate: str,
    *,
    reads_root: bool = True,
    env: dict[str, str] | None = None,
    timeout: int = GATE_TIMEOUT,
) -> subprocess.CompletedProcess[str]:
    """Run the copy of ``gate`` that ``root`` carries.

    The root is passed as an argument, never through the environment: an ambient
    variable that selects the tree a gate reads can redirect it in production too.

    ``reads_root`` says whether the gate SCANS a tree. Most do, and take --root
    to say which. One does not: check-buildx.sh asks the local docker whether the
    buildx plugin is installed, which is a property of the machine rather than of
    a tree. Handing it a flag it does not parse would be a seam that looks
    meaningful and is not, so it is not handed one. ``root`` still selects WHICH
    COPY of the script runs, which is the runner's other job and the reason a
    gate that reads no tree still belongs here.

    Nothing enforces this for a gate that ignores unknown arguments, and that is
    said rather than left to be assumed: set ``reads_root=True`` for the buildx
    gate and its suite still passes, because a bash script that never reads "$@"
    does not notice. The flag is a statement about the gate's contract, not a
    check on it.

    This parameter exists because the alternative was worse: the buildx suite
    wrote its own subprocess call while importing this module's timeout and its
    marks — reaching in for the constants and re-implementing the function that
    uses them. That is the shape this module's docstring records happening once
    already, when one gate runner grew an ``env`` parameter and a longer timeout
    and the other two did not.

    ``env`` exists only so one suite can put a deliberately broken scanner, or a
    stub docker, on PATH — it selects which binary runs, never which tree is
    read. Leaving it at ``None`` starts from the caller's environment. Either
    way it goes through ``fixture_env``, because the gates run git against the
    tree they are handed and an inherited GIT_DIR would point them elsewhere.
    """
    cmd = ["bash", str(root / "scripts" / gate)]
    if reads_root:
        cmd += ["--root", str(root)]
    return subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        check=False,
        timeout=timeout,
        env=fixture_env(env),
    )


def guard_marks(*, skip_when: bool, reason: str = GATE_ABSENT_REASON) -> list[pytest.MarkDecorator]:
    """The two marks a structural guard suite declares.

    ``stack_isolation("isolated")`` makes the autouse cleanup dispatch a no-op,
    which would otherwise resolve a live Postgres DSN these suites have no use
    for.

    The skip takes the caller's own condition rather than a path this function
    tests for itself. One suite asks whether a file is present and another asks
    about a directory, and a shared ``exists()`` would quietly weaken both.
    """
    return [
        pytest.mark.stack_isolation("isolated"),
        pytest.mark.skipif(skip_when, reason=reason),
    ]


def make_recipe(makefile: str, target: str) -> list[str]:
    """The command lines of ``target`` in ``makefile``: the tab-indented lines after it.

    The lint guards check that ``make`` still runs the linter they test, with the
    arguments they test it with, so they read the recipe rather than trusting it.
    """
    lines = makefile.splitlines()
    start = lines.index(f"{target}:") + 1
    recipe = []
    for line in lines[start:]:
        if not line.startswith("\t"):
            break
        recipe.append(line.strip())
    return recipe


def private_key_pem() -> bytes:
    """A real, freshly generated Ed25519 private key.

    Generated rather than pasted as a constant: a committed literal key is the
    thing the secret gate exists to keep out of the tree, and a guard's own
    fixture is not exempt from that.
    """
    return ed25519.Ed25519PrivateKey.generate().private_bytes(
        Encoding.PEM, PrivateFormat.PKCS8, NoEncryption()
    )
