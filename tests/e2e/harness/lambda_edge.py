"""Drive the Lambda@Edge worker through the AWS Lambda runtime emulator.

The emulator speaks the Lambda invoke protocol and nothing else: one POST route
that takes an event as JSON and returns whatever the function returned. So a
test does not "request a URL" from this runtime — it builds the CloudFront
viewer-request event CloudFront itself would deliver, invokes the function with
it, and reads the JSON that comes back.

That returned JSON has two shapes, and telling them apart is the whole point of
the Lambda@Edge entry:

* a **generated response** — a denial, or a well-known document — carries
  ``status``, ``headers`` and ``body``, and CloudFront returns it to the viewer
  without ever contacting the origin;
* a **pass-through** — the answer for an authorized read — is the incoming
  REQUEST object (``uri``, ``querystring``, ``headers``), which tells CloudFront
  to go and fetch the origin.

The translation between a URL and a CloudFront event stays here, in the test
harness, rather than in an HTTP front-end inside the container. A front-end
would have to decide which of the two shapes to turn back into an HTTP response,
and that decision is exactly the behavior these tests exist to prove.

:func:`invoke` returns either shape as one typed :class:`ViewerResult`, so a test
reads ``status``, ``header(...)`` or ``uri`` and never the invoke API's JSON.
The CloudFront header map is keyed by LOWERCASED header name, each entry a list
of ``{"key", "value"}`` pairs; :meth:`ViewerResult.header` reads it so tests do
not repeat the shape.

Delivery URLs for this runtime are minted here, not through Broker and Exchange.
CloudFront terminates TLS, so a Lambda@Edge function only ever sees an https
request, while the compose stack's Exchanges mint their delivery URLs on http.
The signature covers the scheme, so an Exchange-minted URL could only prove a
mismatch here. :func:`load_exchange_delivery_key` loads exchange-a's own
delivery-URL key and refuses to go on unless that Exchange publishes it, and
:func:`mint_delivery_url` signs with it through the SDK's signed-URL helper. The
proof of possession a bound URL needs is minted by the SDK's own delivery-proof
helper over the agent's signing transport (:func:`proof_of_possession`).
"""

from __future__ import annotations

import json
import os
import time
from collections.abc import Mapping
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any
from urllib.parse import urlsplit

import httpx
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.hazmat.primitives.serialization import load_pem_private_key
from fora_sdk.b64 import b64url_decode
from fora_sdk.client.content import DEFAULT_PROOF_WINDOW_SEC, proof_headers
from fora_sdk.signedurl import sign_ed25519_signed_url
from fora_sdk.thumbprint import thumbprint as ed25519_thumbprint
from fora_sdk.window import clock_window

from .poll import poll_until
from .protocol_documents import wba_directory_at
from .sdk_client import agent_signer

# The emulator's one route. The path is fixed by the Lambda runtime API and is
# the same for every function it hosts.
INVOKE_PATH = "/2015-03-31/functions/function/invocations"

# The publisher the Lambda@Edge deployment fronts. It is written into the
# package's index.mjs at build time (tests/e2e/lambda-edge/write-configs.mjs),
# so a delivery URL for this runtime names this host.
PUBLISHER = "lambda.demo.fora-protocol.org"

# An ordinary content path. Nothing is fetched from an origin on this
# deployment, because CloudFront would own that, so the path only has to look
# like a real article read.
ARTICLE_PATH = "/articles/on-the-examined-life"

# The header AWS sets on an invocation the function ended by throwing. Its
# presence turns an otherwise-ordinary 200 into a failed invocation.
_FUNCTION_ERROR_HEADER = "X-Amz-Function-Error"

# Emulators this process gave up waiting on, base URL -> what happened.
#
# The emulator serves ONE invocation at a time and holds it reserved until the
# function returns. A client-side timeout abandons that reservation without
# ending it, so the next POST is a second concurrent invocation — and the
# emulator answers that by aborting the in-flight request unanswered and
# exiting its own process. The container dies, and every later test fails on a
# name that no longer resolves instead of on the timeout that actually
# happened.
#
# So a timeout retires that emulator for the rest of the session. Module state
# rather than a fixture because the reservation is a property of the emulator
# process, not of any one test; keyed by base URL because the stack runs two
# emulator containers and only the one that timed out is in doubt. Nothing
# clears it: once the slot state is unknown it stays unknown.
_ABANDONED: dict[str, str] = {}


def viewer_request_event(
    url: str,
    *,
    method: str = "GET",
    headers: Mapping[str, str] | None = None,
) -> dict[str, Any]:
    """Build the CloudFront viewer-request event for ``url``.

    ``url`` is split the way CloudFront delivers a request: the authority
    becomes the Host header (which is how the function reconstructs the URL it
    verifies signatures against), the path becomes ``uri`` and the query becomes
    ``querystring`` — without the leading "?".
    """
    parts = urlsplit(url)
    cf_headers: dict[str, list[dict[str, str]]] = {
        "host": [{"key": "Host", "value": parts.netloc}],
    }
    for name, value in (headers or {}).items():
        cf_headers[name.lower()] = [{"key": name, "value": value}]
    return {
        "Records": [
            {
                "cf": {
                    "config": {
                        "distributionDomainName": "e2e.cloudfront.example",
                        "distributionId": "E2ELAMBDAEDGE",
                        "eventType": "viewer-request",
                        "requestId": "e2e-lambda-edge",
                    },
                    "request": {
                        "clientIp": "203.0.113.7",
                        "method": method,
                        "uri": parts.path,
                        "querystring": parts.query,
                        "headers": cf_headers,
                    },
                }
            }
        ]
    }


