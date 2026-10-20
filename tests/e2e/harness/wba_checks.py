"""The WG-00 verifier checks the WBA oracle applies around pyauth's signature check.

RFC 9421 verification proves a signature over some components; Web Bot Auth
(draft-ietf-webbotauth-httpsig-protocol-00, "WG-00") then says which components
and parameters a signature must have before a verifier attributes it. pyauth
does none of that, so it is written out here, one named :class:`Check` per rule,
so a failing conformance test says which rule FORA broke rather than "invalid
signature".

Each :class:`Check` member names the WG-00 section it enforces, or says why it
is there when WG-00 has no rule for it. Where WG-00 only RECOMMENDs something, or
leaves it open, the knob for it is on :class:`Require` with the reason, and its
default is stated there.
"""

from __future__ import annotations

import base64
import binascii
import re
import urllib.parse
from collections.abc import Mapping
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

import httpx
from http_message_signatures import http_sfv


class Check(StrEnum):
    """One WG-00 verifier rule. :func:`wba_oracle.verify` runs them in this order."""

    # Signature and Signature-Input parse as Dictionary fields with matching
    # labels (RFC 9421 Section 4; WG-00 Section 5.4, steps 1 to 3).
    PARSE = "parse"
    # Exactly one signature is selected: the label asked for, or the only one
    # tagged web-bot-auth.
    SELECT = "select"
    # tag="web-bot-auth" (WG-00 Section 5.2 MUST).
    TAG = "tag"
    # Only alg/created/expires/keyid/nonce/tag. RFC 9421 permits others; pyauth
    # rejects them, and so would any verifier built on it.
    PARAMS = "params"
    # alg, when present, is "ed25519": the oracle verifies Ed25519 only.
    ALG = "alg"
    # created and expires are integers (WG-00 Section 5.2 MUST), created is not
    # after expires or in the verifier's future, expires has not passed, and the
    # signature is no older than Require.max_age.
    EXPIRY = "expiry"
    # expires - created is at most Require.max_lifetime (WG-00 Section 5.2
    # RECOMMENDS no more than 24 hours).
    LIFETIME = "lifetime"
    # nonce present and of Require.nonce_bytes bytes, when that is set.
    NONCE = "nonce"
    # bare @authority or bare @target-uri is covered (WG-00 Section 5.2 MUST).
    TARGET = "target"
    # A Signature-Agent header is present and the signature covers one of its
    # members, which names an https origin (WG-00 Sections 5.2.1 and 5.5).
    SIGNATURE_AGENT = "signature_agent"
    # Every component in Require.components is covered.
    COMPONENTS = "components"
    # keyid is an unpadded base64url SHA-256 JWK thumbprint (WG-00 Section 5.2
    # MUST) and names a key the verifier was given.
    KEYID = "keyid"
    # The RFC 9421 signature verifies (pyauth).
    SIGNATURE = "signature"


ALLOWED_PARAMS = frozenset({"alg", "created", "expires", "keyid", "nonce", "tag"})
DIRECTORY_TYPE = "directory"
URI_TYPES = frozenset({"jwks_uri", "cimd"})
# An unpadded base64url SHA-256 digest is 43 characters of the URL-safe alphabet.
THUMBPRINT_RE = re.compile(r"^[A-Za-z0-9_-]{43}$")


@dataclass(frozen=True)
class Require:
    """What a verification run demands beyond the WG-00 MUSTs, with defaults.

    The defaults are WG-00 as written plus a 24-hour limit on age and on
    lifetime. WG-00 Section 5.2 RECOMMENDS the lifetime limit; the age limit is
    the default of both pyauth's and the Cloudflare library's verifier.
    """

    # WG-00 Section 5.2.1: signers MUST send the dictionary form; a verifier
    # MAY accept the legacy sf-string. False accepts it, as the E.2.2 vector needs.
    dictionary_form: bool = True
    # WG-00 Section 5.2.1 asks for the member keyed to the signature label, while
    # its own Appendix E.2.1 vector signs member "agent2" under label "sig2".
    # False accepts any one covered member, as that vector needs.
    member_key_is_label: bool = False
    # WG-00 does not require alg. The Cloudflare web-bot-auth verifier does.
    alg: bool = False
    max_age: int | None = 86_400
    max_lifetime: int | None = 86_400
    clock_skew: int = 5
    # WG-00 sets no nonce rule. 64 is what Appendix E and the Cloudflare
    # verifier use; None skips the check.
    nonce_bytes: int | None = None
    # Extra components a caller's policy needs covered, written as in
    # Signature-Input: "@method", "content-digest", '"@query-param";name="q"'.
    components: tuple[str, ...] = ()


class WBAFailure(Exception):
    """A named check failed. Raised inside the oracle, returned as a result."""

    def __init__(self, check: Check, detail: str) -> None:
        super().__init__(f"{check}: {detail}")
        self.check = check
        self.detail = detail


