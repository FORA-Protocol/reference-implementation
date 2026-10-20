"""CatalogService's other two RPCs: RemoveResources and RefreshCatalog.

CATALOG-016 and CATALOG-021, from the pinned protocol module
``github.com/FORA-Protocol/protocol``: ``fora.proto`` defines both RPCs on
CatalogService beside PushResources, and the multi-tenant page states that the
push's authentication applies to them.

Each call is the SDK's own ``CatalogClient`` verb, signed as the caller's
keyfile; a removal names each resource by domain and path, the way a push does,
and what it left is read back through ``DiscoverResources`` as the test's own
agent. The reference Exchange implements PushResources only and
answers both calls ``unimplemented``, so each test is a strict xfail that holds
only while the answer is ``unimplemented``.
"""

from __future__ import annotations

import pytest
from fora_sdk.client import CallError

from ..catalog_calls import (
    HOME_EXCHANGE,
    HOME_SEAT,
    NOT_CATALOG_CONTRIBUTOR,
    Catalog,
    accepted,
    answer,
    contributor,
    fresh_entry,
    keyfile_signer,
    rejection,
    stored,
)
from ..defects import REFRESH_CATALOG_UNIMPLEMENTED, REMOVE_RESOURCES_UNIMPLEMENTED, Defect
from ..fresh_agent import FreshAgent
from ..obligation_registry import Obligation, obligation
from ..seed import SELFPUB_SFX_KEY_PATH, SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _implemented(defect: Defect, outcome: object) -> None:
    defect.check(
        not (isinstance(outcome, CallError) and outcome.code == "unimplemented"),
        f"answered {outcome}",
    )


@obligation(Obligation.CATALOG_016)
@REMOVE_RESOURCES_UNIMPLEMENTED.mark
def test_remove_resources_takes_a_pushed_entry_out_of_the_catalog(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The contributor removes an entry it pushed, named by domain and path: removed = 1.

    The URI is no longer offered afterwards.
    """
    catalog = contributor(compose_stack)
    entry = fresh_entry()
    accepted(catalog.push(HOME_SEAT, [entry]))
    assert stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the pushed entry was not stored"

    outcome = answer(catalog.remove(HOME_SEAT, [entry]))
    _implemented(REMOVE_RESOURCES_UNIMPLEMENTED, outcome)
    assert not isinstance(outcome, CallError), f"the removal was refused: {outcome}"
    assert outcome.removed == 1, outcome
    assert not stored(fresh_agent, HOME_EXCHANGE, entry.uri), "the removed entry is still offered"


@obligation(Obligation.CATALOG_016, negative=True)
@REMOVE_RESOURCES_UNIMPLEMENTED.mark
def test_a_caller_the_manifest_does_not_list_cannot_remove_entries(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
    fresh_agent: FreshAgent,
) -> None:
    """The sfx publisher asks exchange-a to remove the philosophy publisher's page: refused.

    RemoveResources refuses an unauthorized signer "as a push is refused", so the
    refusal carries catalog_rejection NOT_CATALOG_CONTRIBUTOR naming the page, the
    reason a push by a caller the manifest does not list carries (CATALOG-004).
    The reference Exchange does not implement the RPC, so a refusal of any other
    shape is a defect of the RPC once it exists, and fails here plainly.
    """
    entry = fresh_entry()
    accepted(contributor(compose_stack).push(HOME_SEAT, [entry]))
    stranger = Catalog(compose_stack.exchange, keyfile_signer(SELFPUB_SFX_KEY_PATH))
    outcome = answer(stranger.remove(HOME_SEAT, [entry]))
    _implemented(REMOVE_RESOURCES_UNIMPLEMENTED, outcome)
    assert isinstance(outcome, CallError), f"a stranger's removal was answered: {outcome!r}"
    assert outcome.code != "unauthenticated", f"the signature gate refused it: {outcome}"
    assert stored(fresh_agent, HOME_EXCHANGE, entry.uri), "a stranger removed the entry"
    reason, paths = rejection(outcome)
    assert (reason, paths) == (NOT_CATALOG_CONTRIBUTOR, [entry.path]), (
        f"refused {outcome.code} with {reason} naming {paths}, want {NOT_CATALOG_CONTRIBUTOR} "
        f"naming {entry.path}"
    )


@obligation(Obligation.CATALOG_021)
@REFRESH_CATALOG_UNIMPLEMENTED.mark
def test_refresh_catalog_is_answered(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed lists the contributor's publisher
) -> None:
    """A signed RefreshCatalog for exchange-a's own seat is answered with a RefreshCatalogResponse."""
    outcome = answer(contributor(compose_stack).refresh(HOME_SEAT))
    _implemented(REFRESH_CATALOG_UNIMPLEMENTED, outcome)
    assert not isinstance(outcome, CallError), f"the refresh was refused: {outcome}"
