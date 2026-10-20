"""Guard: the e2e-only image content stays out of every production image.

Two service images carry content only the e2e stack may have:

* the Exchange's ``e2e`` target builds its server with the ``e2e`` Go build tag,
  which compiles in the e2e-only control service (ADR-028 D6);
* the identity service's ``e2e`` target adds the operator's revocation CLI,
  ``identity-operator``, which the suite runs inside the container.

In each Dockerfile the production image is the LAST stage, named ``production``,
so a build that names no target gets it: the publishing workflow, the registry
push scripts and the deployment guides all build that way. The Go side of the
property, that an untagged build cannot contain the control package, is checked
by ``internal/guards/e2e_control_build_tag_guard_test.go``. This module checks the
image side:

* the production stage is the last stage, and copies only from the production
  builder;
* e2e content is built only in a stage the ``e2e`` target alone reads, and the
  production stage reads none of those stages, directly or through another stage.
  A stage builds e2e content when any instruction other than COPY carries the word
  ``e2e`` (so every spelling of the build tag counts: ``-tags e2e``,
  ``-tags=e2e``, quoted, in a tag list, or in ``GOFLAGS``), names the operator
  CLI's package, or builds a Go package wildcard (``/...``), which would build the
  operator CLI with everything else. A stage reads another when it is built FROM
  it, COPYs from it, or bind-mounts it in a RUN;
* every e2e service built from either Dockerfile builds its ``e2e`` target, so
  the suite runs against what it expects;
* no script or workflow that builds production images mentions ``e2e`` outside a
  comment, so none can name the e2e target or pass the e2e tag as a build argument.

Each rule is also run on a small Dockerfile of the same shape with one violation
written in, and must report it: the five spellings of the tag, a production or
runtime stage built FROM an e2e stage, a bind mount of one, a package wildcard,
and a build argument carrying the tag.

It reads files only. The Dockerfiles, the workflow and the push scripts are not
copied into the e2e runner image, so it skips there and runs on the host.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from pathlib import Path

import pytest
import yaml

from ._compose import COMPOSE_FILE, REPO_ROOT
from .guard_harness import guard_marks

EXCHANGE = REPO_ROOT / "src" / "exchange" / "Dockerfile"
IDENTITY = REPO_ROOT / "src" / "identity" / "Dockerfile"

#: Each Dockerfile, and the stages only its ``e2e`` target may read.
_E2E_ONLY_STAGES = {EXCHANGE: {"builder-e2e"}, IDENTITY: {"builder-operator"}}

#: e2e-only content on an instruction line other than COPY: the word ``e2e`` in any
#: spelling of a tag or flag, the operator CLI's package, or a Go package wildcard.
_E2E_CONTENT = re.compile(r"\be2e\b|cmd/operator\b|/\.\.\.(?=\s|$)")

#: What builds the production images: none may mention e2e outside a comment.
_PRODUCTION_BUILDERS = [
    REPO_ROOT / ".github" / "workflows" / "publish-images.yml",
    REPO_ROOT / "scripts" / "ecr-push.sh",
    REPO_ROOT / "deploy" / "terraform" / "scripts" / "build-push-images.sh",
]

pytestmark = guard_marks(
    skip_when=not (EXCHANGE.is_file() and IDENTITY.is_file()),
    reason="the service Dockerfiles are absent from the e2e runner image; this guard runs on the host",
)


@dataclass
class Stage:
    """One build stage: its name, the image or stage it is built FROM, and its lines."""

    name: str
    base: str
    lines: list[str] = field(default_factory=list)

    def copies_from(self) -> set[str]:
        """The stages this stage's COPY instructions copy from."""
        return {m.group(1) for line in self.lines for m in re.finditer(r"--from=(\S+)", line)}

    def reads(self, stage_names: set[str]) -> set[str]:
        """Every stage this one takes content from: its base, COPY sources and RUN mounts."""
        mounts = {
            m.group(1)
            for line in self.lines
            for m in re.finditer(r"--mount=\S*?\bfrom=([^,\s]+)", line)
        }
        base = {self.base} & stage_names
        return base | self.copies_from() | mounts

    def builds_e2e_content(self) -> bool:
        return any(
            _E2E_CONTENT.search(line) for line in self.lines if not line.upper().startswith("COPY")
        )


def _parse_stages(text: str) -> list[Stage]:
    """The stages of a Dockerfile's ``text`` in order, continuation lines joined."""
    joined = re.sub(r"\\\n", " ", text)
    stages: list[Stage] = []
    for raw in joined.splitlines():
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        start = re.match(r"FROM\s+(\S+)(?:\s+AS\s+(\S+))?", line, re.IGNORECASE)
        if start:
            name = start.group(2) or f"stage-{len(stages)}"
            stages.append(Stage(name=name, base=start.group(1)))
            continue
        assert stages, f"an instruction comes before the first FROM: {line}"
        stages[-1].lines.append(line)
    return stages


