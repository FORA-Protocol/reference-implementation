"""The Python WBA oracle against the WG-00 Appendix E vectors, and its every check.

The oracle (``wba_oracle.py``) is test infrastructure, and the conformance tests
trust its verdicts. This suite is what earns that trust, in three parts:

- the extension it adds to pyauth is needed (stock pyauth resolves the
  dictionary member wrongly) and is right (the vectors reproduce byte for byte);
- every :class:`Check` and :class:`DirectoryCheck` fires on the defect it names,
  so a conformance failure reported as one check is not some other defect;
- the directory fetch never follows a redirect and refuses plain http.

No stack, no network: the directory fetches go to an in-process
key directory server (``wba_vectors.directory_server``). That is the
oracle's own HTTP boundary, not a FORA service.
"""

from __future__ import annotations

import base64
from collections.abc import Callable
from typing import Any

import pytest
from http_message_signatures import http_sfv
from http_message_signatures.resolvers import HTTPSignatureComponentResolver

from . import wba_vectors
from .wba_directory import DirectoryCheck
from .wba_oracle import (
    Check,
    Message,
    Require,
    WBAComponentResolver,
    check_directory,
    check_directory_response,
    fresh_key,
    key_thumbprint,
    message,
    private_key_from_jwk,
    public_jwk,
    sign,
    verify,
)

# Pure parser/crypto tests of test infrastructure: no compose stack, no DB.
# `isolated` keeps the autouse per-test cleanup chain, which needs Postgres, off.
pytestmark = pytest.mark.stack_isolation("isolated")

URL = "https://publisher.example/articles/1?view=full"
AGENT = "https://agents.example"
# Vector data is read inside the tests, never at import: see wba_vectors.FORMS.
AGENT_KEY = fresh_key()
OTHER_KEY = fresh_key()
NOW = 1_790_000_000


def _vector_request(vector: dict[str, Any]) -> Message:
    return message(vector["method"], vector["url"])


def _vector_require(vector: dict[str, Any]) -> Require:
    # Appendix E signs with an expiry a century out, "far enough out that they
    # do not age", so the 24-hour lifetime limit is off for the vectors.
    return Require(dictionary_form=vector["form"] == "dictionary", max_lifetime=None)


def test_stock_pyauth_resolves_the_whole_dictionary_not_the_member() -> None:
    """The defect the oracle's resolver exists for, and the fix, side by side.

    If a pyauth release starts honouring ``key``, the first assertion fails: the
    extension in ``wba_pyauth.py`` is then redundant and should go.
    """
    vector = wba_vectors.request_vector("dictionary")
    signed = message("GET", vector["url"], headers=vector["headers"])
    component = http_sfv.Item()
    component.parse(b'"signature-agent";key="agent2"')
    assert HTTPSignatureComponentResolver(signed).resolve(component) == (
        'agent2="https://signature-agent.test"'
    )
    assert WBAComponentResolver(signed).resolve(component) == '"https://signature-agent.test"'


@pytest.mark.parametrize("form", wba_vectors.FORMS)
def test_sign_reproduces_appendix_e(form: str) -> None:
    """Ed25519 is deterministic, so equal inputs must give the vector's exact headers."""
    vector = wba_vectors.request_vector(form)
    headers = sign(
        _vector_request(vector),
        private_key_from_jwk(wba_vectors.private_jwk()),
        vector["agent_origin"],
        label=vector["label"],
        agent_key=vector["agent_key"],
        nonce=vector["nonce"],
        created=vector["created"],
        expires=vector["expires"],
        legacy=vector["form"] == "legacy",
    )
    assert headers == vector["headers"]


@pytest.mark.parametrize("form", wba_vectors.FORMS)
def test_verify_accepts_appendix_e(form: str) -> None:
    vector = wba_vectors.request_vector(form)
    result = verify(
        vector["headers"],
        vector["method"],
        vector["url"],
        b"",
        wba_vectors.public_jwk(),
        require=_vector_require(vector),
        now=vector["created"] + 60,
    )
    assert result.ok, result
    assert result.label == vector["label"]
    assert result.keyid == wba_vectors.vectors()["key"]["thumbprint"]
    assert result.signature_agent == vector["agent_origin"]


def test_verify_refuses_the_legacy_vector_by_default() -> None:
    """WG-00 Section 5.2.1: signers MUST send the dictionary form."""
    legacy = wba_vectors.request_vector("legacy")
    result = verify(
        legacy["headers"],
        legacy["method"],
        legacy["url"],
        b"",
        wba_vectors.public_jwk(),
        require=Require(max_lifetime=None),
        now=legacy["created"] + 60,
    )
    assert result.failed is Check.SIGNATURE_AGENT, result


