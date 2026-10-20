"""Local-stack catalog proof driver (items 2-5).

Re-runnable proof that the live e2e stack ingests and serves the multi-publisher
catalog through the REAL JSONL ingestion path. NOT a pytest module — a
standalone script run from the host against an already-up stack with host
ports (`make e2e-up E2E_HOST_PORTS=1`):

    uv run --project tests/e2e python tests/e2e/demo_proof.py

What it does, end to end:

1. Registers the Exchange SEAT (a fora.tenants row keyed on the exchange's own
   hostname), the demo payee, and the three demo sites (one per edge runtime /
   signing scheme), each listed ACTIVE on that seat. The Exchange resolves its
   seat from EXCHANGE_DOMAIN and the entry's domain from fora.publisher_domains,
   so the tenant the ingest binary pushes under is the SEAT's, and every site it
   pushes for needs an active listing. No product surface creates these rows
   yet, so they are written through harness/operator_writes.py, the harness's
   one database writer, which names the missing surface for each.
2. Nothing else is registered. The Exchange learns the catalog contributor's key
   and the buyer's key from their own Web Bot Auth directories when each first
   signs a call, and the Broker reads its exchange registry from its
   configuration file at boot.
3. Runs the production ingest binary `cmd/fora-ingest` ONCE PER FEED
   (philosophy/music/sfx), each as signed PushResources RPCs — one per
   submission of at most the wire bound; every demo feed fits in one. A
   non-zero exit (the binary exits non-zero on any refusal) fails the proof.
4. A Broker resolve for a demo resource (socrates), through the SDK's own
   BrokerClient, which verifies the offer against exchange-a's published key —
   proving ingest+discovery on the real catalog.
5. A purchase of the canary article thales-of-miletus.txt through the Broker
   (BrokerClient.execute), then a fetch of the signed URL it mints THROUGH the
   Cloudflare edge, asserting the body contains FORA-DEMO-CANARY-8FK3J2-0418 —
   proving ingest -> discover -> buy -> sign -> edge -> origin.

The SDK's guarded client refuses plaintext http and loopback addresses unless
ALLOW_INSECURE and SKIP_SSRF are set, and this script reads the exchange's key
directory from a loopback port over http. So it sets both for its own process
when they are unset, exactly as the runner service does for the suite.

Signed-URL host note: the Exchange signs the canonical `GET\n<url>` where the
url host is the publisher domain with NO port (http://demo.fora-protocol.org/...).
We reach the edge on its host-mapped port but send `Host: demo.fora-protocol.org`
(no port); the edge reconstructs the same port-less authority, so the signature
verifies byte-for-byte.
"""

from __future__ import annotations

import os
import sys
from contextlib import closing
from pathlib import Path

import httpx

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "tests" / "e2e"))

from harness._compose import host_project, resolve_host_port  # noqa: E402
from harness.broker_purchase import only_item, resolve_offer  # noqa: E402
from harness.sdk_client import offer_verifier, sdk_broker_client  # noqa: E402
from harness.stack_urls import StackURLs  # noqa: E402
from harness.exchanges import EXCHANGE_A_DOMAIN, seat_tenant_id  # noqa: E402
from harness.ingest_runner import ingest_succeeded, run_ingest  # noqa: E402
from harness.operator_writes import (  # noqa: E402
    list_site_on_seat,
    pg_dsn,
    provision_seat,
    register_publisher,
    register_site,
)
from harness.seed import DEMO_PUBLISHER_ID, DEMO_PUBLISHER_NAME  # noqa: E402

COMPOSE_FILE = REPO_ROOT / "docker-compose.e2e.yml"
CONTRIBUTOR_KEY = (
    REPO_ROOT / "tests" / "e2e" / "harness" / "fixtures" / "catalog_contributor_key.json"
)

# Feed paths are REPO_ROOT-relative because ingest runs with cwd=REPO_ROOT.
CATALOG_DIR_REL = "tests/e2e/harness/fixtures/catalog"

