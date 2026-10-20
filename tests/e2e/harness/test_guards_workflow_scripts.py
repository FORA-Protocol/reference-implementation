"""Meta-guard — what a workflow may put in a script, and the console's build argument.

GitHub substitutes a ``${{ ... }}`` expression into a script as TEXT, before the
interpreter reads it. A value that arrives that way is command text, not an
argument, and a git ref name is allowed to hold ``$``, backticks, parentheses,
``;``, ``&`` and ``|``. git does reject some characters — spaces, backslashes,
control characters, and the revision syntax it needs for itself — but not one of
those omissions closes the hole. The publishing workflow holds a token with
``packages: write`` for every image it publishes, so a script built this way
turns "can push a tag" into "can run anything". The fix is an ``env:`` entry,
which delivers the value as data.

Two positions count as a script. ``run:`` is the obvious one. ``with: script:``
is the other: ``actions/github-script`` evaluates that value as JavaScript, so an
expression interpolated there is the same defect wearing a different key.

The second rule is about versions. Every published tag comes from the git tag and
nowhere else (ADR-024 D2), so a version written into a script is a second place
that has to be edited at release time, and the last one anybody thinks to check.
Confining the rule to scripts is what makes it exception-free: the pinned
third-party actions carry their version in a comment on the ``uses:`` line, which
is never inside one.

The third rule is about the one build argument the publishing workflow passes.
The console image keeps the git tag it was built from as CONSOLE_EDGE_RELEASE_REF,
the version of the edge packages the console tells every publisher to build from.
It gets there in two halves: the console's matrix entry sets EDGE_RELEASE_REF from
``github.ref_name``, and the build step forwards ``matrix.build-args``. If either
half goes missing, the console image is published naming no version, and the
build still succeeds. No other entry may set a build argument, so an argument
moved to the wrong service is reported as well.

What each case reads, because the two kinds are not interchangeable:

- The cases built on ``_workflow_with`` drive the extraction over SYNTHETIC text.
  They are what proves the checks fire, and that they fire on scripts only — the
  synthetic workflow always carries an expression in an action input and in an
  ``env:`` value, so a check that scanned the whole file instead would fail its
  clean case.
- The cases built on ``_publishing_workflow`` do the same for the build-argument
  rule: one clean case, then one case for each way the wiring can break.
- The cases built on ``_WORKFLOWS`` and ``_PUBLISHING_WORKFLOW`` read the REAL
  files under ``.github/workflows/``. Nothing else in this repository parses them:
  there is no actionlint, no yamllint and no yq.

Pure parsing work: no stack, no Docker, no database. The marks come from
``guard_harness.guard_marks``, whose docstring says why the isolation one is
there.
"""

from __future__ import annotations

import re
from pathlib import Path

import yaml

from .conftest import REPO_ROOT
from .guard_harness import guard_marks
from .published_paths import held_back, shared_array

# REPO_ROOT rather than a parents[N] walk: inside the runner container the
# harness sits at /runner/harness, which has fewer parents than the host layout,
# and computing the walk here raises at import — killing collection for the whole
# tier rather than this one module.
_WORKFLOW_DIR = REPO_ROOT / ".github" / "workflows"

# Both extensions, because GitHub reads both. A file renamed from one to the
# other must stay inside the scan rather than leaving it silently.
_WORKFLOWS = sorted([*_WORKFLOW_DIR.glob("*.yml"), *_WORKFLOW_DIR.glob("*.yaml")])

_EXPRESSION = re.compile(r"\$\{\{")
_VERSION_LITERAL = re.compile(r"\bv?[0-9]+\.[0-9]+\.[0-9]+")

# The build-argument rule is about one workflow, so it reads that file by name. A
# rename fails the real-file case loudly instead of leaving the rule unchecked.
_PUBLISHING_WORKFLOW = _WORKFLOW_DIR / "publish-images.yml"
_BUILD_ACTION = "docker/build-push-action@"
_RELEASE_ARGUMENT = "EDGE_RELEASE_REF=${{ github.ref_name }}"
_FORWARDED_ARGUMENTS = "${{ matrix.build-args }}"
# GitHub reads ${{github.ref_name}} and ${{ github.ref_name }} alike, so the
# spacing inside the braces is free.
_RELEASE_ARGUMENT_SHAPE = re.compile(r"^EDGE_RELEASE_REF=\$\{\{\s*github\.ref_name\s*\}\}$")
_FORWARDED_ARGUMENTS_SHAPE = re.compile(r"^\$\{\{\s*matrix\.build-args\s*\}\}$")

pytestmark = guard_marks(
    skip_when=not _WORKFLOW_DIR.is_dir(),
    reason="the workflow directory is absent from the e2e runner image; this guard runs on the host",
)


