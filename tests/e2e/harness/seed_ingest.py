"""Ingesting the e2e feeds through the production catalog-ingest binary.

Split out of ``seed`` so the seed module stays under the test-file length cap.
The dependency is one-directional: ``seed`` calls ``ingest_all_feeds`` and hands
it the publisher table; nothing here imports ``seed``.
"""

from __future__ import annotations

import os
from pathlib import Path

from .exchanges import seat_tenant_id
from .ingest_runner import ingest_succeeded, run_ingest


def _ingest_binary_path() -> str:
    """Locate the fora-ingest binary.

    In the runner image the Go builder stage installs it at
    ``/usr/local/bin/fora-ingest``; a ``FORA_INGEST_BIN`` override
    lets a host run point at a locally-built or ``go run`` shim.
    """
    return os.environ.get("FORA_INGEST_BIN", "/usr/local/bin/fora-ingest")


def _ingest_feed(
    *,
    exchange_url: str,
    exchange: str,
    tenant_id: str,
    feed_abspath: Path,
    binary: str,
    key_path: Path,
) -> None:
    """Shell out to the production fora-ingest binary for one feed.

    The binary signs (RFC 9421) and sends ``PushResources`` RPCs — one per
    submission of at most the wire bound; every e2e feed fits in one — the only
    catalog write. ``key_path`` is the publisher's SELF-PUBLISH key (kid ==
    domain); the binary's caller_id is that kid, so Gate-1 self-signup resolves
    it from the publisher's edge well-known and Gate-2 passes caller==domain — no
    DB pre-seed. A non-zero exit fails the seed: the Exchange stores or refuses
    a submission whole and the binary exits non-zero on the first submission
    that did not store — refused, or never answered — so the exit code is the
    whole verdict.

    ``exchange`` overrides the recipient the binary would otherwise take from
    ``exchange_url``'s host. The harness needs the override because a host run
    reaches the exchange through a mapped 127.0.0.1 port, which names no
    exchange at all; a deployment dialling the exchange's own origin needs
    nothing here.
    """
    proc = run_ingest(
        argv_prefix=[binary],
        exchange_url=exchange_url,
        exchange=exchange,
        tenant_id=tenant_id,
        key_path=key_path,
        feed=feed_abspath,
    )
    if not ingest_succeeded(proc):
        msg = (
            f"fora-ingest failed for {feed_abspath} (tenant={tenant_id}): "
            f"rc={proc.returncode}\nSTDERR:\n{proc.stderr}\nSTDOUT:\n{proc.stdout}"
        )
        raise RuntimeError(msg)


def ingest_all_feeds(
    url_by_exchange: dict[str, str],
    publishers: tuple[tuple[str, str, str, Path, Path], ...],
) -> None:
    """Ingest each e2e feed into the catalog DB of the exchange that sells it.

    ``url_by_exchange`` maps each exchange identity to the URL this run reaches
    it at; the identity itself is who the push is addressed to. The music
    catalog lands in exchange-b's DB and the sfx catalog in exchange-c's DB —
    the catalog isolation the separate-DB topology guarantees.

    The tenant id carried on the wire is the SEAT's, not the publisher's. The
    Exchange resolves its own seat from EXCHANGE_DOMAIN and refuses a push that
    names a different one.
    """
    binary = _ingest_binary_path()
    for _domain, exchange, _scheme, feed_abspath, key_path in publishers:
        _ingest_feed(
            exchange_url=url_by_exchange[exchange],
            exchange=exchange,
            tenant_id=seat_tenant_id(exchange),
            feed_abspath=feed_abspath,
            binary=binary,
            key_path=key_path,
        )
