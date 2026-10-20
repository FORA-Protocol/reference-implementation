"""E2E: catalog-write trust learned ONLY via the writer's well-known key directory.

Core Invariant under test
-------------------------
The Exchange must learn every catalog-writer's Ed25519 signing key by fetching
that writer's Web Bot Auth directory, never from a pre-seeded agent row. The seed
deliberately registers NO catalog-writer key, so a push that the Exchange accepts
proves the key was fetched: had it not been, the signature would be refused as an
unknown caller.

The catalog obligation tests (``obligations/test_catalog_push.py``) already prove
a publisher pushing its own page on exchange-a (CATALOG-020), a listed
contributor pushing for the music publisher and a caller the manifest does not
list being refused NOT_CATALOG_CONTRIBUTOR (CATALOG-004). This module keeps the
two cases they do not cover:

* **self-publish behind the AWS-shim edge**: the sfx publisher signs its own push
  to exchange-c, whose Web Bot Auth directory the raw-Node shim serves. The
  pushed resource becomes discoverable, which only a fetched key could admit.
* **a writer whose directory host serves nothing**: the push names a host with no
  well-known at all, and is signed by a real key no directory serves. The
  Exchange cannot resolve the key, so it refuses the push and stores nothing.

Every push goes through the SDK's own catalog client (``harness.catalog_calls``)
and every read-back is a DiscoverResources as the test's own agent.
"""

from __future__ import annotations

import uuid

import pytest

from .catalog_calls import Catalog, accepted, fresh_entry, keyfile_signer, refusal, stored
from .conftest import StackURLs
from .exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN
from .fresh_agent import FreshAgent
from .httpsig_signer import unpublished_signer
from .seed import (
    DEMO_PHILOSOPHY_DOMAIN,
    DEMO_SFX_DOMAIN,
    SEAT_OF_PHILOSOPHY,
    SEAT_OF_SFX,
    SELFPUB_SFX_KEY_PATH,
    SeededFixture,
)

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The codes a push refused because the writer's key cannot be resolved may carry.
#: The protocol names no code for a directory that cannot be fetched: a verifier
#: that cannot resolve the key refuses the signature (``unauthenticated``), and this
#: stack, whose directory host does not resolve, answers ``unavailable``. Either is
#: a refusal that stores nothing; an answer, or any other refusal, is not.
_UNRESOLVABLE_KEY_CODES = frozenset({"unauthenticated", "unavailable"})


def test_self_publish_push_accepted_via_wellknown_aws_edge(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed registers the seat, sites and edges
    fresh_agent: FreshAgent,
) -> None:
    """Self-publish trust holds behind the AWS-shim edge too (sfx publisher, exchange-c).

    sfx is an AWS_CLOUDFRONT_RSA site, but the catalog-push signature is always
    Ed25519 RFC 9421: the site's scheme only governs URL minting. The Exchange
    fetches the sfx edge's Web Bot Auth directory, served by the raw-Node shim, and
    the publisher's own domain authorizes the push.
    """
    publisher = Catalog(compose_stack.exchange_c, keyfile_signer(SELFPUB_SFX_KEY_PATH))
    assert publisher.signer.signature_agent == f"https://{DEMO_SFX_DOMAIN}", publisher.signer
    entry = fresh_entry(DEMO_SFX_DOMAIN)
    accepted(publisher.push(SEAT_OF_SFX, [entry]))
    assert stored(fresh_agent, EXCHANGE_C_DOMAIN, entry.uri), (
        f"pushed resource {entry.uri!r} is not offered: the well-known key fetch did not "
        f"admit the push"
    )


def test_unfetchable_domain_caller_rejected_at_gate1(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: seed registers the seat, sites and edges
    fresh_agent: FreshAgent,
) -> None:
    """A writer whose directory host serves no key is refused, and nothing is written.

    The push names a host with no compose alias and no well-known as its directory,
    and is signed with a real, freshly generated key that no directory serves. The
    Exchange cannot resolve the key, so the push is refused before any
    authorization of the entry is consulted.
    """
    unfetchable = f"no-wellknown-{uuid.uuid4().hex}.invalid"
    stranger = Catalog(compose_stack.exchange, unpublished_signer(unfetchable))
    entry = fresh_entry(DEMO_PHILOSOPHY_DOMAIN)
    error = refusal(stranger.push(SEAT_OF_PHILOSOPHY, [entry]), "a directory host serving nothing")
    assert error.code in _UNRESOLVABLE_KEY_CODES, f"refused as {error.code!r}: {error}"
    assert not stored(fresh_agent, EXCHANGE_A_DOMAIN, entry.uri), (
        f"the refused resource {entry.uri!r} leaked into the catalog"
    )
