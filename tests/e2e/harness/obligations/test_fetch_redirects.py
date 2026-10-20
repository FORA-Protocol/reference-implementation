"""Obligations FETCH-004 and FETCH-010: no redirect for a key directory, five for a revocation list.

The pinned protocol module ``github.com/FORA-Protocol/protocol`` ("Fetching
directories, revocation lists and manifests" on the authentication page;
"Redirects" under Well-Known Discovery and ``WBAFile.revocation_url`` in
``fora.proto``): a key directory is fetched with no redirect, and a verifier treats a
redirect, like any other status, as a discovery failure, because the directory's
address is the origin the signer committed to in its covered ``Signature-Agent``
member. A revocation list carries nothing to leak, so a party fetching one MAY
follow up to five redirects.

Surface: the Exchange's DiscoverResources through the agents' own SDK clients,
read back as the SDK's typed answer or typed refusal. The redirects are served by
the stack's agent-directory service, which answers a registered path on a
registered host with a 302 to another host of the agent zone; a chain of N
redirects is N such hosts, each pointing at the next.

* FETCH-004: agent D publishes its key at its own host and is answered: the
  control. Agent R signs with the same key under its own host, whose directory
  path answers with a single redirect to D's directory. The only directory
  publishing the key is at the end of that redirect, and the verifier must not
  follow it, so no directory publishes the key for R and its discovery is refused
  SIGNATURE_INVALID.
* FETCH-010: the agent's directory publishes two keys and names a revocation list
  on its own host, which reaches a list naming the second key through five
  redirects. The first key's discovery is answered: the control. The second key's
  discovery is refused SIGNATURE_INVALID, which only a verifier that read the list
  at the end of the chain can do.

Each chain is registered before any verifier has seen its hosts, because a
verifier caches a directory it has read.
"""

from __future__ import annotations

import pytest
from fora_sdk.identity import generate_key

from ..constants import REVOCATION_PATH
from ..fresh_agent import (
    AgentFactory,
    new_host,
    publish_revocations,
    redirect_chain,
    revocation_url,
)
from ..identity_requests import SIGNATURE_INVALID, assert_auth_refusal, discover_outcome
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The most redirects the protocol lets a fetch of these documents follow.
_MAX_REDIRECTS = 5


@obligation(Obligation.FETCH_004, negative=True)
def test_a_key_directory_behind_a_redirect_authenticates_nobody(
    fresh_agent_factory: AgentFactory,
) -> None:
    """Served at its own host: answered. Behind one redirect: refused SIGNATURE_INVALID."""
    direct = fresh_agent_factory()
    assert discover_outcome(direct) is None, "control: the key's own directory host was refused"
    redirected = fresh_agent_factory.redirected_to(direct, hops=1)

    refusal = discover_outcome(redirected)

    assert refusal is not None, (
        f"a discovery signed under {redirected.host}, whose directory path redirects to "
        f"{direct.host}'s directory, was answered: the verifier followed the redirect"
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)


@obligation(Obligation.FETCH_010, negative=True)
def test_a_revocation_list_reached_through_five_redirects_revokes_the_keys_it_names(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The agent's revocation_url reaches a list naming its second key in five hops: revoked."""
    agent = fresh_agent_factory()
    second, second_thumbprint = generate_key()
    target = new_host()
    publish_revocations(agent.stack, target, [second_thumbprint])
    agent.publish([agent.ed25519, second], revocations=True)
    redirect_chain(agent.stack, agent.host, REVOCATION_PATH, revocation_url(target), _MAX_REDIRECTS)

    assert discover_outcome(agent) is None, (
        "control: the first key, which no list names, was refused"
    )
    refusal = discover_outcome(agent.signing_as(second))

    assert refusal is not None, (
        f"the second key, named by the list at the end of a {_MAX_REDIRECTS}-redirect chain "
        f"ending at {target}, was answered"
    )
    assert_auth_refusal(refusal, SIGNATURE_INVALID)
