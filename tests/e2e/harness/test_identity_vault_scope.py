"""E2E: the Identity Service's Vault token in this stack is scoped, not root.

identity-vault-init writes the policy from deploy/storage/vault/identity-policy.hcl
and hands the identity service a token that carries only that policy. The tests
read the token and the mount from the running identity container itself, so a
change to what that service is given cannot pass unnoticed. A lookup with the
root token shows the token carries exactly that policy, and these tests drive
Vault's HTTP API with it the way the service's key custody does:

- the Vault calls behind key creation, rotation (a rewrite that adds a version, and
  the agent listing) and revocation or pruning (a metadata delete, which erases
  every version) all succeed under agents/;
- everything else is refused with 403: a secret outside agents/ that is shown to
  exist first, enabling a mount, and even a lookup of the token itself, because the
  token carries no "default" policy. The refused policy write is checked in the
  keystore Go suite: its Vault body has a key this harness's FORA-body guard reads
  as a protocol request.

The shape is that of a custody smoke check, which writes and destroys one
throwaway secret with the service's own token. The rotation scheduler
and the revocation service themselves run under this policy in the Go integration
suites of src/identity/internal/lifecycle and src/identity/internal/transport; this
file proves the e2e stack wires the Identity Service to that policy and nothing
more.

In-network only: identity-vault publishes no host port.
"""

from __future__ import annotations

import os
import uuid
from collections.abc import Iterator
from dataclasses import dataclass
from http import HTTPStatus

import pytest

from ._compose import running_service_env
from .conftest import StackURLs
from .identity_vault import VaultAnswer, VaultSession

POLICY_NAME = "fora-identity"


@dataclass(frozen=True)
class _Vault:
    """The two Vault sessions these tests need, and the mount the service uses."""

    scoped: VaultSession
    root: VaultSession
    mount: str
    token: str


@pytest.fixture
def vault(identity_ready: StackURLs) -> Iterator[_Vault]:
    identity_env = running_service_env("identity")
    token = identity_env.get("VAULT_TOKEN", "")
    root_token = os.environ["IDENTITY_VAULT_ROOT_TOKEN"]
    assert token, "the identity service is given no Vault token"

    with VaultSession(token) as scoped, VaultSession(root_token) as root:
        yield _Vault(scoped, root, identity_env["IDENTITY_KV_MOUNT"], token)


def _expect(answer: VaultAnswer, status: HTTPStatus, action: str) -> VaultAnswer:
    assert answer.status == status, f"{action} = {answer.status} {answer.excerpt}, want {status}"
    return answer


def test_the_identity_token_carries_only_the_identity_policy(vault: _Vault) -> None:
    # Root, or any other valid token with more than this policy, keeps the service
    # healthy; only its policies tell it apart.
    lookup = _expect(vault.root.lookup_token(vault.token), HTTPStatus.OK, "look up the token")
    assert lookup.data["policies"] == [POLICY_NAME], (
        f"the identity service's Vault token has policies "
        f"{lookup.data['policies']}, want exactly [{POLICY_NAME}]"
    )


def test_the_identity_token_can_create_rotate_and_erase_a_key(vault: _Vault) -> None:
    key_path = f"agents/_e2e-check/{uuid.uuid4().hex}"
    data = f"/v1/{vault.mount}/data/{key_path}"
    metadata = f"/v1/{vault.mount}/metadata/{key_path}"

    # Creation writes version 1; rotation's retirement rewrites the record as version 2.
    _expect(vault.scoped.write(data, {"seed": "one"}), HTTPStatus.OK, "create a key")
    _expect(vault.scoped.write(data, {"seed": "two"}), HTTPStatus.OK, "rewrite a key")
    read = _expect(vault.scoped.read(data), HTTPStatus.OK, "read a key")
    assert read.data["metadata"]["version"] == 2

    # The scheduler finds agents and their keys by listing the metadata tree.
    listing = _expect(
        vault.scoped.list_keys(f"/v1/{vault.mount}/metadata/agents/_e2e-check"),
        HTTPStatus.OK,
        "list keys",
    )
    assert key_path.rsplit("/", 1)[1] in listing.data["keys"]

    # Revocation and pruning erase the key with a metadata delete: every version goes.
    _expect(vault.scoped.delete(metadata), HTTPStatus.NO_CONTENT, "erase a key")
    for version in (1, 2):
        _expect(
            vault.root.read(data, version=version),
            HTTPStatus.NOT_FOUND,
            f"read version {version} after erase",
        )


def test_the_identity_token_is_refused_outside_agent_keys(vault: _Vault) -> None:
    outside = f"/v1/{vault.mount}/data/e2e-outside-agents/{uuid.uuid4().hex}"
    # Shown to exist first, so a 404 cannot pass for isolation.
    _expect(vault.root.write(outside, {"value": "not for identity"}), HTTPStatus.OK, "root write")
    try:
        _expect(vault.root.read(outside), HTTPStatus.OK, "root read")
        _expect(vault.scoped.read(outside), HTTPStatus.FORBIDDEN, "read a secret outside agents/")
    finally:
        vault.root.delete(outside.replace("/data/", "/metadata/", 1))

    _expect(vault.scoped.enable_kv_mount("e2e-rogue"), HTTPStatus.FORBIDDEN, "enable a mount")
    _expect(vault.scoped.lookup_self(), HTTPStatus.FORBIDDEN, "look itself up")
