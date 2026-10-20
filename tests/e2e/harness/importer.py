"""Running the automatic catalog importer once, and reading what it did for one site.

The importer (``src/exchange/cmd/fora-import``) is a job: production runs it from
a timer, never as a daemon, and each run imports every listing the seat has made
eligible (ACTIVE, with a sitemap). The stack defines it as the profiled
``importer`` service, shaped like the production template's, and
:func:`run_import` runs that definition once through the docker socket.

One run covers every eligible listing on the seat, including the listings other
tests and earlier passes registered, and its exit status is non-zero when ANY of
them did not complete. So a test never reads the exit status. It reads the one
structured line the job logs for its own listing (``importer.listing``, written
by ``src/exchange/internal/importer/job.go``), which carries the outcome, the six
counts and the reason. That line is the job's operational record, the same one an
operator reads; the listing's stored outcome has no read surface a test may use.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import Any

from ._compose import run_job

#: The job service in docker-compose.e2e.yml, and the service whose image it runs.
IMPORTER_SERVICE = "importer"
IMPORTER_IMAGE_OF = "exchange"

#: The outcome the job records for a listing whose every page it did not skip was
#: accepted (``repo.ImportOutcomeComplete``).
COMPLETE = "complete"

#: The importer bounds one listing at five minutes (IMPORT_LISTING_TIMEOUT); a
#: run walks every eligible listing on the seat one after another.
_RUN_TIMEOUT_SEC = 600.0

#: The message of the per-listing log line, recorded or not.
_LISTING_LINES = frozenset({"importer.listing", "importer.listing.unrecorded"})


@dataclass(frozen=True)
class ListingOutcome:
    """What one run of the importer did for one listing, as its log line records it.

    The line also counts the entries left unconfirmed or unsent; no test reads those
    two counts, so they are not carried here.
    """

    domain: str
    publisher: str
    outcome: str
    entries: int
    accepted: int
    refused: int
    skipped: int
    reason: str


def _listing_lines(output: str) -> list[dict[str, Any]]:
    lines: list[dict[str, Any]] = []
    for raw in output.splitlines():
        try:
            record = json.loads(raw)
        except ValueError:
            continue
        if isinstance(record, dict) and record.get("msg") in _LISTING_LINES:
            lines.append(record)
    return lines


def run_import(domain: str) -> ListingOutcome:
    """Run the importer once; return the outcome it logged for ``domain``'s listing.

    A run that logs no line for ``domain`` fails here with the run's output: the
    listing was not eligible, or the run stopped before it reached it.
    """
    exit_code, output = run_job(
        IMPORTER_SERVICE, image_of=IMPORTER_IMAGE_OF, timeout_s=_RUN_TIMEOUT_SEC
    )
    mine = [line for line in _listing_lines(output) if line.get("domain") == domain]
    assert len(mine) == 1, (
        f"the importer logged {len(mine)} listing lines for {domain} (exit {exit_code}); "
        f"its output:\n{output[-4000:]}"
    )
    line = mine[0]
    return ListingOutcome(
        domain=domain,
        publisher=str(line.get("publisher", "")),
        outcome=str(line.get("outcome", "")),
        entries=int(line.get("entries", 0)),
        accepted=int(line.get("accepted", 0)),
        refused=int(line.get("refused", 0)),
        skipped=int(line.get("skipped", 0)),
        reason=str(line.get("reason", "")),
    )
