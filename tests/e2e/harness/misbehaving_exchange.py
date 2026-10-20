"""The Exchange impostor the Broker's fault-path suites buy through.

``BrokerService.ExecuteTransaction`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol`` says what the Broker does when an Exchange
refuses a whole sub-request, gives no answer, or answers OK with the wrong number
of items, and when an item names an Exchange the Broker cannot route to or does
not approve. The stack's reference Exchanges do none of those things, so the
stack runs a party that does: the ``misbehaving-exchange`` compose service,
built from ``tests/e2e/misbehaving-exchange/``.

It answers as three identities, each a network alias of the one container:

* :data:`MISBEHAVING_DOMAIN` is in the Broker's registry as VERIFIED, so the
  Broker sends it purchases, and it answers each one the way the test chose;
* :data:`UNAPPROVED_DOMAIN` is in the registry as DISCOVERED: the Broker knows it
  and may quote it, but may not pay it;
* :data:`UNLISTED_DOMAIN` is in no registry at all.

Every identity issues real offers: a test discovers them through the SDK's own
``DiscoverResources``, and the SDK verifies each against the key the impostor
publishes in its Web Bot Auth directory, as it verifies any Exchange's offer.
The impostor itself verifies nothing and keeps no ledger.

**How a test picks the answer.** :meth:`MisbehavingExchange.answer` sets the
:class:`Mode` the impostor answers one agent's sub-requests in, keyed by that
agent's ``requester.domain``. Every test buys as an agent of its own
(``fresh_agent``), so a mode set by one test never reaches another test's
purchase, and nothing has to be reset afterwards. An agent nothing was set for
is answered honestly.

**How a test reads what arrived.** :meth:`MisbehavingExchange.arrivals` lists the
sub-requests the Broker sent for the agent, in arrival order, with the
idempotency key and the offer ids each carried. It is a record of the Broker's
outbound calls, the one thing the impostor can observe that the Broker's answer
does not show.

In-network only, like ``sites``: the impostor publishes no host port, and the
Broker reaches it by names only the compose network resolves.
"""

from __future__ import annotations

from contextlib import closing
from dataclasses import dataclass
from enum import StrEnum

import httpx
from fora_sdk.core import VerifiedOffer
from fora_sdk.wire import to_wire
from wire.models import ErrorDetail

from .broker_purchase import one_group_per_uri, resource_query
from .fresh_agent import FreshAgent
from .sdk_client import STACK_SCHEME, offer_verifier, sdk_exchange_client

#: The compose service that answers for every impostor identity.
SERVICE = "misbehaving-exchange"
#: Routable and approved: the Broker's registry names it VERIFIED.
MISBEHAVING_DOMAIN = "misbehaving-exchange:8081"
#: Known to the Broker but not approved for purchases: registered as DISCOVERED.
UNAPPROVED_DOMAIN = "unapproved-exchange:8081"
#: Served by the impostor and named by no registry entry.
UNLISTED_DOMAIN = "unlisted-exchange:8081"
#: Every identity the impostor answers as, in the order its compose service lists them.
IDENTITIES = (MISBEHAVING_DOMAIN, UNAPPROVED_DOMAIN, UNLISTED_DOMAIN)
#: The trust level the Broker's registry file gives each impostor identity it names.
REGISTERED_TRUST = {MISBEHAVING_DOMAIN: "VERIFIED", UNAPPROVED_DOMAIN: "DISCOVERED"}

#: Where a run inside the compose network reaches the impostor's control routes.
CONTROL_URL = f"{STACK_SCHEME}://{MISBEHAVING_DOMAIN}"

#: Resources only the impostor sells. Under the reserved ``.example`` zone, so
#: none can name a real site; the impostor offers whatever URI it is asked about.
FAULT_URIS = (
    "http://faults.example/items/first.txt",
    "http://faults.example/items/second.txt",
)

_TIMEOUT_SEC = 10.0


class Mode(StrEnum):
    """How the impostor answers one agent's sub-requests."""

    #: One result item per item sent, each bought, under ids derived from the
    #: requester, the idempotency key and the offer, so a repeated sub-request is
    #: answered with the same transaction.
    HONEST = "honest"
    #: A non-OK answer to the whole sub-request: the chosen Connect code, and the
    #: chosen ErrorDetail when one is given.
    REFUSE = "refuse"
    #: The connection closes with no answer at all.
    DROP = "drop"
    #: No answer until long after the Broker stops waiting.
    STALL = "stall"
    #: OK, with one result item fewer than the items sent.
    FEWER = "fewer"
    #: OK, with one result item more than the items sent.
    MORE = "more"


@dataclass(frozen=True)
class Arrival:
    """One sub-request the Broker sent the impostor."""

    exchange: str
    idempotency_key: str
    offer_ids: tuple[str, ...]


@dataclass(frozen=True)
class MisbehavingExchange:
    """The impostor, reached at ``control_url``."""

    control_url: str

    def offer(self, agent: FreshAgent, identity: str, uri: str) -> VerifiedOffer:
        """The one offer ``identity`` makes ``agent`` for ``uri``, verified by the SDK.

        Discovered through the SDK's own ``DiscoverResources``, addressed to the
        identity, with a verifier holding only that identity's published key.
        """
        assert identity in IDENTITIES, f"the impostor does not answer as {identity!r}"
        client = sdk_exchange_client(
            agent.stack,
            identity,
            offer_verifier([identity]),
            agent_id=agent.host,
            key_path=agent.key_path,
            base_url=f"{STACK_SCHEME}://{identity}",
        )
        with closing(client):
            found = client.discover(resource_query(identity, [uri]))
        group = one_group_per_uri(found, [uri], f"discovery of {uri} at {identity}")[uri]
        rejected = [(r.offer.get("offer_id"), r.reason) for r in group.result.rejected]
        assert not rejected, f"the impostor's offers for {uri} failed verification: {rejected}"
        offers = list(group.result.verified)
        assert len(offers) == 1, f"{identity} offered {len(offers)} offers for {uri}, want one"
        return offers[0]

    def answer(
        self,
        agent: FreshAgent,
        mode: Mode,
        *,
        code: str | None = None,
        detail: ErrorDetail | None = None,
    ) -> None:
        """Answer every later sub-request for ``agent`` in ``mode``.

        ``code`` is the Connect code of a :attr:`Mode.REFUSE`, in its wire form
        ("permission_denied"), and ``detail`` the ErrorDetail it carries; no
        other mode takes either.
        """
        body: dict[str, object] = {"mode": mode.value}
        if code is not None:
            body["code"] = code
        if detail is not None:
            body["detail"] = to_wire(detail)
        response = httpx.put(
            f"{self.control_url}/control/faults/{agent.host}", json=body, timeout=_TIMEOUT_SEC
        )
        assert response.status_code == httpx.codes.NO_CONTENT, (
            f"the impostor refused mode {body}: {response.status_code} {response.text}"
        )

    def arrivals(self, agent: FreshAgent) -> list[Arrival]:
        """Every sub-request the Broker sent the impostor for ``agent``, in arrival order."""
        response = httpx.get(
            f"{self.control_url}/control/arrivals/{agent.host}", timeout=_TIMEOUT_SEC
        )
        assert response.status_code == httpx.codes.OK, (
            f"the impostor did not list its arrivals: {response.status_code} {response.text}"
        )
        return [
            Arrival(
                exchange=entry["exchange"],
                idempotency_key=entry["idempotency_key"],
                offer_ids=tuple(entry["offer_ids"]),
            )
            for entry in response.json()
        ]
