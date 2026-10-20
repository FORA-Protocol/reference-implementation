"""The Broker's own revocation list, written by the suite, and the Broker's own key.

The e2e Broker serves its KeyRevocationList from a file (``BROKER_REVOCATION_FILE``)
it re-reads whenever the file's mtime changes, so writing the file is the whole
publish step: no restart, no admin call. The stack's ``revocations`` volume is
mounted into the Broker at ``/revocations`` AND into the runner at
``/runner/harness/revocations``. A host run brings the stack up with
``docker-compose.e2e.ports.yml``, which binds ``harness/revocations/`` into the
Broker instead. Drop the runner's mount and the write still succeeds, into the
runner image's own layer, where the Broker never sees it; that is why every
publish here waits until the Broker serves what was written.

A consumer keeps the snapshot with the newest ``as_of`` and ignores an older or
equal one, so every publish carries an ``as_of`` strictly newer than the one the
Broker serves. Withdrawing a revocation is a newer, empty list. Deleting the file
would not do: the Broker would serve its epoch-dated empty baseline, which every
consumer discards as a rollback, and the revocation would stay in force for the
rest of the run and the next one.

Infrastructure for the revocation obligations; a test reaches the file only
through :func:`broker_list_naming`.
"""

from __future__ import annotations

import json
import os
import tempfile
import time
from collections.abc import Iterator, Sequence
from contextlib import contextmanager
from datetime import UTC, datetime
from pathlib import Path

from fora_sdk.b64 import b64url_decode
from fora_sdk.resolvers import read_revocation_list
from fora_sdk.thumbprint import thumbprint

from .constants import REVOCATION_PATH
from .poll import poll_until
from .protocol_documents import wba_directory_at

_REVOCATION_FILE = Path(__file__).parent / "revocations" / "revocations.json"

#: How long the Broker may take to serve a list after the file changes.
_SERVE_DEADLINE_S = 15.0


def _rfc3339(epoch_seconds: int) -> str:
    """``epoch_seconds`` as an RFC 3339 'Z' timestamp the revocation schema accepts."""
    stamp = datetime.fromtimestamp(epoch_seconds, tz=UTC)
    return stamp.replace(microsecond=0).isoformat().replace("+00:00", "Z")


def served_as_of(broker_url: str) -> int:
    """Epoch seconds of the ``as_of`` the Broker currently serves."""
    as_of = read_revocation_list(f"{broker_url}{REVOCATION_PATH}").message.as_of
    assert as_of is not None, "the Broker's revocation list carries no as_of"
    return int(as_of.timestamp())


def served_thumbprints(broker_url: str) -> list[str]:
    """The thumbprints the Broker's revocation list names right now."""
    return list(read_revocation_list(f"{broker_url}{REVOCATION_PATH}").message.revoked or [])


def broker_thumbprints(broker_url: str) -> list[str]:
    """The thumbprints of every key the Broker's directory publishes: the Broker's own keys."""
    keys = wba_directory_at(broker_url).message.keys or []
    assert keys, "the Broker's directory publishes no key"
    out: list[str] = []
    for key in keys:
        assert isinstance(key.x, str), f"a Broker key carries no x: {key}"
        out.append(thumbprint(b64url_decode(key.x)))
    return out


def _publish(broker_url: str, thumbprints: Sequence[str]) -> int:
    """Write a list naming ``thumbprints`` with a newer ``as_of``; wait until it is served.

    The file is published by rename. The Broker stats it on every request and
    reads it again when its mtime changes
    (src/broker/internal/transport/revocation_source.go), so a write in place
    could be read half done; a rename swaps in the whole file at once. A rename
    also needs only a writable directory, where a chmod needs the caller to own
    the file, so publishing still works when an earlier run, as another user,
    left the file behind. ``mkstemp`` creates the file readable by its owner
    only, and the Broker runs as another user, so the file is opened up to
    0o644 before the rename. The shared ``write_text_atomic`` helper in
    scripts/lib is out of reach here: the runner image does not copy scripts/lib.
    """
    as_of = max(int(time.time()), served_as_of(broker_url) + 1)
    body = json.dumps({"as_of": _rfc3339(as_of), "revoked": list(thumbprints)})
    fd, tmp = tempfile.mkstemp(
        dir=_REVOCATION_FILE.parent, prefix=f".{_REVOCATION_FILE.name}.", suffix=".tmp"
    )
    try:
        with os.fdopen(fd, "w") as handle:
            handle.write(body)
        os.chmod(tmp, 0o644)
        os.replace(tmp, _REVOCATION_FILE)
    except BaseException:
        # Interrupts included: the directory's ignore rule names only the
        # published file, so a stray temporary file would show up in git.
        Path(tmp).unlink(missing_ok=True)
        raise
    poll_until(
        lambda: served_as_of(broker_url) >= as_of,
        _SERVE_DEADLINE_S,
        failure=lambda: (
            f"the Broker never served the list written at as_of {as_of} within "
            f"{_SERVE_DEADLINE_S:.0f}s; it serves as_of {served_as_of(broker_url)}. The "
            f"broker's /revocations and the runner's harness/revocations must mount the "
            f"same volume."
        ),
        interval_s=0.5,
    )
    return as_of


@contextmanager
def broker_list_naming(broker_url: str, thumbprints: Sequence[str]) -> Iterator[None]:
    """The Broker's revocation list names ``thumbprints`` for the length of the block.

    On entry an empty list is published first, so a revocation a killed run left
    behind cannot decide what this block sees. On exit, whatever happened inside, a
    newer empty list withdraws the revocation. A caller that revoked something every
    later test depends on, such as the Broker's own key, waits for its consumers to
    accept the withdrawal before it returns.
    """
    _publish(broker_url, [])
    _publish(broker_url, thumbprints)
    try:
        yield
    finally:
        _publish(broker_url, [])
