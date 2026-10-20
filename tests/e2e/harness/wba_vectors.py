"""The WG-00 Appendix E test vectors, as the two oracle test suites read them.

The vectors live in ``testdata/wba-httpsig-protocol-00-vectors.json`` with their
source cited in the file. Both oracles are validated against the same file, the
Python one by ``test_wba_oracle.py`` and the TypeScript one by
``test_wba_oracle_ts.py``, so this module is the one place that turns it into
requests and responses. It is also the one place that serves them from an
in-process key directory server (:func:`directory_server`), so a test module
never builds an HTTP client of its own.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from functools import cache
from typing import Any

import httpx

from ._compose import REPO_ROOT

WBA_VECTORS_PATH = REPO_ROOT / "testdata" / "wba-httpsig-protocol-00-vectors.json"

# The two Signature-Agent forms the request vectors cover, named here so a test
# can parametrize over them without reading the file. Nothing in this module
# reads the file at import time: a test module imports it at collection, and a
# collection-time read would abort the whole package's collection wherever
# testdata/ is absent (test_guards_harness_collection.py builds such a tree).
FORMS = ("dictionary", "legacy")

#: A directory response, as :func:`directory_response` builds it and the oracle's
#: directory check reads it.
Response = httpx.Response


@cache
def _load() -> dict[str, Any]:
    return dict(json.loads(WBA_VECTORS_PATH.read_text()))


def vectors() -> dict[str, Any]:
    """The whole vector file, parsed. Callers must not mutate it."""
    return _load()


def request_vector(form: str) -> dict[str, Any]:
    """The signed-request vector of ``form``: E.2.1 "dictionary" or E.2.2 "legacy"."""
    (vector,) = [v for v in _load()["requests"] if v["form"] == form]
    return dict(vector)


def private_jwk() -> dict[str, Any]:
    """RFC 9421 Appendix B.1.4's Ed25519 key, private half, as Appendix E uses it."""
    return dict(_load()["key"]["private_jwk"])


def public_jwk() -> dict[str, Any]:
    """The public half of :func:`private_jwk`."""
    return dict(_load()["key"]["public_jwk"])


def directory_response(
    *,
    status: int | None = None,
    body: str | None = None,
    headers: Mapping[str, str | None] | None = None,
) -> Response:
    """The E.2.3 signed directory response, with optional changes for negative tests.

    ``headers`` entries replace the vector's; a None value removes that header.
    The response carries the fetching request, which its ``"@authority";req``
    component is resolved against.
    """
    directory = _load()["directory"]
    merged: dict[str, str | None] = {**directory["response_headers"], **(headers or {})}
    request = httpx.Request("GET", directory["request_url"], headers=directory["request_headers"])
    return httpx.Response(
        directory["status"] if status is None else status,
        headers={k: v for k, v in merged.items() if v is not None},
        content=(directory["body"] if body is None else body).encode(),
        request=request,
    )


def directory_server(seen: list[httpx.Request]) -> httpx.Client:
    """An in-process key directory server: it serves E.2.3 and redirects ``/moved``.

    Every request it receives is appended to ``seen``, so a test can show which
    URL the oracle fetched, and that it fetched nothing when it must not. There is
    no network: the client's transport answers in process. That is the oracle's
    own HTTP boundary, not a FORA service.
    """
    vector = directory_response()

    def handle(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        if request.url.path == "/moved":
            return httpx.Response(302, headers={"Location": vector.request.url.path})
        return httpx.Response(200, headers=vector.headers, content=vector.content)

    return httpx.Client(transport=httpx.MockTransport(handle))