# The catalog feeds this script ingests are the HARNESS-OWNED ones under
# harness/fixtures/catalog/, the same feeds seed.py uses — NOT the demo
# deployment's feeds under deploy/fixtures/demo/.
#
# Both this script and the pytest harness seed the SAME local stack, under the
# same publisher domains and the same URIs, and fora.catalog has UNIQUE(uri):
# whichever ingests last wins. Pointing this script at a different feed set would
# silently replace the catalog the tests expect, and the damage would surface as
# unrelated tests failing on terms they never asked for.
#
# The signing scheme is a fact about the site's CDN, so it is written on the
# site's fora.publisher_domains row. No tenant id appears here: this script
# drives one exchange, and its seat is the single tenant every push names.
#
# (domain, signing_scheme, feed file, edge service).
E2E_PUBLISHERS = [
    (
        "demo.fora-protocol.org",
        "ED25519",
        f"{CATALOG_DIR_REL}/philosophy.jsonl",
        "edge",
    ),
    (
        "music.demo.fora-protocol.org",
        "ED25519",
        f"{CATALOG_DIR_REL}/music.jsonl",
        "fastly-edge",
    ),
    (
        "sfx.demo.fora-protocol.org",
        "AWS_CLOUDFRONT_RSA",
        f"{CATALOG_DIR_REL}/sfx.jsonl",
        "aws-edge",
    ),
]

# The seat this script drives: exchange-a, addressed by its own hostname. Every
# push names it, and all three demo sites are listed on it because this script
# sends all three feeds to that one exchange.
SEAT_TENANT_ID = seat_tenant_id(EXCHANGE_A_DOMAIN)
SEAT_DOMAIN = EXCHANGE_A_DOMAIN

# The buyer this proof runs as. This script never registers it, so on a stack
# only this script has seeded the agent holds
# no account, and a paid purchase is refused ACCOUNT_NOT_REGISTERED. The pytest
# seed registers it, and its first Register grants the welcome credit in USD and
# EUR.
#
# socrates, the discovery resource, carries a FREE term, and a zero charge never
# reaches the billing gate. thales, the canary, does not: the e2e catalog feed
# prices it FLAT 9.99 EUR. So the canary purchase needs the account the pytest
# seed opens, and this script does not open one itself.
#
# Its kid == agent_id so the requester label matches the signing agent in the
# evidence rows.
DEMO_AGENT_ID = "agent-demo-eur"
DEMO_AGENT_KEY_PATH = (
    REPO_ROOT / "tests" / "e2e" / "harness" / "fixtures" / "agent_demo_eur_key.json"
)


def _port(service: str, container_port: int) -> str:
    # The stack publishes host ports only when brought up with
    # docker-compose.e2e.ports.yml, which binds them to loopback.
    port = resolve_host_port(COMPOSE_FILE, service, container_port, project=host_project())
    return f"127.0.0.1:{port}"


def _register_seat_and_payee(dsn: str) -> None:
    """Create the exchange's seat and the demo payee, through the harness's operator writes."""
    provision_seat(dsn, tenant_id=SEAT_TENANT_ID, domain=SEAT_DOMAIN, allow_broker_relay=True)
    register_publisher(dsn, publisher_id=DEMO_PUBLISHER_ID, display_name=DEMO_PUBLISHER_NAME)


def _register_site(dsn: str, domain: str, scheme: str) -> None:
    """Register one site and list it ACTIVE on this seat, through the harness's operator writes."""
    register_site(dsn, domain=domain, publisher_id=DEMO_PUBLISHER_ID, scheme=scheme)
    list_site_on_seat(dsn, tenant_id=SEAT_TENANT_ID, domain=domain)


def _ingest(exchange_url: str, tenant_id: str, feed: str) -> None:
    """Push one feed. ``exchange_url`` is a host-mapped 127.0.0.1 port, which
    names no exchange, so ``--exchange`` overrides the recipient the binary
    would otherwise derive from it."""
    proc = run_ingest(
        argv_prefix=["go", "run", "./src/exchange/cmd/fora-ingest"],
        exchange_url=exchange_url,
        exchange=EXCHANGE_A_DOMAIN,
        tenant_id=tenant_id,
        key_path=CONTRIBUTOR_KEY,
        feed=Path(feed),
        cwd=REPO_ROOT,
    )
    tail = proc.stderr.strip().splitlines()[-1] if proc.stderr.strip() else ""
    print(f"  ingest {feed} -> rc={proc.returncode} | {tail}")
    if not ingest_succeeded(proc):
        print(proc.stderr)
        msg = f"ingest failed for {feed}"
        raise SystemExit(msg)