@dataclass(frozen=True)
class ViewerResult:
    """What the function returned for one viewer-request event, in either shape.

    A generated response carries ``status``, ``headers`` and ``body``; a
    pass-through carries ``uri`` and ``querystring`` and no ``status``.
    """

    status: str | None
    body: str | None
    uri: str | None
    querystring: str | None
    headers: dict[str, list[dict[str, str]]] = field(default_factory=dict)

    @property
    def pass_through(self) -> bool:
        """True when the function handed the REQUEST back for CloudFront to fetch.

        A generated response always carries ``status``; a request object never
        does, and carries ``uri`` instead.
        """
        return self.status is None and self.uri is not None

    def header(self, name: str) -> str | None:
        """One header of a generated response, or None when absent."""
        entries = self.headers.get(name.lower())
        if not entries:
            return None
        value = entries[0].get("value")
        return str(value) if value is not None else None

    def json_body(self) -> dict[str, Any]:
        """The generated response's JSON body, saying clearly when there isn't one.

        Every answer this worker generates (the well-known documents and each
        denial) is JSON. A 403 that arrives as HTML or empty is exactly the
        failure worth naming, and a bare ``json.loads`` would raise before an
        assertion could say what it saw.

        The media type is checked by its "+json" family rather than by an exact
        string: denials and the manifest are application/json, while the Web Bot
        Auth directory is application/http-message-signatures-directory+json, as the
        profile requires. The route that cares about the exact value asserts it.
        """
        content_type = self.header("content-type") or ""
        assert content_type.split(";")[0].strip().endswith("json"), (
            f"expected a JSON body; content-type was {content_type!r}, "
            f"body {str(self.body)[:160]!r}"
        )
        body = json.loads(self.body or "")
        assert isinstance(body, dict), f"body is not a JSON object: {body!r}"
        return body


def _viewer_result(result: Mapping[str, Any]) -> ViewerResult:
    def text(name: str) -> str | None:
        value = result.get(name)
        return None if value is None else str(value)

    return ViewerResult(
        status=text("status"),
        body=text("body"),
        uri=text("uri"),
        querystring=text("querystring"),
        headers=dict(result.get("headers") or {}),
    )


def invoke(base_url: str, event: Mapping[str, Any], *, timeout: float = 30.0) -> ViewerResult:
    """Invoke the function with ``event`` and return what it returned.

    Raises ``AssertionError`` when the invocation itself failed — the function
    threw, or the emulator answered something other than 200. Those are never
    the outcome under test, and the error message carries the function's own
    message so the failure names its cause instead of a shape mismatch three
    assertions later.

    Also refuses to POST at all once an earlier call to this emulator timed out
    client-side, and records a timeout so later calls can refuse. See
    ``_ABANDONED`` for why posting into that state destroys the container.
    """
    abandoned = _ABANDONED.get(base_url)
    if abandoned is not None:
        msg = (
            f"not invoking {base_url}: an earlier invocation was abandoned "
            f"client-side ({abandoned}) and the emulator may still hold it "
            "reserved. Posting again makes the emulator exit and takes the "
            "container down, which turns one timeout into every later test "
            "failing on an unresolvable name."
        )
        raise AssertionError(msg)
    try:
        resp = httpx.post(f"{base_url}{INVOKE_PATH}", json=dict(event), timeout=timeout)
    except httpx.TimeoutException as exc:
        _ABANDONED[base_url] = f"{type(exc).__name__} after {timeout}s"
        raise
    assert resp.status_code == httpx.codes.OK, (
        f"lambda runtime emulator answered {resp.status_code}: {resp.text[:256]}"
    )
    result = resp.json()
    assert isinstance(result, dict), f"function returned {type(result).__name__}, want an object"
    if _FUNCTION_ERROR_HEADER in resp.headers or "errorMessage" in result:
        raise AssertionError(f"the function invocation failed: {result}")
    return _viewer_result(result)


@dataclass(frozen=True)
class ExchangeDeliveryKey:
    """exchange-a's Ed25519 delivery-URL signing key.

    ``kid`` is the RFC 7638 thumbprint of the public half, which is both the
    keyid the Exchange stamps on a delivery URL and the name the key is
    published under in the Exchange's Web Bot Auth directory.
    """

    seed: bytes
    kid: str


