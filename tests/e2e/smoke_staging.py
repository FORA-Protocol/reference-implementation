"""Staging smoke proof: discover -> execute -> signed URL -> edge -> origin.

Env-parameterized smoke driver for the Terraform staging stack
(deploy/terraform/stacks/staging-aws). NOT a pytest module — a standalone
script run against a live, already-seeded staging deployment
(seed-staging.sh), normally via deploy/terraform/scripts/smoke.sh:

    FORA_STAGING_EXCHANGE_URL=https://exchange.<domain> \
    FORA_STAGING_BROKER_URL=https://broker.<domain> \
    FORA_STAGING_PUBLISHER=demo.<domain> \
    FORA_STAGING_AGENT_ID=smoke-agent.<domain> \
    FORA_STAGING_AGENT_KEY=deploy/terraform/stacks/staging-aws/keys/agent-key.json \
    uv run --project tests/e2e python tests/e2e/smoke_staging.py

What it proves, end to end, over the real public surface (HTTPS, Cloudflare
DNS, the deployed edge worker):

1. Licensed discovery: Broker Resolve for a demo resource (socrates), through
   the SDK's own BrokerClient, returns verified offers — ingest + discovery
   work. Resolve is DISCOVERY-ONLY: it carries ranked signed Offers, never a
   signed URL.
2. Two-phase delivery for the canary: the winning Offer is bought through the
   Broker (BrokerService.ExecuteTransaction, through the SDK) and the Exchange
   mints the agent-bound signed retrievalEndpoint on the result item. A bare fetch of that URL (no proof of
   possession) is expected to DELIVER the origin body by default, matching an
   edge deployed with fora_enforce_binding = "false"; set
   FORA_STAGING_ENFORCE_BINDING=true for an edge that enforces binding, where
   the bare fetch must instead be REFUSED (403). Fetching WITH
   proof-of-possession headers (harness.edge_fetch.fetch_signed) delivers the
   origin body containing FORA-DEMO-CANARY-8FK3J2-0418 — mint -> edge verify
   (signature + agent binding) -> origin round trip.
3. The public surface every unlicensed caller sees. The first two legs run
   on the same article as item 2, so a failure isolates the UA handling:
   - a BROWSER User-Agent with no signature gets the article (200 + canary
     marker) — the bot gate only sends AI bots to negotiate, never people;
   - an AI-BOT User-Agent with no signature is refused (403) by the edge
     itself, with no redirect followed, WITH the negotiation payload: the {error, reason: "ai_bot"} JSON body, the
     X-Content-Rules header pointing at this publisher's
     /.well-known/fora.json, and the X-FORA-Exchange header naming the
     exchange to negotiate with. (The retired demo also announced MCP
     endpoints on this response; the current worker does not, so nothing MCP
     is asserted here.)
   - the site root serves the human-facing landing page to a browser
     User-Agent (200 HTML naming the demo catalog) — a stakeholder demo
     starts in a browser, so the first impression must be the catalog index,
     not an error page. An AI-bot User-Agent, following no redirect, is
     refused there (403) when the edge licenses the whole site, and reads the
     page itself (200) when the edge's licensed paths
     (FORA_STAGING_LICENSED_PATHS_JSON) leave the root out;
   - /.well-known/fora.json answers any User-Agent with no signature (200,
     domain matching this publisher) — the guard against a bot being locked
     out of the very document that tells it how to negotiate.

Both legs traverse the full deployed stack: agent-signed calls -> Broker ->
Exchange -> signed URL -> edge worker -> Caddy -> origin container. Nothing
reads or writes staging state directly — seeding is seed-staging.sh's job.

Scope note: the staging stack deploys the Exchange with the tigerbeetle
billing adapter, but this script asserts nothing about billing — it proves
the licensing/delivery path only.
"""

from __future__ import annotations

import json
import os
import sys
from http import HTTPStatus
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "tests" / "e2e"))

from fora_sdk.client import CallError  # noqa: E402
from fora_sdk.hosts import host_of  # noqa: E402
from fora_sdk.resolvers import ResolverError, guarded_client, read_manifest  # noqa: E402

from harness.broker_purchase import AI_INPUT, only_verified_offer, resolve_request  # noqa: E402
from harness.constants import AI_BOT_UA, BROWSER_UA  # noqa: E402
from harness.edge_fetch import browse, fetch_signed, fetch_without_proof, present  # noqa: E402
from harness.sdk_client import offer_verifier, sdk_broker_client  # noqa: E402
from harness.stack_urls import StackURLs  # noqa: E402