def _stages(dockerfile: Path) -> list[Stage]:
    """The stages of ``dockerfile`` in order."""
    return _parse_stages(dockerfile.read_text())


def _e2e_content_problems(stages: list[Stage], e2e_only: set[str]) -> list[str]:
    """Every way the e2e-only content can reach a stage other than ``e2e``; empty when none."""
    by_name = {stage.name: stage for stage in stages}
    names = set(by_name)
    problems: list[str] = []
    for name in sorted(e2e_only):
        if name not in by_name:
            problems.append(f"no {name!r} stage")
        elif not by_name[name].builds_e2e_content():
            problems.append(f"{name!r} builds no e2e content")
    allowed = e2e_only | {"e2e"}
    for name in sorted(n for n, s in by_name.items() if s.builds_e2e_content()):
        if name not in allowed:
            problems.append(f"{name!r} builds e2e content")
    readers = {n for n, s in by_name.items() if s.reads(names) & e2e_only}
    if readers != {"e2e"}:
        problems.append(f"the e2e-only stages are read by {sorted(readers)}")
    if "production" in by_name:
        seen: set[str] = set()
        pending = [by_name["production"]]
        while pending:
            for read in pending.pop().reads(names) - seen:
                seen.add(read)
                pending.append(by_name[read])
        if seen & allowed:
            problems.append(f"production depends on {sorted(seen & allowed)}")
    return problems


def _builder_problems(text: str) -> list[str]:
    """The lines of a production build script that mention e2e outside a comment."""
    return [
        line.strip()
        for line in text.splitlines()
        if not line.strip().startswith("#") and re.search(r"\be2e\b", line)
    ]


@pytest.mark.parametrize("dockerfile", [EXCHANGE, IDENTITY], ids=["exchange", "identity"])
def test_the_default_target_is_production_and_copies_from_its_own_builder(
    dockerfile: Path,
) -> None:
    """The last stage is ``production``, and it copies from ``builder`` and nothing else."""
    stages = _stages(dockerfile)
    assert stages[-1].name == "production", (
        f"the last stage of {dockerfile} is {stages[-1].name!r}: a build naming no target "
        "must get the production image"
    )
    assert stages[-1].copies_from() == {"builder"}, (
        f"the production stage of {dockerfile} copies from {stages[-1].copies_from()}"
    )


@pytest.mark.parametrize("dockerfile", [EXCHANGE, IDENTITY], ids=["exchange", "identity"])
def test_e2e_content_is_built_only_where_the_e2e_target_alone_reads(dockerfile: Path) -> None:
    """The e2e tag or the operator CLI is built only in an e2e-only stage, read only by ``e2e``.

    Each e2e-only stage must build that content, so this check cannot pass by
    looking for a stage that is not there.
    """
    problems = _e2e_content_problems(_stages(dockerfile), _E2E_ONLY_STAGES[dockerfile])
    assert not problems, f"{dockerfile}: {problems}"


#: A Dockerfile of the real shape, holding no violation; every case below edits one line.
_CLEAN = """\
FROM golang:1.26-bookworm AS source
WORKDIR /src
COPY . .

FROM source AS builder
RUN go build -o /out/server ./src/exchange/cmd/server

FROM source AS builder-e2e
RUN go build -tags e2e -o /out/server ./src/exchange/cmd/server

FROM gcr.io/distroless/base-debian12:nonroot AS runtime

FROM runtime AS e2e
COPY --from=builder-e2e /out/server /server

FROM runtime AS production
COPY --from=builder /out/server /server
"""

_PRODUCTION_BUILD = "RUN go build -o /out/server ./src/exchange/cmd/server"


def _edited(old: str, new: str) -> str:
    """``_CLEAN`` with ``old`` replaced by ``new``; refuses an edit that would change nothing."""
    assert old in _CLEAN, (
        f"the clean Dockerfile no longer holds {old!r}, so this case tests nothing"
    )
    return _CLEAN.replace(old, new, 1)


def test_the_clean_dockerfile_has_no_e2e_content_problem() -> None:
    """The control: without it, the cases below are satisfied by a check that reports anything."""
    assert _e2e_content_problems(_parse_stages(_CLEAN), {"builder-e2e"}) == []


_TAG_IN_PRODUCTION_BUILDER = ["'builder' builds e2e content"]
_E2E_IN_PRODUCTION = ["production depends on ['builder-e2e', 'e2e']"]


