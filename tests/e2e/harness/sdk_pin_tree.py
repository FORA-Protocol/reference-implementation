"""Scratch trees for the SDK pin gate's meta-guards, in either pin form.

``scripts/check-sdk-pin-consistency.sh`` checks the protocol pins in one of two
forms, and go.mod decides which. The release form names one release in all four
manifests. The pre-release form pins Go, the e2e harness and both edge workers to
one commit of the protocol's integration branch -- a pseudo-version in go.mod, a
``[tool.uv.sources]`` table at that commit in the harness pyproject, and
``github:FORA-Protocol/protocol#<commit>`` in both edge manifests.

Two test modules drive the gate, one per form, and both start from a copy of the
real manifests rather than invented ones. A synthetic ``uv.lock`` would be a guess
about a format uv owns, and a guess that drifts is how a meta-guard starts passing
against inputs the real gate never sees.

The real tree is in exactly one form at a time, so one of the two trees has to be
derived from it, and the derivation runs in one direction only:

    real tree in the pre-release form -> both trees available
        the pre-release tree is the copy; the release tree is derived from it by
        ``_derive_release_form``, which performs the conversion a release re-pin
        performs.
    real tree in the release form     -> the release tree only
        a pre-release tree needs a real commit of the integration branch, with
        its uv.lock and both package-locks resolved from it, and none of that can
        be derived from a release. Go refuses a pseudo-version for the tagged
        commit itself ("is already canonical"), so the release commit cannot
        stand in. The pre-release cases skip, and the skip reason says so.

No stack, no Docker, no database -- but not "no environment" either. A ``go``
toolchain on PATH, a reachable module proxy or a warm module cache, the npm
registry once per session when the release tree is derived (see
``_release_install``), and an installed SDK under ``node_modules`` in both edge
directories. The last has a skip; the others are what the "a check that cannot
run" cases are about, so they fail rather than skip.
"""

from __future__ import annotations

import atexit
import functools
import json
import re
import shutil
import subprocess
import tempfile
import tomllib
from pathlib import Path

import pytest

from .conftest import REPO_ROOT
from .guard_harness import GIT_TIMEOUT, fixture_env, guard_marks, install_gate, run_gate

GATE_NAME = "check-sdk-pin-consistency.sh"
GATE = REPO_ROOT / "scripts" / GATE_NAME

# The protocol module path, as go.mod and the lockfiles spell it. Written once
# because many cases need it and another spelling is how a fixture drifts.
MODULE = "github.com/FORA-Protocol/protocol"

# A pre-release pin this tree will never use again: an old commit of the protocol
# repository, which the Go module proxy serves under this pseudo-version. It is
# the go.mod pin "that names another commit" and the commit a forged lock
# resolves. The go.mod case depends on the proxy continuing to serve it; if it
# stops, the case fails on its own assertion about the gate's message, naming the
# real cause.
HISTORICAL_PSEUDO_VERSION = "v1.0.1-0.20260909130004-5cba5fe4d11c"
HISTORICAL_COMMIT = "5cba5fe4d11c719d33cf4eea497c6d37b6279f00"

# Every file the gate reads, relative to the repository root. Copied whole into
# each case's scratch tree so the gate under test is the shipped one reading real
# inputs.
FIXTURE_FILES = (
    "go.mod",
    "go.sum",
    "tests/e2e/pyproject.toml",
    "tests/e2e/uv.lock",
    "src/edge/package.json",
    "src/edge/package-lock.json",
    "tests/e2e/fastly-edge/package.json",
    "tests/e2e/fastly-edge/package-lock.json",
    "testdata/acceptance-vectors.json",
    "testdata/licenseterm-vectors.json",
    "testdata/manifest-version-vectors.json",
    "testdata/request-acceptance-vectors.json",
    "testdata/thumbprint-vectors.json",
    "testdata/pop-sign-vectors.json",
)

# The one corpus copy that wraps the module's array beside a note: its "vectors"
# member is the whole module file named here, which the gate compares as JSON.
WRAPPED_CORPORA = {"testdata/pop-sign-vectors.json": "pop-vectors.json"}

