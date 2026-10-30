"""Signature-Input and Accept-Signature, read with the SDK's structured-field parser.

Both fields are Dictionaries of Inner Lists (RFC 9421 Sections 4.1 and 5.1). Each
member names one signature by its label, lists the components it covers and carries
its parameters: what a signature declares in Signature-Input, or what a refusal asks
a signature to cover in Accept-Signature. The harness's own modules parse either
field here, whichever request it holds: one an SDK client sent, one the capture
proxy recorded, or the refusal a verifier answered.

Two kinds of code read the fields without this parser. The third-party
conformance oracles (``harness.wba_oracle``, ``harness.wba_oracle_ts``,
``harness.wba_checks``, ``harness.wba_directory``, ``harness.wba_pyauth``) parse
them with their own libraries on purpose, so a defect the SDK's parser shares with
the services cannot hide (ADR-028 D2). And code that needs no parse works on the
raw value: it hands the field to a verifier, checks that one quoted component is
present or absent, or rewrites or drops the field to build a negative.
"""

from __future__ import annotations

from dataclasses import dataclass

from fora_sdk import sfv


@dataclass(frozen=True)
class DeclaredSignature:
    """One signature as a Signature-Input or Accept-Signature member declares it.

    ``components`` are the covered components written as in the field, parameters
    included (``"@method"``, ``"signature-agent";key="sig1"``); ``names`` are the
    same components without quotes or parameters (``@method``, ``signature-agent``).
    ``params`` holds ``created``, ``expires``, ``keyid``, ``nonce``, ``tag`` and
    the rest.
    """

    components: tuple[str, ...]
    names: tuple[str, ...]
    params: dict[str, object]


def declared_signatures(
    field_lines: list[str], *, field: str = "Signature-Input", where: str = "the request"
) -> dict[str, DeclaredSignature]:
    """Each signature ``field_lines`` declare, by label; none when the field is absent.

    ``where`` names the request in the failure for a malformed field.
    """
    if not field_lines:
        return {}
    try:
        members = sfv.parse_dictionary(field_lines)
    except sfv.StructuredFieldError as exc:
        raise AssertionError(f"{where} carries a malformed {field}") from exc
    declared: dict[str, DeclaredSignature] = {}
    for label, member in members.items():
        assert isinstance(member, sfv.InnerList), f"{where}: {field} member {label} is no list"
        declared[label] = DeclaredSignature(
            components=tuple(sfv.serialize_item(item) for item in member.items),
            names=tuple(str(item.value) for item in member.items),
            params=dict(member.params),
        )
    return declared
