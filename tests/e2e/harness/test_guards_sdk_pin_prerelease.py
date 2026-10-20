"""Meta-guard — the SDK pin gate's pre-release form must be able to fail.

Between protocol releases the Go services, the e2e harness and both edge workers
are pinned to one commit of the protocol's integration branch. go.mod requires
that commit's pseudo-version, the harness pyproject installs both Python
distributions from the protocol repository at the same full rev through a
[tool.uv.sources] table, and both edge manifests pin
``github:FORA-Protocol/protocol#<the same commit>``. The gate checks that form
with its own set of properties, numbered P1 to P7 in the gate's header, and this
module breaks each one and watches the gate refuse it for the right reason.

Each negative case asserts on the gate's own words for the property it breaks.
Where the planted state could also trip some other check, the case also asserts
that the checks it did not touch still printed PASS. That second assertion is
what shows the failure came from the property under test and not from a side
effect of the edit.

The release-form cases live in test_guards_sdk_pin. Both modules build their
trees through ``sdk_pin_tree``, whose docstring covers how the trees are made, why
these cases skip when the real tree is in the release form, and the environment
they need.
"""

from __future__ import annotations

import json
import subprocess
from collections.abc import Callable
from pathlib import Path

import pytest

from .guard_harness import GATE_TIMEOUT, fixture_env
from .hook_env import assert_untouched, build_stand_in, snapshot
from .sdk_pin_tree import (
    GATE_NAME,
    HISTORICAL_COMMIT,
    HISTORICAL_PSEUDO_VERSION,
    MODULE,
    PYPROJECT,
    SDK_PIN_MARKS,
    UV_LOCK,
    build_prerelease_tree,
    bump_patch,
    fail_lines,
    gate,
    pass_lines,
    pyproject_version,
    read,
    replace_module_version,
    retable,
    sdk_commit,
    set_uv_sources,
    uv_sources,
    write,
)

pytestmark = SDK_PIN_MARKS

# The [tool.uv.sources] keys of the two distributions, as the real pyproject
# spells them.
_PROTO = "fora-protocol"
_SDK = "fora-protocol-sdk"

# The two edge directories, and the key the SDK entry has in each lock.
_EDGE = "src/edge"
_FASTLY = "tests/e2e/fastly-edge"
_SDK_ENTRY = "node_modules/@fora-protocol/sdk"

# The PASS lines of the properties, by the words the gate prints. A case that
# breaks one property asserts the others still printed these.
_P2_PASS = "both [tool.uv.sources] entries install from"
_P1_PASS = "pre-release pins agree"
_P3_PASS = "both distributions resolve from"
_P4_PASS = "fora-protocol == fora-protocol-sdk"
_P5_PASS = "all three SDKs build from one commit"
_P6_LOCK_PASS = "package-lock.json resolves @fora-protocol/sdk from"
_P6_ENTRY_PASS = "package-lock.json SDK entry is from"
_INTEGRITY_PASS = "both package-locks record one integrity hash"


@pytest.fixture()
def tree(tmp_path: Path) -> Path:
    """A scratch copy of every file the gate reads, in the pre-release form."""
    return build_prerelease_tree(tmp_path / "repo")


def _pinned_rev(root: Path) -> str:
    """The full rev both [tool.uv.sources] entries name in the fixture."""
    revs = {entry["rev"] for entry in uv_sources(read(root, PYPROJECT)).values()}
    assert len(revs) == 1, f"fixture pyproject names {sorted(revs)}"
    return revs.pop()


def _edit_sources(root: Path, key: str, **fields: str | None) -> None:
    """Change fields of one [tool.uv.sources] entry. ``None`` removes the field."""
    entries = uv_sources(read(root, PYPROJECT))
    for field, value in fields.items():
        if value is None:
            entries[key].pop(field, None)
        else:
            entries[key][field] = value
    write(root, PYPROJECT, set_uv_sources(read(root, PYPROJECT), entries))


def _edit_json(root: Path, rel: str, edit: Callable[[dict], None]) -> None:
    doc = json.loads(read(root, rel))
    edit(doc)
    write(root, rel, json.dumps(doc, indent=2) + "\n")