PYPROJECT = "tests/e2e/pyproject.toml"
UV_LOCK = "tests/e2e/uv.lock"

# The two directories whose node_modules the SDK-metadata section reads. It needs
# an INSTALLED copy of the SDK to compare each lockfile's recorded metadata
# against, and building one per case would mean an npm install per case. The gate
# reads exactly two files of an installed tree, so those two are copied into each
# case's scratch tree: a case may then forge the installed copy without touching
# the working tree.
_NODE_MODULES_DIRS = ("src/edge", "tests/e2e/fastly-edge")
INSTALLED_FILES = (
    "node_modules/.package-lock.json",
    "node_modules/@fora-protocol/sdk/package.json",
)

_UNINSTALLED = tuple(
    rel for rel in _NODE_MODULES_DIRS if not (REPO_ROOT / rel / "node_modules").is_dir()
)

# Two preconditions, two skips, shared by both modules.
#
# The first covers the container leg: scripts/ is not COPYed into the runner image
# and not bind-mounted, so there is nothing to drive there.
#
# The second is an installed SDK in both edge directories. The gate's metadata
# section reads node_modules and fails when it is absent, saying to run `npm ci` —
# correctly, because a check that did not run is not a check that succeeded. But
# that failure then reaches every case here: the clean-tree case reports "clean
# tree rejected", naming the wrong cause, and every negative case passes on an exit
# code the gate would have produced whatever the case did to the tree. A fresh
# clone has neither directory, and so does every worktree the concurrent-agent
# rules create. `make test-fast` runs these modules on every commit, so without
# this skip a commit goes red for a reason unrelated to the change under test.
SDK_PIN_MARKS = [
    *guard_marks(skip_when=not GATE.is_file()),
    pytest.mark.skipif(
        bool(_UNINSTALLED),
        reason=(
            "the gate reads an installed SDK and "
            f"{', '.join(_UNINSTALLED)} has no node_modules — run 'npm ci' in each"
        ),
    ),
]


def read(root: Path, rel: str) -> str:
    return (root / rel).read_text(encoding="utf-8")


def write(root: Path, rel: str, text: str) -> None:
    (root / rel).write_text(text, encoding="utf-8")


