"""Catalog obligations about who may push, and what a push stores or refuses.

CATALOG-001 to CATALOG-005, CATALOG-007, CATALOG-013 to CATALOG-015, CATALOG-017,
CATALOG-018, CATALOG-020 and CATALOG-024, from the pinned protocol module
``github.com/FORA-Protocol/protocol`` (``fora.proto`` and the website docs).

Every push is the SDK's own ``CatalogClient`` call, signed as the caller's
keyfile (``harness.catalog_calls``); a negative that alters the request is a
declared ``malformed()`` mutation beside its control push. What a push stored is
read back through ``DiscoverResources``, as the test's own agent. A refusal is
read through the SDK's typed ``CallError``: its Connect code and its
``catalog_rejection``, never the message text.

Every resource is pushed under a fresh path and content id, so each test passes
again on a stack it has already run on (ADR-028 D7).
"""

from __future__ import annotations

import uuid
from decimal import Decimal

import pytest
from fora_sdk.client import CallError

from ..after_signing import strip_signature
from ..catalog_calls import (
    DOMAIN_NOT_VERIFIED,
    HOME_EXCHANGE,
    HOME_SEAT,
    NOT_CATALOG_CONTRIBUTOR,
    UNKNOWN_VOCAB_TOKEN,
    URI_UNAVAILABLE,
    Catalog,
    accepted,
    added,
    answer,
    assert_push_naming_tenant_refused,
    assert_typed_refusal,
    broken,
    contributor,
    fresh_entry,
    keyfile_signer,
    offers,
    refusal,
    sole,
    stored,
)
from ..catalog_push import (
    OBLIGATION_KIND_SHARE_ALIKE,
    OBLIGATION_TRIGGER_ON_DISTRIBUTION,
    PRICING_MODEL_FLAT,
    PRICING_MODEL_PER_UNIT,
    RESTRICTION_KIND_FUNCTION,
    CatalogEntry,
    license_term,
    restriction,
)
from ..defects import (
    CATALOG_ATTESTATIONS_UNVERIFIED,
    CATALOG_CALLER_ID_REQUIRED,
    CATALOG_REFUSAL_UNTYPED,
    CATALOG_SIGNATURE_REFUSAL_UNTYPED,
    CATALOG_TENANT_MISMATCH_UNTYPED,
)
from ..e2e_catalog import (
    CATALOG_CONTRIBUTOR_ID,
    DEMO_MUSIC_DOMAIN,
    DEMO_PHILOSOPHY_DOMAIN,
    SEAT_OF_MUSIC,
    SEAT_OF_SFX,
)
from ..exchanges import EXCHANGE_B_DOMAIN
from ..fresh_agent import FreshAgent
from ..httpsig_signer import unpublished_signer
from ..malformed import Alteration, Mutation, Request, malformed
from ..money_path import typed_reasons
from ..obligation_registry import Obligation, obligation
from ..publisher_sites import SITE_ZONE
from ..seed import SELFPUB_PHILOSOPHY_KEY_PATH, SELFPUB_SFX_KEY_PATH, SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: A Pricing.unit no vocabulary registers, bare (not namespaced).
_UNREGISTERED_UNIT = "widgets"

#: The typed signature refusals for a catalog call: request_auth_failure, as for
#: every signed request (CATALOG_REJECTION_REASON_SIGNATURE_INVALID is never sent).
_SIGNATURE_REFUSALS = {
    "request_auth_failure": {
        "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_MISSING",
        "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_INVALID",
        "REQUEST_AUTH_FAILURE_REASON_SIGNATURE_STALE",
    },
}


def _unknown_unit_term() -> dict[str, object]:
    return license_term(
        model=PRICING_MODEL_PER_UNIT, rate=0.1, unit=_UNREGISTERED_UNIT, estimated_quantity=1
    )


