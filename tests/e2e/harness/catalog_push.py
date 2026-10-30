"""The catalog vocabulary the e2e harness pushes: a resource entry and its terms.

:class:`CatalogEntry` is one ``fora.v1.ResourceEntry``. :func:`license_term` and
:func:`restriction` build its terms as the generated ``LicenseTerm`` and
``Restriction`` models, each refused unless the protocol's strict check accepts it,
so a member the protocol does not define fails here rather than being dropped. The
enum names the wire carries are constants beside them, taken from the generated
enums.

Sending a push is :mod:`harness.catalog_calls`: ``Catalog(...).push(...)`` builds
the ``PushResourcesRequest`` and the SDK's own ``CatalogClient`` signs and sends
it, so this module holds no request body and no client.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from decimal import Decimal
from typing import Any

from fora_sdk import to_wire
from fora_sdk.money import format_money
from wire.models import (
    LicenseTerm,
    ObligationKind,
    ObligationTrigger,
    PricingModel,
    Restriction,
    RestrictionKind,
    TermSemantics,
)

from .protocol_documents import parse_document


def _money(value: float) -> str:
    """``value`` as the protocol's money string, from the exact decimal the literal states.

    Money travels as a decimal string, never a JSON number; ``Decimal(str(value))``
    takes the decimal the literal states rather than its binary float.
    """
    return format_money(Decimal(str(value)))


@dataclass(frozen=True)
class CatalogEntry:
    """One resource to seed. Mirrors ``fora.v1.ResourceEntry`` on the wire."""

    domain: str
    path: str
    content_id: str
    # Publisher-declared licensing terms, built by :func:`license_term`. Each term
    # is surfaced on Offer.terms after the Exchange projects the entry's terms by
    # the requester's scope coverage (``selectTerms`` in
    # ``src/exchange/internal/service/termselect.go``). Empty = the entry is pushed
    # with the default per-access term (``catalog_calls.DEFAULT_ENTRY_RATE``).
    terms: tuple[LicenseTerm, ...] = ()
    # ResourceEntry.estimated_quantity: the resource's estimate, in its metered
    # term's unit, which the protocol lets a publisher state once here instead of
    # on each term. None leaves it unset.
    estimated_quantity: int | None = None
    # ResourceEntry.ext and ResourceEntry.ext_critical, carried as given.
    ext: dict[str, Any] | None = None
    ext_critical: tuple[str, ...] = ()
    # Further ResourceEntry fields under their wire names (title, attestations,
    # resource_mutability, ...), merged into the entry as given.
    fields: dict[str, Any] | None = None

    @property
    def uri(self) -> str:
        """The catalog URI the entry is stored under in this stack (http, the stack's scheme)."""
        return f"http://{self.domain}{self.path}"


# Enum-name constants for LicenseTerm building, as the wire carries them.
PRICING_MODEL_FREE = PricingModel.PRICING_MODEL_FREE.value
PRICING_MODEL_PER_UNIT = PricingModel.PRICING_MODEL_PER_UNIT.value
PRICING_MODEL_FLAT = PricingModel.PRICING_MODEL_FLAT.value

# TermSemantics enum-name constants. Every LicenseTerm MUST declare its
# semantics (UNSPECIFIED is rejected at ingest by protovalidate CEL
# license_term.semantics_specified). ENUMERATED = the machine fields are
# authoritative (the default for priced terms with no governing document);
# REFERENCE_ONLY = the machine fields defer to a License document and MUST
# carry a non-empty license.uri.
TERM_SEMANTICS_ENUMERATED = TermSemantics.TERM_SEMANTICS_ENUMERATED.value
TERM_SEMANTICS_REFERENCE_ONLY = TermSemantics.TERM_SEMANTICS_REFERENCE_ONLY.value

RESTRICTION_KIND_FUNCTION = RestrictionKind.RESTRICTION_KIND_FUNCTION.value
RESTRICTION_KIND_USER_TYPE = RestrictionKind.RESTRICTION_KIND_USER_TYPE.value
RESTRICTION_KIND_GEOGRAPHY = RestrictionKind.RESTRICTION_KIND_GEOGRAPHY.value

OBLIGATION_KIND_SHARE_ALIKE = ObligationKind.OBLIGATION_KIND_SHARE_ALIKE.value

# Every Obligation MUST declare its firing trigger; UNSPECIFIED is rejected at
# ingest (protovalidate CEL obligation.trigger_specified).
OBLIGATION_TRIGGER_ON_USE = ObligationTrigger.OBLIGATION_TRIGGER_ON_USE.value
OBLIGATION_TRIGGER_ON_DISTRIBUTION = ObligationTrigger.OBLIGATION_TRIGGER_ON_DISTRIBUTION.value


def restriction(
    kind: str,
    *,
    permitted: tuple[str, ...] = (),
    prohibited: tuple[str, ...] = (),
    advisory: bool = False,
) -> Restriction:
    """Build a Restriction, refused unless the protocol's strict check accepts it.

    ``advisory`` mirrors proto ``Restriction.advisory`` (replaced the old
    ``critical``, semantics inverted): omitted/false = binding (the default),
    true = advisory. Under scope-only projection the Exchange does not evaluate
    restrictions at discovery, so this flag is offer metadata for the agent's
    self-selection (ADR-014 §Selection).
    """
    r: dict[str, Any] = {"kind": kind}
    if permitted:
        r["permitted"] = list(permitted)
    if prohibited:
        r["prohibited"] = list(prohibited)
    if advisory:
        r["advisory"] = True
    return parse_document(Restriction, json.dumps(r))


def license_term(
    *,
    model: str,
    rate: float = 0.0,
    currency: str = "USD",
    unit: str | None = None,
    unit_cost: float | None = None,
    estimated_quantity: int | None = None,
    restrictions: tuple[Restriction, ...] = (),
    obligations: tuple[dict[str, Any], ...] = (),
    semantics: str = TERM_SEMANTICS_ENUMERATED,
    **fields: Any,
) -> LicenseTerm:
    """Build a LicenseTerm, refused unless the protocol's strict check accepts it.

    Pricing is always emitted — the Exchange hard-rejects a term with no
    Pricing. ``model=FREE`` must keep ``rate=0`` (protovalidate CEL).
    ``model=PER_UNIT`` requires ``unit`` (protovalidate CEL); pass it.
    ``semantics`` defaults to ENUMERATED (machine fields authoritative); it MUST
    be set — UNSPECIFIED is rejected at ingest. REFERENCE_ONLY additionally
    requires a non-empty ``license.uri``. ``unit_cost`` is the per-unit charge
    when it differs from the headline ``rate``; left out, the Exchange charges
    the rate. ``obligations`` are given as their wire objects, the form a test
    compares the offered term with. ``fields`` sets any other LicenseTerm member
    (``license``, ``quotas``, ``part_label``, ...) under its wire name.
    """
    pricing: dict[str, Any] = {"model": model, "rate": _money(rate), "currency": currency}
    if unit is not None:
        pricing["unit"] = unit
    if unit_cost is not None:
        pricing["unit_cost"] = _money(unit_cost)
    if estimated_quantity is not None:
        pricing["estimated_quantity"] = estimated_quantity
    term: dict[str, Any] = {"semantics": semantics, "pricing": pricing, **fields}
    if restrictions:
        term["restrictions"] = [to_wire(r) for r in restrictions]
    if obligations:
        term["obligations"] = list(obligations)
    return parse_document(LicenseTerm, json.dumps(term))
