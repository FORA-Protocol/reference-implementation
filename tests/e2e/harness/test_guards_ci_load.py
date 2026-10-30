"""Meta-guard — the CI e2e job's build-step limit, and its host-load record.

``deploy/ci/buildkitd.toml`` limits the builder the CI e2e job builds its images
with to two build steps at a time. BuildKit ignores a key it does not know,
without an error, so a misspelled or misplaced key would remove the limit and
nothing would fail: the build would just load the host again. So the file is
checked here for the one key BuildKit reads, in the section it reads it from.

``scripts/host-load.sh`` runs in the background for the whole CI e2e job.
``record`` writes vmstat's line every five seconds with the time, the load
averages, the available memory and the stack's container count added, and
``summary`` reads the record back at the end and prints the peaks in the job log.
The record is kept as an artifact: it is the one account of what a run cost the
machine it ran on that does not depend on someone watching that machine.

Nothing else would notice either half going wrong. A summary that read the wrong
column, or counted vmstat's first line (an average since boot), would print a
plausible number on every run, and the job passes whatever it prints. So the
summary runs here on records whose answers are known, and the recorder runs
against stub commands whose answers are known.

The recorder cases need vmstat and /proc, so they skip where those are missing.
Pure subprocess work otherwise: no stack, no Docker, no database. The marks come
from ``guard_harness.guard_marks``, whose docstring says why the isolation one is
there.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import tomllib
from pathlib import Path

import pytest

from .conftest import REPO_ROOT
from .guard_harness import guard_marks, run_gate, stub_commands_env

SCRIPT = "host-load.sh"
BUILDKIT_CONFIG = REPO_ROOT / "deploy" / "ci" / "buildkitd.toml"

pytestmark = guard_marks(skip_when=not (REPO_ROOT / "scripts" / SCRIPT).is_file())

_needs_vmstat = pytest.mark.skipif(
    shutil.which("vmstat") is None or not Path("/proc/loadavg").is_file(),
    reason="the recorder reads vmstat and /proc, which this machine does not have",
)

_HOST = (
    "# host-load record: interval=5 cpus=6 mem_mib=16000 swap_mib=4096 "
    "source=vmstat_from_procps-ng_4.0.4"
)
# vmstat's columns as procps 4 prints them; procps 3 has no guest column (gu).
_PROCPS4 = tuple("r b swpd free buff cache si so bi bo in cs us sy id wa st gu".split())
_PROCPS3 = _PROCPS4[:-1]
_ADDED = tuple("load1 load5 load15 avail_mib containers".split())

# The one call the recorder makes. Any other call gets five ids, so a recorder
# that stopped filtering on the compose project would count the builder too.
_DOCKER = (
    'if [ "$*" = "ps -q --filter label=com.docker.compose.project" ]; then\n'
    "    printf 'id1\\nid2\\n'\n"
    "else\n"
    "    printf 'id1\\nid2\\nid3\\nid4\\nid5\\n'\n"
    "fi\n"
)


def _row(time: str, columns: tuple[str, ...] = _PROCPS4, **values: str | int) -> str:
    """One record line at ``time``. Columns not given are quiet: idle CPU, no swapping."""
    line: dict[str, str | int] = dict.fromkeys(columns, 0)
    line.update(free=900, cache=2000, id=100, load1="0.10", load5="0.05", load15="0.01")
    line.update(avail_mib=8000, containers=0)
    line.update(values)
    return " ".join((f"2026-10-21T{time}Z", *(str(line[name]) for name in (*columns, *_ADDED))))


def _record(*rows: str, columns: tuple[str, ...] = _PROCPS4) -> str:
    """A record as ``record`` writes one: the host line, vmstat's group headings
    as a comment, the column names, then the rows."""
    header = " ".join(("time", *columns, *_ADDED))
    return "\n".join((_HOST, "#   --procs-- --memory-- --swap-- --cpu--", header, *rows)) + "\n"


def _build_then_stack(columns: tuple[str, ...] = _PROCPS4) -> list[str]:
    """An image build that keeps every CPU busy, then a stack that starts.

    The first line is vmstat's average since boot. Its 99% busy and its swapping
    must not count: they describe the machine's whole uptime, not this run.
    """
    return [
        _row("10:00:00", columns, us=99, id=0, wa=1, si=50, so=50, r=1, load1="0.50"),
        _row(
            "10:00:05", columns, us=80, sy=17, id=3, r=31, b=4, so=3, load1="12.00", avail_mib=5000
        ),
        _row(
            "10:00:10", columns, us=60, sy=30, id=0, wa=10, r=20, b=1, load1="23.40", avail_mib=2104
        ),
        _row(
            "10:00:15",
            columns,
            us=10,
            sy=5,
            id=80,
            wa=5,
            r=2,
            load1="20.00",
            avail_mib=3000,
            containers=38,
        ),
    ]


_EXPECTED = [
    "host load: 4 samples every 5s from 10:00:00 to 10:00:15 UTC; "
    "host has 6 CPUs, 16000 MiB memory, 4096 MiB swap",
    "  1-minute load average: peak 23.40 at 10:00:10",
    "  tasks running or waiting for a CPU: peak 31 at 10:00:05; tasks blocked on I/O: peak 4",
    "  CPU busy (not idle, not waiting for I/O): peak 97% at 10:00:05; 90% or more for 0m10s; "
    "I/O wait peak 10%",
    "  swapping: in peak 0 MiB/s, out peak 3 MiB/s",
    "  memory available: lowest 2104 MiB at 10:00:10",
    "  stack containers first running at 10:00:15",
]


def _run(*args: str, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return run_gate(REPO_ROOT, SCRIPT, reads_root=False, env=env, args=args)


def _summary(tmp_path: Path, text: str) -> list[str]:
    record = tmp_path / "record.txt"
    record.write_text(text)
    proc = _run("summary", str(record))
    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    return proc.stdout.splitlines()


@pytest.mark.parametrize("columns", [_PROCPS4, _PROCPS3], ids=["procps-4", "procps-3"])
def test_summary_reports_the_peaks(tmp_path: Path, columns: tuple[str, ...]) -> None:
    """Every figure is checked against a record whose answers are known. The two
    procps layouts give the same answers, because the columns are found by name."""
    assert _summary(tmp_path, _record(*_build_then_stack(columns), columns=columns)) == _EXPECTED


def test_summary_skips_a_line_cut_short(tmp_path: Path) -> None:
    """A kill mid-write leaves a short last line. Read as a sample, its 50 tasks
    would become the peak and its missing idle column would read as 100% busy."""
    cut = _row("10:00:20", r=50, us=100, id=0)[:30]

    assert _summary(tmp_path, _record(*_build_then_stack(), cut)) == _EXPECTED


def test_summary_echoes_the_recorders_errors(tmp_path: Path) -> None:
    """The recorder writes why it stopped into the record; the job log is where
    anyone reads it."""
    error = "# error: vmstat stopped early, so the record ends here"

    lines = _summary(tmp_path, _record(*_build_then_stack()) + error + "\n")

    assert lines == [error, *_EXPECTED]


@pytest.mark.parametrize(
    ("containers", "expected"),
    [
        ("-", "  stack containers: unknown (the Docker daemon could not be asked)"),
        (0, "  stack containers: none ran"),
    ],
    ids=["docker-unreachable", "no-stack"],
)
def test_summary_says_when_no_stack_was_seen(
    tmp_path: Path, containers: str | int, expected: str
) -> None:
    rows = (_row("10:00:00", containers=containers), _row("10:00:05", containers=containers))

    assert _summary(tmp_path, _record(*rows))[-1] == expected


def test_summary_of_a_single_sample(tmp_path: Path) -> None:
    """vmstat's first line is all there is, and it describes the time since boot."""
    lines = _summary(tmp_path, _record(_row("10:00:00", us=99, id=0)))

    assert "  CPU and swapping: one sample only, which covers the time since boot" in lines
    assert not any("CPU busy" in line for line in lines)