@obligation(Obligation.CATALOG_001, negative=True)
def test_a_push_with_a_wire_tier_failure_stores_nothing(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A SHARE_ALIKE term without scope_license, beside a fresh valid entry: neither is stored.

    Control: the push of one valid entry is accepted and its URI offered, so the
    read that finds nothing below finds nothing because nothing was stored.
    """
    catalog = contributor(compose_stack)
    control = fresh_entry()
    sibling = fresh_entry()
    bad = fresh_entry()

    def share_alike_without_license(entry: dict[str, object]) -> None:
        terms = entry["terms"]
        assert isinstance(terms, list)
        terms[0]["obligations"] = [
            {"kind": OBLIGATION_KIND_SHARE_ALIKE, "trigger": OBLIGATION_TRIGGER_ON_DISTRIBUTION}
        ]

    error = malformed(
        Mutation.MESSAGE,
        "an added entry's term carries SHARE_ALIKE without scope_license",
        control=catalog.push(HOME_SEAT, [control]),
        alter=Alteration(
            before_sign=added(
                broken(sibling, lambda _: None), broken(bad, share_alike_without_license)
            )
        ),
    )
    assert error.code == "invalid_argument", f"refused as {error.code!r}: {error}"
    assert stored(fresh_agent, HOME_EXCHANGE, control.uri), "the control push was not stored"
    assert not stored(fresh_agent, HOME_EXCHANGE, sibling.uri), "the valid sibling was stored"
    assert not stored(fresh_agent, HOME_EXCHANGE, bad.uri), "the invalid entry was stored"


@obligation(Obligation.CATALOG_001, negative=True)
def test_a_push_with_an_ingest_tier_failure_stores_nothing(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """An unregistered bare unit refuses the push, and its valid sibling is not stored.

    Control: the same valid sibling pushed alone is stored.
    """
    catalog = contributor(compose_stack)
    alone = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [alone]))
    assert stored(fresh_agent, HOME_EXCHANGE, alone.uri), "the valid entry alone was not stored"

    sibling = fresh_entry()
    bad = fresh_entry(terms=(_unknown_unit_term(),))
    error = refusal(catalog.push(HOME_SEAT, [sibling, bad]), "an unregistered bare unit")
    assert error.code != "ok", error
    assert not stored(fresh_agent, HOME_EXCHANGE, sibling.uri), "the valid sibling was stored"
    assert not stored(fresh_agent, HOME_EXCHANGE, bad.uri), "the refused entry was stored"


@obligation(Obligation.CATALOG_002, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_refused_push_names_its_reason_and_exactly_the_failing_entry(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """catalog_rejection UNKNOWN_VOCAB_TOKEN, rejected_paths the failing entry's path alone."""
    sibling = fresh_entry()
    bad = fresh_entry(terms=(_unknown_unit_term(),))
    error = refusal(
        contributor(compose_stack).push(HOME_SEAT, [sibling, bad]), "an unregistered bare unit"
    )
    assert not stored(fresh_agent, HOME_EXCHANGE, sibling.uri), "the valid sibling was stored"
    assert_typed_refusal(error, UNKNOWN_VOCAB_TOKEN, [bad.path])


@obligation(Obligation.CATALOG_003, negative=True)
@CATALOG_SIGNATURE_REFUSAL_UNTYPED.mark
def test_an_unsigned_push_is_refused_unauthenticated(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The contributor's push with its signature removed: refused, typed, nothing stored.

    Control: the same caller's signed push of another fresh entry is stored.
    """
    catalog = contributor(compose_stack)
    _assert_control_stored(catalog, fresh_agent)
    entry = fresh_entry()
    error = refusal(catalog.push(HOME_SEAT, [entry]), "no signature", after=strip_signature)
    _assert_signature_refusal(error, fresh_agent, entry.uri)


@obligation(Obligation.CATALOG_003, negative=True)
@CATALOG_SIGNATURE_REFUSAL_UNTYPED.mark
def test_a_push_signed_by_a_key_the_caller_does_not_publish_is_refused(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The push names the listed contributor's directory; the signing key is one it does not serve.

    Control: the same caller's push signed by its published key is stored.
    """
    catalog = contributor(compose_stack)
    _assert_control_stored(catalog, fresh_agent)
    entry = fresh_entry()
    error = refusal(
        catalog.push(HOME_SEAT, [entry]),
        "a key the contributor's directory does not publish",
        signer=unpublished_signer(CATALOG_CONTRIBUTOR_ID),
    )
    _assert_signature_refusal(error, fresh_agent, entry.uri)


def _assert_control_stored(catalog: Catalog, agent: FreshAgent) -> None:
    control = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [control]))
    assert stored(agent, HOME_EXCHANGE, control.uri), "the control push was not stored"


