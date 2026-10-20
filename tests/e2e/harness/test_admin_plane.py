"""E2E guard: every Exchange's admin listener admits the runner and nothing else in the stack.

The admin listener carries the operator setters and the evidence read. It takes
no request signature and has no operator identity, so the source address is its
only gate (ADR-022). The stack opens it to the runner, because the suite sets
reporting policy and reads evidence there, and it must stay closed to every
other container: the Broker, the agents' directory host and the edges sit on the
same network, and an allowlist wide enough to cover any of them would let a
party the stack treats as untrusted rewrite policy.

Two checks, both against the running stack:

* each Exchange's ``ADMIN_ALLOWED_CIDRS``, read from its running container,
  covers the runner's address and the address of no other running container in
  the project;
* a request from another container is refused 403 by the allowlist, and the
  same request from the runner gets past it. The request is the evidence read
  with a transaction id that is not one, so the runner's copy is answered 400 by
  the route behind the gate and changes nothing.
"""

from __future__ import annotations

import ipaddress
from http import HTTPStatus

import pytest

from ._compose import exec_in_service, project_addresses, running_service_env, runner_addresses
from .admin_plane import (
    TRANSACTION_EVIDENCE_PATH,
    admin_url,
    evidence_read_status,
    require_admin_plane,
)
from .exchanges import EXCHANGE_D_DOMAIN, SERVICE_IDENTITIES
from .obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-without-cleanup")

#: A container on the stack's network that runs Python and is not the runner.
_PEER_SERVICE = "agent-directory"

#: A transaction id that is not one: past the allowlist the evidence read is a 400.
_NOT_A_TRANSACTION = "not-a-transaction"

#: Prints the HTTP status a GET of argv[1] is answered with.
_STATUS_OF = (
    "import sys, urllib.request, urllib.error\n"
    "try:\n"
    "    print(urllib.request.urlopen(sys.argv[1], timeout=10).status)\n"
    "except urllib.error.HTTPError as e:\n"
    "    print(e.code)\n"
)

_Network = ipaddress.IPv4Network | ipaddress.IPv6Network


def _allowlist(service: str) -> list[_Network]:
    """The networks ``service``'s admin listener admits, parsed as the Exchange parses them."""
    raw = running_service_env(service).get("ADMIN_ALLOWED_CIDRS", "")
    return [ipaddress.ip_network(item.strip()) for item in raw.split(",") if item.strip()]


def _covered(address: str, allowed: list[_Network]) -> bool:
    return any(ipaddress.ip_address(address) in network for network in allowed)


@pytest.fixture(autouse=True)
def _in_network() -> None:
    require_admin_plane()


@obligation(Obligation.ADMIN_002)
def test_every_exchange_admits_the_runner_and_no_other_container() -> None:
    """Each allowlist covers the runner's address and no other running container's."""
    runner = runner_addresses()
    assert runner, "the runner container holds no address on the stack's network"
    others = {
        name: addresses
        for name, addresses in project_addresses().items()
        if not set(addresses) & set(runner)
    }
    assert others, "found no container in the stack other than the runner"
    for service in SERVICE_IDENTITIES:
        allowed = _allowlist(service)
        assert allowed, f"{service} admits nobody on its admin listener"
        assert all(_covered(address, allowed) for address in runner), (
            f"{service}'s admin allowlist {allowed} does not cover the runner at {runner}"
        )
        admitted = {
            name: addresses
            for name, addresses in others.items()
            if any(_covered(address, allowed) for address in addresses)
        }
        assert not admitted, (
            f"{service}'s admin allowlist {allowed} also admits {admitted}; it must "
            f"admit the runner only"
        )


@obligation(Obligation.ADMIN_002, negative=True)
def test_a_container_other_than_the_runner_is_refused_and_the_runner_is_not() -> None:
    """From the agents' directory host the listener answers 403; from the runner it does not."""
    url = f"{admin_url(EXCHANGE_D_DOMAIN)}{TRANSACTION_EVIDENCE_PATH}?tx={_NOT_A_TRANSACTION}"

    from_runner = evidence_read_status(EXCHANGE_D_DOMAIN, _NOT_A_TRANSACTION)
    assert from_runner == HTTPStatus.BAD_REQUEST, (
        f"the runner's request was answered {from_runner}, want the 400 the "
        f"route behind the allowlist gives a bad transaction id"
    )

    exit_code, output = exec_in_service(_PEER_SERVICE, ["python", "-c", _STATUS_OF, url])
    assert exit_code == 0, f"the probe in {_PEER_SERVICE} failed to run: {output}"
    assert output.strip() == str(HTTPStatus.FORBIDDEN.value), (
        f"{_PEER_SERVICE}'s request to {url} was answered {output.strip()}, want 403 "
        f"from the admin allowlist"
    )