def _repin_edge(root: Path, rel: str, commit: str) -> None:
    """Move one edge manifest AND its lock to another commit, consistently."""
    pinned = sdk_commit(read(root, f"{rel}/package.json"))
    for name in ("package.json", "package-lock.json"):
        write(root, f"{rel}/{name}", read(root, f"{rel}/{name}").replace(pinned, commit))


def test_the_prerelease_tree_passes(tree: Path) -> None:
    """The gate does not cry wolf in the pre-release form.

    Without this every case below is satisfied by a gate that always fails. The
    FORM line and the PASS lines show the pre-release checks are the ones that
    ran, the TypeScript ones included.
    """
    result = gate(tree)
    assert result.returncode == 0, f"clean tree rejected:\n{result.stdout}\n{result.stderr}"
    assert "all pins agree" in result.stdout
    assert "FORM  pre-release:" in result.stdout, result.stdout
    passed = pass_lines(result)
    for line in (_P2_PASS, _P1_PASS, _P3_PASS, _P4_PASS, _P5_PASS, _INTEGRITY_PASS):
        assert line in passed, passed
    for rel in (_EDGE, _FASTLY):
        assert f"{rel}: {_P6_LOCK_PASS}" in passed, passed
        assert f"{rel}: {_P6_ENTRY_PASS}" in passed, passed


def test_a_hook_run_leaves_the_repository_its_git_dir_names_untouched(
    tree: Path, tmp_path: Path
) -> None:
    """The gate keeps out of the repository a git hook's GIT_DIR names.

    The pre-push hook runs ``make quality-ci``, which runs this gate, and from a
    linked worktree the hook inherits a GIT_DIR naming the worktree's admin
    directory. A git command the gate started for a temporary repository would
    act on the developer's repository instead: ``git init`` writes
    ``core.bare = true`` into its shared config, and a fetch writes objects and
    refs into it. The gate runs no git command itself, but it starts
    ``go mod download`` in a throwaway module, and the Go toolchain runs git for
    a module it fetches directly.

    The gate runs here with that GIT_DIR aimed at a stand-in repository. The
    pass shows every pre-release check ran, and the stand-in's config, objects
    and refs, every byte under it, must be unchanged.
    """
    stand_in = build_stand_in(tmp_path / "stand-in")
    before = snapshot(stand_in)

    # Not gate(): run_gate scrubs exactly the variable this case has to keep.
    result = subprocess.run(
        ["bash", str(tree / "scripts" / GATE_NAME), "--root", str(tree)],
        capture_output=True,
        text=True,
        check=False,
        timeout=GATE_TIMEOUT,
        env={**fixture_env(), **stand_in.hook_env("linked-worktree-admin-dir")},
    )

    assert_untouched(stand_in, before)
    assert result.returncode == 0, f"clean tree rejected:\n{result.stdout}\n{result.stderr}"
    assert _P5_PASS in pass_lines(result), pass_lines(result)


def test_a_go_commit_other_than_the_pinned_commit_is_rejected(tree: Path) -> None:
    """P1 and P5: go.mod and the other two SDKs must build from one commit.

    Only go.mod moves. The Python entries, the lock and both edge manifests still
    agree with each other, so P2, P3 and P4 still pass, and the failures are the
    comparisons with go.mod's prefix, one per language.
    """
    write(tree, "go.mod", replace_module_version(read(tree, "go.mod"), HISTORICAL_PSEUDO_VERSION))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    prefix = HISTORICAL_COMMIT[:12]
    assert f"pre-release pin split-brain: go.mod requires commit {prefix} but [tool.uv" in failures
    assert (
        f"go.mod requires commit {prefix} but both TypeScript manifests pin {_pinned_rev(tree)}"
        in failures
    ), failures
    passed = pass_lines(result)
    for line in (_P2_PASS, _P3_PASS, _P4_PASS):
        assert line in passed, passed


