"""The stack's local CA, from the harness side: the TLS contexts a test trusts it through.

The capture proxy (docker-compose.e2e.yml, built from tests/e2e/capture-proxy/)
is the stack's one TLS endpoint: it answers https for every party that signs or
serves a key directory, under a certificate from a local CA that tls-keygen mints
at stack-up. The runner trusts that CA through SSL_CERT_FILE, so a plain httpx
client verifies the proxy's certificate. This module builds the two contexts a
test needs to show that the local CA, and nothing laxer, is what makes https
verify: one that trusts the local CA alone, and one over the public roots alone.

What the proxy records about the requests it forwards is read through
``harness/capture_proxy.py``, whichever leg a request arrived on.

In-network only: the names the proxy answers for resolve to it only inside the
compose network.
"""

from __future__ import annotations

import os
import ssl
from dataclasses import dataclass

import httpx
import pytest

# The runner's setting that names the local CA certificate (the x-tls-ca-file
# anchor in docker-compose.e2e.yml).
_CA_FILE_ENV = "FORA_E2E_TLS_CA_FILE"


def require_in_network() -> None:
    """Skip unless the runner is inside the compose network, where the proxy is."""
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip(
            "the capture proxy's https leg is reachable only in-network (FORA_E2E_IN_NETWORK=1)"
        )


def local_ca_file() -> str:
    """Return the path of the local CA certificate, or fail naming the setting.

    No default: a path written here would be a second copy of the compose file's,
    and the failure mode of a stale copy is a verification error that names a
    certificate rather than the mismatch.
    """
    path = os.environ.get(_CA_FILE_ENV)
    if not path:
        pytest.fail(
            f"{_CA_FILE_ENV} is not set. docker-compose.e2e.yml sets it on the runner "
            "from the x-tls-ca-file anchor; run this test through `make test-e2e`."
        )
    return path


def local_ca_context() -> ssl.SSLContext:
    """A verifying TLS context that trusts the local CA and nothing else."""
    return ssl.create_default_context(cafile=local_ca_file())


def public_roots_context() -> ssl.SSLContext:
    """A verifying TLS context over the public roots alone, without the local CA.

    httpx's own default with the environment ignored: its bundled public roots,
    which is what a client that was never told about the local CA trusts. The
    environment has to be ignored because the runner's SSL_CERT_FILE names the
    bundle that already holds the local CA.
    """
    return httpx.create_ssl_context(trust_env=False)


@dataclass(frozen=True)
class HttpsAnswer:
    """What an https request got: the status and the start of the body, or why it got none.

    ``refused`` is the transport error when no answer came back (a certificate the
    trust store refused, a connection that failed); ``status`` is then 0.
    """

    url: str
    status: int
    text: str
    refused: str = ""

    @property
    def certificate_refused(self) -> bool:
        return "CERTIFICATE_VERIFY_FAILED" in self.refused


def https_request(
    method: str,
    url: str,
    *,
    trust: ssl.SSLContext | None = None,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> HttpsAnswer:
    """Send one request over https, trusting ``trust`` (the local CA alone by default).

    No redirect is followed. A transport failure is returned, not raised, so a test
    can tell a certificate refusal from an answer.
    """
    verify = trust if trust is not None else local_ca_context()
    try:
        with httpx.Client(verify=verify, follow_redirects=False, timeout=10.0) as client:
            response = client.request(method, url, content=body, headers=headers)
    except httpx.HTTPError as exc:
        return HttpsAnswer(url=url, status=0, text="", refused=repr(exc))
    return HttpsAnswer(url=url, status=response.status_code, text=response.text[:300])
