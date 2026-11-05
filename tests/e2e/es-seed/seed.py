"""Stands in for the index owner: fills the e2e index, stores the search template
and mints the Broker's key.

The Broker only reads Elasticsearch, so something else has to write to it. This
one-shot builds one document per record of a catalog feed, with the URL the
Exchange stores for it, and one document the Exchange does not hold, so a stale
hit can be tested. Each document also gets a language and a publication date the
example filter schema can select on: the seed assigns them by the record's
position in the feed (see ``documents``), they are not read from the article. It
then stores the example search template under the id the Broker names, mints a
read-only API key and leaves it where the Broker reads it.

Running it again changes nothing while the stored key still works, the index
holds every document and the stored template is the file's: the key on the volume
keeps its bytes, so a Broker reading it is unaffected. Otherwise it rebuilds the
index, stores the template again and replaces the key.

Standard library only, like tests/e2e/fake-bedrock: the image is a bare Python.
"""

from __future__ import annotations

import base64
import json
import os
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

ES_URL = os.environ["ES_URL"]
ES_PASSWORD = os.environ["ES_PASSWORD"]
ES_INDEX = os.environ["ES_INDEX"]
FEED_FILE = Path(os.environ["FEED_FILE"])
BODIES_DIR = Path(os.environ["BODIES_DIR"])
CATALOG_URI_SCHEME = os.environ["CATALOG_URI_SCHEME"]
API_KEY_OUT = Path(os.environ["API_KEY_OUT"])
TEMPLATE_ID = os.environ["TEMPLATE_ID"]
TEMPLATE_FILE = Path(os.environ["TEMPLATE_FILE"])

KEY_NAME = "broker-es"
WAIT_SECONDS = 120

# On the publisher the feed names, under a path no feed sells: the index is out of
# date, which is the owner's to fix and the Exchange's to refuse.
STALE_PATH = "/articles/philosophers/diogenes.txt"
STALE_TITLE = "Diogenes"

MAPPINGS = {
    "properties": {
        "url": {"type": "keyword"},
        "title": {"type": "text"},
        "body": {"type": "text"},
        "language": {"type": "keyword"},
        "date_published": {"type": "date"},
    }
}
# One shard and no replicas, so scores do not depend on which shard holds a term,
# and the index goes green on a single node.
SETTINGS = {"number_of_shards": 1, "number_of_replicas": 0}


def superuser() -> str:
    return "Basic " + base64.b64encode(f"elastic:{ES_PASSWORD}".encode()).decode()


def call(
    method: str,
    path: str,
    body: bytes | None = None,
    *,
    authorization: str | None = None,
    content_type: str = "application/json",
) -> tuple[int, Any]:
    """One request. An HTTP error comes back as its status; a connection error raises."""
    request = urllib.request.Request(ES_URL + path, data=body, method=method)
    request.add_header("Authorization", authorization or superuser())
    request.add_header("Content-Type", content_type)
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)


def must(method: str, path: str, payload: Any = None) -> Any:
    body = None if payload is None else json.dumps(payload).encode()
    status, answer = call(method, path, body)
    if status >= 300:
        raise SystemExit(f"{method} {path}: {status} {answer}")
    return answer


def wait_for_cluster() -> None:
    """Wait for a 200 from the node, whatever else it answers meanwhile."""
    deadline = time.monotonic() + WAIT_SECONDS
    last = "no answer"
    while time.monotonic() < deadline:
        try:
            status, _ = call("GET", "/")
            if status == 200:
                return
            last = f"status {status}"
        except OSError as error:
            last = str(error)
        time.sleep(1)
    raise SystemExit(f"Elasticsearch never answered 200 within {WAIT_SECONDS}s: {last}")


def filter_fields(position: int) -> dict[str, str]:
    """The fields the example filters select on, by the document's position.

    The rule is the seed's, not the articles': an even position is "en" and an odd
    one "el", and the document at position i was published on 2026-01-(i+1). The
    tests derive their expected results from this rule and the feed order.
    """
    return {
        "language": "en" if position % 2 == 0 else "el",
        "date_published": f"2026-01-{position + 1:02d}T00:00:00Z",
    }


