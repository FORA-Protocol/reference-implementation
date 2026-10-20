"""Obligations IDENTITY-007, -008 and -017 to -020: whom a direct discovery names.

Surface: the Exchange's DiscoverResources, and for one leg its Register and
GetAccountStatus, through a fresh agent's own SDK client. Each negative changes
one field of the request in the SDK's pre-signing hook (``patch_body``), so the
signature is valid over the bytes sent and the changed field is the only thing
wrong with them. Every negative is a declared mutation (``harness.malformed``)
whose control, the agent's unaltered discovery, is answered with a verified
offer first. A refusal is read through the SDK's typed error.

The requester (IDENTITY-007, -008, -017):

* on a direct request ``requester.domain`` must be the host of the covered
  Signature-Agent, compared case-folded, with its port, a subdomain being another
  party. A mismatch is ``unauthenticated`` SIGNATURE_INVALID. The Exchange applies
  no such binding on discovery today, so those negatives are a strict xfail; the
  case-folded positive passes;
* a ``requester.domain`` carrying a scheme, a path or a query fails the field's
  shape and is ``invalid_argument``.

The recipient (IDENTITY-018 to -020): a request whose ``exchange`` is not the
recipient's own domain, matched exactly with its port, is refused
``invalid_argument``. A value is shape-checked before it is case-folded, so a
fullwidth homograph is refused while the same domain in upper case is answered.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.client import CallError

from ..defects import DIRECT_REQUESTER_DOMAIN_UNBOUND
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..identity_requests import (
    SIGNATURE_INVALID,
    answered_with_offer,
    assert_auth_refusal,
    discovery_by,
    probe_query,
)
from ..malformed import Alteration, Mutation, malformed, malformed_outcome, patch_body
from ..obligation_registry import Obligation, obligation
from ..seed_accounts import has_account, registration_request

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _set(path: tuple[str, ...], value: str) -> Callable[[dict[str, Any]], None]:
    """A body patch setting the field at ``path`` to ``value``."""

    def patch(body: dict[str, Any]) -> None:
        target = body
        for key in path[:-1]:
            target = target[key]
        target[path[-1]] = value

    return patch


def _altered(agent: FreshAgent, kind: Mutation, why: str, path: tuple[str, ...], value: str) -> Any:
    """Send ``agent``'s discovery, then the same with the field at ``path`` set to ``value``."""
    return malformed_outcome(
        kind,
        why,
        control=discovery_by(agent),
        alter=Alteration(before_sign=patch_body(_set(path, value))),
        accepted=answered_with_offer(),
    )


def _requester_domains(agent: FreshAgent, other: FreshAgent) -> dict[str, str]:
    return {
        "another-agents-directory": other.host,
        "a-subdomain-of-the-directory-host": f"eu.{agent.host}",
        "the-directory-host-on-another-port": f"{agent.host}:8443",
    }


_REQUESTER_CASES = [
    pytest.param(name, marks=obligation(proves, negative=True), id=name)
    for name, proves in (
        ("another-agents-directory", Obligation.IDENTITY_007),
        ("a-subdomain-of-the-directory-host", Obligation.IDENTITY_008),
        ("the-directory-host-on-another-port", Obligation.IDENTITY_008),
    )
]


@pytest.mark.parametrize("case", _REQUESTER_CASES)
@DIRECT_REQUESTER_DOMAIN_UNBOUND.mark
def test_a_discovery_naming_another_requester_domain_is_refused(
    fresh_agent_factory: AgentFactory, case: str
) -> None:
    """requester.domain is not the Signature-Agent host: refused as SIGNATURE_INVALID."""
    agent, other = fresh_agent_factory(), fresh_agent_factory()
    domain = _requester_domains(agent, other)[case]
    outcome = _altered(
        agent, Mutation.IDENTITY, f"requester.domain is {case}", ("requester", "domain"), domain
    )
    DIRECT_REQUESTER_DOMAIN_UNBOUND.check(
        isinstance(outcome, CallError), f"requester.domain {domain!r} was answered"
    )
    assert isinstance(outcome, CallError)
    assert_auth_refusal(outcome, SIGNATURE_INVALID)


