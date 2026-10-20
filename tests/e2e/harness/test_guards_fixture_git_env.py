"""Meta-guard — the guard suites' own git commands stay inside their fixtures.

The guard suites build scratch repositories and drive shipped scripts against
them. The pre-push hook runs ``make quality-ci``, which runs these suites, and
from a linked worktree git hands the hook a GIT_DIR naming the worktree's admin
directory. Every fixture command that inherited it acted on the real repository:
the suites committed fixture trees onto the pushed branch, deleted tracked files,
wrote a fixture identity into ``.git/config``, and set ``core.bare = true`` there.

``guard_harness.fixture_env`` is the fix: every process a guard suite starts that
runs git takes its environment from it, and it removes the variables that select
a repository. This module runs a sample of the suites in a child pytest whose
environment carries a hook's variables, aimed at a stand-in repository, and
asserts the stand-in came out byte for byte as it went in.

The sample is one case per mechanism, not the whole suites, because every case
here is paid for twice per commit, once per hook shape. Each case below reaches
the real repository a different way when the environment is not scrubbed:

- the deployment-docs case builds a fixture with ``init_scratch_repo``, commits,
  pushes between local remotes, and runs the release tool, which commits;
- the release cases drive the release tool, one of them through a
  caller-built environment rather than the default one;
- the image-version case runs a gate that lists the tree with ``git ls-files``;
- the published-secrets case runs the gitleaks gate through ``run_gate``;
- the publish case runs the publish tool, which adds and removes worktrees,
  and builds its stand-in remote with ``git init --bare``.

The last two need gitleaks, and the child run skips them where it is missing.
That leaves fewer mechanisms covered, but no case passes without running.

Pure subprocess work: no stack, no Docker, no network.
"""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

import pytest

from .conftest import REPO_ROOT
from .guard_harness import guard_marks
from .hook_env import HOOK_SHAPES, assert_untouched, build_stand_in, snapshot

_E2E_DIR = Path(__file__).resolve().parent.parent

_SAMPLE = (
    "harness/test_guards_deployment_docs.py"
    "::test_a_document_added_to_the_shared_list_is_rewritten_by_the_release_tool",
    "harness/test_guards_release_version.py::test_the_happy_path_edits_commits_and_pushes",
    "harness/test_guards_release_version.py::test_a_failed_write_leaves_no_temporary_file",
    "harness/test_guards_image_version.py::test_gate_passes_on_a_clean_tree",
    "harness/test_guards_published_secrets.py::test_clean_tree_passes",
    "harness/test_guards_publish_gates.py::test_clean_tree_reaches_the_dry_run_summary",
)

# The child run takes a few seconds. This bounds a hang, not the normal case.
_CHILD_TIMEOUT = 300

pytestmark = guard_marks(skip_when=not (REPO_ROOT / "scripts").is_dir())


@pytest.mark.parametrize("shape", HOOK_SHAPES)
def test_guard_suites_leave_the_repository_a_hook_names_untouched(
    tmp_path: Path, shape: str
) -> None:
    """Run the sample under a hook's environment and find the stand-in unchanged.

    The stand-in is asserted first because it is the subject. The child's exit
    status is the weaker signal. With the scrubbing removed, this sample also
    fails in the child, but a leak does not have to make a case fail: in the push
    that exposed this, the leaked commits succeeded and landed on the branch.
    """
    stand_in = build_stand_in(tmp_path / "stand-in")
    before = snapshot(stand_in)

    # Deliberately not fixture_env: this is the inherited environment under test.
    env = {**os.environ, **stand_in.hook_env(shape)}
    proc = subprocess.run(
        [sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider", *_SAMPLE],
        cwd=str(_E2E_DIR),
        capture_output=True,
        text=True,
        check=False,
        timeout=_CHILD_TIMEOUT,
        env=env,
    )

    assert_untouched(stand_in, before)
    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout[-4000:]}\nSTDERR:\n{proc.stderr[-4000:]}"