def documents() -> list[dict[str, str]]:
    """One document per feed record, plus the one the Exchange does not hold, last.

    A record without its body file stops the seed: an empty body would change what
    the queries match, and the tests would fail far from the cause.
    """
    records = [json.loads(line) for line in FEED_FILE.read_text().splitlines() if line.strip()]
    docs = []
    for record in records:
        body = BODIES_DIR / Path(record["path"]).name
        if not body.is_file():
            raise SystemExit(f"no body file for {record['path']}: {body}")
        docs.append(
            {
                "url": f"{CATALOG_URI_SCHEME}://{record['domain']}{record['path']}",
                "title": record["title"],
                "body": body.read_text(),
            }
        )
    docs.append(
        {
            "url": f"{CATALOG_URI_SCHEME}://{records[0]['domain']}{STALE_PATH}",
            "title": STALE_TITLE,
            "body": "",
        }
    )
    return [doc | filter_fields(position) for position, doc in enumerate(docs)]


def already_seeded(expected: int, template: str) -> bool:
    if not API_KEY_OUT.is_file():
        return False
    key = "ApiKey " + API_KEY_OUT.read_text().strip()
    status, _ = call("GET", "/_security/_authenticate", authorization=key)
    if status != 200:
        return False
    status, answer = call("GET", f"/{ES_INDEX}/_count", authorization=key)
    if status != 200 or answer["count"] != expected:
        return False
    # Reading a stored script needs a cluster privilege the Broker's key lacks.
    status, answer = call("GET", f"/_scripts/{TEMPLATE_ID}")
    return status == 200 and answer.get("script", {}).get("source") == template


def store_template(template: str) -> None:
    """Store the search template under the id the Broker names, as the owner would."""
    must("PUT", f"/_scripts/{TEMPLATE_ID}", {"script": {"lang": "mustache", "source": template}})


def rebuild_index(docs: list[dict[str, str]]) -> None:
    status, answer = call("DELETE", f"/{ES_INDEX}")
    if status not in (200, 404):
        raise SystemExit(f"DELETE /{ES_INDEX}: {status} {answer}")
    must("PUT", f"/{ES_INDEX}", {"settings": SETTINGS, "mappings": MAPPINGS})
    lines: list[str] = []
    for doc in docs:
        lines.append(json.dumps({"index": {"_index": ES_INDEX, "_id": doc["url"]}}))
        lines.append(json.dumps(doc))
    payload = ("\n".join(lines) + "\n").encode()
    status, answer = call(
        "POST", "/_bulk?refresh=true", payload, content_type="application/x-ndjson"
    )
    if status >= 300 or answer["errors"]:
        raise SystemExit(f"POST /_bulk: {status} {answer}")


def mint_key() -> None:
    """Replace the key by name, so a re-mint leaves one key that works."""
    status, answer = call("DELETE", "/_security/api_key", json.dumps({"name": KEY_NAME}).encode())
    if status not in (200, 404):
        raise SystemExit(f"DELETE /_security/api_key: {status} {answer}")
    grant = {"indices": [{"names": [ES_INDEX], "privileges": ["read"]}]}
    minted = must(
        "POST",
        "/_security/api_key",
        {"name": KEY_NAME, "role_descriptors": {"broker_read": grant}},
    )
    # Every version answers with the id and the key; only later ones also send the
    # encoded form, so it is composed here.
    encoded = base64.b64encode(f"{minted['id']}:{minted['api_key']}".encode()).decode()
    API_KEY_OUT.write_text(encoded)
    # The Broker runs as uid 65532 and this one-shot as root, on a shared volume.
    API_KEY_OUT.chmod(0o644)


def main() -> None:
    wait_for_cluster()
    docs = documents()
    template = TEMPLATE_FILE.read_text()
    if already_seeded(len(docs), template):
        print("index, template and key are in place")
        return
    rebuild_index(docs)
    print(f"indexed {len(docs)} documents")
    store_template(template)
    print(f"stored the search template {TEMPLATE_ID}")
    mint_key()
    print("minted the read-only key")


if __name__ == "__main__":
    main()