@obligation(Obligation.IDENTITY_008)
def test_requester_domain_is_compared_case_folded(fresh_agent: FreshAgent) -> None:
    """requester.domain is the agent's directory host in upper case: answered."""
    client = fresh_agent.exchange_client(
        EXCHANGE_A_DOMAIN,
        before_sign=patch_body(_set(("requester", "domain"), fresh_agent.host.upper())),
    )
    answered_with_offer()(client.discover(probe_query()))


_DOMAIN_SHAPES = ("scheme-and-path", "path", "query-string")


@pytest.mark.parametrize("shape", _DOMAIN_SHAPES)
@obligation(Obligation.IDENTITY_017, negative=True)
def test_a_requester_domain_that_is_not_a_bare_host_is_refused(
    fresh_agent: FreshAgent, shape: str
) -> None:
    """requester.domain carries a scheme, a path or a query: refused as invalid_argument."""
    host = fresh_agent.host
    values = (f"https://{host}/x", f"{host}/x", f"{host}?q=1")
    domain = dict(zip(_DOMAIN_SHAPES, values, strict=True))
    refusal = malformed(
        Mutation.MESSAGE,
        f"requester.domain carries a {shape}",
        control=discovery_by(fresh_agent),
        alter=Alteration(before_sign=patch_body(_set(("requester", "domain"), domain[shape]))),
        accepted=answered_with_offer(),
    )
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"


_MISADDRESSED = {
    "another-exchange": (Obligation.IDENTITY_018, Mutation.IDENTITY, EXCHANGE_B_DOMAIN),
    "no-port": (Obligation.IDENTITY_020, Mutation.IDENTITY, "exchange"),
    "port-443": (Obligation.IDENTITY_020, Mutation.IDENTITY, "exchange:443"),
    "subdomain": (Obligation.IDENTITY_020, Mutation.IDENTITY, f"eu.{EXCHANGE_A_DOMAIN}"),
    "fullwidth-homograph": (Obligation.IDENTITY_019, Mutation.MESSAGE, "ｅxchange:8081"),
}


@pytest.mark.parametrize(
    "case",
    [
        pytest.param(name, marks=obligation(proves, negative=True), id=name)
        for name, (proves, _, _) in _MISADDRESSED.items()
    ],
)
def test_a_discovery_addressed_to_another_recipient_is_refused(
    fresh_agent: FreshAgent, case: str
) -> None:
    """The discovery's ``exchange`` is not exchange-a's own domain: refused as invalid_argument."""
    _proves, kind, value = _MISADDRESSED[case]
    refusal = malformed(
        kind,
        f"exchange names {value!r}, not exchange-a's own domain",
        control=discovery_by(fresh_agent),
        alter=Alteration(before_sign=patch_body(_set(("exchange",), value))),
        accepted=answered_with_offer(),
    )
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"


@obligation(Obligation.IDENTITY_019)
def test_the_recipient_in_upper_case_is_answered(fresh_agent: FreshAgent) -> None:
    """The discovery's ``exchange`` is exchange-a's domain in upper case: answered."""
    client = fresh_agent.exchange_client(
        EXCHANGE_A_DOMAIN, before_sign=patch_body(_set(("exchange",), EXCHANGE_A_DOMAIN.upper()))
    )
    answered_with_offer()(client.discover(probe_query()))


@obligation(Obligation.IDENTITY_018, negative=True)
def test_a_registration_addressed_to_another_exchange_opens_no_account(
    fresh_agent: FreshAgent,
) -> None:
    """A Register at exchange-a naming exchange-b is refused and opens no account there.

    The same Register naming exchange-a, sent afterwards, opens the account: the
    control, which shows the refusal was the recipient's and nothing else.
    """
    misaddressed = fresh_agent.exchange_client(
        EXCHANGE_A_DOMAIN, before_sign=patch_body(_set(("exchange",), EXCHANGE_B_DOMAIN))
    )
    request = registration_request(EXCHANGE_A_DOMAIN)
    with pytest.raises(CallError) as refused:
        misaddressed.register(request)
    assert refused.value.code == "invalid_argument", f"refused as {refused.value.code!r}"
    exchange = fresh_agent.exchange(EXCHANGE_A_DOMAIN)
    assert not has_account(exchange, EXCHANGE_A_DOMAIN), "a refused registration opened an account"

    assert exchange.register(request).billing_ref, (
        "control: the registration naming exchange-a opened no account"
    )
    assert has_account(exchange, EXCHANGE_A_DOMAIN)