@pytest.mark.parametrize(
    ("text", "problems"),
    [
        pytest.param(
            _edited(
                _PRODUCTION_BUILD, "RUN go build -tags=e2e -o /out/server ./src/exchange/cmd/server"
            ),
            _TAG_IN_PRODUCTION_BUILDER,
            id="equals",
        ),
        pytest.param(
            _edited(
                _PRODUCTION_BUILD,
                'RUN go build -tags "e2e" -o /out/server ./src/exchange/cmd/server',
            ),
            _TAG_IN_PRODUCTION_BUILDER,
            id="double-quoted",
        ),
        pytest.param(
            _edited(
                _PRODUCTION_BUILD,
                "RUN go build -tags 'e2e' -o /out/server ./src/exchange/cmd/server",
            ),
            _TAG_IN_PRODUCTION_BUILDER,
            id="single-quoted",
        ),
        pytest.param(
            _edited(
                _PRODUCTION_BUILD,
                "RUN go build -tags integration,e2e -o /out/server ./src/exchange/cmd/server",
            ),
            _TAG_IN_PRODUCTION_BUILDER,
            id="tag-list",
        ),
        pytest.param(
            _edited("WORKDIR /src\n", "WORKDIR /src\nENV GOFLAGS=-tags=e2e\n"),
            ["'source' builds e2e content"],
            id="goflags-env",
        ),
        pytest.param(
            _edited(_PRODUCTION_BUILD, "RUN go build -o /out/ ./src/identity/cmd/..."),
            _TAG_IN_PRODUCTION_BUILDER,
            id="package-wildcard",
        ),
        pytest.param(
            _edited("FROM runtime AS production", "FROM e2e AS production"),
            _E2E_IN_PRODUCTION,
            id="from-e2e",
        ),
        pytest.param(
            _edited(
                "FROM gcr.io/distroless/base-debian12:nonroot AS runtime",
                "FROM builder-e2e AS runtime",
            ),
            [
                "the e2e-only stages are read by ['e2e', 'runtime']",
                "production depends on ['builder-e2e']",
            ],
            id="runtime-from-builder-e2e",
        ),
        pytest.param(
            _edited(
                "COPY --from=builder /out/server /server",
                "RUN --mount=type=bind,from=builder-e2e,source=/out,target=/m cp /m/server /server",
            ),
            [
                "'production' builds e2e content",
                "the e2e-only stages are read by ['e2e', 'production']",
                "production depends on ['builder-e2e']",
            ],
            id="mount-from-builder-e2e",
        ),
    ],
)
def test_the_e2e_content_check_refuses_the_known_bypasses(text: str, problems: list[str]) -> None:
    """Each tag spelling and each other way into the production image is reported, and only it."""
    assert _e2e_content_problems(_parse_stages(text), {"builder-e2e"}) == problems


def test_every_e2e_service_built_from_those_dockerfiles_builds_its_e2e_target() -> None:
    """Each compose service whose build names either Dockerfile sets ``target: e2e``."""
    services = yaml.safe_load(COMPOSE_FILE.read_text())["services"]
    wanted = {str(path.relative_to(REPO_ROOT)) for path in _E2E_ONLY_STAGES}
    built = {
        name: service["build"]
        for name, service in services.items()
        if isinstance(service.get("build"), dict) and service["build"].get("dockerfile") in wanted
    }
    assert {build["dockerfile"] for build in built.values()} == wanted, built
    wrong = {
        name: build.get("target") for name, build in built.items() if build.get("target") != "e2e"
    }
    assert not wrong, f"these services build a target other than e2e: {wrong}"


@pytest.mark.parametrize("builder", _PRODUCTION_BUILDERS, ids=lambda p: p.name)
def test_no_production_image_build_mentions_e2e(builder: Path) -> None:
    """The workflow and scripts that build production images never name the e2e target or tag."""
    assert builder.is_file(), f"{builder} is gone; update this guard's list"
    hits = _builder_problems(builder.read_text())
    assert not hits, f"{builder} mentions e2e outside a comment: {hits}"


@pytest.mark.parametrize(
    ("text", "hits"),
    [
        pytest.param("docker build -t exchange:latest .\n", [], id="clean"),
        pytest.param("# the e2e target is built elsewhere\n", [], id="comment"),
        pytest.param(
            "docker build --target e2e -t exchange .\n",
            ["docker build --target e2e -t exchange ."],
            id="target-e2e",
        ),
        pytest.param(
            "docker build --build-arg GOFLAGS=-tags=e2e .\n",
            ["docker build --build-arg GOFLAGS=-tags=e2e ."],
            id="build-arg-goflags",
        ),
    ],
)
def test_the_build_script_check_refuses_e2e_and_only_e2e(text: str, hits: list[str]) -> None:
    """The production-build check reports a target or a build argument naming e2e, never a comment."""
    assert _builder_problems(text) == hits