def _scripts(text: str) -> list[tuple[str, str]]:
    """Every script in one workflow, each labelled with its owner and its key.

    Two keys carry a script: ``run:``, which bash executes, and ``with: script:``,
    which actions/github-script evaluates as JavaScript. The label is what a
    failure prints, so it names the step and the key rather than an index into a
    list the reader cannot see.

    ``or []`` on the step list rather than a default argument: a job written with
    ``steps:`` and nothing under it parses to None, and a reusable-workflow job
    has no ``steps`` key at all.
    """
    document = yaml.safe_load(text) or {}
    found: list[tuple[str, str]] = []
    for job_id, job in (document.get("jobs") or {}).items():
        for index, step in enumerate(job.get("steps") or []):
            if not isinstance(step, dict):
                continue
            name = step.get("name") or f"step {index}"
            if "run" in step:
                found.append((f"{job_id} / {name} [run:]", str(step["run"])))
            script = (step.get("with") or {}).get("script")
            if script is not None:
                found.append((f"{job_id} / {name} [script:]", str(script)))
    return found


def _expressions_in_scripts(text: str) -> list[str]:
    return [label for label, body in _scripts(text) if _EXPRESSION.search(body)]


def _versions_in_scripts(text: str) -> list[tuple[str, str]]:
    return [
        (label, hit) for label, body in _scripts(text) for hit in _VERSION_LITERAL.findall(body)
    ]


def _workflow_with(body: str, *, key: str = "run") -> str:
    """A minimal workflow whose only script is ``body``, written under ``key``.

    It always carries an expression in an action input and in an ``env:`` value.
    Those are the two safe positions, so the clean case below fails if either
    check ever widens from the scripts to the whole file.
    """
    if key == "run":
        script_step = (
            "      - name: The script\n"
            "        env:\n"
            "          TRIGGER_REF: ${{ github.ref }}\n"
            "        run: |\n" + _indent(body, 10)
        )
    else:
        script_step = (
            "      - name: The script\n"
            "        uses: actions/github-script"
            "@0000000000000000000000000000000000000000 # v9.9.9\n"
            "        with:\n"
            "          script: |\n" + _indent(body, 12)
        )
    return (
        "name: probe\n"
        "on:\n"
        "  workflow_dispatch:\n"
        "jobs:\n"
        "  probe:\n"
        "    runs-on: ubuntu-24.04\n"
        "    steps:\n"
        "      - name: An action input, which is not a script\n"
        "        uses: some/action@0000000000000000000000000000000000000000 # v9.9.9\n"
        "        with:\n"
        "          ref: ${{ github.ref }}\n" + script_step
    )


def _indent(body: str, width: int) -> str:
    pad = " " * width
    return "\n".join(f"{pad}{line}" for line in body.splitlines()) + "\n"


def _build_argument_problems(text: str) -> list[str]:
    """Everything wrong with how one workflow passes build arguments to its images.

    Each problem starts with the place it is about (``workflow``, ``build step``,
    ``matrix`` or ``<service> entry``), then a colon. The list is empty when the
    workflow has exactly one build step, that step forwards ``matrix.build-args``,
    the matrix has exactly one console entry, that entry sets EDGE_RELEASE_REF from
    ``github.ref_name``, and no other entry sets a build argument. A build step or a
    console entry that cannot be found is a problem too, so a renamed action or a
    restructured matrix fails instead of passing unexamined.
    """
    document = yaml.safe_load(text) or {}
    builds = [
        (job, step)
        for job in (document.get("jobs") or {}).values()
        for step in job.get("steps") or []
        if isinstance(step, dict) and str(step.get("uses", "")).startswith(_BUILD_ACTION)
    ]
    if len(builds) != 1:
        return [f"workflow: {len(builds)} {_BUILD_ACTION} steps; there must be exactly one"]
    job, build = builds[0]
    problems: list[str] = []
    forwarded = str((build.get("with") or {}).get("build-args") or "").strip()
    if not _FORWARDED_ARGUMENTS_SHAPE.match(forwarded):
        problems.append(
            f"build step: build-args is {forwarded!r}; it must be {_FORWARDED_ARGUMENTS}"
        )
    matrix = (job.get("strategy") or {}).get("matrix") or {}
    entries = [entry for entry in matrix.get("include") or [] if isinstance(entry, dict)]
    consoles = [entry for entry in entries if entry.get("service") == "console"]
    if len(consoles) != 1:
        problems.append(f"matrix: {len(consoles)} console entries; there must be exactly one")
    for entry in entries:
        lines = str(entry.get("build-args") or "").splitlines()
        arguments = [line.strip() for line in lines if line.strip()]
        if entry.get("service") != "console":
            if arguments:
                problems.append(
                    f"{entry.get('service')} entry: passes {arguments}; "
                    "only the console takes a build argument"
                )
            continue
        release = [argument for argument in arguments if argument.startswith("EDGE_RELEASE_REF=")]
        if len(release) != 1 or not _RELEASE_ARGUMENT_SHAPE.match(release[0]):
            problems.append(
                f"console entry: passes {release or 'no EDGE_RELEASE_REF'}; "
                f"it must pass {_RELEASE_ARGUMENT} exactly once"
            )
    return problems


