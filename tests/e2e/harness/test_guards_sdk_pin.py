"""Meta-guard — the SDK pin gate must be able to fail.

``scripts/check-sdk-pin-consistency.sh`` is the only thing holding four dependency
manifests and three lockfiles on one revision of the protocol repository. It is
wired into ``make quality``, so a green quality run is read as evidence that the
Go services, the e2e harness and both edge workers are built against the same SDK.

That evidence is worth exactly as much as the gate's ability to say no, and for a
while it was worth less than it looked. When the Go and Python pins moved from git
revisions to the v1.0.3 release, the section that reads ``uv.lock`` was rewritten
from a whole-file sweep for protocol git revisions into a read of one
``[[package]]`` table. The sweep's property went with it and nothing replaced it,
so three separate states put the harness back on a git checkout — or on another
package index entirely — while the gate printed "all pins agree". A fourth let a
``replace`` directive in ``go.mod`` redirect the Go build with every comparison in
the gate still reporting agreement.

None of the four was found by reading the script. All four were found by breaking
something and watching what the gate said, which is what this module now does on
every commit.

The cases fall into four groups:

    clean tree                  -> PASS   (it does not cry wolf)
    a pin undone                -> FAIL   (the four states above, one case each)
    a pin spelled differently   -> PASS   (it reads both forms Go writes)
    a check that cannot run     -> FAIL   (and names which, and why)

The last group is the one that decays quietly. A gate that cannot reach the Go
toolchain has not checked anything, and the rule this repository holds — a check
that did not run is not a check that succeeded — is only observable by taking the
toolchain away and looking at the exit code.

Every case here runs against the RELEASE form of the pins. The gate also checks
a pre-release form, and test_guards_sdk_pin_prerelease drives that one. Both
modules build their trees through ``sdk_pin_tree``, which copies the real
manifests and, when the real tree is in the pre-release form, derives the release
form from it the way a release re-pin would. That module's docstring says what
the derivation changes and why it runs in one direction only. A case that needs
the pinned version reads it out of the fixture -- see ``module_version`` and
``pyproject_version`` -- so the next re-pin moves the fixture with the tree and
those cases need no editing.

Four cases do write a version literal for the protocol pin, and all four write a
version this tree will never pin again: ``v0.0.1`` and ``v1.0.9`` are forgeries,
``v1.0.2`` is a released tag behind the current one, and
``HISTORICAL_PSEUDO_VERSION`` is a pseudo-version of an old commit.

The environment this module needs -- a Go toolchain, a module proxy or warm
cache, installed edge SDKs -- is listed once, in ``sdk_pin_tree``.
"""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from .sdk_pin_tree import (
    HISTORICAL_COMMIT,
    HISTORICAL_PSEUDO_VERSION,
    MODULE,
    SDK_PIN_MARKS,
    build_release_tree,
    bump_patch,
    fail_lines,
    gate,
    module_version,
    pyproject_version,
    read,
    replace_module_version,
    retable,
    sdk_version,
    write,
)

pytestmark = SDK_PIN_MARKS


@pytest.fixture()
def tree(tmp_path: Path) -> Path:
    """A scratch copy of every file the gate reads, in the release form."""
    return build_release_tree(tmp_path / "repo")


def test_the_release_form_tree_passes(tree: Path) -> None:
    """The gate does not cry wolf.

    Without this every other case here is satisfied by a gate that always fails.
    The FORM line shows the gate checked the release form, so the cases below are
    driving the release checks and not the pre-release ones.
    """
    result = gate(tree)
    assert result.returncode == 0, f"clean tree rejected:\n{result.stdout}\n{result.stderr}"
    assert "all pins agree" in result.stdout
    assert "FORM  release:" in result.stdout, result.stdout


def test_a_uv_sources_override_is_rejected(tree: Path) -> None:
    """Restoring [tool.uv.sources] puts the harness back on git, silently.

    This is the cheapest way to undo the release move and the one the version
    strings cannot see: the ``==`` specifier stays correct, the lock still
    resolves from PyPI until someone re-locks, and uv installs from the override.
    """
    override = (
        "\n[tool.uv.sources]\n"
        f'fora-protocol-sdk = {{ git = "https://{MODULE}.git", subdirectory = "sdk/python" }}\n'
    )
    write(tree, "tests/e2e/pyproject.toml", read(tree, "tests/e2e/pyproject.toml") + override)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "[tool.uv.sources] overrides fora-protocol-sdk" in failures, failures


