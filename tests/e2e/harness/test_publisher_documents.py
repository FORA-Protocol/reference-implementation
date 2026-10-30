"""Each HTTP edge returns the publisher's own documents exactly as its origin serves them.

A demo publisher's discovery document and RSL file are files on its origin
(``harness/publisher_origins.py``). The edge in front of it serves neither: it
passes both addresses through to the origin for every client, an AI bot
included, and returns the origin's answer unchanged. Each read below goes
through the edge the way an agent's does, and is compared with a read of the
same address straight from the origin: status, media type, entity tag and the
body's bytes. A file the origin does not have is the origin's 404, passed through
as it is.

The three HTTP edges are Cloudflare (Miniflare), Fastly (Viceroy) and the
CloudFront RSA stand-in (``tests/e2e/aws-edge/server.mjs``). Lambda@Edge hands
both addresses back to CloudFront instead, which ``test_lambda_edge.py`` covers.
The edges' own key directories are covered by ``test_wba_directories.py`` and
``obligations/test_key_directories.py``.
"""

from __future__ import annotations

import pytest

from .constants import AI_BOT_UA, BROWSER_UA, PUBLISHER_DOCUMENT_PATHS
from .e2e_catalog import DEMO_MUSIC_DOMAIN, DEMO_PHILOSOPHY_DOMAIN, DEMO_SFX_DOMAIN
from .edge_fetch import EdgeAnswer, present
from .publisher_origins import PUBLISHER_ORIGINS
from .stack_urls import StackURLs

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-without-cleanup")

#: The publishers an HTTP edge fronts: Cloudflare, Fastly, the CloudFront RSA stand-in.
_EDGE_FRONTED = (DEMO_PHILOSOPHY_DOMAIN, DEMO_MUSIC_DOMAIN, DEMO_SFX_DOMAIN)

_CLIENTS = {"browser": {"User-Agent": BROWSER_UA}, "ai-bot": {"User-Agent": AI_BOT_UA}, "no-ua": {}}


def _as_compared(answer: EdgeAnswer) -> tuple[object, ...]:
    """What must not change between the origin and the reader: status, type, tag, bytes."""
    return (answer.status, answer.header("content-type"), answer.header("etag"), answer.body)


@pytest.mark.parametrize("client", list(_CLIENTS), ids=list(_CLIENTS))
@pytest.mark.parametrize("path", PUBLISHER_DOCUMENT_PATHS)
@pytest.mark.parametrize("publisher", _EDGE_FRONTED)
def test_the_edge_returns_the_origins_answer_unchanged(
    compose_stack: StackURLs, publisher: str, path: str, client: str
) -> None:
    """The edge's answer for a publisher document is the origin's, whoever asks."""
    del compose_stack  # ordering only: the stack is up
    from_origin = present(f"{PUBLISHER_ORIGINS[publisher]}{path}", headers={})
    through_edge = present(f"http://{publisher}{path}", headers=_CLIENTS[client])
    assert _as_compared(through_edge) == _as_compared(from_origin), (
        f"{publisher}{path} for {client}: the edge answered {through_edge.status} "
        f"{through_edge.text[:200]!r}, the origin {from_origin.status} {from_origin.text[:200]!r}"
    )