def test_signed_directory_vector_passes() -> None:
    directory = wba_vectors.vectors()["directory"]
    result = check_directory_response(
        wba_vectors.directory_response(), require_signed=True, now=directory["created"] + 60
    )
    assert result.ok, result
    assert result.signed_by == (wba_vectors.vectors()["key"]["thumbprint"],)


def test_default_signature_meets_the_strict_profile() -> None:
    """A default signature satisfies every knob Require offers, set to its strictest."""
    request = message("GET", URL)
    headers = sign(request, AGENT_KEY, AGENT)
    strict = Require(alg=True, nonce_bytes=64, member_key_is_label=True)
    result = verify(headers, "GET", URL, b"", public_jwk(AGENT_KEY), require=strict)
    assert result.ok, result
    assert headers["Signature-Agent"] == f'sig1="{AGENT}"'
    assert result.covered == ('"@authority"', '"signature-agent";key="sig1"')
    assert result.params["expires"] - result.params["created"] == 300
    assert result.params["keyid"] == key_thumbprint(AGENT_KEY)
    assert len(base64.b64decode(result.params["nonce"])) == 64


def test_extra_components_are_signed_and_required() -> None:
    request = message("POST", URL, headers={"Content-Digest": "sha-256=:AAAA:"}, content=b"x")
    extra = ("@method", "@path", "@query", "content-digest")
    headers = sign(request, AGENT_KEY, AGENT, extra_components=extra)
    signed = {**dict(request.headers), **headers}
    ok = verify(signed, "POST", URL, b"x", public_jwk(AGENT_KEY), require=Require(components=extra))
    assert ok.ok, ok
    moved = verify(signed, "GET", URL, b"x", public_jwk(AGENT_KEY))
    assert moved.failed is Check.SIGNATURE, moved


def test_a_signature_without_a_tag_fails_the_tag_check() -> None:
    """``tag=None`` leaves the tag out, and the oracle names that, not a bad signature."""
    request = message("GET", URL)
    headers = sign(request, AGENT_KEY, AGENT, tag=None)
    assert "tag=" not in headers["Signature-Input"], headers
    assert verify(headers, "GET", URL, b"", public_jwk(AGENT_KEY)).failed is Check.TAG


def test_the_unquoted_form_is_neither_the_dictionary_nor_the_legacy_string() -> None:
    """The bare origin a v1.0.8 SDK sends verifies cryptographically and fails WG-00.

    Accepting the legacy form does not admit it: the value is not a String, so
    the oracle refuses it at the Signature-Agent check in both modes, and the
    signature under it is otherwise sound (only the form is wrong).
    """
    request = message("GET", URL)
    headers = sign(request, AGENT_KEY, AGENT, unquoted=True)
    assert headers["Signature-Agent"] == AGENT, headers
    assert '"signature-agent"' in headers["Signature-Input"], headers
    for policy in (Require(), Require(dictionary_form=False)):
        result = verify(headers, "GET", URL, b"", public_jwk(AGENT_KEY), require=policy)
        assert result.failed is Check.SIGNATURE_AGENT, result


def _unsigned_directory(alg: str | None) -> wba_vectors.Response:
    """The E.2.3 directory with its response signature removed and ``alg`` set."""
    body = wba_vectors.vectors()["directory"]["body"]
    if alg is not None:
        body = body.replace('"use"', f'"alg":"{alg}","use"')
    return wba_vectors.directory_response(
        body=body, headers={"Signature": None, "Signature-Input": None}
    )


@pytest.mark.parametrize(
    ("alg", "passes"),
    [("EdDSA", True), ("ed25519", False), (None, False)],
    ids=["JOSE name", "registry name", "absent"],
)
def test_an_expected_alg_admits_that_alg_alone(alg: str | None, passes: bool) -> None:
    """``expected_alg`` requires exactly that alg and overrides the registry rule."""
    result = check_directory_response(_unsigned_directory(alg), now=_later(), expected_alg="EdDSA")
    assert result.ok is passes, result
    if not passes:
        assert result.failed is DirectoryCheck.JWKS, result


Mutation = Callable[[dict[str, str]], dict[str, str]]


def _replace(header: str, old: str, new: str) -> Mutation:
    def mutate(headers: dict[str, str]) -> dict[str, str]:
        assert old in headers[header], (old, headers[header])
        return {**headers, header: headers[header].replace(old, new)}

    return mutate


def _drop(header: str) -> Mutation:
    return lambda headers: {k: v for k, v in headers.items() if k != header}


def _keep(headers: dict[str, str]) -> dict[str, str]:
    return headers


