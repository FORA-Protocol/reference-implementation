"""The deployment-document fixture, shared by the two suites built on it.

Each deployment document declares the published image version once, and every
docker command in it reuses that declaration. Two suites need a miniature of
that arrangement: the one guarding the agreement gate, and the one guarding the
tool that performs the release edit. They need the same paths, the same scan
roots, the same document body and the same tree builder.

They were one suite's private helpers until the second suite needed them.
Copying would have been the shape the testing doctrine forbids.

The path and scan-root literals stay literals. Deriving them from the tree would
make the fixture agree with a bug in the gate's own list, which is the reason the
first suite spelled them out.

A module of its own rather than something in ``guard_harness``: that module holds
what EVERY structural guard suite needs, and a deployment document is not that.
The split is the same one ``publish_harness`` makes.
"""

from __future__ import annotations

from pathlib import Path

from .conftest import REPO_ROOT
from .guard_harness import commit_all, init_scratch_repo, install_gate

# REPO_ROOT rather than a parents[N] walk: inside the runner container the
# harness sits at /runner/harness, which has fewer parents than the host layout,
# and computing the walk here raises at import — killing collection for the whole
# tier rather than this one module.
GATE = REPO_ROOT / "scripts" / "check-image-version.sh"

# The shared definition the gate sources for its document list. The gate refuses
# to run without it, so a fixture that carries one and not the other tests
# nothing.
DOCS_DEFINITION = REPO_ROOT / "scripts" / "deployment-docs.sh"

# The documents the shipped definition lists.
#
# This literal is DELIBERATELY not read from DOCS_DEFINITION, and must stay that
# way. A fixture that derives its expectations from the file under test agrees
# with a bug in that file and reports success. Two suites assert against this
# tuple, and both would go blind the day it started sourcing the real thing.
#
# The cost is the one it looks like: adding a service means editing this tuple
# by hand as well as the shared definition, and the suites go red until it is
# done. That failure IS the check — it is what catches a service added to one
# list and not the other.
DOCS = (
    "src/exchange/DEPLOYMENT.md",
    "src/broker/DEPLOYMENT.md",
    "src/identity/DEPLOYMENT.md",
    "src/console/DEPLOYMENT.md",
)

# Every directory the gate refuses to run without. It dies on a missing scan
# root rather than reporting a clean tree it never read.
SCAN_ROOTS = ("src", "docs", "deploy", ".github")

# What the fixture documents declare before a test changes anything.
DECLARED_VERSION = "1.0.0-rc.1"


def doc_body(service: str, version: str = DECLARED_VERSION, declarations: int = 1) -> str:
    """A miniature of one deployment document's section 3.

    Only the shape the gate reads: the declaration, and one command that uses it
    rather than a literal.
    """
    lines = ["# " + service.title(), "", "## 3. Build or pull the image", "", "```bash"]
    lines += [f"VERSION={version}"] * declarations
    lines += [
        f"docker pull ghcr.io/fora-protocol/{service}:$VERSION",
        f"docker build -t ghcr.io/fora-protocol/{service}:dev .",
        f"docker pull ghcr.io/fora-protocol/{service}@sha256:<the digest that printed>",
        # The real documents carry an untagged reference in the sentence saying
        # a pull without a version fails. It has no tag, so no rule applies to
        # it — and a fixture without one would not prove that.
        f"# docker pull ghcr.io/fora-protocol/{service}   <- fails, there is no default tag",
        "```",
        "",
    ]
    return "\n".join(lines)


def copy_gate_into(root: Path) -> None:
    """Put the shipped gate, and the definition it sources, in the tree under test.

    The way the sibling guards do it: the tree under test carries the checker it
    is checked with.

    Both files, because the gate sources its document list from beside itself and
    refuses to start when it cannot read it. A fixture carrying only the gate
    fails every case with the same message, which looks like a broken suite
    rather than a missing file.
    """
    install_gate(root, GATE.name, DOCS_DEFINITION.name)


def write_docs(root: Path, version: str = DECLARED_VERSION) -> None:
    """The three documents, each declaring ``version`` once."""
    for doc in DOCS:
        target = root / doc
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(doc_body(Path(doc).parent.name, version=version))


def build_tree(root: Path, *, branch: str) -> None:
    """A committed tree with the three documents and every scan root required.

    A real git repository, because the gate reads the tracked file list rather
    than walking the filesystem — a plain directory would make it die, and a case
    built on one would then pass for the wrong reason.

    The branch name is a parameter because the two callers depend on different
    ones: the agreement gate does not care, and the release tool refuses to run
    anywhere but the release branch.
    """
    init_scratch_repo(root, branch=branch)

    for directory in SCAN_ROOTS:
        (root / directory).mkdir(parents=True, exist_ok=True)
        # git tracks files, not directories, and the gate requires every scan
        # root to exist and to hold something.
        (root / directory / ".keep").write_text("")

    write_docs(root)
    copy_gate_into(root)
    commit_all(root, "baseline")