def _places(problems: list[str]) -> list[str]:
    """The place each problem is about: the text before its first colon."""
    return [problem.split(":", 1)[0] for problem in problems]


def _publishing_workflow(*, console: str | None, other: str | None, forwarded: str | None) -> str:
    """A minimal publishing workflow: an exchange entry, a console entry, one build step.

    ``console`` and ``other`` become the build-args of the console and exchange
    entries, ``forwarded`` the build step's. None leaves that key out.
    """
    exchange: dict[str, str] = {"service": "exchange", "dockerfile": "src/exchange/Dockerfile"}
    console_entry: dict[str, str] = {"service": "console", "dockerfile": "src/console/Dockerfile"}
    build: dict[str, str] = {"context": ".", "file": "${{ matrix.dockerfile }}"}
    for target, value in ((exchange, other), (console_entry, console), (build, forwarded)):
        if value is not None:
            target["build-args"] = value
    job = {
        "runs-on": "ubuntu-24.04",
        "strategy": {"matrix": {"include": [exchange, console_entry]}},
        "steps": [{"name": "Build and push", "uses": f"{_BUILD_ACTION}{'0' * 40}", "with": build}],
    }
    return yaml.safe_dump({"name": "probe", "jobs": {"publish": job}})


def test_a_clean_script_is_not_reported() -> None:
    """Without this the cases that follow prove nothing.

    A check that reported every workflow would satisfy all of them. This one also
    pins the boundary: the synthetic workflow puts an expression in an action
    input and in an env: value, and neither is a finding.
    """
    text = _workflow_with('echo "ref is ${TRIGGER_REF}"')

    assert _scripts(text), "the fixture contributed no script, so nothing was examined"
    assert _expressions_in_scripts(text) == []
    assert _versions_in_scripts(text) == []


def test_an_expression_inside_a_run_script_is_reported() -> None:
    """The injection shape: the ref becomes part of the command bash runs."""
    text = _workflow_with('echo "ref is ${{ github.ref }}"')

    assert _expressions_in_scripts(text) == ["probe / The script [run:]"]


def test_an_expression_inside_a_github_script_is_reported() -> None:
    """The same defect under a different key, evaluated as JavaScript.

    A check that looked only at ``run:`` would report nothing here, and the
    module's claim to cover the injection class would be false.
    """
    text = _workflow_with("console.log('ref is ${{ github.ref }}')", key="script")

    assert _expressions_in_scripts(text) == ["probe / The script [script:]"]


def test_a_version_written_into_a_script_is_reported() -> None:
    """A version in a script is a second place a release has to edit."""
    text = _workflow_with('echo "for example v1.0.0"')

    assert _versions_in_scripts(text) == [("probe / The script [run:]", "v1.0.0")]


def test_the_workflows_are_present_and_contribute_scripts() -> None:
    """A PASS on the real-file cases means nothing if nothing was read.

    Each of them iterates the scripts these files contain, so a rename that
    emptied the glob, or a restructure that left no script at all, would turn
    them green having examined nothing.
    """
    assert _WORKFLOWS, f"no workflow file under {_WORKFLOW_DIR}"

    scripts = [body for path in _WORKFLOWS for body in _scripts(path.read_text())]

    assert scripts, "the workflows parse but contribute no script to examine"


def test_no_workflow_interpolates_an_expression_into_a_script() -> None:
    """Read over the real files, which nothing else in this repository parses."""
    found: list[str] = []
    for path in _WORKFLOWS:
        found += [
            f"{_relative(path)}: {label}" for label in _expressions_in_scripts(path.read_text())
        ]

    assert found == [], (
        "these scripts are built by text substitution; pass the value through env: instead:\n"
        + "\n".join(found)
    )


def test_no_workflow_names_a_version_inside_a_script() -> None:
    """Read over the real files. The tag is the only source of a version."""
    found: list[str] = []
    for path in _WORKFLOWS:
        found += [
            f"{_relative(path)}: {label} names {hit}"
            for label, hit in _versions_in_scripts(path.read_text())
        ]

    assert found == [], (
        "a version written into a script is a second place a release has to edit:\n"
        + "\n".join(found)
    )


