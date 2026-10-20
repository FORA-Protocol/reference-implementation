"""Publisher sites for the e2e suite's per-test publishers.

A publisher journey needs a domain of the publisher's own: the console reads its
home page to tell which CDN serves it, the Exchange reads the discovery document
published there before it activates a listing and before it accepts a catalog
push, and the catalog importer walks the sitemap published there. A fixed origin
cannot do this for a test that must pass twice on one stack, because a domain can
be registered only once. So each test claims a fresh domain, and this server is
the origin for every one of them at once:

* the stack's wildcard-DNS sidecar answers every name under ``SITE_ZONE`` with
  this container's address, so ``<anything>.<SITE_ZONE>`` reaches it on port 80;
* ``PUT /sites/<host>`` registers, or replaces, the files one site serves. The
  e2e runner sends it over the compose network. The body is a JSON object
  ``{"files": {"<path>": {"type": "<media type>", "body": "<text>"}}}``; the
  runner builds every file, including the discovery document the console handed
  the publisher, and this server stores and serves what it is given. A file may
  also carry ``"headers": {"<name>": "<value>"}``, served with it as given: a
  site's key directory carries the response signature the Web Bot Auth profile
  requires (``Content-Digest``, ``Signature-Input``, ``Signature``), which the
  runner made with the SDK for the site's host. An optional
  ``"package": {"guards": ["<path prefix>"], "exchange_origin": "<origin>"}``
  member puts the publisher's edge package in front of those paths; see below;
* ``GET <path>`` answers with the file registered at that path for the
  request's ``Host``, and 404 when the host or the path is not registered.

Every answer carries ``Cache-Control: no-store, max-age=0``. The Exchange reads
a publisher's discovery document through a cache that keeps it for an hour
unless the origin asks for less (``internal/forawellknown/cache.go``), and a
catalog push reads it through that cache. A zero max-age is what lets a test
that changes a site's document see the change at the next push rather than an
hour later.

Every answer for a registered host carries a ``cf-ray`` header. That is the
response header the console's CDN detection reads as Cloudflare
(``internal/cdn/classify.go``), the integration the console's wizard supports,
so a registered site reads as a site behind Cloudflare. No edge runs in front of
these sites: the journey's purchase settles at the Exchange, and nothing here
verifies a signed delivery address.

The console verifies a saved domain only once the publisher's edge package
answers on the site, and it asks the way an AI agent would: a request under the
first licensed path, as ``GPTBot``. A site registered with a ``package`` answers
that request the way the package does (``denyBot`` in ``src/edge/src/app.ts``):
403, an ``X-Content-Rules`` header naming the site's discovery document, an
``X-FORA-Exchange`` header naming the exchange it sells through, and the
package's ``{"error", "reason": "ai_bot"}`` body. Only the AI agents the
package's deny list names are refused (``src/edge/src/bot-classification.ts``);
its generic bot, crawler and spider patterns are left out, so the stack's own
readers of these sites are served as before. Every other request is answered
from the registered files.

The registry lives in this process's memory, as the agent-directory service's
does: nothing is shared through a database or a volume, and a restart forgets
every site. A site is served only for a host inside ``SITE_ZONE``, so this server
never answers for a name the wildcard does not point here.
"""

from __future__ import annotations

import json
import os
import re
import threading
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

SITE_ZONE = os.environ["SITE_ZONE"]
SITES_PREFIX = "/sites/"
# The header and value every registered site answers with; see the module text.
CDN_HEADER = ("cf-ray", "e2e0000000000000-FRA")
# One DNS label: what a test may put in front of the zone.
_LABEL = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
# A site is a discovery document, a sitemap and a few pages.
_MAX_BODY = 512 * 1024
# Where a site's discovery document is served, which a package refusal names.
_WELL_KNOWN_PATH = "/.well-known/fora.json"
# The AI agents the edge package's deny list names, by the token in their
# User-Agent (src/edge/src/bot-classification.ts).
_AI_AGENT = re.compile(
    r"GPTBot|ClaudeBot|anthropic-ai|OAI-SearchBot|CCBot|PerplexityBot|Bytespider"
    r"|Google-Extended|Meta-ExternalAgent|Claude-User|ChatGPT-User|Perplexity-User"
    r"|Meta-ExternalFetcher",
    re.IGNORECASE,
)

#: A registered file: the media type, the body and the extra headers it is served with.
_File = tuple[str, bytes, dict[str, str]]


@dataclass(frozen=True)
class Package:
    """The publisher's edge package in front of a site: the path prefixes it
    guards and the exchange it sends refused agents to."""

    paths: tuple[str, ...]
    exchange: str


@dataclass(frozen=True)
class Site:
    files: dict[str, _File]
    package: Package | None


_SITES: dict[str, Site] = {}
_LOCK = threading.Lock()