def gate(root: Path, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    """This gate's parameters. The mechanics are shared, and only these differ.

    ``env`` is passed by one case, which takes the Go toolchain off PATH: that is
    how the "a check that cannot run" group is driven. It never selects which tree
    is read.
    """
    return run_gate(root, GATE_NAME, env=env)


def fail_lines(result: subprocess.CompletedProcess[str]) -> str:
    """Every FAIL line the gate printed, joined — the assertion surface.

    Asserting on the failure TEXT and not only on the exit code is deliberate.
    Most states these cases plant are also caught by some other section for an
    unrelated reason, so an exit-code-only assertion would pass while the section
    under test stayed silent.
    """
    combined = f"{result.stdout}\n{result.stderr}"
    return "\n".join(line for line in combined.splitlines() if line.startswith("FAIL"))


def pass_lines(result: subprocess.CompletedProcess[str]) -> str:
    """Every PASS line, joined: how a case shows the OTHER checks still held."""
    return "\n".join(line for line in result.stdout.splitlines() if line.startswith("PASS"))


# ----- reading the fixture -----


def module_version(gomod: str) -> str:
    """The version go.mod names for the protocol module, with its leading v."""
    for line in gomod.splitlines():
        stripped = line.strip()
        if stripped.startswith(("require ", MODULE)) and f"{MODULE} v" in stripped:
            if "=>" in stripped:
                continue
            return stripped.split(f"{MODULE} ", 1)[1].split()[0]
    raise AssertionError("fixture go.mod does not pin the protocol module")


def replace_module_version(gomod: str, version: str) -> str:
    return gomod.replace(f"{MODULE} {module_version(gomod)}", f"{MODULE} {version}")


def sdk_version(manifest: str) -> str:
    """The exact version an edge package.json pins @fora-protocol/sdk at.

    A range would not match: the gate requires an exact version, and a case that
    accepted one would stop driving the section it is about.
    """
    doc = json.loads(manifest)
    pinned = (doc.get("dependencies") or {}).get("@fora-protocol/sdk")
    if isinstance(pinned, str) and re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", pinned):
        return pinned
    raise AssertionError(
        f"fixture package.json does not pin the SDK at an exact version: {pinned!r}"
    )


def sdk_commit(manifest: str) -> str:
    """The full commit a pre-release edge package.json pins the SDK at."""
    doc = json.loads(manifest)
    pinned = (doc.get("dependencies") or {}).get("@fora-protocol/sdk")
    found = re.fullmatch(r"github:FORA-Protocol/protocol#([0-9a-f]{40})", str(pinned))
    if found:
        return found.group(1)
    raise AssertionError(f"fixture package.json does not pin the SDK at a commit: {pinned!r}")


def pyproject_version(pyproject: str, dep: str) -> str:
    """The version a pyproject pins for a distribution, read out of the fixture."""
    needle = f'"{dep}=='
    for line in pyproject.splitlines():
        stripped = line.strip()
        if stripped.startswith(needle):
            return stripped[len(needle) :].split('"')[0]
    raise AssertionError(f"fixture pyproject.toml does not pin {dep}")


def bump_patch(version: str) -> str:
    """A different version of the same shape, derived from the one in the fixture."""
    head, _, patch = version.rpartition(".")
    return f"{head}.{int(patch) + 1}"


def retable(lock: str, package: str, key: str, value: str | None, replacement: str = "") -> str:
    """Rewrite one key inside one [[package]] table of a uv.lock.

    A table-scoped edit, because these keys are not unique in the file: `version`
    and `source` appear in every package block, and a whole-file replace would
    rewrite the wrong one.
    """
    out, inside, done = [], False, False
    for line in lock.splitlines():
        if line == "[[package]]":
            inside = False
        elif line == f'name = "{package}"':
            inside = True
        elif inside and not done and line.startswith(f"{key} = "):
            out.append(replacement or f'{key} = "{value}"')
            done = True
            continue
        out.append(line)
    assert done, f"fixture uv.lock has no {key} for {package}"
    return "\n".join(out) + "\n"


def uv_sources(pyproject: str) -> dict[str, dict[str, str]]:
    """The pyproject's [tool.uv.sources] table, parsed; empty when it has none."""
    doc = tomllib.loads(pyproject)
    sources = doc.get("tool", {}).get("uv", {}).get("sources", {})
    assert isinstance(sources, dict), f"[tool.uv.sources] is not a table: {sources!r}"
    return sources


def strip_uv_sources(pyproject: str) -> str:
    """The pyproject with its [tool.uv.sources] table removed.

    Removes the bare-header table the real file uses, up to the next table header.
    Any other spelling of the table survives, and the parse at the end says so
    rather than handing a case a tree that is still half in the pre-release form.
    """
    out, skipping = [], False
    for line in pyproject.splitlines():
        if line.strip() == "[tool.uv.sources]":
            skipping = True
            continue
        if skipping and line.startswith("["):
            skipping = False
        if not skipping:
            out.append(line)
    stripped = "\n".join(out) + "\n"
    assert not uv_sources(stripped), "the fixture spells [tool.uv.sources] some other way"
    return stripped


def set_uv_sources(pyproject: str, entries: dict[str, dict[str, str]]) -> str:
    """The pyproject with its [tool.uv.sources] table replaced by ``entries``."""
    rendered = "".join(
        f"{key} = {{ "
        + ", ".join(f"{field} = {json.dumps(value)}" for field, value in entry.items())
        + " }\n"
        for key, entry in entries.items()
    )
    table = f"\n[tool.uv.sources]\n{rendered}" if entries else ""
    return strip_uv_sources(pyproject) + table


# ----- building the trees -----


def real_tree_form() -> str:
    """Which form the real tree is in, read from the structure rather than the gate.

    The table's presence is the signal, not go.mod's version string. Reading the
    gate's pseudo-version pattern here would make the fixture agree with the gate
    by construction, which is the one thing a meta-guard must not do.

    A real tree that is half in each form still fails a clean-tree case, because
    that case runs the gate on a plain copy. go.mod on a pseudo-version without the
    table reads as "release" here, and the release-form clean-tree case then fails
    on the gate's half-converted message. go.mod on a release with the table reads
    as "pre-release", and the pre-release clean-tree case fails the same way.
    """
    return "pre-release" if uv_sources((REPO_ROOT / PYPROJECT).read_text("utf-8")) else "release"


def _copy_real_tree(root: Path) -> None:
    for rel in FIXTURE_FILES:
        source = REPO_ROOT / rel
        if not source.is_file():
            # Every entry is tracked, so an absent one means this list and the tree
            # have drifted apart -- which is the moment a meta-guard has to speak.
            pytest.fail(f"fixture input {rel} is absent from this checkout")
        target = root / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(source, target)

    install_gate(root, GATE_NAME)

    # Unconditionally. The module-level skip has already established that both
    # directories exist, so a conditional copy here could only build a tree the
    # gate cannot check while saying nothing about it.
    for rel in _NODE_MODULES_DIRS:
        install_sdk_files(root / rel, REPO_ROOT / rel)


def install_sdk_files(target: Path, source: Path) -> None:
    """Copy the two files of an installed tree the gate reads from ``source``."""
    for name in INSTALLED_FILES:
        (target / name).parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source / name, target / name)