@dataclass(frozen=True)
class VerifyResult:
    """The outcome of one :func:`wba_oracle.verify` run."""

    ok: bool
    failed: Check | None
    detail: str
    label: str | None = None
    keyid: str | None = None
    signature_agent: str | None = None
    covered: tuple[str, ...] = ()
    params: dict[str, Any] = field(default_factory=dict)

    @classmethod
    def failure(cls, failure: WBAFailure) -> VerifyResult:
        return cls(ok=False, failed=failure.check, detail=failure.detail)


@dataclass(frozen=True)
class SelectedSignature:
    """One Signature-Input member: its label, covered components and parameters."""

    label: str
    components: tuple[http_sfv.Item, ...]
    params: Mapping[str, Any]

    @property
    def covered(self) -> tuple[str, ...]:
        return tuple(str(c) for c in self.components)


def parse_dictionary(headers: httpx.Headers, name: str, check: Check) -> http_sfv.Dictionary:
    """Parse header ``name`` as a Dictionary Structured Field or fail ``check``."""
    if name not in headers:
        raise WBAFailure(check, f"{name} header is missing")
    parsed = http_sfv.Dictionary()
    try:
        parsed.parse(headers[name].encode())
    except ValueError as exc:
        raise WBAFailure(check, f"{name} is not a Dictionary Structured Field") from exc
    return parsed


def parse_signature_fields(
    headers: httpx.Headers, *, label: str | None, tag: str
) -> SelectedSignature:
    """Parse Signature/Signature-Input and select the one signature to verify."""
    inputs = parse_dictionary(headers, "Signature-Input", Check.PARSE)
    signatures = parse_dictionary(headers, "Signature", Check.PARSE)
    if set(inputs) != set(signatures):
        raise WBAFailure(Check.PARSE, "Signature and Signature-Input carry different labels")
    if label is not None:
        if label not in inputs:
            raise WBAFailure(Check.SELECT, f"no signature labelled {label!r}")
        chosen = label
    else:
        tagged = [name for name, member in inputs.items() if member.params.get("tag") == tag]
        if not tagged:
            raise WBAFailure(Check.TAG, f"no signature is tagged {tag!r}")
        if len(tagged) > 1:
            raise WBAFailure(Check.SELECT, f"{len(tagged)} signatures are tagged {tag!r}")
        chosen = tagged[0]
    member = inputs[chosen]
    if not isinstance(member, http_sfv.InnerList):
        raise WBAFailure(Check.PARSE, f"Signature-Input {chosen} is not an Inner List")
    if member.params.get("tag") != tag:
        raise WBAFailure(Check.TAG, f"tag is {member.params.get('tag')!r}, not {tag!r}")
    return SelectedSignature(label=chosen, components=tuple(member), params=dict(member.params))


def _integer(params: Mapping[str, Any], name: str) -> int:
    value = params.get(name)
    if not isinstance(value, int) or isinstance(value, bool):
        raise WBAFailure(Check.EXPIRY, f"{name} is {value!r}, not an integer")
    return value


def check_signature_params(params: Mapping[str, Any], policy: Require, *, now: int) -> None:
    """Apply the PARAMS, ALG, EXPIRY, LIFETIME and NONCE checks."""
    unknown = sorted(set(params) - ALLOWED_PARAMS)
    if unknown:
        raise WBAFailure(Check.PARAMS, f"unexpected signature parameters {unknown}")
    alg = params.get("alg")
    if alg is None and policy.alg:
        raise WBAFailure(Check.ALG, "alg is missing")
    if alg is not None and alg != "ed25519":
        raise WBAFailure(Check.ALG, f"alg is {alg!r}, not 'ed25519'")
    created = _integer(params, "created")
    expires = _integer(params, "expires")
    if created > expires:
        raise WBAFailure(Check.EXPIRY, f"created {created} is after expires {expires}")
    if created > now + policy.clock_skew:
        raise WBAFailure(Check.EXPIRY, f"created {created} is in the future (now {now})")
    if expires < now - policy.clock_skew:
        raise WBAFailure(Check.EXPIRY, f"expires {expires} has passed (now {now})")
    if policy.max_age is not None and now - created > policy.max_age + policy.clock_skew:
        raise WBAFailure(Check.EXPIRY, f"signature is {now - created}s old, over {policy.max_age}s")
    if policy.max_lifetime is not None and expires - created > policy.max_lifetime:
        lifetime = expires - created
        raise WBAFailure(Check.LIFETIME, f"lifetime {lifetime}s is over {policy.max_lifetime}s")
    if policy.nonce_bytes is not None:
        nonce = params.get("nonce")
        decoded = b64_any(nonce) if isinstance(nonce, str) else None
        if decoded is None or len(decoded) != policy.nonce_bytes:
            raise WBAFailure(Check.NONCE, f"nonce {nonce!r} is not {policy.nonce_bytes} bytes")


