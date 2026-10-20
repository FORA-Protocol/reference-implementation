"""The identity service operator's break-glass CLI, run the way an operator runs it.

Revoking an agent's key is deliberately not an HTTP endpoint on the identity
service: an unauthenticated one would let anyone kill any agent's keys. The lever
is the ``identity-operator`` CLI (``src/identity/cmd/operator``). It publishes the
key to the agent's revocation list and destroys the key material, writing to the
same Vault and Postgres the running service reads, so the service picks the
revocation up within its document cache lifetime.

The stack's identity image is built from its ``e2e`` target, which carries the CLI
beside the server; the production image does not
(``harness/test_guards_e2e_images.py``). The harness runs it inside the running
identity container through the Docker socket, the same access
:func:`harness._compose.stopped_service` uses, so it reads exactly the service's
own configuration and credentials. In-network only.

This is the only module that runs the CLI.
"""

from __future__ import annotations

import os

import pytest

from ._compose import exec_in_service, running_service_env

#: The compose service the CLI runs in, and where its image puts the binary.
_SERVICE = "identity"
_CLI = "/identity-operator"

#: How long the service may serve a cached document by default: the directory TTL
#: the identity service applies when IDENTITY_DIRECTORY_TTL is unset
#: (``publisher.DefaultTTL``).
_DEFAULT_DOCUMENT_TTL_S = 300.0


def require_operator_cli() -> None:
    """Skip unless this run can reach the identity container through the Docker socket."""
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip("the operator CLI runs inside the identity container (FORA_E2E_IN_NETWORK=1)")


def revoke_agent_key(subdomain: str, thumbprint: str) -> None:
    """Revoke ``thumbprint``, a key of the agent at ``subdomain``, as the operator would.

    The CLI exits non-zero when the key is not that agent's or the write fails;
    either fails the calling test with the CLI's own output.
    """
    exit_code, output = exec_in_service(_SERVICE, [_CLI, "revoke", subdomain, thumbprint])
    assert exit_code == 0, (
        f"identity-operator revoke {subdomain} {thumbprint} exited {exit_code}: {output}"
    )


def document_ttl_seconds() -> float:
    """How long the running identity service may serve a cached directory or revocation list."""
    raw = running_service_env(_SERVICE).get("IDENTITY_DIRECTORY_TTL", "").strip()
    if not raw:
        return _DEFAULT_DOCUMENT_TTL_S
    assert raw.endswith("s") and raw[:-1].isdigit(), (
        f"IDENTITY_DIRECTORY_TTL={raw!r} is not a whole number of seconds this reader understands"
    )
    return float(raw[:-1])