def _assert_signature_refusal(error: CallError, agent: FreshAgent, uri: str) -> None:
    assert error.code == "unauthenticated", f"refused as {error.code!r}: {error}"
    assert not stored(agent, HOME_EXCHANGE, uri), "the refused push was stored"
    detail = error.detail
    typed = {
        block: getattr(detail, block).reason.value
        for block in _SIGNATURE_REFUSALS
        if detail is not None and getattr(detail, block) is not None
    }
    CATALOG_SIGNATURE_REFUSAL_UNTYPED.check(bool(typed), f"the refusal carries {detail}")
    assert detail is None or detail.catalog_rejection is None, (
        f"a signature refusal carries catalog_rejection: {detail}"
    )
    assert any(value in _SIGNATURE_REFUSALS[block] for block, value in typed.items()), (
        f"the refusal's typed reason is not a signature reason: {typed}"
    )


@obligation(Obligation.CATALOG_004)
def test_a_listed_contributor_pushes_for_the_publisher(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The music publisher's manifest lists the operator's contributor: its push is stored."""
    entry = fresh_entry(DEMO_MUSIC_DOMAIN)
    accepted(
        Catalog(compose_stack.exchange_b, contributor(compose_stack).signer).push(
            SEAT_OF_MUSIC, [entry]
        )
    )
    assert stored(fresh_agent, EXCHANGE_B_DOMAIN, entry.uri), (
        "the contributor's push was not stored"
    )


@obligation(Obligation.CATALOG_004, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_caller_the_manifest_does_not_list_cannot_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The sfx publisher signs a push for the music publisher, which does not list it.

    Its key is fetchable, so the signature verifies and the refusal is the
    authorization gate's: NOT_CATALOG_CONTRIBUTOR, and nothing stored.
    """
    entry = fresh_entry(DEMO_MUSIC_DOMAIN)
    stranger = Catalog(compose_stack.exchange_b, keyfile_signer(SELFPUB_SFX_KEY_PATH))
    error = refusal(stranger.push(SEAT_OF_MUSIC, [entry]), "a caller the manifest does not list")
    assert error.code != "unauthenticated", f"the signature gate refused it: {error}"
    assert not stored(fresh_agent, EXCHANGE_B_DOMAIN, entry.uri), "the stranger's push was stored"
    assert_typed_refusal(error, NOT_CATALOG_CONTRIBUTOR, [entry.path])


@obligation(Obligation.CATALOG_005, negative=True)
@CATALOG_TENANT_MISMATCH_UNTYPED.mark
def test_a_push_naming_another_tenant_is_refused_permission_denied(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """tenant_id names exchange-c's seat in a push to exchange-a: refused, nothing stored.

    Control: the same caller's push naming exchange-a's own seat is stored.
    """
    assert_push_naming_tenant_refused(compose_stack, fresh_agent, SEAT_OF_SFX)


@obligation(Obligation.CATALOG_007, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_push_for_an_unregistered_domain_is_refused_domain_not_verified(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """A domain no publisher ever registered at exchange-a: DOMAIN_NOT_VERIFIED, nothing stored."""
    domain = f"unregistered{uuid.uuid4().hex[:12]}.{SITE_ZONE}"
    entry = fresh_entry(domain)
    error = refusal(contributor(compose_stack).push(HOME_SEAT, [entry]), "an unregistered domain")
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the unregistered domain was stored"
    assert_typed_refusal(error, DOMAIN_NOT_VERIFIED, [entry.path])


@obligation(Obligation.CATALOG_013, negative=True)
@CATALOG_REFUSAL_UNTYPED.mark
def test_a_push_claiming_a_held_uri_is_refused_and_the_holder_is_unchanged(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Another content_id at a held URI: URI_UNAVAILABLE, and the holder's offer keeps its price."""
    catalog = contributor(compose_stack)
    holder = fresh_entry(terms=(license_term(model=PRICING_MODEL_FLAT, rate=1.5),))
    accepted(catalog.push(HOME_SEAT, [holder]))
    claimant = CatalogEntry(
        domain=holder.domain,
        path=holder.path,
        content_id=f"claimant-{uuid.uuid4().hex}",
        terms=(license_term(model=PRICING_MODEL_FLAT, rate=0.5),),
    )
    error = refusal(catalog.push(HOME_SEAT, [claimant]), "another resource at a held URI")
    offer = sole(offers(fresh_agent, HOME_EXCHANGE, holder.uri), "offer for the held URI")
    pricing = offer.offer.get("pricing") or {}
    assert Decimal(str(pricing.get("rate"))) == Decimal("1.5"), f"the holder changed: {pricing}"
    assert_typed_refusal(error, URI_UNAVAILABLE, [claimant.path])


@obligation(Obligation.CATALOG_014, negative=True)
def test_a_push_addressed_to_another_exchange_is_refused_untyped(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """exchange names exchange-b in a push sent to exchange-a: invalid_argument, no typed reason.

    The request is well formed and validly signed; only its recipient statement
    names another Exchange. Control: the same caller's push naming exchange-a is
    stored.
    """
    catalog = contributor(compose_stack)
    control = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [control]))
    assert stored(fresh_agent, HOME_EXCHANGE, control.uri), "the control push was not stored"

    entry = fresh_entry()
    push = catalog.push(HOME_SEAT, [entry])
    readdressed = Request(
        client=push.client, verb=push.verb, message={**push.message, "exchange": EXCHANGE_B_DOMAIN}
    )
    error = refusal(readdressed, "the push names another Exchange as its recipient")
    assert error.code == "invalid_argument", f"refused as {error.code!r}: {error}"
    assert typed_reasons(error) == [], f"a mis-addressed push carries a typed reason: {error}"
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the mis-addressed push was stored"


@obligation(Obligation.CATALOG_015, negative=True)
@CATALOG_ATTESTATIONS_UNVERIFIED.mark
def test_an_attestation_by_an_unauthorized_verifier_refuses_the_push(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The attestation's verifier is listed nowhere and its signature verifies under no key.

    Control: the same entry without the attestation is stored.
    """
    catalog = contributor(compose_stack)
    control = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [control]))
    entry = fresh_entry()
    attested = CatalogEntry(
        domain=entry.domain,
        path=entry.path,
        content_id=entry.content_id,
        terms=entry.terms,
        fields={
            "attestations": [
                {
                    "verifier": f"verifier{uuid.uuid4().hex[:8]}.example",
                    "keyid": "not-a-published-thumbprint",
                    "attested_at": "2026-10-01T00:00:00Z",
                    "uri": entry.uri,
                    "claims": {"word_count": 120},
                    "signature": "AAAA",
                }
            ]
        },
    )
    outcome = answer(catalog.push(HOME_SEAT, [attested]))
    CATALOG_ATTESTATIONS_UNVERIFIED.check(
        isinstance(outcome, CallError), f"the push was accepted: {outcome!r}"
    )
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the refused entry was stored"


@obligation(Obligation.CATALOG_017)
def test_a_successful_push_reports_its_size_and_warnings_refuse_nothing(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """Three entries, one with an unregistered restriction token: accepted 3, warned, all offered."""
    token = f"made-up-use-{uuid.uuid4().hex[:8]}"
    warned = fresh_entry(
        terms=(
            license_term(
                model=PRICING_MODEL_FLAT,
                rate=0.2,
                restrictions=(restriction(RESTRICTION_KIND_FUNCTION, permitted=(token,)),),
            ),
        )
    )
    entries = [fresh_entry(), warned, fresh_entry()]
    response = accepted(contributor(compose_stack).push(HOME_SEAT, entries))
    assert not response.rejected, f"a successful push reports rejected {response.rejected}"
    assert any(token in w for w in response.warnings or []), response.warnings
    for entry in entries:
        assert stored(fresh_agent, HOME_EXCHANGE, entry.uri), f"{entry.uri} was not stored"


@obligation(Obligation.CATALOG_018)
def test_a_re_push_replaces_the_resource_and_its_offers(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """FREE, then FLAT 2.25 at the same URI: one offer, at the FLAT price."""
    catalog = contributor(compose_stack)
    first = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [first]))
    second = CatalogEntry(
        domain=first.domain,
        path=first.path,
        content_id=first.content_id,
        terms=(license_term(model=PRICING_MODEL_FLAT, rate=2.25),),
    )
    accepted(catalog.push(HOME_SEAT, [second]))
    offer = sole(offers(fresh_agent, HOME_EXCHANGE, first.uri), "offer after the re-push")
    pricing = offer.offer.get("pricing") or {}
    assert pricing.get("model") == PRICING_MODEL_FLAT, pricing
    assert Decimal(str(pricing.get("rate"))) == Decimal("2.25"), pricing


@obligation(Obligation.CATALOG_018, negative=True)
def test_a_re_push_cannot_move_a_resource_to_another_uri(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The same content_id at another path: refused; the first URI still offered, the second not."""
    catalog = contributor(compose_stack)
    first = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [first]))
    moved = CatalogEntry(
        domain=first.domain,
        path=f"/articles/probe/moved-{uuid.uuid4().hex}.txt",
        content_id=first.content_id,
        terms=first.terms,
    )
    refusal(catalog.push(HOME_SEAT, [moved]), "the resource moved to another path")
    assert stored(fresh_agent, HOME_EXCHANGE, first.uri), "the resource left its first URI"
    assert not stored(fresh_agent, HOME_EXCHANGE, moved.uri), "the resource moved"


@obligation(Obligation.CATALOG_020)
def test_a_publisher_pushes_its_own_catalog(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the publisher's domain
    fresh_agent: FreshAgent,
) -> None:
    """The philosophy publisher signs as its own domain and pushes its own page: stored."""
    entry = fresh_entry(DEMO_PHILOSOPHY_DOMAIN)
    publisher = Catalog(compose_stack.exchange, keyfile_signer(SELFPUB_PHILOSOPHY_KEY_PATH))
    assert publisher.signer.signature_agent == f"https://{DEMO_PHILOSOPHY_DOMAIN}", publisher.signer
    accepted(publisher.push(HOME_SEAT, [entry]))
    assert stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the publisher's own push was not stored"


@obligation(Obligation.CATALOG_024)
@CATALOG_CALLER_ID_REQUIRED.mark
def test_a_push_without_caller_id_is_stored(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The listed contributor's push with no caller_id in the body: stored.

    caller_id is deprecated and never needed; the caller is whoever signs. The
    push is otherwise an ordinary push by the listed contributor, signed by its
    published key.
    """
    entry = fresh_entry()
    push = contributor(compose_stack).push(HOME_SEAT, [entry])
    message = {name: value for name, value in push.message.items() if name != "caller_id"}
    outcome = answer(Request(client=push.client, verb=push.verb, message=message))
    CATALOG_CALLER_ID_REQUIRED.check(
        not isinstance(outcome, CallError), f"the push without caller_id was refused: {outcome}"
    )
    assert not isinstance(outcome, CallError)
    assert outcome.accepted == 1, outcome
    assert stored(fresh_agent, HOME_EXCHANGE, entry.uri), (
        "the push without caller_id was not stored"
    )