# (case id, sign() overrides, header mutation, verify() overrides, expected check)
_NEGATIVE_CASES: list[tuple[str, dict[str, Any], Mutation, dict[str, Any], Check]] = [
    ("unparseable input", {}, _replace("Signature-Input", "sig1=(", "sig1=(("), {}, Check.PARSE),
    ("labels differ", {}, _replace("Signature", "sig1=", "sig9="), {}, Check.PARSE),
    ("unknown label", {}, _keep, {"label": "sig9"}, Check.SELECT),
    ("wrong tag", {}, _replace("Signature-Input", '"web-bot-auth"', '"other"'), {}, Check.TAG),
    ("extra param", {}, _replace("Signature-Input", ';tag="', ';foo=1;tag="'), {}, Check.PARAMS),
    ("wrong alg", {}, _replace("Signature-Input", '"ed25519"', '"rsa-pss-sha512"'), {}, Check.ALG),
    ("expired", {}, _keep, {"now": NOW + 400}, Check.EXPIRY),
    ("created in future", {}, _keep, {"now": NOW - 60}, Check.EXPIRY),
    ("too old", {"expires": NOW + 200_000}, _keep, {"now": NOW + 90_000}, Check.EXPIRY),
    ("over 24h", {"expires": NOW + 86_401}, _keep, {}, Check.LIFETIME),
    (
        "no nonce",
        {"include_nonce": False},
        _keep,
        {"require": Require(nonce_bytes=64)},
        Check.NONCE,
    ),
    ("short nonce", {"nonce": "AAAA"}, _keep, {"require": Require(nonce_bytes=64)}, Check.NONCE),
    ("no @authority", {"target": "@method"}, _keep, {}, Check.TARGET),
    ("legacy form", {"legacy": True}, _keep, {}, Check.SIGNATURE_AGENT),
    ("no agent header", {}, _drop("Signature-Agent"), {}, Check.SIGNATURE_AGENT),
    (
        "agent has path",
        {},
        _replace("Signature-Agent", AGENT, f"{AGENT}/x"),
        {},
        Check.SIGNATURE_AGENT,
    ),
    (
        "plain http agent",
        {},
        _replace("Signature-Agent", "https:", "http:"),
        {},
        Check.SIGNATURE_AGENT,
    ),
    (
        "unknown type",
        {},
        _replace("Signature-Agent", f'"{AGENT}"', f'"{AGENT}";type=carrier'),
        {},
        Check.SIGNATURE_AGENT,
    ),
    (
        "member not label",
        {"agent_key": "agent2"},
        _keep,
        {"require": Require(member_key_is_label=True)},
        Check.SIGNATURE_AGENT,
    ),
    (
        "member missing",
        {},
        _replace("Signature-Agent", "sig1=", "sig2="),
        {},
        Check.SIGNATURE_AGENT,
    ),
    (
        "uncovered @method",
        {},
        _keep,
        {"require": Require(components=("@method",))},
        Check.COMPONENTS,
    ),
    (
        "keyid not thumbprint",
        {},
        _replace("Signature-Input", 'keyid="', 'keyid="x'),
        {},
        Check.KEYID,
    ),
    ("unknown key", {}, _keep, {"jwks_or_key": public_jwk(OTHER_KEY)}, Check.KEYID),
    (
        "agent swapped",
        {},
        _replace("Signature-Agent", AGENT, "https://other.example"),
        {},
        Check.SIGNATURE,
    ),
    ("signature bytes", {}, _replace("Signature", "sig1=:", "sig1=:AA"), {}, Check.SIGNATURE),
]


@pytest.mark.parametrize(
    ("sign_overrides", "mutate", "verify_overrides", "expected"),
    [case[1:] for case in _NEGATIVE_CASES],
    ids=[case[0] for case in _NEGATIVE_CASES],
)
def test_each_check_fires_on_its_defect(
    sign_overrides: dict[str, Any],
    mutate: Mutation,
    verify_overrides: dict[str, Any],
    expected: Check,
) -> None:
    """Each named check reports the defect it names, and no earlier check does."""
    headers = sign(message("GET", URL), AGENT_KEY, AGENT, created=NOW, **sign_overrides)
    arguments: dict[str, Any] = {"jwks_or_key": public_jwk(AGENT_KEY), "now": NOW + 10}
    arguments.update(verify_overrides)
    jwks_or_key = arguments.pop("jwks_or_key")
    result = verify(mutate(headers), "GET", URL, b"", jwks_or_key, **arguments)
    assert result.failed is expected, result
    assert not result.ok


def test_several_tagged_signatures_need_a_label() -> None:
    first = sign(message("GET", URL), AGENT_KEY, AGENT, label="a")
    second = sign(message("GET", URL), AGENT_KEY, AGENT, label="b")
    merged = {
        name: f"{first[name]}, {second[name]}"
        for name in ("Signature", "Signature-Input", "Signature-Agent")
    }
    jwk = public_jwk(AGENT_KEY)
    assert verify(merged, "GET", URL, b"", jwk).failed is Check.SELECT
    assert verify(merged, "GET", URL, b"", jwk, label="b").ok


