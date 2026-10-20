"""Known product defects, each a strict xfail that holds only for its own failure.

A test that proves an obligation FORA does not meet yet is a strict xfail
(ADR-008 D3). A plain ``xfail(strict=True)`` holds for ANY exception, so a test
that broke in its setup, or on a different assertion, would still read as the
known defect. Each :class:`Defect` here closes that gap:

* :meth:`Defect.check` is the one assertion the defect breaks. It raises the
  defect's own exception class, a subclass of :class:`SpecViolation`;
* :meth:`Defect.mark` is the strict xfail, with ``raises`` naming that class, so
  the xfail holds only when that check fails. Any other failure, including a
  plain ``AssertionError`` from another assertion in the same test, fails the
  test.

The reason names the defect in plain words. It carries no ticket id: the
tracker entry points here, not the other way round. When the product is fixed,
the check passes, the strict xfail turns into a failure, and the marker is
removed together with the constant.
"""

from __future__ import annotations

from collections.abc import Callable
from dataclasses import dataclass, field
from typing import Any, TypeVar

import pytest

F = TypeVar("F", bound=Callable[..., Any])


class SpecViolation(AssertionError):
    """The product broke an obligation a test checks through a :class:`Defect`."""


@dataclass(frozen=True)
class Defect:
    """One product defect: what it is, and the exception its one check raises."""

    name: str
    reason: str
    exception: type[SpecViolation] = field(init=False)

    def __post_init__(self) -> None:
        object.__setattr__(self, "exception", type(self.name, (SpecViolation,), {}))

    @property
    def marker(self) -> pytest.MarkDecorator:
        """The strict xfail itself, for one case of a parametrized test.

        ``pytest.param(case, marks=defect.marker)`` marks only the case the defect
        breaks; :meth:`mark` marks a whole test.
        """
        return pytest.mark.xfail(strict=True, raises=self.exception, reason=self.reason)

    def mark(self, test: F) -> F:
        """Mark ``test`` as a strict xfail that holds only when :meth:`check` fails."""
        marked: F = self.marker(test)
        return marked

    def check(self, holds: bool, detail: str) -> None:
        """Assert the obligation this defect breaks; ``detail`` says what arrived."""
        if not holds:
            raise self.exception(f"{self.reason}: {detail}")


RELAYED_ACCEPTANCE_REFUSES_WHOLE_SUBREQUEST = Defect(
    "RelayedAcceptanceRefusesWholeSubRequest",
    "the Exchange refuses the whole relayed sub-request as unauthenticated when one item's "
    "AgentAcceptance does not verify, instead of denying that item in the body with "
    "DENIAL_REASON_SIGNATURE_INVALID and deciding the others",
)

RELAY_OPT_OUT_REFUSES_WHOLE_SUBREQUEST = Defect(
    "RelayOptOutRefusesWholeSubRequest",
    "the Exchange refuses the whole relayed sub-request with transaction_denial "
    "RELAY_NOT_ACCEPTED, instead of denying each item of a provider that does not accept "
    "relayed purchases in the body",
)

RESOLVE_BUDGET_REPORTED_AS_NOT_AUTHORIZED = Defect(
    "ResolveBudgetReportedAsNotAuthorized",
    "the Broker reports an offer the budget filtered out as OFFER_ABSENCE_REASON_NOT_AUTHORIZED, "
    "not OFFER_ABSENCE_REASON_BUDGET_EXCEEDED",
)


FOREIGN_IDEMPOTENCY_KEY_REFUSED = Defect(
    "ForeignIdempotencyKeyRefused",
    "the Exchange refuses an agent's purchase with permission_denied because another agent "
    "already used the same idempotency key, instead of scoping the key per agent",
)

RETRY_ON_ANOTHER_ROUTE_REFUSED = Defect(
    "RetryOnAnotherRouteRefused",
    "the Exchange binds an idempotency key to the digest of the agent's whole request "
    "acceptance, so the same items retried under the same key on another route, whose "
    "acceptance covers a different item set, are refused already_exists instead of being "
    "answered from the stored result",
)