def _module_dir(version: str) -> Path:
    """The module cache directory of one version of the protocol module.

    Resolved the way the gate resolves it -- `go mod download` in a throwaway
    module -- so this fixture writes nothing to the repository either.
    """
    with tempfile.TemporaryDirectory() as scratch:
        subprocess.run(
            ["go", "mod", "init", "sdkpinfixture"],
            cwd=scratch,
            check=True,
            capture_output=True,
            timeout=GIT_TIMEOUT,
            env=fixture_env(),
        )
        done = subprocess.run(
            ["go", "mod", "download", "-json", f"{MODULE}@{version}"],
            cwd=scratch,
            check=False,
            capture_output=True,
            text=True,
            timeout=GIT_TIMEOUT,
            env=fixture_env(),
        )
    info = json.loads(done.stdout or "{}")
    if not info.get("Dir"):
        pytest.fail(
            f"could not resolve {MODULE}@{version} to derive the release tree: {done.stdout}{done.stderr}"
        )
    return Path(info["Dir"])


@functools.cache
def _release_install(release: str) -> Path:
    """An npm install of the SDK release, made once per session.

    The release tree's TypeScript half needs what a release re-pin leaves: a lock
    entry resolved from the registry and an installed copy built from it. Neither
    can be derived from the git install the pre-release tree carries, because a
    registry entry records the registry's own metadata (its URL, its integrity,
    the abbreviated field set). So the release is installed into a temp directory
    and the derivation copies its lock entry and its two installed files.
    """
    scratch = Path(tempfile.mkdtemp(prefix="sdk-pin-release-"))
    atexit.register(shutil.rmtree, scratch, ignore_errors=True)
    (scratch / "package.json").write_text(
        json.dumps({"name": "sdk-pin-release", "private": True}), encoding="utf-8"
    )
    done = subprocess.run(
        [
            "npm",
            "install",
            "--ignore-scripts",
            "--no-audit",
            "--no-fund",
            "--save-exact",
            f"@fora-protocol/sdk@{release}",
        ],
        cwd=scratch,
        check=False,
        capture_output=True,
        text=True,
        timeout=GIT_TIMEOUT * 3,
    )
    if done.returncode != 0:
        pytest.fail(
            f"could not install @fora-protocol/sdk@{release} to derive the release tree: {done.stderr}"
        )
    return scratch


def _release_lock(lock: str, release: str, entry: dict[str, object]) -> str:
    """A package-lock.json moved from the commit pin to the registry release."""
    doc = json.loads(lock)
    for path, package in (doc.get("packages") or {}).items():
        if path.rsplit("node_modules/", 1)[-1] == "@fora-protocol/sdk":
            doc["packages"][path] = entry
            continue
        for field in ("dependencies", "devDependencies"):
            deps = package.get(field) or {}
            if "@fora-protocol/sdk" in deps:
                deps["@fora-protocol/sdk"] = release
    return json.dumps(doc, indent=2) + "\n"


