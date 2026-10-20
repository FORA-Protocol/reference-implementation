"""E2E: the Broker publishes a live key-revocation channel.

The producer-shape assertions confirm the Broker advertises ``revocation_url`` in
its Web Bot Auth directory (after the WBA split, identity and revocation moved off
fora.json) and serves a schema-shaped ``KeyRevocationList`` at that route.

What a revocation does to a verifier is proven by the revocation obligations in
``harness/obligations/test_revocation_scope.py``. A list revokes only the keys of
the directory that publishes it: the Broker's list covers the Broker's own key,
and never an agent's.
"""

from __future__ import annotations

from fora_sdk.resolvers import read_revocation_list

from .conftest import StackURLs
from .constants import REVOCATION_PATH
from .protocol_documents import wba_directory_at

#: BROKER_REVOCATION_URL in docker-compose.e2e.yml: the list at the Broker's https
#: origin, where the capture proxy answers for it.
_EXPECTED_REVOCATION_URL = f"https://broker{REVOCATION_PATH}"


def test_broker_wba_directory_advertises_revocation_url(compose_stack: StackURLs) -> None:
    directory = wba_directory_at(compose_stack.broker).message
    # WBA directory: keys[] (no kid; the strict read refuses one) plus a
    # directory-level revocation_url.
    assert directory.keys, directory
    assert directory.revocation_url == _EXPECTED_REVOCATION_URL, directory


def test_broker_serves_key_revocation_list(compose_stack: StackURLs) -> None:
    revocations = read_revocation_list(f"{compose_stack.broker}{REVOCATION_PATH}").message
    # KeyRevocationList: as_of required; revoked optional (the default baseline
    # serves the empty, epoch-dated "nothing revoked" snapshot). After the WBA
    # split the revoked[] entries are RFC 7638 thumbprints, not kids.
    assert revocations.as_of is not None, revocations