PROJECTION_MISMATCH_REFUSED_WHOLE = Defect(
    "ProjectionMismatchRefusedWhole",
    "the Exchange refuses a request whose items are not the set the agent's request acceptance "
    "signs as unauthenticated, instead of answering OK and denying every item "
    "DENIAL_REASON_SIGNATURE_INVALID in the body",
)

DISPUTE_UNIMPLEMENTED = Defect(
    "DisputeUnimplemented",
    "the Exchange answers DisputeTransaction with unimplemented, so no dispute can be filed "
    "and no refusal names its check",
)

METERED_COST_FROM_UNIT_COST = Defect(
    "MeteredCostFromUnitCost",
    "the Exchange charges a metered item unit_cost times the estimate, not the estimate "
    "times the offer's rate",
)

REPORT_REFUSED_FOR_QUANTITY = Defect(
    "ReportRefusedForQuantity",
    "the Exchange refuses a usage report whose consumed quantity differs from the estimate by "
    "more than its tolerance, and any non-zero quantity when the offer states no estimate, "
    "though a report is never refused for its quantity",
)

RECORD_ESTIMATE_DROPPED = Defect(
    "RecordEstimateDropped",
    "the Exchange drops the estimate the publisher stated for the resource on "
    "ResourceEntry.estimated_quantity: its offer for a metered term that states no estimate "
    "of its own carries none, instead of carrying the resource's",
)

REUSED_REPORT_KEY_ANSWERED = Defect(
    "ReusedReportKeyAnswered",
    "the Exchange answers a usage report that reuses an idempotency key with a different report "
    "from the stored result, instead of refusing it with already_exists",
)

LATE_REPORT_ACCEPTED = Defect(
    "LateReportAccepted",
    "the Exchange accepts a usage report filed after the transaction's reporting window closed, "
    "instead of refusing it with usage_report_rejection WINDOW_EXPIRED",
)

USAGE_REPORT_REFUSAL_UNTYPED = Defect(
    "UsageReportRefusalUntyped",
    "the Exchange refuses a usage report without ErrorDetail.usage_report_rejection, so the "
    "refusal carries no typed reason",
)

CATALOG_REFUSAL_UNTYPED = Defect(
    "CatalogRefusalUntyped",
    "the Exchange refuses a catalog push without ErrorDetail.catalog_rejection: the reason and "
    "the entries that failed travel only in the message",
)

CATALOG_SIGNATURE_REFUSAL_UNTYPED = Defect(
    "CatalogSignatureRefusalUntyped",
    "the Exchange refuses a catalog call whose signature is missing or does not verify as "
    "unauthenticated without ErrorDetail.request_auth_failure, the typed reason every signed "
    "request's signature refusal carries",
)

CATALOG_TENANT_MISMATCH_UNTYPED = Defect(
    "CatalogTenantMismatchUntyped",
    "the Exchange refuses a push naming another tenant as invalid_argument with the reason in "
    "the message, instead of permission_denied with catalog_rejection TENANT_MISMATCH",
)

CATALOG_CONTRIBUTOR_REFUSAL_NOT_PERMISSION_DENIED = Defect(
    "CatalogContributorRefusalNotPermissionDenied",
    "the Exchange refuses a catalog call from a signer not authorized for an entry's domain as "
    "invalid_argument with the reason in the message, instead of permission_denied with "
    "catalog_rejection NOT_CATALOG_CONTRIBUTOR",
)

CATALOG_ATTESTATIONS_UNVERIFIED = Defect(
    "CatalogAttestationsUnverified",
    "the Exchange stores the attestations a push carries without verifying them, so a push "
    "carrying an attestation by a verifier the publisher never authorized is accepted",
)

CATALOG_CALLER_ID_REQUIRED = Defect(
    "CatalogCallerIdRequired",
    "the Exchange refuses a catalog push that leaves the deprecated caller_id empty, though the "
    "caller is whoever signs the request and each entry's domain names whose resource it is",
)

CATALOG_PUSH_IGNORES_EXT_CRITICAL = Defect(
    "CatalogPushIgnoresExtCritical",
    "the Exchange applies a catalog push whose own ext_critical names a key it does not "
    "understand, instead of refusing it with CATALOG_REJECTION_REASON_UNKNOWN_CRITICAL_EXTENSION",
)