# One edit per way the two [tool.uv.sources] entries can stop naming one commit
# of the protocol repository: the entry it edits, the fields it sets (from the
# pinned rev; ``None`` removes a field), and the words the gate uses for it.
_Fields = Callable[[str], dict[str, str | None]]
_SOURCE_VIOLATIONS: dict[str, tuple[str, _Fields, str]] = {
    "the entries name different revs": (
        _SDK,
        lambda rev: {"rev": "0" * 40},
        "the entries name different revs",
    ),
    "a foreign repository": (
        _SDK,
        lambda rev: {"git": "https://github.com/example/protocol"},
        f"{_SDK} installs from https://github.com/example/protocol, not https://{MODULE}",
    ),
    "a swapped subdirectory": (
        _SDK,
        lambda rev: {"subdirectory": "gen/python"},
        f"{_SDK} builds from subdirectory gen/python, not sdk/python",
    ),
    "a branch in place of the rev": (
        _PROTO,
        lambda rev: {"rev": None, "branch": "main"},
        "is not exactly { git, rev, subdirectory }",
    ),
    "an abbreviated rev": (
        _PROTO,
        lambda rev: {"rev": rev[:12]},
        "which is not a full 40-hex commit",
    ),
}


@pytest.mark.parametrize("violation", sorted(_SOURCE_VIOLATIONS))
def test_sources_that_do_not_name_one_commit_are_rejected(tree: Path, violation: str) -> None:
    """P2: both entries name the protocol repository at one full commit.

    A branch moves without anyone re-pinning, and an abbreviated rev is not the
    40-hex commit the lock records. A foreign repository or a swapped subdirectory
    builds something other than the two distributions the lock names.
    """
    key, fields, words = _SOURCE_VIOLATIONS[violation]
    _edit_sources(tree, key, **fields(_pinned_rev(tree)))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "do not name one commit of the protocol repository" in failures, failures
    assert words in failures, failures


def _lock_at_another_commit(root: Path) -> None:
    write(root, UV_LOCK, read(root, UV_LOCK).replace(_pinned_rev(root), HISTORICAL_COMMIT))


def _lock_from_pypi(root: Path) -> None:
    lock = read(root, UV_LOCK)
    registry = next(line for line in lock.splitlines() if line.startswith("source = { registry = "))
    write(root, UV_LOCK, retable(lock, _SDK, "source", None, replacement=registry))


def _lock_with_a_stray_package(root: Path) -> None:
    rev = _pinned_rev(root)
    stray = (
        "\n[[package]]\n"
        'name = "fora-protocol-extras"\n'
        'version = "0.0.1"\n'
        f'source = {{ git = "https://{MODULE}?subdirectory=gen%2Fpython&rev={rev}#{rev}" }}\n'
    )
    write(root, UV_LOCK, read(root, UV_LOCK) + stray)


def _lock_metadata_at_another_commit(root: Path) -> None:
    """Only the requires-dist line moves; both [[package]] sources stay correct."""
    rev = _pinned_rev(root)
    lines = read(root, UV_LOCK).splitlines()
    moved = 0
    for index, line in enumerate(lines):
        if line.lstrip().startswith(f'{{ name = "{_SDK}", git = '):
            lines[index] = line.replace(rev, HISTORICAL_COMMIT)
            moved += 1
    assert moved == 1, "fixture uv.lock has no requires-dist git entry for the SDK"
    write(root, UV_LOCK, "\n".join(lines) + "\n")


_LOCK_VIOLATIONS = {
    "the lock resolves another commit": (_lock_at_another_commit, f"{_SDK} resolves from"),
    "one distribution from PyPI": (_lock_from_pypi, f"{_SDK} resolves from {{'registry'"),
    "a stray package from the repository": (
        _lock_with_a_stray_package,
        "package 'fora-protocol-extras' also resolves from the protocol repository",
    ),
    "requires-dist at another commit": (
        _lock_metadata_at_another_commit,
        "names the protocol repository other than at",
    ),
}


@pytest.mark.parametrize("violation", sorted(_LOCK_VIOLATIONS))
def test_a_lock_that_does_not_resolve_the_pinned_commit_is_rejected(
    tree: Path, violation: str
) -> None:
    """P3: uv installs what the lock resolved, so the lock must name the commit.

    The pyproject is untouched in every variant, so P2 and P1 still pass: a
    pyproject re-pinned without re-locking is exactly this state.
    """
    plant, words = _LOCK_VIOLATIONS[violation]
    plant(tree)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "and from nothing else" in failures, failures
    assert words in failures, failures
    passed = pass_lines(result)
    for line in (_P2_PASS, _P1_PASS):
        assert line in passed, passed