@pytest.mark.parametrize(
    "text", ["", _HOST + "\n", _record()], ids=["empty", "host-line", "no-rows"]
)
def test_summary_of_a_record_without_samples(tmp_path: Path, text: str) -> None:
    assert _summary(tmp_path, text) == ["host load: no samples recorded"]


def test_summary_of_a_missing_record(tmp_path: Path) -> None:
    """after_script prints the summary even for a job whose recorder never ran."""
    absent = tmp_path / "absent.txt"

    proc = _run("summary", str(absent))

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert proc.stdout.splitlines() == [f"host load: no record at {absent}"]


def test_summary_refuses_a_record_without_a_column_it_reads(tmp_path: Path) -> None:
    """A vmstat that renamed a column must not leave a figure silently at zero."""
    columns = tuple(name for name in _PROCPS4 if name != "wa")

    lines = _summary(tmp_path, _record(_row("10:00:00", columns), columns=columns))

    assert lines == ["host load: the record lacks the column(s) wa"]


def _record_with(
    tmp_path: Path, stubs: dict[str, str]
) -> tuple[subprocess.CompletedProcess[str], Path]:
    record = tmp_path / "out" / "record.txt"
    env = stub_commands_env(tmp_path, stubs)
    return _run("record", str(record), "--interval", "1", "--count", "2", env=env), record


