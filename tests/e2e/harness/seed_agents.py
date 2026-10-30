"""The agent the seed sets up before any test runs, which a test never signs as on its own.

ADR-028 D7 gives every test its own agent (``fresh_agent.py``). The seed sets up
one agent instead, because what is needed of it cannot be made at run time: the
CREDIT-LESS buyer, ``agent-nobilling-e2e``. Its account has to be opened while
every seat holds new agents for review, so it is granted no welcome credit, and
then approved, which only the operator does, out of band; the seed does both
through the Exchanges' e2e-only controls (``seed_accounts``). A test reaches it
only through the ``credit_less_buyer`` fixture.

Its key is served under its own host name by the key-directories service in
``docker-compose.e2e.yml`` (the kid is the agent id), and
``scripts/gen-e2e-keys.sh`` mints it.

Only the seed and the conftest import this module, and both import the module
itself (``from . import seed_agents``), so no module a test can reach binds this
identity under a name of its own. ``ruff-harness.toml`` bans the module in the
test modules (ADR-028 H11).
"""

from __future__ import annotations

from .e2e_catalog import _FIXTURES_DIR
from .seed_accounts import SeededBuyer

#: Registered while every seat holds new agents for review, then approved: an
#: active account that holds nothing, so the billing gate refuses its paid purchase.
CREDIT_LESS_BUYER = SeededBuyer(
    "agent-nobilling-e2e", _FIXTURES_DIR / "agent_nobilling_e2e_key.json"
)
