"""The stack's protocol documents, read through the SDK's document readers.

The SDK reads every document the protocol publishes over HTTPS:
``fora_sdk.resolvers.read_manifest``, ``read_wba_directory``,
``read_revocation_list`` and ``read_license_document``. Each fetches through the
SDK's guarded client, refuses a document served under the wrong media type, applies
the published strict JSON Schema and the cross-field rules (``fora_sdk.check_strict``),
and returns the generated model in a ``Document``, or raises. That is the one place a
document is fetched, checked and parsed; this module adds none of it.

What is left here is addressing and one parse:

* :func:`manifest_at` and :func:`wba_directory_at` take a stack origin such as
  ``http://edge:80``, because the SDK reads the manifest by host and scheme;
* :func:`parse_document` reads a document handed over as bytes rather than served,
  such as the manifest file the console generates or a Lambda@Edge invocation's
  body. The SDK readers fetch, so these bytes go through the SDK's strict check and
  then the generated model.

A revocation list and a license document are addressed by URL, so a test calls the
SDK reader directly.

One property of a served document is not in the SDK's ``Document``: how long a cache
may keep it. :func:`served_freshness` reads the ``Cache-Control`` a document is served
with, which is what a revocation list's freshness lifetime is stated in.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from typing import TypeVar
from urllib.parse import urlsplit

import httpx
from fora_sdk import check_strict
from fora_sdk.resolvers import Document, guarded_client, read_manifest, read_wba_directory
from pydantic import BaseModel
from wire.models import WBAFile, WellKnownManifest

M = TypeVar("M", bound=BaseModel)


def manifest_at(origin: str) -> Document[WellKnownManifest]:
    """The manifest ``origin`` serves, read by the SDK's ``read_manifest``."""
    host, scheme = _host_and_scheme(origin)
    return read_manifest(host, scheme=scheme)


def wba_directory_at(origin: str) -> Document[WBAFile]:
    """The Web Bot Auth directory ``origin`` serves, read by the SDK's ``read_wba_directory``."""
    host, scheme = _host_and_scheme(origin)
    return read_wba_directory(host, scheme=scheme)


def parse_document(model: type[M], body: str | bytes) -> M:
    """``body`` as a ``model`` message, refused unless the SDK's strict check accepts it."""
    check_strict(f"fora.v1.{model.__name__}", json.loads(body))
    return model.model_validate_json(body)


_FRESHNESS_TIMEOUT_SEC = 10.0


@dataclass(frozen=True)
class Freshness:
    """The ``Cache-Control`` a document was served with, read as a freshness lifetime.

    ``lifetime_s`` is the longest any cache may serve the document without asking
    the server again. It is 0 for ``no-store`` or ``no-cache``. Otherwise it is the
    larger of ``max-age`` and ``s-maxage``, because ``s-maxage`` replaces
    ``max-age`` for the shared caches between a consumer and the server (RFC 9111
    Section 5.2.2.10). It is ``None`` when the header states no lifetime at all,
    which leaves a cache free to choose a heuristic one (RFC 9111 Section 4.2.2).
    """

    cache_control: str | None

    @property
    def lifetime_s(self) -> int | None:
        """The longest seconds any cache may keep the document, or None when none is stated."""
        directives: dict[str, str] = {}
        for part in (self.cache_control or "").split(","):
            name, _, value = part.strip().partition("=")
            if name:
                directives[name.lower()] = value.strip().strip('"')
        if "no-store" in directives or "no-cache" in directives:
            return 0
        stated = [int(v) for n in ("max-age", "s-maxage") if (v := directives.get(n, "")).isdigit()]
        return max(stated) if stated else None


def served_freshness(url: str) -> Freshness:
    """The freshness lifetime the document at ``url`` is served with.

    Dialled through the SDK's guarded client, as the SDK's own readers are. The
    document must be served (200), or there is no lifetime to read.
    """
    with guarded_client(timeout=_FRESHNESS_TIMEOUT_SEC) as client:
        response = client.get(url)
    assert response.status_code == httpx.codes.OK, (
        f"{url} answered {response.status_code}, not the document: {response.text[:200]}"
    )
    return Freshness(cache_control=response.headers.get("cache-control"))


def _host_and_scheme(origin: str) -> tuple[str, str]:
    parts = urlsplit(origin)
    assert parts.scheme and parts.netloc and parts.path in ("", "/"), (
        f"{origin!r} is not an origin: scheme://host[:port]"
    )
    return parts.netloc, parts.scheme
