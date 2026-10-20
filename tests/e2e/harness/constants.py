"""Shared constants for the E2E harness: well-known routes, User-Agents, and the
requester object every addressed request carries.

Single source of truth for each, so a spec change (e.g. a WBA directory route
rename) touches one line, not four.
"""

from __future__ import annotations

# One representative on each side of the edge's bot gate: a mainstream
# browser signature (allowed through to the origin) and an AI crawler from
# the worker's built-in deny list (sent to negotiate).
BROWSER_UA = (
    "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"
    " (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)
AI_BOT_UA = "GPTBot/1.0 (+https://openai.com/gptbot)"

# Two search-engine crawlers, as their operators document their User-Agents. Neither
# is on the protocol's maintained AI-bot list, so the edge must let them read as a
# browser does.
GOOGLEBOT_UA = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
BINGBOT_UA = "Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)"

# Web Bot Auth directory: after the WBA split, identity/revocation keys moved
# off fora.json and are served here as a JWK set (keys named by RFC 7638
# thumbprint, no kid).
WBA_DIRECTORY_PATH = "/.well-known/http-message-signatures-directory"

# Broker-published key-revocation channel (KeyRevocationList wire shape).
REVOCATION_PATH = "/.well-known/fora-key-revocations.json"


def requester(
    agent_id: str,
    domain: str | None = None,
    **facets: object,
) -> dict[str, object]:
    """The requester object every addressed request carries.

    One builder rather than a dict written out per call site: the three keys and
    the domain default were copied five times, and the two copies that left the
    default out are the ones that sent an empty ``domain`` — a value the wire
    refuses for its shape.

    ``domain`` names the directory a verifier fetches the agent's keys from, which
    is the host of the agent's signing directory. In this stack an agent's id, its
    signing directory and its key fixture's kid are one string, so the default is
    the agent's own id. A caller whose ``agent_id`` is a free label rather than
    that host passes ``domain`` explicitly. Facets left out are omitted entirely
    rather than sent empty.
    """
    out: dict[str, object] = {
        "id": agent_id,
        "domain": domain or agent_id,
        "type": "REQUESTER_TYPE_AGENT",
    }
    for key, value in facets.items():
        if value is not None:
            out[key] = value
    return out
