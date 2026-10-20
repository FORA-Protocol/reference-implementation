"""Obligations KEYS-002, -003, -010 and -011: every participant's key directory.

The contract is the key directory and revocation channel of the pinned module
``github.com/FORA-Protocol/protocol``: ``JsonWebKey`` and ``KeyRevocationList`` in
``fora.proto``, and the authentication page of its documentation.

* every directory key is an Ed25519 signing key (OKP, Ed25519, EdDSA, use sig) and
  carries no ``kid``;
* at least one key in a directory is valid at the moment it is served, inside its
  half-open ``[not_before, not_after)`` window;
* a directory's ``revocation_url`` serves a ``KeyRevocationList``: ``as_of``, and
  ``revoked`` as RFC 7638 thumbprints;
* that list SHOULD be served with a short or zero freshness lifetime, so the
  300-second revocation poll is not defeated by an intermediary cache.

The participants are the four Exchanges, the Broker, the publisher console and an
agent whose key the identity service holds, each at the origin a verifier reads
it from. Every directory and every revocation list is read through the SDK's
readers (``read_wba_directory``, ``read_revocation_list``), which refuse a
directory not served as ``application/http-message-signatures-directory+json`` and
apply the published strict schema. That schema admits no member ``JsonWebKey`` does not define, so a
key carrying a ``kid`` fails the read itself. The one thing those readers do not
report is a document's ``Cache-Control``, read by
``protocol_documents.served_freshness``.

The Broker's revocation list is shared state another suite writes, so it is only
read here. The agent lives on the identity service, which is reachable only
inside the compose network, and so is every advertised ``revocation_url``.

The Web Bot Auth wire profile of these directories (the draft's media type, its
response signature, the import into an independent verifier) is checked by the
Web Bot Auth conformance suites, not here. KEYS-002 is driven because what it
adds is FORA's: ``use`` and ``alg`` required, and no ``kid`` at all.
"""

from __future__ import annotations

import os
import re
from datetime import UTC, datetime

import pytest
from fora_sdk.b64 import b64url_decode_strict
from fora_sdk.resolvers import read_revocation_list
from wire.models import JsonWebKey, WBAFile

from ..defects import (
    BROKER_REVOCATION_LIST_STATES_NO_LIFETIME,
    IDENTITY_REVOCATION_LIST_CACHED_TOO_LONG,
    Defect,
)
from ..exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_C_DOMAIN,
    EXCHANGE_D_DOMAIN,
    exchange_url,
)
from ..obligation_registry import Obligation, obligation
from ..protocol_documents import served_freshness, wba_directory_at
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_EXCHANGES = {
    "exchange-a": EXCHANGE_A_DOMAIN,
    "exchange-b": EXCHANGE_B_DOMAIN,
    "exchange-c": EXCHANGE_C_DOMAIN,
    "exchange-d": EXCHANGE_D_DOMAIN,
}

#: Every participant whose directory this module reads. "agent" is an agent the
#: identity service signed up for this session; its host is known only at run time.
_PARTICIPANTS = [*_EXCHANGES, "broker", "console", "agent"]

#: The longest freshness lifetime a revocation list may be served with, in seconds.
_SHORT_LIFETIME_S = 60

#: An RFC 7638 thumbprint of an Ed25519 key: SHA-256, base64url with no padding.
_THUMBPRINT = re.compile(r"[A-Za-z0-9_-]{43}")

_ED25519_KEY_BYTES = 32


def _origin(participant: str, request: pytest.FixtureRequest) -> str:
    """The origin ``participant`` serves its directory at."""
    stack: StackURLs = request.getfixturevalue("compose_stack")
    if participant in _EXCHANGES:
        return exchange_url(stack, _EXCHANGES[participant])
    if participant == "broker":
        return stack.broker
    if participant == "console":
        assert stack.console, "this stack runs no publisher console"
        return stack.console
    # The identity service answers each agent's well-known documents on the agent's
    # own host, on port 80, over the scheme this stack runs it with.
    return f"http://{request.getfixturevalue('mcp_agent')}"


def _directory(participant: str, request: pytest.FixtureRequest) -> WBAFile:
    return wba_directory_at(_origin(participant, request)).message


def _advertised_revocation_url(participant: str, request: pytest.FixtureRequest) -> str:
    """The revocation_url ``participant`` advertises; the test is skipped when there is none.

    Read only inside the compose network, where every advertised URL resolves.
    """
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1":
        pytest.skip("an advertised revocation_url resolves only inside the compose network")
    url = _directory(participant, request).revocation_url
    if not url:
        pytest.skip(f"{participant} advertises no revocation_url")
    return url