def _later() -> int:
    """An instant inside the E.2.3 directory signature's validity window."""
    return int(wba_vectors.vectors()["directory"]["created"]) + 60


BodyEdit = Callable[[str], str] | None

# (case id, directory_response() overrides, edit to the vector body, require_signed,
#  seconds after the directory signature's created, expected check)
_DIRECTORY_CASES: list[tuple[str, dict[str, Any], BodyEdit, bool, int, DirectoryCheck]] = [
    (
        "redirect",
        {"status": 301, "headers": {"Location": "/x"}},
        None,
        False,
        60,
        DirectoryCheck.STATUS,
    ),
    ("not found", {"status": 404}, None, False, 60, DirectoryCheck.STATUS),
    (
        "plain json",
        {"headers": {"Content-Type": "application/json"}},
        None,
        False,
        60,
        DirectoryCheck.MEDIA_TYPE,
    ),
    ("no keys", {"body": '{"keys":[]}'}, None, False, 60, DirectoryCheck.JWKS),
    ("not json", {"body": "keys"}, None, False, 60, DirectoryCheck.JWKS),
    (
        "JOSE alg",
        {},
        lambda body: body.replace('"use"', '"alg":"EdDSA","use"'),
        False,
        60,
        DirectoryCheck.JWKS,
    ),
    (
        "private d",
        {},
        lambda body: body.replace('"use"', '"d":"AAAA","use"'),
        False,
        60,
        DirectoryCheck.JWKS,
    ),
    (
        "kid label",
        {},
        lambda body: body.replace('"kid":"', '"kid":"key-1'),
        False,
        60,
        DirectoryCheck.KID,
    ),
    (
        "body swapped",
        {},
        lambda body: body.replace('"sig"', '"enc"'),
        False,
        60,
        DirectoryCheck.SIGNED_RESPONSE,
    ),
    (
        "unsigned but required",
        {"headers": {"Signature": None, "Signature-Input": None}},
        None,
        True,
        60,
        DirectoryCheck.SIGNED_RESPONSE,
    ),
    ("signed in future", {}, None, False, -60, DirectoryCheck.SIGNED_RESPONSE),
    (
        "signature bytes",
        {"headers": {"Signature": "binding=:AAAA:"}},
        None,
        False,
        60,
        DirectoryCheck.SIGNED_RESPONSE,
    ),
]


@pytest.mark.parametrize(
    ("overrides", "body_edit", "require_signed", "offset", "expected"),
    [case[1:] for case in _DIRECTORY_CASES],
    ids=[case[0] for case in _DIRECTORY_CASES],
)
def test_each_directory_check_fires_on_its_defect(
    overrides: dict[str, Any],
    body_edit: BodyEdit,
    require_signed: bool,
    offset: int,
    expected: DirectoryCheck,
) -> None:
    directory = wba_vectors.vectors()["directory"]
    arguments = dict(overrides)
    if body_edit is not None:
        arguments["body"] = body_edit(directory["body"])
    response = wba_vectors.directory_response(**arguments)
    now = int(directory["created"]) + offset
    result = check_directory_response(response, require_signed=require_signed, now=now)
    assert result.failed is expected, result


def test_unsigned_directory_passes_unless_required() -> None:
    unsigned = wba_vectors.directory_response(headers={"Signature": None, "Signature-Input": None})
    assert check_directory_response(unsigned, now=_later()).ok


def test_check_directory_fetches_the_well_known_path() -> None:
    seen: list[Message] = []
    with wba_vectors.directory_server(seen) as client:
        result = check_directory(
            "https://signature-agent.test", client, require_signed=True, now=_later()
        )
    assert result.ok, result
    assert [str(r.url) for r in seen] == [
        "https://signature-agent.test/.well-known/http-message-signatures-directory"
    ]
    assert seen[0].headers["Accept"] == "application/http-message-signatures-directory+json"


def test_check_directory_does_not_follow_a_redirect() -> None:
    seen: list[Message] = []
    with wba_vectors.directory_server(seen) as client:
        result = check_directory("https://signature-agent.test/moved", client, now=_later())
    assert result.failed is DirectoryCheck.STATUS, result
    assert len(seen) == 1


def test_check_directory_refuses_plain_http_before_fetching() -> None:
    seen: list[Message] = []
    with wba_vectors.directory_server(seen) as client:
        result = check_directory("http://signature-agent.test", client, now=_later())
    assert result.failed is DirectoryCheck.HTTPS, result
    assert seen == []


def test_the_forms_name_every_request_vector() -> None:
    """``wba_vectors.FORMS`` is written out by hand; it must match the file."""
    forms = sorted(v["form"] for v in wba_vectors.vectors()["requests"])
    assert forms == sorted(wba_vectors.FORMS)