def test_a_python_distribution_version_split_is_rejected(tree: Path) -> None:
    """P4: the two distributions resolve at one version string.

    fora_sdk.acceptance imports wire.models, so a split between the two means the
    harness signs one payload shape while the services verify another. Only the
    lock's version line moves, so the commit checks still pass.
    """
    lock = read(tree, UV_LOCK)
    current = _lock_version(lock, _PROTO)
    forged = bump_patch(current)
    write(tree, UV_LOCK, retable(lock, _PROTO, "version", forged))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"resolves fora-protocol {forged} but fora-protocol-sdk {current}" in failures, failures
    passed = pass_lines(result)
    for line in (_P2_PASS, _P1_PASS, _P3_PASS):
        assert line in passed, passed


def _lock_version(lock: str, package: str) -> str:
    """The version one [[package]] table of the fixture lock records."""
    inside = False
    for line in lock.splitlines():
        if line == "[[package]]":
            inside = False
        elif line == f'name = "{package}"':
            inside = True
        elif inside and line.startswith("version = "):
            return line.split('"')[1]
    raise AssertionError(f"fixture uv.lock records no version for {package}")


def test_typescript_on_another_commit_is_rejected(tree: Path) -> None:
    """P5: the edge builds from the commit Go and the harness build from.

    Both edge manifests and both locks move together to another commit, so the
    two edge pins agree with each other and with their locks, and only the
    comparisons with go.mod and the Python rev can fail.
    """
    pinned = _pinned_rev(tree)
    for rel in (_EDGE, _FASTLY):
        _repin_edge(tree, rel, HISTORICAL_COMMIT)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert (
        f"go.mod requires commit {pinned[:12]} but both TypeScript manifests pin {HISTORICAL_COMMIT}"
        in failures
    ), failures
    assert (
        f"[tool.uv.sources] installs {pinned} but both TypeScript manifests pin {HISTORICAL_COMMIT}"
        in failures
    ), failures
    passed = pass_lines(result)
    for line in (_P2_PASS, _P1_PASS, _P3_PASS, _P4_PASS, f"{_EDGE}: {_P6_LOCK_PASS}"):
        assert line in passed, passed


def test_a_split_between_the_edge_manifests_is_rejected(tree: Path) -> None:
    """P5: both edge manifests pin one commit.

    One manifest and its lock move together, so section 4 still sees them agree
    and the failure is the cross-manifest comparison's.
    """
    _repin_edge(tree, _EDGE, HISTORICAL_COMMIT)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"TypeScript pin split-brain: src/edge pins commit {HISTORICAL_COMMIT}" in failures
    assert f"{_EDGE}: {_P6_LOCK_PASS}" in pass_lines(result)


# Spellings of the TypeScript pin that are not a pin: each one moves, names
# something other than the protocol, or is not what the lock records.
_SPEC_VIOLATIONS: dict[str, Callable[[str], str]] = {
    "a branch": lambda rev: "github:FORA-Protocol/protocol#main",
    "an abbreviated commit": lambda rev: f"github:FORA-Protocol/protocol#{rev[:12]}",
    "another repository": lambda rev: f"github:example/protocol#{rev}",
    "another spelling of the repository": lambda rev: (
        f"git+https://github.com/FORA-Protocol/protocol.git#{rev}"
    ),
}


