"""Key directories for the e2e suite's per-test agents.

ADR-028 D7 gives every e2e test its own agent identity. A verifier learns an
agent's key in one way only: it fetches the Web Bot Auth key directory at the
host the request's ``Signature-Agent`` names. So each per-test agent needs a
host the Broker and every Exchange can resolve, and that host has to serve the
agent's directory.

This server is that host, for every per-test agent at once:

* the stack's wildcard-DNS sidecar answers every name under ``AGENT_ZONE`` with
  this container's address, so ``<anything>.<AGENT_ZONE>`` reaches it on port 80;
* ``GET /.well-known/http-message-signatures-directory`` answers with the
  directory registered for the request's ``Host``, and 404 for a host nobody
  registered;
* ``GET /.well-known/fora-key-revocations.json`` answers with the revocation
  list registered for the request's ``Host``, and 404 for a host with none;
* ``PUT /hosts/<host>`` registers a host's directory, ``PUT
  /hosts/<host>/revocations`` its revocation list. A directory is registered
  with the response signature the Web Bot Auth profile requires (WG-00 Section
  5.5): the ``Content-Digest``, ``Signature-Input`` and ``Signature`` headers of
  the PUT, which the runner made with the SDK's ``sign_directory_response`` for
  the host. They are served with the document exactly as given. A directory PUT
  without them is stored and served unsigned, for the tests that check a
  verifier refuses an unsigned directory; The e2e runner sends both over
  the compose network. The runner builds each document with the SDK's identity
  helpers and the generated models, so this server stores and serves the JSON it
  is given and builds no key material of its own;
* ``PUT /hosts/<host>/redirects`` makes the host answer one of those two paths
  with a ``302``, for the tests that check a verifier follows no redirect when it
  fetches a key directory or a revocation list. The body maps a path to the
  ``Location`` it answers with. A location must name a host inside
  ``AGENT_ZONE``, so this server never redirects a verifier anywhere else.

The registry lives in this process's memory. Nothing is shared through a
database or a volume, so registrations from concurrent tests cannot race on a
file. A restart forgets every agent, which is fine: the agents belong to the
tests of one run.

A document is served only for a host inside ``AGENT_ZONE``. That keeps this
server from answering for a name the wildcard does not point here, such as the
compose service name it is registered through.
"""

from __future__ import annotations

import json
import os
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any
from urllib.parse import urlsplit

AGENT_ZONE = os.environ["AGENT_ZONE"]
DIRECTORY_PATH = "/.well-known/http-message-signatures-directory"
REVOCATION_PATH = "/.well-known/fora-key-revocations.json"
HOSTS_PREFIX = "/hosts/"
# The media type the Web Bot Auth profile serves a key directory with.
DIRECTORY_MEDIA_TYPE = "application/http-message-signatures-directory+json"
# The response-signature headers a directory is registered and served with.
_SIGNATURE_HEADERS = ("Content-Digest", "Signature-Input", "Signature")
REVOCATION_MEDIA_TYPE = "application/json"
# One DNS label: what a test may put in front of the zone.
_LABEL = re.compile(r"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$")
# A directory document is a few hundred bytes. Anything far larger is a mistake.
_MAX_BODY = 64 * 1024

_DIRECTORIES: dict[str, bytes] = {}
_DIRECTORY_SIGNATURES: dict[str, dict[str, str]] = {}
_REVOCATIONS: dict[str, bytes] = {}
_REDIRECTS: dict[tuple[str, str], str] = {}
_LOCK = threading.Lock()


def _agent_host(value: str) -> str | None:
    """``value`` lower-cased if it is exactly one label inside ``AGENT_ZONE``, else None.

    A ``Host`` header can carry a port; the port is dropped first.
    """
    host = value.split(":", 1)[0].strip().lower()
    suffix = f".{AGENT_ZONE}"
    if not host.endswith(suffix):
        return None
    return host if _LABEL.match(host[: -len(suffix)]) else None


def _zone_location(value: Any) -> str | None:
    """``value`` if it is an http(s) URL on a host inside ``AGENT_ZONE``, else None."""
    if not isinstance(value, str):
        return None
    parts = urlsplit(value)
    if parts.scheme not in {"http", "https"} or parts.username or parts.password:
        return None
    return value if _agent_host(parts.netloc) else None