def test_a_clean_build_argument_wiring_is_not_reported() -> None:
    """The clean case of the build-argument rule. Without it the next three prove nothing."""
    text = _publishing_workflow(
        console=_RELEASE_ARGUMENT, other=None, forwarded=_FORWARDED_ARGUMENTS
    )

    assert _build_argument_problems(text) == []


def test_a_console_entry_without_its_release_argument_is_reported() -> None:
    """The console image would be published naming no edge release."""
    text = _publishing_workflow(console=None, other=None, forwarded=_FORWARDED_ARGUMENTS)

    assert _places(_build_argument_problems(text)) == ["console entry"]


def test_a_release_argument_moved_to_another_entry_is_reported() -> None:
    """Both halves of the move are reported: the console lacks it, the exchange has it."""
    text = _publishing_workflow(
        console=None, other=_RELEASE_ARGUMENT, forwarded=_FORWARDED_ARGUMENTS
    )

    assert _places(_build_argument_problems(text)) == ["exchange entry", "console entry"]


def test_a_build_step_that_does_not_forward_the_matrix_arguments_is_reported() -> None:
    """The console's entry is right, but the build never receives it."""
    text = _publishing_workflow(console=_RELEASE_ARGUMENT, other=None, forwarded=None)

    assert _places(_build_argument_problems(text)) == ["build step"]


def test_the_publishing_workflow_gives_only_the_console_its_release_tag() -> None:
    """Read over the real publishing workflow, which nothing else in this repository parses."""
    problems = _build_argument_problems(_PUBLISHING_WORKFLOW.read_text())

    assert problems == [], (
        "the console image must be built with the git tag as EDGE_RELEASE_REF, "
        "and no other image with a build argument:\n" + "\n".join(problems)
    )


def _relative(path: Path) -> str:
    return str(path.relative_to(REPO_ROOT))


def _dockerfile_problems(text: str, root: Path, held: list[str]) -> list[str]:
    """Every matrix entry's Dockerfile must exist under ``root`` and ship with it.

    The workflow runs only on the public repository, where a path the publish
    holds back does not exist, and a matrix entry whose Dockerfile is absent
    fails the whole run.
    """
    document = yaml.safe_load(text) or {}
    problems: list[str] = []
    for job in (document.get("jobs") or {}).values():
        matrix = ((job or {}).get("strategy") or {}).get("matrix") or {}
        for entry in matrix.get("include") or []:
            if not isinstance(entry, dict) or "dockerfile" not in entry:
                continue
            dockerfile = str(entry["dockerfile"])
            if held_back(dockerfile, held):
                problems.append(
                    f"{entry.get('service')}: {dockerfile} is held back from the publish"
                )
            elif not (root / dockerfile).is_file():
                problems.append(f"{entry.get('service')}: {dockerfile} is not in the tree")
    return problems


def _matrix_workflow(dockerfile: str) -> str:
    """A publishing workflow with one matrix entry building ``dockerfile``."""
    job = {"strategy": {"matrix": {"include": [{"service": "probe", "dockerfile": dockerfile}]}}}
    return yaml.safe_dump({"name": "probe", "jobs": {"publish": job}})


def test_every_matrix_dockerfile_ships_with_the_public_tree() -> None:
    """The real matrix names only services whose Dockerfile the publish carries.

    The held-back paths are read from the shared definition inside the body, for
    the reason ``published_paths.first_entry`` gives.
    """
    problems = _dockerfile_problems(
        _PUBLISHING_WORKFLOW.read_text(), REPO_ROOT, shared_array("DENY_PATHS")
    )

    assert problems == [], problems


def test_a_matrix_dockerfile_the_publish_holds_back_is_reported(tmp_path: Path) -> None:
    """A Dockerfile under a held-back directory is reported even though it exists here."""
    dockerfile = tmp_path / "src" / "kept" / "Dockerfile"
    dockerfile.parent.mkdir(parents=True)
    dockerfile.write_text("FROM scratch\n")

    problems = _dockerfile_problems(
        _matrix_workflow("src/kept/Dockerfile"), tmp_path, ["src/kept/"]
    )

    assert problems == ["probe: src/kept/Dockerfile is held back from the publish"], problems


def test_a_matrix_dockerfile_missing_from_the_tree_is_reported(tmp_path: Path) -> None:
    """A Dockerfile the tree does not carry is reported before the run fails on it."""
    problems = _dockerfile_problems(_matrix_workflow("src/absent/Dockerfile"), tmp_path, [])

    assert problems == ["probe: src/absent/Dockerfile is not in the tree"], problems