def _valid_at(key: JsonWebKey, now: datetime) -> bool:
    """Whether ``now`` falls inside the key's half-open ``[not_before, not_after)`` window.

    A bound that is missing, unparseable or without a UTC offset is not an RFC 3339
    timestamp, so the key has no window that could hold ``now``.
    """
    try:
        not_before = datetime.fromisoformat(key.not_before or "")
        not_after = datetime.fromisoformat(key.not_after or "")
    except ValueError:
        return False
    if not_before.tzinfo is None or not_after.tzinfo is None:
        return False
    return not_before <= now < not_after


@obligation(Obligation.KEYS_002)
@pytest.mark.parametrize("participant", _PARTICIPANTS)
def test_every_directory_key_is_an_ed25519_signing_key(
    participant: str, request: pytest.FixtureRequest
) -> None:
    """Each key: kty OKP, crv Ed25519, alg EdDSA, use sig, and a 32-byte x.

    The absence of ``kid`` is checked by the read itself, as the module says.
    """
    keys = _directory(participant, request).keys or []
    assert keys, f"{participant} serves a directory with no keys"
    for key in keys:
        assert (key.kty, key.crv, key.alg, key.use) == ("OKP", "Ed25519", "EdDSA", "sig"), (
            f"{participant} publishes a key that is not an Ed25519 signing key: {key}"
        )
        assert len(b64url_decode_strict(key.x or "")) == _ED25519_KEY_BYTES, (
            f"{participant} publishes an x that is not a 32-byte Ed25519 key: {key}"
        )


@obligation(Obligation.KEYS_003)
@pytest.mark.parametrize("participant", _PARTICIPANTS)
def test_every_directory_holds_a_key_valid_when_it_is_served(
    participant: str, request: pytest.FixtureRequest
) -> None:
    """At least one key's ``[not_before, not_after)`` window holds the moment it was read.

    The clock is this process's, which the stack's containers share, read right
    after the directory arrives.
    """
    keys = _directory(participant, request).keys or []
    now = datetime.now(UTC)
    windows = [(key.not_before, key.not_after) for key in keys]
    assert any(_valid_at(key, now) for key in keys), (
        f"no key {participant} serves is valid at {now.isoformat()}: windows {windows}"
    )


@obligation(Obligation.KEYS_011)
@pytest.mark.parametrize("participant", _PARTICIPANTS)
def test_every_advertised_revocation_list_is_a_snapshot_of_thumbprints(
    participant: str, request: pytest.FixtureRequest
) -> None:
    """The SDK's reader accepts the list; as_of is set and each entry is a thumbprint."""
    url = _advertised_revocation_url(participant, request)
    revocations = read_revocation_list(url).message
    assert revocations.as_of is not None, f"{url} serves a revocation list with no as_of"
    malformed = [entry for entry in revocations.revoked or [] if not _THUMBPRINT.fullmatch(entry)]
    assert not malformed, f"{url} lists entries that are not RFC 7638 thumbprints: {malformed}"


#: The participants that advertise a revocation_url, each with the defect that keeps
#: its list from a short freshness lifetime. The Exchanges and the console
#: advertise none; the KEYS-011 test above skips them and says so.
_FRESHNESS_DEFECTS: dict[str, Defect] = {
    "broker": BROKER_REVOCATION_LIST_STATES_NO_LIFETIME,
    "agent": IDENTITY_REVOCATION_LIST_CACHED_TOO_LONG,
}


@obligation(Obligation.KEYS_010)
@pytest.mark.parametrize(
    "participant",
    [pytest.param(p, marks=defect.marker, id=p) for p, defect in _FRESHNESS_DEFECTS.items()],
)
def test_every_advertised_revocation_list_has_a_short_freshness_lifetime(
    participant: str, request: pytest.FixtureRequest
) -> None:
    """The list is served with Cache-Control no-store, or a lifetime of 60 seconds or less."""
    url = _advertised_revocation_url(participant, request)
    freshness = served_freshness(url)
    lifetime = freshness.lifetime_s
    _FRESHNESS_DEFECTS[participant].check(
        lifetime is not None and lifetime <= _SHORT_LIFETIME_S,
        f"{url} is served with Cache-Control {freshness.cache_control!r}",
    )