def _redirects(document: Any) -> dict[str, str] | None:
    """The path-to-location map of a redirects body, or None when it is not one."""
    if not isinstance(document, dict) or not document:
        return None
    redirects: dict[str, str] = {}
    for path, location in document.items():
        target = _zone_location(location)
        if path not in {DIRECTORY_PATH, REVOCATION_PATH} or target is None:
            return None
        redirects[path] = target
    return redirects


class Handler(BaseHTTPRequestHandler):
    def do_GET(self) -> None:
        if self.path == "/healthz":
            self._send(200, b"ok\n", "text/plain")
            return
        if self.path not in {DIRECTORY_PATH, REVOCATION_PATH}:
            self._send(404, b"not found\n", "text/plain")
            return
        host = _agent_host(self.headers.get("Host", ""))
        if host is None:
            self._send(404, b"no document registered for this host\n", "text/plain")
            return
        with _LOCK:
            location = _REDIRECTS.get((host, self.path))
            store = _DIRECTORIES if self.path == DIRECTORY_PATH else _REVOCATIONS
            document = store.get(host)
            signature = _DIRECTORY_SIGNATURES.get(host, {}) if self.path == DIRECTORY_PATH else {}
        if location is not None:
            self._redirect(location)
            return
        if document is None:
            self._send(404, b"no document registered for this host\n", "text/plain")
            return
        media_type = DIRECTORY_MEDIA_TYPE if self.path == DIRECTORY_PATH else REVOCATION_MEDIA_TYPE
        self._send(200, document, media_type, signature)

    def do_PUT(self) -> None:
        if not self.path.startswith(HOSTS_PREFIX):
            self._send(404, b"not found\n", "text/plain")
            return
        name, _, document_kind = self.path[len(HOSTS_PREFIX) :].partition("/")
        host = _agent_host(name)
        if host is None:
            self._send(
                400, f"the host must be one label under {AGENT_ZONE}\n".encode(), "text/plain"
            )
            return
        if document_kind not in {"", "revocations", "redirects"}:
            self._send(404, b"not found\n", "text/plain")
            return
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= _MAX_BODY:
            self._send(400, b"the body must be a JSON document\n", "text/plain")
            return
        body = self.rfile.read(length)
        try:
            document: Any = json.loads(body)
        except ValueError:
            document = None
        error = self._store(host, document_kind, body, document)
        if error:
            self._send(400, f"{error}\n".encode(), "text/plain")
            return
        self._send(204, b"", "text/plain")

    def _store(self, host: str, document_kind: str, body: bytes, document: Any) -> str:
        """Record one registration; the reason it was refused, or "" when stored."""
        if document_kind == "redirects":
            redirects = _redirects(document)
            if redirects is None:
                return (
                    "the body must map the directory or revocation path to an http(s) "
                    f"URL under {AGENT_ZONE}"
                )
            with _LOCK:
                for path, location in redirects.items():
                    _REDIRECTS[(host, path)] = location
            return ""
        if document_kind == "revocations":
            if not isinstance(document, dict) or not isinstance(document.get("as_of"), str):
                return "the body must be a JSON object with an as_of"
            with _LOCK:
                _REVOCATIONS[host] = body
            return ""
        if not isinstance(document, dict) or not isinstance(document.get("keys"), list):
            return "the body must be a JSON object with a keys array"
        signature = {
            name: self.headers[name] for name in _SIGNATURE_HEADERS if self.headers.get(name)
        }
        if signature and len(signature) != len(_SIGNATURE_HEADERS):
            return f"a signed directory carries all of {', '.join(_SIGNATURE_HEADERS)}"
        with _LOCK:
            _DIRECTORIES[host] = body
            _DIRECTORY_SIGNATURES[host] = signature
        return ""

    def _redirect(self, location: str) -> None:
        self.send_response(302)
        self.send_header("Location", location)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def _send(
        self, status: int, payload: bytes, media_type: str, extra: dict[str, str] | None = None
    ) -> None:
        self.send_response(status)
        self.send_header("Content-Type", media_type)
        for name, value in (extra or {}).items():
            self.send_header(name, value)
        self.send_header("Cache-Control", "no-store")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 80), Handler).serve_forever()