def _site_host(value: str) -> str | None:
    """``value`` lower-cased if it is exactly one label inside ``SITE_ZONE``, else None.

    A ``Host`` header can carry a port; the port is dropped first.
    """
    host = value.split(":", 1)[0].strip().lower()
    suffix = f".{SITE_ZONE}"
    if not host.endswith(suffix):
        return None
    return host if _LABEL.match(host[: -len(suffix)]) else None


def _files_of(document: Any) -> dict[str, _File] | None:
    """The ``files`` member of a registration as ``{path: (media type, bytes, headers)}``, or None."""
    if not isinstance(document, dict) or not isinstance(document.get("files"), dict):
        return None
    files: dict[str, _File] = {}
    for path, file in document["files"].items():
        if not (isinstance(path, str) and path.startswith("/") and isinstance(file, dict)):
            return None
        media_type, body, headers = file.get("type"), file.get("body"), file.get("headers", {})
        if not (
            isinstance(media_type, str) and isinstance(body, str) and isinstance(headers, dict)
        ):
            return None
        if not all(isinstance(k, str) and isinstance(v, str) for k, v in headers.items()):
            return None
        files[path] = (media_type, body.encode(), headers)
    return files


def _package_of(document: dict[str, Any]) -> Package | None:
    """The ``package`` member of a registration, or None when it has none.

    Raises ValueError when the member is there and is not a package.
    """
    package = document.get("package")
    if package is None:
        return None
    paths = package.get("guards") if isinstance(package, dict) else None
    exchange = package.get("exchange_origin") if isinstance(package, dict) else None
    if not (isinstance(paths, list) and paths and isinstance(exchange, str) and exchange):
        raise ValueError("a package names the paths it guards and its exchange")
    if not all(isinstance(p, str) and p.startswith("/") for p in paths):
        raise ValueError("a package guards absolute paths")
    return Package(paths=tuple(paths), exchange=exchange)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path == "/healthz":
            self._send(200, b"ok\n", "text/plain")
            return
        host = _site_host(self.headers.get("Host", ""))
        with _LOCK:
            site = _SITES.get(host) if host else None
        if site is None:
            self._send(404, b"no site registered for this host\n", "text/plain")
            return
        path = self.path.split("?", 1)[0]
        package = site.package
        if (
            package is not None
            and any(path.startswith(guarded) for guarded in package.paths)
            and _AI_AGENT.search(self.headers.get("User-Agent", ""))
        ):
            self._deny_agent(host, package)
            return
        if path not in site.files:
            self._send(404, b"not found\n", "text/plain", cdn=True)
            return
        media_type, body, headers = site.files[path]
        self._send(200, body, media_type, cdn=True, extra=tuple(headers.items()))

    def do_PUT(self) -> None:
        if not self.path.startswith(SITES_PREFIX):
            self._send(404, b"not found\n", "text/plain")
            return
        host = _site_host(self.path[len(SITES_PREFIX) :])
        if host is None:
            message = f"the host must be one label under {SITE_ZONE}\n".encode()
            self._send(400, message, "text/plain")
            return
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= _MAX_BODY:
            self._send(400, b"the body must be a site registration\n", "text/plain")
            return
        try:
            document = json.loads(self.rfile.read(length))
            files = _files_of(document)
            package = _package_of(document) if files is not None else None
        except ValueError:
            files = None
        if files is None:
            message = (
                b'the body must be {"files": {"/path": {"type": ..., "body": ...}}}, with '
                b'an optional "package": {"guards": ["/prefix"], "exchange_origin": "origin"}\n'
            )
            self._send(400, message, "text/plain")
            return
        with _LOCK:
            _SITES[host] = Site(files=files, package=package)
        self._send(204, b"", "text/plain")

    def _deny_agent(self, host: str, package: Package) -> None:
        """Refuse an AI agent the way the edge package does."""
        payload = json.dumps(
            {"error": "Access denied. Negotiate access via the exchange.", "reason": "ai_bot"}
        ).encode()
        self._send(
            403,
            payload,
            "application/json",
            cdn=True,
            extra=(
                ("X-Content-Rules", f"http://{host}{_WELL_KNOWN_PATH}"),
                ("X-FORA-Exchange", package.exchange),
            ),
        )

    def _send(
        self,
        status: int,
        payload: bytes,
        media_type: str,
        *,
        cdn: bool = False,
        extra: tuple[tuple[str, str], ...] = (),
    ) -> None:
        self.send_response(status)
        self.send_header("Content-Type", media_type)
        self.send_header("Cache-Control", "no-store, max-age=0")
        self.send_header("Content-Length", str(len(payload)))
        if cdn:
            self.send_header(*CDN_HEADER)
        for name, value in extra:
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(payload)


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 80), Handler).serve_forever()