@pytest.mark.parametrize("violation", sorted(_SPEC_VIOLATIONS))
def test_a_typescript_pin_that_does_not_name_a_full_commit_is_rejected(
    tree: Path, violation: str
) -> None:
    """P5: only `github:FORA-Protocol/protocol#<full 40-hex commit>` is a pin.

    The manifest alone moves. Section 6 then has no commit to hold the lock entry
    to, and says it did not check one rather than passing.
    """
    spec = _SPEC_VIOLATIONS[violation](_pinned_rev(tree))
    _edit_json(
        tree,
        f"{_EDGE}/package.json",
        lambda doc: doc["dependencies"].update({"@fora-protocol/sdk": spec}),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"'{spec}' is not github:FORA-Protocol/protocol#<full 40-hex commit>" in failures
    assert (
        f"{_EDGE}: package.json names no commit, so the SDK entry of the lock was NOT checked"
        in failures
    )


def test_a_lock_resolving_another_commit_is_rejected(tree: Path) -> None:
    """P6: npm installs what the lock resolved, not what the manifest names.

    Only the resolved URL of the lock's SDK entry moves, so the manifest and the
    lock's root spec still name the pinned commit.
    """
    pinned = _pinned_rev(tree)

    def plant(doc: dict) -> None:
        entry = doc["packages"][_SDK_ENTRY]
        entry["resolved"] = entry["resolved"].replace(pinned, HISTORICAL_COMMIT)

    _edit_json(tree, f"{_EDGE}/package-lock.json", plant)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert (
        f"{_EDGE}: package-lock.json resolves @fora-protocol/sdk from "
        f"git+ssh://git@github.com/FORA-Protocol/protocol.git#{HISTORICAL_COMMIT}"
    ) in failures, failures
    assert _P5_PASS in pass_lines(result)


def test_a_lock_not_regenerated_after_the_repin_is_rejected(tree: Path) -> None:
    """P6: the lock's root spec is npm's copy of the manifest's spec.

    A root spec naming another commit means the manifest was re-pinned and the
    lock was not regenerated, even where the entry itself still resolves the
    commit.
    """
    pinned = _pinned_rev(tree)

    def plant(doc: dict) -> None:
        deps = doc["packages"][""]["dependencies"]
        deps["@fora-protocol/sdk"] = deps["@fora-protocol/sdk"].replace(pinned, HISTORICAL_COMMIT)

    _edit_json(tree, f"{_FASTLY}/package-lock.json", plant)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert (
        f"{_FASTLY}: package-lock.json records the root dependency on @fora-protocol/sdk"
        in failures
    )


def test_a_lock_resolving_another_repository_is_rejected(tree: Path) -> None:
    """P6: the entry resolves from the protocol repository and nowhere else.

    The commit is right and the repository is not, which section 4's commit
    comparison and section 6's source check both refuse; this case asserts the
    source check.
    """
    pinned = _pinned_rev(tree)
    forged = f"git+ssh://git@github.com/example/protocol.git#{pinned}"
    _edit_json(
        tree,
        f"{_EDGE}/package-lock.json",
        lambda doc: doc["packages"][_SDK_ENTRY].update({"resolved": forged}),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"{_EDGE}: the lock resolves the SDK from {forged}, not" in failures, failures
    assert "a source other than the pinned one" in failures, failures


def test_an_installed_copy_from_another_commit_is_not_evidence(tree: Path) -> None:
    """P6: the installed copy is matched to the lock by commit, not by version.

    Every commit between two releases carries the last release's version string,
    so a tree installed from another commit reads the same version as the lock.
    The resolved URL is what tells them apart.
    """
    pinned = _pinned_rev(tree)

    def plant(doc: dict) -> None:
        entry = doc["packages"][_SDK_ENTRY]
        entry["resolved"] = entry["resolved"].replace(pinned, HISTORICAL_COMMIT)

    _edit_json(tree, f"{_EDGE}/node_modules/.package-lock.json", plant)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert (
        f"{_EDGE}: node_modules holds git+ssh://git@github.com/FORA-Protocol/protocol.git#{HISTORICAL_COMMIT}"
        in failures
    )
    assert "the installed copy cannot stand for the pinned release" in failures, failures


def test_a_lock_entry_that_disagrees_with_the_installed_copy_is_rejected(tree: Path) -> None:
    """P6: a hand-edited entry disagrees with the package npm actually installed.

    The dependency list is the field with teeth: `npm ci` builds the tree from it.
    """
    _edit_json(
        tree,
        f"{_EDGE}/package-lock.json",
        lambda doc: doc["packages"][_SDK_ENTRY]["dependencies"].pop("ajv"),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"{_EDGE}: package-lock.json disagrees with the installed" in failures, failures
    assert "the entry was edited rather than regenerated" in failures, failures


def test_locks_recording_two_integrities_are_rejected(tree: Path) -> None:
    """P6 and section 8: one commit is one tarball, so the locks agree on its hash.

    The forged value is still a well-formed sha512 SRI, so section 6 passes it and
    the failure is the comparison between the two locks.
    """
    forged = "sha512-" + "A" * 86 + "=="
    _edit_json(
        tree,
        f"{_FASTLY}/package-lock.json",
        lambda doc: doc["packages"][_SDK_ENTRY].update({"integrity": forged}),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "package-locks disagree on the SDK tarball: src/edge records" in failures, failures
    assert f"while fastly-edge records {forged}" in failures, failures


@pytest.mark.parametrize("removed", [(_PROTO, _SDK), (_SDK,), (_PROTO,)])
def test_a_pseudo_version_without_the_sources_table_is_half_converted(
    tree: Path, removed: tuple[str, ...]
) -> None:
    """P7: go.mod on a commit while the harness installs the last release.

    Removing either entry is as complete a half-conversion as removing both: the
    distribution without an entry installs from PyPI.
    """
    entries = {k: v for k, v in uv_sources(read(tree, PYPROJECT)).items() if k not in removed}
    write(tree, PYPROJECT, set_uv_sources(read(tree, PYPROJECT), entries))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "half-converted pin: go.mod requires the pseudo-version" in failures, failures
    assert f"no [tool.uv.sources] entry for {', '.join(removed)} —" in failures, failures


def test_a_pseudo_version_with_an_edge_on_the_release_is_half_converted(tree: Path) -> None:
    """P7 from the TypeScript side: go.mod on a commit, an edge manifest on npm.

    The edge would run the last release while Go and the harness run the commit.
    """
    release = pyproject_version(read(tree, PYPROJECT), _SDK)
    _edit_json(
        tree,
        f"{_FASTLY}/package.json",
        lambda doc: doc["dependencies"].update({"@fora-protocol/sdk": release}),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "half-converted pin: go.mod requires the pseudo-version" in failures, failures
    assert (
        f"but {_FASTLY}/package.json pins @fora-protocol/sdk {release} from the npm registry"
        in failures
    )


def test_a_release_go_mod_with_the_sources_table_is_half_converted(tree: Path) -> None:
    """P7 from the other side: go.mod back on the release, the table left behind.

    The gate then checks the release form, whose override check refuses the table
    and names the half-converted state, and whose manifest check does the same for
    each edge manifest still on the commit.
    """
    release = pyproject_version(read(tree, PYPROJECT), _SDK)
    write(tree, "go.mod", replace_module_version(read(tree, "go.mod"), f"v{release}"))

    result = gate(tree)
    assert result.returncode == 1
    assert "FORM  release:" in result.stdout, result.stdout
    failures = fail_lines(result)
    assert "[tool.uv.sources] overrides" in failures, failures
    assert f"while go.mod requires the release v{release} — a half-converted pin" in failures
    assert (
        "half-converted pin: src/edge/package.json pins @fora-protocol/sdk to the protocol commit"
        in failures
    )


def test_a_replace_directive_is_rejected_in_the_prerelease_form(tree: Path) -> None:
    """The redirect refusal runs in both forms.

    A replace redirects the Go build away from the pinned commit while every
    comparison against that commit still agrees.
    """
    write(tree, "go.mod", read(tree, "go.mod") + f"\nreplace {MODULE} => ./local-copy\n")

    result = gate(tree)
    assert result.returncode == 1
    assert f"directive names {MODULE}" in fail_lines(result)


def test_a_stale_corpus_is_rejected_in_the_prerelease_form(tree: Path) -> None:
    """The corpus comparison runs in both forms, against the pinned commit's module."""
    corpus = "testdata/licenseterm-vectors.json"
    write(tree, corpus, read(tree, corpus).replace("[", "[ ", 1))

    result = gate(tree)
    assert result.returncode == 1
    assert f"{corpus} differs from the pinned module's copy" in fail_lines(result)