def check_coverage(headers: httpx.Headers, selected: SelectedSignature, policy: Require) -> str:
    """Apply the TARGET, SIGNATURE_AGENT and COMPONENTS checks.

    Returns the Signature-Agent URL the signature is attributed to.
    """
    bare = {str(c.value) for c in selected.components if not c.params}
    if not bare & {"@authority", "@target-uri"}:
        raise WBAFailure(Check.TARGET, f"covers neither bare @authority nor @target-uri: {bare}")
    agent = _covered_signature_agent(headers, selected, policy)
    covered = set(selected.covered)
    for wanted in policy.components:
        normalized = wanted if wanted.startswith('"') else f'"{wanted}"'
        if normalized not in covered:
            raise WBAFailure(Check.COMPONENTS, f"{normalized} is not covered")
    return agent


def _covered_signature_agent(
    headers: httpx.Headers, selected: SelectedSignature, policy: Require
) -> str:
    if "Signature-Agent" not in headers:
        raise WBAFailure(Check.SIGNATURE_AGENT, "Signature-Agent header is missing")
    raw = headers["Signature-Agent"].strip()
    agent_components = [c for c in selected.components if c.value == "signature-agent"]
    if raw.startswith('"'):
        return _legacy_agent(raw, agent_components, policy)
    members = parse_dictionary(headers, "Signature-Agent", Check.SIGNATURE_AGENT)
    keys = [c.params.get("key") for c in agent_components if set(c.params) == {"key"}]
    if not keys:
        raise WBAFailure(Check.SIGNATURE_AGENT, 'no "signature-agent";key=... is covered')
    if len(keys) > 1 and selected.label not in keys:
        detail = f"members {keys} are covered and none is keyed to label {selected.label!r}"
        raise WBAFailure(Check.SIGNATURE_AGENT, detail)
    key = selected.label if selected.label in keys else keys[0]
    if policy.member_key_is_label and key != selected.label:
        raise WBAFailure(Check.SIGNATURE_AGENT, f"member {key!r} is not the label")
    if key not in members:
        raise WBAFailure(Check.SIGNATURE_AGENT, f"Signature-Agent has no member {key!r}")
    member = members[key]
    if not isinstance(member, http_sfv.Item) or not isinstance(member.value, str):
        raise WBAFailure(Check.SIGNATURE_AGENT, f"member {key!r} is not a String")
    discovery = str(member.params.get("type", DIRECTORY_TYPE))
    if discovery != DIRECTORY_TYPE and discovery not in URI_TYPES:
        raise WBAFailure(Check.SIGNATURE_AGENT, f"member {key!r} has unknown type {discovery!r}")
    _check_agent_url(member.value, origin_only=discovery == DIRECTORY_TYPE)
    return member.value


def _legacy_agent(raw: str, agent_components: list[http_sfv.Item], policy: Require) -> str:
    if policy.dictionary_form:
        raise WBAFailure(Check.SIGNATURE_AGENT, "Signature-Agent is the legacy sf-string form")
    if not any(not c.params for c in agent_components):
        raise WBAFailure(
            Check.SIGNATURE_AGENT, 'legacy form but bare "signature-agent" not covered'
        )
    item = http_sfv.Item()
    try:
        item.parse(raw.encode())
    except ValueError as exc:
        raise WBAFailure(Check.SIGNATURE_AGENT, "Signature-Agent is not a String") from exc
    if not isinstance(item.value, str):
        raise WBAFailure(Check.SIGNATURE_AGENT, "Signature-Agent is not a String")
    _check_agent_url(item.value, origin_only=True)
    return item.value


def _check_agent_url(url: str, *, origin_only: bool) -> None:
    # WG-00 Sections 5.2.1 and 5.5.1 require https.
    parts = urllib.parse.urlsplit(url)
    if parts.scheme != "https" or not parts.hostname:
        raise WBAFailure(Check.SIGNATURE_AGENT, f"{url!r} is not an https URL")
    if origin_only and (parts.path not in ("", "/") or parts.query or parts.fragment):
        raise WBAFailure(Check.SIGNATURE_AGENT, f"{url!r} is not an origin (WG-00 Section 5.5)")
    if parts.username is not None or parts.password is not None:
        raise WBAFailure(Check.SIGNATURE_AGENT, f"{url!r} carries userinfo")


def check_keyid_shape(keyid: object, check: Check = Check.KEYID) -> str:
    """keyid is an unpadded base64url SHA-256 thumbprint (WG-00 Section 5.2)."""
    if not isinstance(keyid, str) or not THUMBPRINT_RE.match(keyid):
        raise WBAFailure(check, f"keyid {keyid!r} is not an unpadded base64url SHA-256 thumbprint")
    return keyid


def b64_any(value: str) -> bytes | None:
    """Decode standard base64 or unpadded base64url; None when it is neither."""
    normalized = value.replace("-", "+").replace("_", "/")
    try:
        return base64.b64decode(normalized + "=" * (-len(normalized) % 4), validate=True)
    except (binascii.Error, ValueError):
        return None