def main() -> None:
    dsn = pg_dsn(str(COMPOSE_FILE), "fora")
    exchange_url = f"http://{_port('exchange', 8081)}"
    broker_url = f"http://{_port('broker', 8082)}"
    # The SDK Broker client only consumes stack.broker; the edge fetch below
    # looks up the edge's own host port (every edge listens on 80).
    stack = StackURLs(
        exchange=exchange_url,
        # The multi-exchange topology and the identity service are not part of
        # this script's flow; they are named explicitly rather than defaulted so
        # a future field addition surfaces here instead of silently binding.
        exchange_b="",
        exchange_c="",
        exchange_d="",
        broker=broker_url,
        edge="",
        aws_edge="",
        fastly_edge="",
        lambda_edge="",
        lambda_edge_no_wba="",
        identity="",
        zitadel="",
    )

    print("== item 2: register the seat and the demo sites ==")
    _register_seat_and_payee(dsn)
    print(f"  seat {SEAT_TENANT_ID} ({SEAT_DOMAIN})")
    for domain, scheme, _feed, _svc in E2E_PUBLISHERS:
        _register_site(dsn, domain, scheme)
        print(f"  site {domain} ({scheme}), listed active")

    print("== item 2: ingest all three demo feeds via cmd/fora-ingest ==")
    for _domain, _scheme, feed, _svc in E2E_PUBLISHERS:
        _ingest(exchange_url, SEAT_TENANT_ID, feed)

    os.environ.setdefault("ALLOW_INSECURE", "true")
    os.environ.setdefault("SKIP_SSRF", "true")
    # From the host, exchange-a's domain resolves to nothing, so its key
    # directory is read from the mapped port; the key is still filed under the
    # domain every offer names.
    verifier = offer_verifier(
        [EXCHANGE_A_DOMAIN], dial={EXCHANGE_A_DOMAIN: _port("exchange", 8081)}
    )
    buyer = sdk_broker_client(stack, verifier, agent_id=DEMO_AGENT_ID, key_path=DEMO_AGENT_KEY_PATH)

    with closing(buyer) as client:
        print("== item 3: Broker resolve for socrates ==")
        socrates = "http://demo.fora-protocol.org/articles/philosophers/socrates.txt"
        offer = resolve_offer(client, socrates)
        print(f"  verified offer {offer.offer.get('offer_id')} from {offer.offer.get('exchange')}")

        print("== item 4: buy the canary, then fetch its signed URL through the Cloudflare edge ==")
        canary = "http://demo.fora-protocol.org/articles/philosophers/thales-of-miletus.txt"
        item = only_item(client.execute([resolve_offer(client, canary)]), "canary purchase")
    signed = item.retrieval_endpoint
    print(f"  retrievalEndpoint={signed}")
    if not signed:
        print(f"  item={item}")
        msg = "canary purchase returned no retrievalEndpoint"
        raise SystemExit(msg)

    edge_hostport = _port("edge", 80)
    # Replace the signed URL's host:port with the host-mapped edge, but stamp the
    # original (port-less) Host so the edge reconstructs the signed authority.
    fetch_url = signed.replace("http://demo.fora-protocol.org", f"http://{edge_hostport}")
    content = httpx.get(
        fetch_url,
        headers={"Host": "demo.fora-protocol.org"},
        follow_redirects=True,
        timeout=15.0,
    )
    print(f"  edge fetch status={content.status_code}")
    marker = "FORA-DEMO-CANARY-8FK3J2-0418"
    if marker not in content.text:
        print(content.text[:500])
        msg = "canary marker NOT found in edge-delivered body"
        raise SystemExit(msg)
    print(f"  CANARY FOUND: {marker}")
    print(f"  greek phrase present: {'καῦμα διὰ ῥάμπης' in content.text}")
    print("\nALL PROOF ITEMS 2-5 PASSED")


if __name__ == "__main__":
    main()
