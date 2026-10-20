"""The combined music license document is served at the URI the feed names.

Every playable track's offers reference one document and its SHA-256 digest.
An agent reads the terms from that URI before it buys, so the origin must
serve the document there, and the served bytes must match the digest the
offers carry. The request goes through the compose stack's edge with a browser
User-Agent, so it proves the origin route (deploy/publisher/nginx.conf) and the
file under deploy/content/demo together.
"""

from __future__ import annotations

import json
from pathlib import Path
from urllib.parse import urlsplit

from fora_sdk.resolvers import guarded_client, read_license_document
from wire.models import License

from .conftest import StackURLs
from .constants import BROWSER_UA

_FEED = Path(__file__).resolve().parent / "fixtures" / "catalog" / "music.jsonl"


def _track_licenses() -> list[dict[str, str]]:
    records = [json.loads(line) for line in _FEED.read_text().splitlines() if line.strip()]
    return [r["license"] for r in records if r["path"].endswith(".mp3")]


def test_the_origin_serves_the_music_license_at_its_feed_uri(compose_stack: StackURLs) -> None:
    licenses = _track_licenses()
    assert licenses, "the music feed has no playable tracks"
    uris = {lic["uri"] for lic in licenses}
    digests = {lic["uri_digest"] for lic in licenses}
    assert len(uris) == 1 and len(digests) == 1, (uris, digests)
    # The feed names the document at its public host. The stack serves that host
    # through its edge, so the license is read there, with the feed's own digest.
    served_at = f"{compose_stack.edge}{urlsplit(uris.pop()).path}"
    license = License.model_validate({**licenses[0], "uri": served_at})

    # The SDK's reader checks the license, fetches its uri and refuses bytes that do
    # not hash to uri_digest, so a read that returns is the proof.
    with guarded_client(headers={"User-Agent": BROWSER_UA}) as http:
        document = read_license_document(license, http=http)

    assert document.digest == digests.pop(), served_at
