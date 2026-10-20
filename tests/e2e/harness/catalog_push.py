"""The catalog vocabulary the e2e harness pushes: a resource entry and its terms.

:class:`CatalogEntry` is one ``fora.v1.ResourceEntry`` and :func:`license_term`
and :func:`restriction` build its terms under the canonical snake_case wire
names, with the enum names the wire carries as constants beside them.

Sending a push is :mod:`harness.catalog_calls`: ``Catalog(...).push(...)`` builds
the ``PushResourcesRequest`` and the SDK's own ``CatalogClient`` signs and sends
it, so this module holds no request body and no client.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from typing import Any


def _format_money(rate: float) -> str:
    """Render a numeric rate as the canonical FORA wire money STRING.

    Money-as-string: ``Pricing.rate`` (and every other money field) is an
    exact decimal STRING on the wire, never a JSON number — the Exchange's
    protovalidate rejects ``{"rate": 0.0}`` ("invalid value for string field
    rate"). This mirrors the Go ``helpers.FormatMoney`` semantics the
    Exchange itself uses (sdk/go/helpers/money.go): no sign, no exponent,
    insignificant trailing fractional zeros stripped — 5.0 -> "5", 0.05 ->
    "0.05", 0 -> "0". ``Decimal(str(rate))`` takes the exact decimal from the
    literal (avoiding binary-float artefacts) before normalising.
    """
    text = format(Decimal(str(rate)), "f")
    if "." in text:
        text = text.rstrip("0").rstrip(".")
    return text or "0"


@dataclass(frozen=True)
class CatalogEntry:
    """One resource to seed. Mirrors ``fora.v1.ResourceEntry`` on the wire."""

    domain: str
    path: str
    content_id: str
    # Publisher-declared licensing terms: LicenseTerm dicts under the canonical
    # snake_case wire names. Each term is surfaced on Offer.terms after the
    # Exchange projects the entry's terms by the requester's scope coverage
    # (``selectTerms`` in ``src/exchange/internal/service/termselect.go``). Use
    # :func:`license_term` to build a well-formed dict. Empty = the entry is
    # pushed with the default per-access term (``catalog_calls.DEFAULT_ENTRY_RATE``).
    terms: tuple[dict[str, Any], ...] = ()
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
PRICING_MODEL_FREE = "PRICING_MODEL_FREE"
PRICING_MODEL_PER_UNIT = "PRICING_MODEL_PER_UNIT"
PRICING_MODEL_FLAT = "PRICING_MODEL_FLAT"

# TermSemantics enum-name constants. Every LicenseTerm MUST declare its
# semantics (UNSPECIFIED is rejected at ingest by protovalidate CEL
# license_term.semantics_specified). ENUMERATED = the machine fields are
# authoritative (the default for priced terms with no governing document);
# REFERENCE_ONLY = the machine fields defer to a License document and MUST
# carry a non-empty license.uri.
TERM_SEMANTICS_ENUMERATED = "TERM_SEMANTICS_ENUMERATED"
TERM_SEMANTICS_REFERENCE_ONLY = "TERM_SEMANTICS_REFERENCE_ONLY"

RESTRICTION_KIND_FUNCTION = "RESTRICTION_KIND_FUNCTION"
RESTRICTION_KIND_USER_TYPE = "RESTRICTION_KIND_USER_TYPE"
RESTRICTION_KIND_GEOGRAPHY = "RESTRICTION_KIND_GEOGRAPHY"

OBLIGATION_KIND_SHARE_ALIKE = "OBLIGATION_KIND_SHARE_ALIKE"

# Every Obligation MUST declare its firing trigger; UNSPECIFIED is rejected at
# ingest (protovalidate CEL obligation.trigger_specified).
OBLIGATION_TRIGGER_ON_USE = "OBLIGATION_TRIGGER_ON_USE"
OBLIGATION_TRIGGER_ON_DISTRIBUTION = "OBLIGATION_TRIGGER_ON_DISTRIBUTION"


def restriction(
    kind: str,
    *,
    permitted: tuple[str, ...] = (),
    prohibited: tuple[str, ...] = (),
    advisory: bool = False,
) -> dict[str, Any]:
    """Build a Restriction dict under the canonical snake_case wire names.

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
    return r


def license_term(
    *,
    model: str,
    rate: float = 0.0,
    currency: str = "USD",
    unit: str | None = None,
    unit_cost: float | None = None,
    estimated_quantity: int | None = None,
    restrictions: tuple[dict[str, Any], ...] = (),
    obligations: tuple[dict[str, Any], ...] = (),
    semantics: str = TERM_SEMANTICS_ENUMERATED,
) -> dict[str, Any]:
    """Build a LicenseTerm dict under the canonical snake_case wire names.

    Pricing is always emitted — the Exchange hard-rejects a term with no
    Pricing. ``model=FREE`` must keep ``rate=0`` (protovalidate CEL).
    ``model=PER_UNIT`` requires ``unit`` (protovalidate CEL); pass it.
    ``semantics`` defaults to ENUMERATED (machine fields authoritative); it MUST
    be set — UNSPECIFIED is rejected at ingest. REFERENCE_ONLY additionally
    requires a non-empty ``license.uri``. ``unit_cost`` is the per-unit charge
    when it differs from the headline ``rate``; left out, the Exchange charges
    the rate.
    """
    # Money-as-string: rate rides as a canonical decimal STRING on the wire.
    pricing: dict[str, Any] = {"model": model, "rate": _format_money(rate), "currency": currency}
    if unit is not None:
        pricing["unit"] = unit
    if unit_cost is not None:
        pricing["unit_cost"] = _format_money(unit_cost)
    if estimated_quantity is not None:
        pricing["estimated_quantity"] = estimated_quantity
    term: dict[str, Any] = {"semantics": semantics, "pricing": pricing}
    if restrictions:
        term["restrictions"] = list(restrictions)
    if obligations:
        term["obligations"] = list(obligations)
    return term