def _derive_release_form(root: Path) -> None:
    """Turn a copied pre-release tree into the release form a release re-pin leaves.

    The release is the one the harness pyproject's ==version pins name, which the
    pre-release form keeps in place. Five edits, one per file the gate reads
    differently in the two forms:

    - go.mod requires that release instead of the pseudo-version;
    - the pyproject loses its [tool.uv.sources] table;
    - uv.lock resolves both distributions from the registry the lock already uses
      for every other package, and its requires-dist metadata names the ==version
      pin instead of the git URL. Only the lines the gate reads change, and the
      registry source line is copied from another package rather than written
      here, so it is uv's own spelling;
    - both edge manifests pin the release, each package-lock resolves it from the
      registry with the entry a real install of it records, and the installed
      files come from that install (``_release_install``);
    - the corpora under testdata/ are refreshed from that release's module, which
      is what a re-pin does, so a corpus the pre-release commit changed does not
      make the release tree fail for a reason unrelated to any case.
    """
    release = pyproject_version(read(root, PYPROJECT), "fora-protocol-sdk")
    write(root, "go.mod", replace_module_version(read(root, "go.mod"), f"v{release}"))
    write(root, PYPROJECT, strip_uv_sources(read(root, PYPROJECT)))

    lock = read(root, UV_LOCK)
    registry = next(
        (line for line in lock.splitlines() if line.startswith("source = { registry = ")), None
    )
    assert registry, "fixture uv.lock resolves nothing from a registry to copy the spelling of"
    for dep in ("fora-protocol", "fora-protocol-sdk"):
        lock = retable(lock, dep, "source", None, replacement=registry)
    lock = (
        "\n".join(
            re.sub(r'git = "[^"]*"', f'specifier = "=={release}"', line)
            if MODULE.lower() in line.lower()
            else line
            for line in lock.splitlines()
        )
        + "\n"
    )
    assert MODULE.lower() not in lock.lower(), "the derived release lock still names the repository"
    write(root, UV_LOCK, lock)

    installed = _release_install(release)
    entry = json.loads((installed / "package-lock.json").read_text("utf-8"))["packages"][
        "node_modules/@fora-protocol/sdk"
    ]
    for rel in _NODE_MODULES_DIRS:
        manifest = json.loads(read(root, f"{rel}/package.json"))
        manifest["dependencies"]["@fora-protocol/sdk"] = release
        write(root, f"{rel}/package.json", json.dumps(manifest, indent=2) + "\n")
        write(
            root,
            f"{rel}/package-lock.json",
            _release_lock(read(root, f"{rel}/package-lock.json"), release, entry),
        )
        install_sdk_files(root / rel, installed)

    module = _module_dir(f"v{release}")
    for rel in FIXTURE_FILES:
        if rel in WRAPPED_CORPORA:
            wrapped = json.loads(read(root, rel))
            array = module / "sdk/go/helpers/testdata" / WRAPPED_CORPORA[rel]
            wrapped["vectors"] = json.loads(array.read_text("utf-8"))
            write(root, rel, json.dumps(wrapped, indent=2, ensure_ascii=False) + "\n")
        elif rel.startswith("testdata/"):
            shutil.copyfile(module / "sdk/go/helpers" / rel, root / rel)


def build_release_tree(root: Path) -> Path:
    """A scratch tree in the release form: the real tree, or derived from it."""
    root.mkdir()
    _copy_real_tree(root)
    if real_tree_form() == "pre-release":
        _derive_release_form(root)
    return root


_RELEASE_FORM_SKIP = (
    "the real tree is in the release form, and a pre-release tree needs a real "
    "commit of the protocol repository with every lock resolved from it; one "
    "cannot be derived from a release tree"
)


def build_prerelease_tree(root: Path) -> Path:
    """A scratch tree in the pre-release form, which only the real tree can supply."""
    if real_tree_form() != "pre-release":
        pytest.skip(_RELEASE_FORM_SKIP)
    root.mkdir()
    _copy_real_tree(root)
    return root