def test_a_stray_git_reference_in_the_lock_is_rejected(tree: Path) -> None:
    """A second entry reaching the protocol repo over git is not the pinned pair.

    The per-distribution read cannot see this: both named tables still resolve
    from PyPI at the pinned version. Only the whole-file sweep does.
    """
    stray = (
        "\n[[package]]\n"
        'name = "fora-protocol-extras"\n'
        'version = "0.0.1"\n'
        f'source = {{ git = "https://{MODULE}.git?rev=deadbeef#deadbeef" }}\n'
    )
    write(tree, "tests/e2e/uv.lock", read(tree, "tests/e2e/uv.lock") + stray)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    # The sweep's own words. "uv.lock" alone appears in three different messages
    # this section can print, so it does not pin the check under test.
    assert "still names FORA-Protocol/protocol at line(s)" in failures, failures


def test_a_foreign_package_index_is_rejected(tree: Path) -> None:
    """ "Some registry" is not the property the release pin bought.

    An index that happens to serve a package of the same name at the same version
    serves a different artifact, and every version string in the gate agrees.
    """
    lock = read(tree, "tests/e2e/uv.lock")
    forged = "https://internal.example/simple"
    # The index is read out of the fixture, not written down here. The gate names
    # it in a constant of its own, and a second copy that stops matching would make
    # this case quietly stop driving the check -- which it did: the URL was written
    # out by hand and a mismatch called pytest.skip.
    write(
        tree,
        "tests/e2e/uv.lock",
        retable(
            lock,
            "fora-protocol-sdk",
            "source",
            None,
            replacement=f'source = {{ registry = "{forged}" }}',
        ),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"from {forged}, not " in failures, failures


def test_a_replace_directive_is_rejected(tree: Path) -> None:
    """A replace redirects the Go build and nothing else in the gate notices.

    The require line still names the release, and an explicit module@version
    download ignores replace — so the tag still resolves and every comparison in
    the cross-language section still reports agreement.
    """
    write(tree, "go.mod", read(tree, "go.mod") + f"\nreplace {MODULE} => ./local-copy\n")

    result = gate(tree)
    assert result.returncode == 1
    assert "replace" in fail_lines(result)


def test_a_pseudo_version_pin_names_the_real_cause(tree: Path) -> None:
    """A pseudo-version in a release tree is a half-converted pin, and is named so.

    A pseudo-version in go.mod puts the gate in the pre-release form. This tree has
    no [tool.uv.sources] table, so Go would build from a commit while the harness
    installs the last release from PyPI. The message has to name that state and
    the pseudo-version. Reporting it as a mismatch against the other manifests
    names the wrong fix: it would tell a developer to re-pin the manifests that are
    correct. Reporting it as "could not resolve ... run 'go mod download'", as the
    gate once did, names a remedy that cannot work.
    """
    write(tree, "go.mod", replace_module_version(read(tree, "go.mod"), HISTORICAL_PSEUDO_VERSION))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "half-converted pin: go.mod requires the pseudo-version" in failures, failures
    assert HISTORICAL_PSEUDO_VERSION in failures, failures
    assert "split-brain" not in failures, failures


def test_the_single_line_require_form_is_read(tree: Path) -> None:
    """`go mod edit -require` writes a form the gate must still recognise.

    Reading only the indented require-block form reports the pin missing on a tree
    where the pin is correct, and silently skips the cross-language comparison,
    which is gated on having read a version.
    """
    lines = [
        line
        for line in read(tree, "go.mod").splitlines()
        if f"{MODULE} v" not in line or "=>" in line
    ]
    version = module_version(read(tree, "go.mod"))
    lines.append(f"require {MODULE} {version}")
    write(tree, "go.mod", "\n".join(lines) + "\n")

    result = gate(tree)
    failures = fail_lines(result)
    assert "pin not found" not in failures, failures
    assert "cross-language pins agree" in result.stdout, result.stdout


def test_a_split_typescript_version_is_rejected(tree: Path) -> None:
    """One edge manifest left behind is the split-brain this gate exists for.

    The manifest and its lock are forged TOGETHER so the two agree with each other
    and disagree only with the release the other three pins name. Forging the
    manifest alone also breaks the manifest-against-lock section, which reports its
    own failure naming the same directory, so the case would be satisfied by that
    section and pass with the cross-language comparison deleted -- which it did.
    """
    manifest = read(tree, "src/edge/package.json")
    version = sdk_version(manifest)
    forged = bump_patch(version)
    for rel in ("src/edge/package.json", "src/edge/package-lock.json"):
        write(tree, rel, read(tree, rel).replace(f'"{version}"', f'"{forged}"'))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "cross-language pin split-brain" in failures, failures
    assert forged in failures, failures


# The protocol module's first release, which the split case moves go.mod to. It is
# a real release so the Go module cache answers it after the first run. A version
# that was never released is looked up on the module proxy on every run, because
# Go does not cache a miss, and that lookup cost the case about two and a half
# seconds a run.
_FIRST_RELEASE = "v1.0.0"


def test_a_go_and_python_version_split_is_rejected(tree: Path) -> None:
    """The two release pins are one string or one of them is wrong."""
    version = module_version(read(tree, "go.mod"))
    assert version != _FIRST_RELEASE, "the split needs a version other than the pinned one"
    write(tree, "go.mod", replace_module_version(read(tree, "go.mod"), _FIRST_RELEASE))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "release pin split-brain" in failures, failures
    assert version.lstrip("v") in failures


def test_the_gate_does_not_write_to_go_sum(tree: Path) -> None:
    """A read-only guard must not modify a tracked file, least of all when failing.

    The version-to-commit lookup runs `go mod download`, which records what it
    resolved in the calling module's go.sum. On the split-brain path — the one
    this gate exists to reach — the version it resolves is the wrong one, so the
    lines it would append are noise the developer then has to notice and undo.
    """
    write(tree, "go.mod", replace_module_version(read(tree, "go.mod"), "v1.0.2"))
    before = read(tree, "go.sum")

    result = gate(tree)

    assert result.returncode == 1, "this case is only meaningful on the failing path"
    assert read(tree, "go.sum") == before, "the gate appended to go.sum while checking it"


def test_an_unreachable_go_toolchain_fails_and_says_so(tree: Path) -> None:
    """A check that did not run is not a check that succeeded.

    With no `go` on PATH the tag cannot be resolved, so the TypeScript revisions
    are not compared against anything. The gate has to fail, and the message has
    to name the missing binary rather than blame the module cache.
    """
    result = gate(tree, env={"PATH": "/usr/bin:/bin", "HOME": os.environ.get("HOME", "/tmp")})

    assert result.returncode == 1
    failures = fail_lines(result)
    assert "NOT compared" in failures, failures
    assert "PATH" in failures, failures
    # And the directive check, which needs the same binary, must not have reported
    # a clean go.mod. It used to: `go mod edit -json` failing produced empty
    # output, and empty read as "no directive names the module".
    assert "no replace or exclude" not in result.stdout, result.stdout


def test_an_unreadable_go_mod_is_not_a_clean_go_mod(tree: Path) -> None:
    """A directive list that could not be read is not a directive list without one.

    The replace below redirects the Go build. The unknown directive beside it makes
    `go mod edit -json` refuse the whole file, so the replace is never seen -- and
    the check has to say that it could not look, rather than report the file clean.
    """
    write(
        tree,
        "go.mod",
        read(tree, "go.mod") + f"\nreplace {MODULE} => ./local-copy\nnot-a-directive x y\n",
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "directive list was NOT read" in failures, failures
    assert "no replace or exclude" not in result.stdout, result.stdout


def test_an_exclude_directive_is_rejected(tree: Path) -> None:
    """The other half of the directive check, which nothing drove.

    `exclude` is read from a different field of `go mod edit -json` than `replace`
    -- Path rather than Old.Path -- so a case that plants a replace says nothing
    about it. The Exclude loop could be deleted and every other case still passed.
    """
    excluded = module_version(read(tree, "go.mod"))
    write(tree, "go.mod", read(tree, "go.mod") + f"\nexclude {MODULE} {excluded}\n")

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "exclude" in failures, failures
    assert f"directive names {MODULE}" in failures, failures


def test_a_replace_block_version_does_not_answer_as_the_require_pin(tree: Path) -> None:
    """A versioned replace must not be read as the version go.mod requires.

    The indented form is the one that reaches this: inside a `replace ( ... )`
    block the line looks exactly like a require-block line until the `=>` is read.
    With the require line removed, the only version left in the file is the
    replace's, and the gate has to report the pin missing rather than adopt it.
    """
    gomod = read(tree, "go.mod")
    forged = "1.0.9"
    assert module_version(gomod) != f"v{forged}"
    kept = [line for line in gomod.splitlines() if f"{MODULE} v" not in line]
    write(
        tree,
        "go.mod",
        "\n".join(kept) + f"\n\nreplace (\n\t{MODULE} v{forged} => ./local\n)\n",
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "pin not found" in failures, failures
    assert forged not in failures, failures


# Every spelling uv honours for the same override. The gate used to match lines,
# so it caught the one written here first and walked past the other three: a
# dotted table header ends the section a line matcher is anchored on, a quoted key
# and an indented key both fail an anchored name match, and an underscored name is
# the same distribution after PEP 503 normalisation.
_OVERRIDE_SPELLINGS = {
    "bare key": '[tool.uv.sources]\nfora-protocol-sdk = {{ git = "https://{module}" }}\n',
    "dotted table header": '[tool.uv.sources.fora-protocol-sdk]\ngit = "https://{module}"\n',
    "quoted key": '[tool.uv.sources]\n"fora-protocol-sdk" = {{ git = "https://{module}" }}\n',
    "indented key": '[tool.uv.sources]\n  fora-protocol-sdk = {{ git = "https://{module}" }}\n',
    "underscored name": '[tool.uv.sources]\nfora_protocol_sdk = {{ git = "https://{module}" }}\n',
}


@pytest.mark.parametrize("dep", ["fora-protocol-sdk", "fora-protocol"])
def test_an_override_of_either_distribution_is_rejected(tree: Path, dep: str) -> None:
    """Both distributions are read, and each by its own call.

    fora-protocol is the wire models the services verify against; putting only
    that one back on a git checkout is as complete an undo as the other.
    """
    override = f'\n[tool.uv.sources]\n{dep} = {{ git = "https://{MODULE}" }}\n'
    write(tree, "tests/e2e/pyproject.toml", read(tree, "tests/e2e/pyproject.toml") + override)

    result = gate(tree)
    assert result.returncode == 1
    assert f"[tool.uv.sources] overrides {dep}" in fail_lines(result), result.stdout


@pytest.mark.parametrize("spelling", sorted(_OVERRIDE_SPELLINGS))
def test_every_spelling_of_a_uv_sources_override_is_rejected(tree: Path, spelling: str) -> None:
    """uv reads the document, so the gate has to read the document.

    All five put the harness back on a git checkout with `==1.0.3` still reading
    correct everywhere in this gate. Four of them passed.
    """
    block = _OVERRIDE_SPELLINGS[spelling].format(module=MODULE)
    write(tree, "tests/e2e/pyproject.toml", read(tree, "tests/e2e/pyproject.toml") + "\n" + block)

    result = gate(tree)
    assert result.returncode == 1
    assert "[tool.uv.sources] overrides" in fail_lines(result), result.stdout


def test_a_lowercase_repository_owner_in_the_lock_is_rejected(tree: Path) -> None:
    """GitHub resolves owner names without regard to case, so the sweep must too.

    `github.com/fora-protocol/protocol.git` clones the same repository as the
    spelling this tree uses everywhere else, and a case-sensitive sweep called that
    lock clean.
    """
    stray = (
        "\n[[package]]\n"
        'name = "fora-protocol-extras"\n'
        'version = "0.0.1"\n'
        f'source = {{ git = "https://{MODULE.lower()}.git?rev=deadbeef#deadbeef" }}\n'
    )
    write(tree, "tests/e2e/uv.lock", read(tree, "tests/e2e/uv.lock") + stray)

    result = gate(tree)
    assert result.returncode == 1
    assert "still names FORA-Protocol/protocol at line(s)" in fail_lines(result)


def test_a_python_distribution_split_is_rejected(tree: Path) -> None:
    """The two distributions are one version or the harness signs the wrong shape.

    fora_sdk.acceptance imports wire.models, so a split between fora-protocol-sdk
    and fora-protocol means the harness signs one payload shape while the services
    verify another.
    """
    pyproject = read(tree, "tests/e2e/pyproject.toml")
    version = pyproject_version(pyproject, "fora-protocol")
    forged = bump_patch(version)
    write(
        tree,
        "tests/e2e/pyproject.toml",
        pyproject.replace(f'"fora-protocol=={version}"', f'"fora-protocol=={forged}"'),
    )

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"fora-protocol {forged} != fora-protocol-sdk {version}" in failures, failures


def test_a_lock_resolving_another_version_is_rejected(tree: Path) -> None:
    """A pyproject bumped without re-locking installs the version the lock names.

    uv installs what the lock resolved. The `==` specifier reading the new version
    is what makes this invisible to every other string in the gate.
    """
    lock = read(tree, "tests/e2e/uv.lock")
    version = pyproject_version(read(tree, "tests/e2e/pyproject.toml"), "fora-protocol-sdk")
    forged = bump_patch(version)
    write(tree, "tests/e2e/uv.lock", retable(lock, "fora-protocol-sdk", "version", forged))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"resolves fora-protocol-sdk {forged} but" in failures, failures


def test_a_named_distribution_back_on_a_git_source_is_rejected(tree: Path) -> None:
    """The named table is the entry the release move was about.

    The stray-entry case does not reach this: it appends a package of another name,
    so both named tables still resolve from PyPI and only the whole-file sweep
    fires. This puts the pinned distribution itself back on git.
    """
    lock = read(tree, "tests/e2e/uv.lock")
    forged = retable(
        lock,
        "fora-protocol-sdk",
        "source",
        None,
        replacement=f'source = {{ git = "https://{MODULE}.git?rev=deadbeef#deadbeef" }}',
    )
    write(tree, "tests/e2e/uv.lock", forged)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "is not resolved from a registry" in failures, failures


def test_a_foreign_npm_registry_is_rejected(tree: Path) -> None:
    """The npm half of the property invariant 3 holds for PyPI.

    A registry that happens to serve a package of the same name and version serves
    different bytes, and every version string in this gate still agrees. This is a
    branch the move off the git pin created: a git URL had no registry to name.
    """
    lock = read(tree, "src/edge/package-lock.json")
    forged = lock.replace("https://registry.npmjs.org/", "https://registry.internal.example/")
    assert forged != lock, "fixture lock does not name the npm registry"
    write(tree, "src/edge/package-lock.json", forged)

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "registry.internal.example" in failures, failures


def test_an_integrity_that_is_not_a_sha512_sri_is_rejected(tree: Path) -> None:
    """The integrity is what npm checks the downloaded bytes against.

    An entry carrying something else in that field has nothing holding it to one
    artifact. npm verifies the SRI on every install -- flipping one character of it
    fails `npm ci` with EINTEGRITY -- so an entry that never had a well-formed one
    is the case this catches before `npm ci` ever runs.
    """
    doc = json.loads(read(tree, "src/edge/package-lock.json"))
    doc["packages"]["node_modules/@fora-protocol/sdk"]["integrity"] = "sha1-deadbeef"
    write(tree, "src/edge/package-lock.json", json.dumps(doc, indent=2) + "\n")

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "not a sha512 SRI" in failures, failures


def test_a_commit_pin_in_a_release_tree_is_half_converted(tree: Path) -> None:
    """P7 from the TypeScript side: go.mod on a release, an edge manifest on a commit.

    The manifest alone moves, to the commit spelling the pre-release form uses, so
    the edge would build from a commit while Go and the harness run the release.
    The message names that state rather than reporting the pin as unreadable.
    """
    manifest = read(tree, "src/edge/package.json")
    version = sdk_version(manifest)
    spec = f"github:FORA-Protocol/protocol#{HISTORICAL_COMMIT}"
    write(tree, "src/edge/package.json", manifest.replace(f'"{version}"', f'"{spec}"'))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert (
        f"half-converted pin: src/edge/package.json pins @fora-protocol/sdk to the protocol commit {HISTORICAL_COMMIT}"
        in failures
    ), failures


def test_a_version_range_is_not_a_pin(tree: Path) -> None:
    """`^1.0.4` lets npm install a release nothing else here names.

    The caret resolves to whatever the registry serves at install time, so the
    manifest reads correct, the other three pins read correct, and the tree runs a
    release none of them chose. Only an exact version is a pin.
    """
    manifest = read(tree, "src/edge/package.json")
    version = sdk_version(manifest)
    write(tree, "src/edge/package.json", manifest.replace(f'"{version}"', f'"^{version}"'))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert "pin not found" in failures, failures


def test_a_stale_testdata_corpus_is_rejected(tree: Path) -> None:
    """The corpora are oracles, and a re-pin that leaves one behind is silent.

    The replay suites read these files. A corpus that stops matching the pinned
    module makes those suites assert the previous revision's answers and pass,
    which is the one failure a re-pin is most likely to produce.
    """
    corpus = "testdata/licenseterm-vectors.json"
    write(tree, corpus, read(tree, corpus).replace("[", "[ ", 1))

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"{corpus} differs from the pinned module's copy" in failures, failures


def test_a_stale_wrapped_corpus_is_rejected(tree: Path) -> None:
    """The delivery-proof corpus wraps the module's array beside a note.

    It is compared as JSON rather than byte for byte, so the note can say how the
    edge double reads it. A vector left behind at a re-pin is still a stale oracle,
    and fails the gate the same way.
    """
    corpus = "testdata/pop-sign-vectors.json"
    wrapped = json.loads(read(tree, corpus))
    wrapped["vectors"] = wrapped["vectors"][:-1]
    write(tree, corpus, json.dumps(wrapped, indent=2) + "\n")

    result = gate(tree)
    assert result.returncode == 1
    failures = fail_lines(result)
    assert f"{corpus}'s \"vectors\" differ from the pinned module's pop-vectors.json" in failures, (
        failures
    )