CANARY_MARKER = "FORA-DEMO-CANARY-8FK3J2-0418"


def _require_env(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        msg = f"missing required env var: {name}"
        raise SystemExit(msg)
    return value


def _bare_fetch_binding_leg(signed: str, *, enforce: bool) -> None:
    """Fetch the agent-bound signed URL WITHOUT proof of possession.

    The staging stack always mints agent-bound URLs, so a URL without the
    agent_id binding is itself a failure regardless of edge enforcement.

    The expected outcome depends on how the edge under test is deployed:

    - enforce=True (the production posture): the edge must REFUSE the bare
      fetch (403). This is the proof that binding is actually enforced, not
      merely that the happy path works.
    - enforce=False (edge deployed with fora_enforce_binding = "false"): the
      signed URL alone is sufficient, so the bare fetch must DELIVER the origin
      body (200 + canary marker).
    """
    if "agent_id=" not in signed:
        print(f"  retrievalEndpoint={signed}")
        msg = "signed URL carries no agent binding — staging must mint agent-bound URLs"
        raise SystemExit(msg)
    bare = fetch_without_proof(signed)
    if enforce:
        if bare.status != HTTPStatus.FORBIDDEN:
            print(f"  bare fetch: status={bare.status} body={bare.text[:200]}")
            msg = "edge accepted an agent-bound URL without proof of possession"
            raise SystemExit(msg)
        print("  bare fetch refused (403) — agent binding enforced")
        return
    if bare.status != HTTPStatus.OK or CANARY_MARKER not in bare.text:
        print(f"  bare fetch: status={bare.status} body={bare.text[:200]}")
        msg = "edge did not deliver the signed URL with binding enforcement off"
        raise SystemExit(msg)
    print("  bare fetch delivered (200) — agent binding not enforced")


def _browser_leg(article: str) -> None:
    """A browser User-Agent with no signature must get the article (200)."""
    resp = browse(article, user_agent=BROWSER_UA)
    if resp.status != HTTPStatus.OK or CANARY_MARKER not in resp.text:
        print(f"  browser fetch: status={resp.status} body={resp.text[:200]}")
        msg = "edge did not serve the article to a browser User-Agent"
        raise SystemExit(msg)
    print("  browser fetch delivered (200 + canary) — people read without a license")


def _bot_negotiation_leg(article: str, publisher: str, exchange_url: str) -> None:
    """An AI-bot User-Agent with no signature must get 403 + how to negotiate.

    The refusal alone is not enough: the bot must be TOLD where to go. The
    payload under test is the JSON {error, reason: "ai_bot"} body, the
    X-Content-Rules header (this publisher's fora.json), and the
    X-FORA-Exchange header (the exchange to negotiate with). No redirect is
    followed: the edge itself must refuse the bot.
    """
    resp = present(article, headers={"User-Agent": AI_BOT_UA})
    if resp.status != HTTPStatus.FORBIDDEN:
        print(f"  bot fetch: status={resp.status} body={resp.text[:200]}")
        msg = "edge did not refuse an AI-bot User-Agent without a signed URL"
        raise SystemExit(msg)
    if resp.reason != "ai_bot":
        print(f"  bot fetch reason={resp.reason}")
        msg = 'bot refusal body did not carry reason "ai_bot"'
        raise SystemExit(msg)
    rules = resp.header("X-Content-Rules") or ""
    expected_rules = f"https://{publisher}/.well-known/fora.json"
    if rules != expected_rules:
        print(f"  X-Content-Rules={rules!r} expected={expected_rules!r}")
        msg = "bot refusal did not point at this publisher's fora.json"
        raise SystemExit(msg)
    exchange_header = resp.header("X-FORA-Exchange") or ""
    if exchange_header != exchange_url:
        print(f"  X-FORA-Exchange={exchange_header!r} expected={exchange_url!r}")
        msg = "bot refusal did not name the exchange to negotiate with"
        raise SystemExit(msg)
    print("  bot fetch refused (403) with fora.json pointer + exchange header")


def _landing_page_leg(publisher: str, licensed_paths: list[str]) -> None:
    """The site root serves the catalog landing page to a browser (200 HTML).

    A stakeholder demo starts by opening the demo hostname in a browser, so
    the root must be an index page, not an error. The bot side of the same
    URL is asserted too, and it follows the edge's licensed scope. With no
    scope the whole site is licensed, so the landing page must not open a
    hole in the bot gate at the root. With a scope that leaves the root out,
    the page is public and the bot reads the page itself (200). The bot's
    request follows no redirect, so either answer is the edge's own.
    """
    url = f"https://{publisher}/"
    resp = browse(url, user_agent=BROWSER_UA)
    if resp.status != HTTPStatus.OK or resp.media_type != "text/html":
        print(f"  landing page: status={resp.status} media type={resp.media_type!r}")
        msg = "site root did not serve the landing page to a browser User-Agent"
        raise SystemExit(msg)
    if "Stoa Press" not in resp.text:
        print(f"  landing page body={resp.text[:300]}")
        msg = "landing page does not name the demo catalog"
        raise SystemExit(msg)
    bot = present(url, headers={"User-Agent": AI_BOT_UA})
    root_licensed = not licensed_paths or any(p.lower() == "/" for p in licensed_paths)
    if not root_licensed:
        if bot.status != HTTPStatus.OK:
            print(f"  bot at root: status={bot.status} body={bot.text[:200]}")
            msg = "site root is outside the licensed paths but refused an AI-bot User-Agent"
            raise SystemExit(msg)
        print("  landing page served to a browser and, outside the licensed paths, to a bot")
        return
    if bot.status != HTTPStatus.FORBIDDEN:
        print(f"  bot at root: status={bot.status} body={bot.text[:200]}")
        msg = "site root did not refuse an AI-bot User-Agent"
        raise SystemExit(msg)
    print("  landing page served to a browser (200 HTML); bot still refused at root")


def _well_known_leg(publisher: str) -> None:
    """/.well-known/fora.json must answer ANY User-Agent with no signature.

    This is the guard against a lockout loop: the refusal above points bots
    at this document, so the bot gate must never apply to it.
    """
    try:
        with guarded_client(headers={"User-Agent": AI_BOT_UA}) as http:
            manifest = read_manifest(publisher, http=http).message
    except (ResolverError, ValueError) as err:
        # ValueError covers a body the protocol's strict schema refuses.
        msg = f"fora.json fetch (bot User-Agent) was refused: {err}"
        raise SystemExit(msg) from err
    # The SDK reader does not compare the document's domain with the one it
    # fetched, so this leg still does.
    if manifest.domain != publisher:
        print(f"  fora.json domain={manifest.domain!r}")
        msg = "fora.json did not carry this publisher's domain"
        raise SystemExit(msg)
    print("  fora.json served unsigned to a bot User-Agent, domain matches")


def main() -> None:
    exchange_url = _require_env("FORA_STAGING_EXCHANGE_URL")
    broker_url = _require_env("FORA_STAGING_BROKER_URL")
    publisher = _require_env("FORA_STAGING_PUBLISHER")
    agent_id = _require_env("FORA_STAGING_AGENT_ID")
    agent_key = Path(_require_env("FORA_STAGING_AGENT_KEY"))
    if not agent_key.is_file():
        msg = f"agent key not found: {agent_key}"
        raise SystemExit(msg)

    # Whether the edge under test enforces agent-key proof-of-possession.
    # Defaults to False, matching the staging edge deployed with
    # fora_enforce_binding = "false": the bare-fetch leg then expects delivery
    # (200). Set FORA_STAGING_ENFORCE_BINDING=true for an edge that enforces
    # binding, where the bare fetch is refused (403). Only the exact string
    # "true" enables the enforcement expectation.
    enforce_binding = os.environ.get("FORA_STAGING_ENFORCE_BINDING", "false") == "true"

    # The path prefixes the edge licenses (its LICENSED_PATHS_JSON). Empty or
    # unset means the whole site, the edge's own default.
    licensed_paths: list[str] = json.loads(
        os.environ.get("FORA_STAGING_LICENSED_PATHS_JSON") or "[]"
    )

    # Only the exchange and broker URLs are consumed by the harness helpers
    # used here (fetch_signed follows the signed URL's own hostname over real
    # DNS, so no edge field is read). The empty strings satisfy the tuple
    # shape on purpose: if a future helper reads one of them, it fails fast
    # on an invalid URL rather than silently hitting the wrong endpoint.
    stack = StackURLs(
        exchange=exchange_url,
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

    # The SDK verifies every offer against its issuing Exchange's published key,
    # read over https from the Exchange's own host, the one every offer names.
    verifier = offer_verifier([host_of(exchange_url)], scheme="https")
    buyer = sdk_broker_client(stack, verifier, agent_id=agent_id, key_path=agent_key)

    print("== item 1: licensed discovery for socrates ==")
    socrates = f"https://{publisher}/articles/philosophers/socrates.txt"
    try:
        result = buyer.resolve(resolve_request([socrates], intended_use=AI_INPUT))
    except CallError as err:
        buyer.close()
        msg = f"socrates resolve was refused: {err}"
        raise SystemExit(msg) from err
    # Resolve is discovery-only: a licensed discovery is a NON-EMPTY group of
    # verified offers for the requested uri (never a retrievalEndpoint — that is
    # minted at purchase time).
    issuers = sorted(
        {
            str(o.offer.get("exchange"))
            for g in result.groups
            if g.uri == socrates
            for o in g.result.verified
        }
    )
    if not issuers:
        buyer.close()
        print(f"  result={result}")
        msg = "socrates discovery returned no verified offers"
        raise SystemExit(msg)
    print(f"  offers from exchange(s): {issuers}")

    print("== item 2: buy canary offer -> signed URL -> edge fetch ==")
    canary = f"https://{publisher}/articles/philosophers/thales-of-miletus.txt"
    # Two phases: Broker Resolve (discovery), then a purchase of the winning
    # offer through the Broker; the Exchange mints the agent-bound signed URL on
    # the purchase's result item.
    try:
        found = buyer.resolve(resolve_request([canary], intended_use=AI_INPUT))
        groups = [g for g in found.groups if g.uri == canary]
        if len(groups) != 1:
            msg = f"canary resolve answered {len(groups)} groups for {canary}"
            raise SystemExit(msg)
        purchase = buyer.execute([only_verified_offer(groups[0], "canary resolve")])
    except (CallError, AssertionError) as err:
        msg = f"canary purchase failed: {err}"
        raise SystemExit(msg) from err
    finally:
        buyer.close()
    items = purchase.items or []
    signed = items[0].retrieval_endpoint if len(items) == 1 else None
    print(f"  retrievalEndpoint={signed}")
    if not signed:
        print(f"  response={purchase}")
        msg = "canary purchase returned no retrievalEndpoint"
        raise SystemExit(msg)

    # Binding leg first: fetch the agent-bound URL WITHOUT proof of possession.
    # The expected outcome (refused vs delivered) tracks how the edge under
    # test is deployed — see FORA_STAGING_ENFORCE_BINDING above.
    _bare_fetch_binding_leg(signed, enforce=enforce_binding)

    # Happy path: real DNS, real TLS — the signed URL's hostname IS the
    # deployed edge worker's route. fetch_signed fetches through the SDK, which
    # presents the PoP headers (X-FORA-Agent-Key + RFC 9421 GET signature); the
    # marker is asserted.
    try:
        content = fetch_signed(signed, stack, expect_marker=CANARY_MARKER, key_path=agent_key)
    except (AssertionError, CallError) as err:
        # Deliberate: fetch_signed asserts internally (it is harness code);
        # converting to SystemExit turns a traceback into a readable smoke
        # failure. The explicit re-check below keeps the proof valid even
        # under `python -O`, where those asserts vanish.
        msg = f"edge delivery failed: {err}"
        raise SystemExit(msg) from err
    # Re-checked explicitly: fetch_signed's internal asserts vanish under
    # `python -O`, and this is the proof the smoke exists for.
    delivered = content.body.decode("utf-8", errors="replace")
    if CANARY_MARKER not in delivered:
        print(f"  edge fetch: body={delivered[:200]}")
        msg = "edge delivery did not return the canary body"
        raise SystemExit(msg)
    print(f"  CANARY FOUND: {CANARY_MARKER}")

    print("== item 3: public surface — browser, AI bot, landing page, well-known ==")
    # The first two legs run on the same article as item 2, so a failure here
    # isolates the UA handling: the content itself was just proven fetchable
    # through the edge. The last two legs run on the site root and fora.json.
    _browser_leg(canary)
    _bot_negotiation_leg(canary, publisher, exchange_url)
    _landing_page_leg(publisher, licensed_paths)
    _well_known_leg(publisher)

    print("\nSTAGING SMOKE PASSED")


if __name__ == "__main__":
    main()