@_needs_vmstat
def test_record_writes_one_line_per_sample(tmp_path: Path) -> None:
    """Two samples of the real vmstat and /proc, and a docker stub that answers
    only the compose-project filter."""
    proc, record = _record_with(tmp_path, {"docker": _DOCKER})

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    host, headings, header, *rows = record.read_text().splitlines()
    assert host.startswith("# host-load record: interval=1 cpus=")
    assert headings.startswith("#")
    names = header.split()
    assert names[0] == "time"
    assert tuple(names[-5:]) == _ADDED
    assert {"r", "b", "si", "so", "id", "wa"} <= set(names)
    assert len(rows) == 2
    for row in rows:
        fields = dict(zip(names, row.split(), strict=True))
        assert re.fullmatch(r"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ", fields["time"]), row
        assert all(re.fullmatch(r"\d+\.\d\d", fields[name]) for name in _ADDED[:3]), row
        assert re.fullmatch(r"\d+", fields["avail_mib"]), row
        assert fields["containers"] == "2", "only containers of a compose project count"


@_needs_vmstat
def test_record_marks_containers_it_could_not_count(tmp_path: Path) -> None:
    """A Docker daemon that does not answer yet, as before dind is up, is a "-",
    never a 0 that would read as "no stack"."""
    proc, record = _record_with(tmp_path, {"docker": "exit 1\n"})

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert [row.split()[-1] for row in record.read_text().splitlines()[3:]] == ["-", "-"]


@_needs_vmstat
def test_record_notes_a_vmstat_that_stopped(tmp_path: Path) -> None:
    """The reason goes into the record, where the summary prints it, and the
    recorder exits non-zero."""
    vmstat = 'if [ "$1" = "-V" ]; then echo "vmstat stub"; exit 0; fi\nexit 1\n'

    proc, record = _record_with(tmp_path, {"docker": "exit 1\n", "vmstat": vmstat})

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert (
        record.read_text().splitlines()[-1]
        == "# error: vmstat stopped early, so the record ends here"
    )


@pytest.mark.parametrize(
    "args",
    [
        (),
        ("record",),
        ("record", "out.txt", "--interval", "0"),
        ("record", "out.txt", "--count", "many"),
        ("record", "out.txt", "--since", "1"),
        ("summary",),
        ("summary", "a.txt", "b.txt"),
        ("graph", "out.txt"),
    ],
    ids=[
        "nothing",
        "no-file",
        "zero-interval",
        "word-count",
        "unknown-option",
        "summary-no-file",
        "summary-two-files",
        "unknown-command",
    ],
)
def test_bad_arguments_are_refused(tmp_path: Path, args: tuple[str, ...]) -> None:
    proc = _run(*args)

    assert proc.returncode == 2, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert "usage: scripts/host-load.sh" in proc.stderr


def _step_limit_problems(text: str) -> list[str]:
    """What keeps ``text`` from limiting BuildKit's parallel build steps; nothing when it does."""
    try:
        config = tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        return [f"not TOML: {exc}"]
    oci = config.get("worker", {}).get("oci", {})
    if "max-parallelism" not in oci:
        return [f"[worker.oci] has no max-parallelism; its keys are {sorted(oci)}"]
    limit = oci["max-parallelism"]
    if isinstance(limit, bool) or not isinstance(limit, int) or limit < 1:
        return [f"max-parallelism must be a whole number of at least 1, not {limit!r}"]
    return []


def test_the_ci_builder_limits_its_build_steps() -> None:
    assert _step_limit_problems(BUILDKIT_CONFIG.read_text()) == []


@pytest.mark.parametrize(
    ("old", "new"),
    [
        ("max-parallelism", "max-paralelism"),
        ("[worker.oci]", "[worker.containerd]"),
        ("max-parallelism = 2", "max-parallelism = 0"),
        ("max-parallelism = 2", 'max-parallelism = "2"'),
        ("max-parallelism = 2", "max-parallelism = true"),
        ("max-parallelism = 2", "max-parallelism ="),
    ],
    ids=["misspelled-key", "other-worker", "zero", "string", "boolean", "not-toml"],
)
def test_the_step_limit_check_refuses_a_limit_buildkit_would_not_apply(old: str, new: str) -> None:
    """Each of these leaves the CI build with no limit and no error. The misspelled
    key is the one BuildKit was seen to ignore without a word."""
    text = BUILDKIT_CONFIG.read_text()
    assert text.count(old) == 1, f"{old!r} must appear once in {BUILDKIT_CONFIG}"

    assert _step_limit_problems(text.replace(old, new)) != []