BROKER_PURCHASE_IGNORES_EXT_CRITICAL = Defect(
    "BrokerPurchaseIgnoresExtCritical",
    "the Broker re-packages and sends on a purchase whose ext_critical names a key it does not "
    "understand, instead of answering OK with every item denied "
    "DENIAL_REASON_UNKNOWN_CRITICAL_EXTENSION and contacting no Exchange",
)

REGISTRATION_IGNORES_EXT_CRITICAL = Defect(
    "RegistrationIgnoresExtCritical",
    "the Exchange registers an agent whose Register request names, in ext_critical, a key it does "
    "not understand, first registration or repeat, instead of refusing it invalid_argument with "
    "REGISTRATION_FAILURE_REASON_UNKNOWN_CRITICAL_EXTENSION",
)

PURCHASE_IGNORES_EXT_CRITICAL = Defect(
    "PurchaseIgnoresExtCritical",
    "the Exchange sells the items of a purchase whose ext_critical names a key it does not "
    "understand, instead of denying every item with DENIAL_REASON_UNKNOWN_CRITICAL_EXTENSION",
)

REPORT_IGNORES_EXT_CRITICAL = Defect(
    "ReportIgnoresExtCritical",
    "the Exchange accepts a usage report whose ext_critical names a key it does not understand, "
    "instead of refusing it with USAGE_REPORT_REJECTION_REASON_UNKNOWN_CRITICAL_EXTENSION",
)

REMOVE_RESOURCES_UNIMPLEMENTED = Defect(
    "RemoveResourcesUnimplemented",
    "the Exchange answers CatalogService.RemoveResources with unimplemented, so no catalog "
    "entry can be removed through the protocol",
)

REFRESH_CATALOG_UNIMPLEMENTED = Defect(
    "RefreshCatalogUnimplemented",
    "the Exchange answers CatalogService.RefreshCatalog with unimplemented",
)

COMP_INPUT_UNVALIDATED = Defect(
    "CompInputUnvalidated",
    "the Exchange accepts a push whose comp.* ext value breaks the fora-comp-v1 profile, "
    "instead of refusing it",
)

COMP_RENDERED_NESTED = Defect(
    "CompRenderedNested",
    'the Exchange renders fora-comp-v1 as one nested Offer.ext["comp"] object, not as the '
    "flat comp.* keys the profile defines, so no offer carries comp.package_id",
)

EXT_WITHHELD_WITHOUT_PROFILES = Defect(
    "ExtWithheldWithoutProfiles",
    "the Exchange withholds profile ext fields from a query that omits supported_profiles, "
    "though absence means send all available metadata",
)

BROKER_DROPS_SUPPORTED_PROFILES = Defect(
    "BrokerDropsSupportedProfiles",
    "the Broker does not forward the agent's supported_profiles into the ResourceQuery it "
    "sends each Exchange, so the agent never receives a declared profile's fields",
)

EXCHANGE_IGNORES_EXT_CRITICAL = Defect(
    "ExchangeIgnoresExtCritical",
    "the Exchange answers a query whose ext_critical names a key it does not understand as if "
    "the key were absent, instead of answering every group UNKNOWN_CRITICAL_EXTENSION",
)

BROKER_IGNORES_EXT_CRITICAL = Defect(
    "BrokerIgnoresExtCritical",
    "the Broker answers a Resolve whose ext_critical names a key it does not understand as if "
    "the key were absent, instead of answering UNKNOWN_CRITICAL_EXTENSION",
)

EDGE_REFUSAL_WITHOUT_HINT = Defect(
    "EdgeRefusalWithoutHint",
    "the edge sends X-Content-Rules only on its AI-bot refusal: the 403 to an expired or "
    "tampered signed URL carries no discovery header",
)

EDGE_AI_BOT_TOKEN_UNLISTED = Defect(
    "EdgeAiBotTokenUnlisted",
    "the edge's built-in AI-bot list has no pattern for cohere-ai or fora-ai-buyer, which the "
    "protocol's maintained list names, so an unsigned read of licensed content under either "
    "User-Agent passes through to the origin instead of being refused 403",
)