def load_exchange_delivery_key(exchange_origin: str) -> ExchangeDeliveryKey:
    """Load the Exchange's delivery-URL key and confirm the Exchange publishes it.

    The confirmation is the point. Without it, a key that had drifted from the
    one the Exchange serves would surface as an ordinary "invalid signature"
    denial in every positive test, a failure that looks like the edge rejecting
    a bad URL rather than the harness signing with the wrong key. Verification
    itself stays on the production path: the worker resolves the key by
    thumbprint from the Exchange's directory, with no key list baked in.
    """
    pem_path = os.environ.get("FORA_EXCHANGE_ED25519_PEM_FILE")
    if not pem_path or not Path(pem_path).is_file():
        pytest.fail(
            "FORA_EXCHANGE_ED25519_PEM_FILE must name the Exchange's Ed25519 key file. "
            "docker-compose.e2e.yml sets it on the runner and mounts the key volume "
            "read-only, so run this through `make test-e2e` (or `docker compose "
            "--profile test run --rm runner`), not a bare pytest."
        )
    private = load_pem_private_key(Path(pem_path).read_bytes(), password=None)
    assert isinstance(private, Ed25519PrivateKey), (
        f"{pem_path} holds a {type(private).__name__}, want an Ed25519 private key"
    )
    kid = ed25519_thumbprint(private.public_key().public_bytes_raw())
    keys = wba_directory_at(exchange_origin).message.keys or []
    published = [ed25519_thumbprint(b64url_decode(k.x or "")) for k in keys]
    assert kid in published, (
        f"the Exchange publishes {published} in its Web Bot Auth directory, not {kid}: "
        "the key the harness signs delivery URLs with is no longer the key the edge resolves"
    )
    return ExchangeDeliveryKey(seed=private.private_bytes_raw(), kid=kid)


def mint_delivery_url(
    key: ExchangeDeliveryKey, url: str, *, agent_id: str = "", ttl_seconds: int = 300
) -> str:
    """``url`` signed as an Exchange-minted delivery URL, through the SDK's signer.

    An empty ``agent_id`` yields a bearer URL; a non-empty one binds the URL to
    that agent's key and turns on the proof-of-possession check at the edge. A
    negative ``ttl_seconds`` produces an already-expired URL, which no Exchange
    would ever mint and which the expired-URL refusal needs.
    """
    return sign_ed25519_signed_url(
        url, seed=key.seed, kid=key.kid, agent_id=agent_id, exp=int(time.time()) + ttl_seconds
    )


def proof_of_possession(url: str, key_path: Path, *, clock_offset_s: int = 0) -> dict[str, str]:
    """The proof-of-possession headers for a bound GET of ``url``, as the agent at ``key_path``.

    Minted by the SDK's own delivery-proof helper over the agent's signing
    transport, the same one ``Client.fetch`` uses. A Lambda@Edge test needs the
    headers themselves because it presents them inside an invoke event rather
    than sending a GET.

    ``clock_offset_s`` moves the clock the proof's window is read from: a proof
    whose window lies wholly in the past is expired, and one created further
    ahead than the edge tolerates is from the future. Zero is the SDK's own proof.
    """
    window = clock_window(lambda: time.time() + clock_offset_s, DEFAULT_PROOF_WINDOW_SEC)
    return proof_headers(url, signer=agent_signer(key_path), window=window)


def wait_ready(base_url: str, timeout_seconds: float = 90.0) -> None:
    """Poll the invoke endpoint until the function answers its /healthz route.

    This is the ONLY readiness invocation, and it is why the container probe
    must not invoke: two invocations at once destroy the emulator. The compose
    healthcheck proves the emulator is accepting connections and nothing more,
    so a stack that is still bringing the function up is caught here.

    Never more than one invocation in flight. The client deadline is whatever
    is left of the budget rather than a fixed slice, so a timeout means the
    budget is spent — it can never mean "retry now" while the emulator still
    holds the previous invocation reserved.
    """
    event = viewer_request_event("http://lambda-edge.invalid/healthz")
    deadline = time.monotonic() + timeout_seconds
    last_err: list[str] = ["no attempt was made"]

    def healthy() -> bool:
        remaining = deadline - time.monotonic()
        # Under a second is not a real attempt, and a sub-second client
        # deadline would report a connect timeout as the reason the function
        # never answered. Answering "not yet" lets the poller run out its
        # deadline without another invocation.
        if remaining < 1.0:
            return False
        try:
            result = invoke(base_url, event, timeout=remaining)
        except httpx.TimeoutException as exc:
            # The emulator may still hold this invocation. `invoke` has already
            # retired it for the session; spend the budget rather than retry.
            msg = (
                f"{base_url} did not answer /healthz after {timeout_seconds}s "
                f"(last: timed out with the invocation still in flight)"
            )
            raise TimeoutError(msg) from exc
        # A refused connection reserved nothing, and a completed invocation
        # that answered badly has already released the slot. Both are safe to
        # retry. TimeoutException is a subclass of HTTPError, which is why it
        # is caught above this line rather than swept in here.
        except (httpx.HTTPError, OSError, AssertionError) as exc:
            last_err[0] = str(exc)[:128]
            return False
        if result.status == "200":
            return True
        last_err[0] = str(result)[:128]
        return False

    poll_until(
        healthy,
        timeout_seconds,
        failure=lambda: (
            f"{base_url} did not answer /healthz after {timeout_seconds}s (last: {last_err[0]})"
        ),
    )
