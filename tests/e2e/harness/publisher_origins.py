"""Where each demo publisher's origin serves the publisher's own documents.

A demo publisher serves its discovery document and its RSL file from its own
origin, as any publisher does, and the edge in front of it passes both
addresses through to that origin (``PUBLISHER_DOCUMENT_PATHS``). The stack's
static origin, the ``publisher`` service, answers each demo publisher on a port
of its own (``deploy/publisher/nginx.conf``), and this table says which. The edge
in front of a publisher forwards to that address (its ``ORIGIN_URL`` in
``docker-compose.e2e.yml``). The Lambda@Edge publisher has no HTTP edge on this
network: a test reads its documents at its address in CloudFront's place.
``test_guards_publisher_documents.py`` holds the table to the compose file and
the nginx map.
"""

from __future__ import annotations

from .e2e_catalog import DEMO_MUSIC_DOMAIN, DEMO_PHILOSOPHY_DOMAIN, DEMO_SFX_DOMAIN
from .lambda_edge import PUBLISHER as LAMBDA_PUBLISHER

#: Each demo publisher's domain, and the base URL of the origin that serves its documents.
PUBLISHER_ORIGINS: dict[str, str] = {
    DEMO_PHILOSOPHY_DOMAIN: "http://publisher:8001",
    DEMO_MUSIC_DOMAIN: "http://publisher:8002",
    DEMO_SFX_DOMAIN: "http://publisher:8003",
    LAMBDA_PUBLISHER: "http://publisher:8004",
}