EDGE_AI_BOT_TOKEN_READ_AS_SEARCH = Defect(
    "EdgeAiBotTokenReadAsSearch",
    "the edge's search-crawler allow list matches Googlebot-Extended, an AI crawler the "
    "protocol's maintained list names, so an unsigned read of licensed content under it "
    "passes through as a search crawler's instead of being refused 403",
)

EDGE_EXCHANGE_HINT_NOT_BARE = Defect(
    "EdgeExchangeHintNotBare",
    "the edge sends its configured Exchange endpoint URL in X-FORA-Exchange, not the "
    "Exchange's bare domain, so the hint never matches the manifest's exchanges[].domain",
)


LEGACY_UNSIGNED_REGISTRATION_ROUTE = Defect(
    "LegacyUnsignedRegistrationRoute",
    "the Exchange still serves the unauthenticated POST /exchange/v1/agents/register, which "
    "registers an agent that no request signature identifies; the protocol makes an agent "
    "known only by a signed request and opens its account only through the signed Register",
)

DIRECT_REQUESTER_DOMAIN_UNBOUND = Defect(
    "DirectRequesterDomainUnbound",
    "the Exchange answers a direct request, a discovery or a purchase, whose requester.domain "
    "is not the host of its Signature-Agent, instead of refusing it as SIGNATURE_INVALID",
)

BROKER_REVOCATION_LIST_STATES_NO_LIFETIME = Defect(
    "BrokerRevocationListStatesNoLifetime",
    "the Broker serves its key revocation list with no Cache-Control, so an intermediary "
    "cache may keep it for a heuristic lifetime, instead of no-store or a max-age of 60 "
    "seconds or less",
)

IDENTITY_REVOCATION_LIST_CACHED_TOO_LONG = Defect(
    "IdentityRevocationListCachedTooLong",
    "the identity service serves an agent's key revocation list with the max-age of its key "
    "directory, 300 seconds by default, instead of no-store or a max-age of 60 seconds or less",
)

BROKER_UNSIGNED_RESOLVE_UNTYPED = Defect(
    "BrokerUnsignedResolveUntyped",
    "the Broker refuses an unsigned Resolve as unauthenticated without ErrorDetail."
    "request_auth_failure, instead of carrying the reason SIGNATURE_MISSING",
)

AGENT_PINNED_TO_ONE_KEY = Defect(
    "AgentPinnedToOneKey",
    "the Exchange pins one key per agent, the first currently-valid key its directory "
    "publishes, and refuses a request or acceptance signed by another key the directory "
    "publishes as valid, so during a rotation overlap only one of the two keys works",
)

KEY_WINDOW_MOVES_ON_RESTART = Defect(
    "KeyWindowMovesOnRestart",
    "the Exchange, the Broker and the console derive each published key's not_before and "
    "not_after from the clock every time they build their key directory, so a restart moves "
    "the validity window of a key that did not change, instead of keeping it fixed for the key",
)

BROKER_FAN_OUT_NAMES_UNVERIFIED_REQUESTER = Defect(
    "BrokerFanOutNamesUnverifiedRequester",
    "the Broker copies the requester.domain the agent wrote into every ResourceQuery it fans "
    "out, instead of the host of the Signature-Agent it verified at Resolve",
)

BROKER_NARROWS_ACCEPTABLE_RESTRICTIONS = Defect(
    "BrokerNarrowsAcceptableRestrictions",
    "the Broker forwards only the first value of the agent's FUNCTION restriction in "
    "ResourceQuery.acceptable_restrictions and drops every other axis and value, instead of "
    "forwarding the agent's acceptable_restrictions unchanged",
)

MCP_TOOL_ERROR_DETAIL_DROPPED = Defect(
    "McpToolErrorDetailDropped",
    "the identity service answers an MCP tool error with text only: its structured content is "
    'not {"refusal": UpstreamRefusal}, so the refusing party, the Connect code it answered '
    "with and the FORA ErrorDetail it attached are not passed through",
)

FOREIGN_REPORT_NOT_HIDDEN = Defect(
    "ForeignReportNotHidden",
    "the Exchange refuses a usage report for a transaction recorded under another agent with "
    "permission_denied and no typed reason, instead of answering it exactly as for an unknown "
    "transaction, not_found with usage_report_rejection TRANSACTION_NOT_FOUND, so the answer "
    "reveals that the transaction exists",
)
