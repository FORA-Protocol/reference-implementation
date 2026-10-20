"""The edge's bot gate: each client class against each decision the edge makes.

EDGEHINT-008 to EDGEHINT-015. The expected decision in each cell comes from the
edge function's documentation in the pinned module
``github.com/FORA-Protocol/protocol`` (``components/edge-function/bot-detection.mdx``
and ``overview.mdx``), never from the edge's code:

==================  ==============  =================  ================  ==============
client              licensed page,  valid signed URL   signed URL that   the manifest
                    unsigned        with its proof     fails to verify
==================  ==============  =================  ================  ==============
browser             served          served             403               200
search crawler      served          served             403               200
AI bot (the list)   403             served             403               200
no User-Agent       403 (DECISION)  --                 --                200
==================  ==============  =================  ================  ==============

* "Known AI crawler User-Agents. This is the primary detection mechanism." Each
  token of the maintained list is driven as the User-Agent (EDGEHINT-008).
* "Unknown User-Agents pass through." A search engine's crawler is not on that list,
  so it reads as a browser does (EDGEHINT-009).
* "a valid signed URL always takes precedence" (EDGEHINT-010), and a signed URL
  that fails verification "always gets a 403, regardless of whether the requester
  is a bot or a browser" (EDGEHINT-011).
* the manifest is served "so protocol-aware agents discover the Exchange without
  triggering a 403" (EDGEHINT-012).

Two cells the protocol does not state are the edge's documented contract
(``src/edge/README.md``), recorded as DECISION entries for the protocol author to
confirm: a request with no User-Agent is refused as an AI bot's (EDGEHINT-014), and
an unsigned request with a method other than GET or HEAD is never gated
(EDGEHINT-015). Open content outside a licensed scope (EDGEHINT-013) is not driven:
no edge in the stack sets a scope, so every path is licensed. The protocol names no
reason token for the AI-bot 403, so its cells assert the status and that nothing was
served; its headers are proved in ``test_edge_hints.py``.

Every cell runs on Cloudflare, Fastly and Lambda@Edge (``harness.edge_runtimes``).
Cloudflare's verified-bot signal is not driven: Miniflare attaches no verified-bot
data to a request, so the gate reads the User-Agent alone, as on the other two.
"""

from __future__ import annotations

from http import HTTPStatus

import pytest
from wire.models import WellKnownManifest

from ..constants import AI_BOT_UA, BINGBOT_UA, BROWSER_UA, GOOGLEBOT_UA
from ..defects import EDGE_AI_BOT_TOKEN_READ_AS_SEARCH, EDGE_AI_BOT_TOKEN_UNLISTED, Defect
from ..edge_fetch import tamper_query_param
from ..edge_runtimes import (
    RUNTIMES,
    EdgeRuntime,
    assert_refused,
    assert_served,
    bound_urls,
    proof,
    read,
)
from ..fresh_agent import FreshAgent
from ..lambda_edge import ExchangeDeliveryKey, load_exchange_delivery_key
from ..obligation_registry import Obligation, obligation
from ..protocol_documents import parse_document
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_EACH_RUNTIME = pytest.mark.parametrize("runtime", RUNTIMES, ids=str)

CONTENT_RULES = "X-Content-Rules"
EXCHANGE_HINT = "X-FORA-Exchange"

#: The protocol's maintained list of AI crawler User-Agents, verbatim from
#: bot-detection.mdx, each sent as the whole User-Agent. Matching is "exact substring
#: match", so a User-Agent that is the token alone carries it.
AI_BOT_TOKENS = (
    "ClaudeBot/1.0",
    "anthropic-ai",
    "GPTBot/1.0",
    "ChatGPT-User",
    "CCBot/2.0",
    "Google-Extended",
    "Googlebot-Extended",
    "Bytespider",
    "PerplexityBot",
    "YouBot",
    "Applebot-Extended",
    "cohere-ai",
    "Meta-ExternalAgent",
    "Amazonbot",
    "AI2Bot",
    "Diffbot",
    "Omgilibot",
    "FacebookBot",
    "fora-ai-buyer",
)

#: The listed tokens the edge lets through, each with its defect.
_MISSED: dict[str, Defect] = {
    "cohere-ai": EDGE_AI_BOT_TOKEN_UNLISTED,
    "fora-ai-buyer": EDGE_AI_BOT_TOKEN_UNLISTED,
    "Googlebot-Extended": EDGE_AI_BOT_TOKEN_READ_AS_SEARCH,
}

_NOT_ON_THE_LIST = {"browser": BROWSER_UA, "googlebot": GOOGLEBOT_UA, "bingbot": BINGBOT_UA}

#: One User-Agent of each class that sends one.
_EACH_CLASS = {"browser": BROWSER_UA, "search crawler": GOOGLEBOT_UA, "AI bot": AI_BOT_UA}


def _token_param(token: str) -> object:
    defect = _MISSED.get(token)
    return pytest.param(token, id=token, marks=defect.marker if defect else ())


@pytest.fixture(scope="module")
def exchange_key(compose_stack: StackURLs) -> ExchangeDeliveryKey:
    """exchange-a's delivery-URL key, confirmed to be the one it publishes."""
    return load_exchange_delivery_key(compose_stack.exchange)


@obligation(Obligation.EDGEHINT_008, negative=True)
@_EACH_RUNTIME
@pytest.mark.parametrize("token", [_token_param(token) for token in AI_BOT_TOKENS])
def test_each_listed_ai_bot_is_refused_an_unsigned_read(
    compose_stack: StackURLs, runtime: EdgeRuntime, token: str
) -> None:
    """An unsigned read of a licensed page under a listed AI crawler's User-Agent is refused 403.

    The control, a browser reading the same page unsigned, is
    ``test_a_reader_not_on_the_ai_list_reads_licensed_content_unsigned``.
    """
    reply = read(compose_stack, runtime, runtime.article, headers={"User-Agent": token})
    defect = _MISSED.get(token)
    if defect is not None:
        defect.check(
            reply.status == HTTPStatus.FORBIDDEN and reply.forwarded is not True,
            f"{runtime}: {token!r} was answered {reply.status}, forwarded={reply.forwarded}",
        )
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, None)


@obligation(Obligation.EDGEHINT_009)
@_EACH_RUNTIME
@pytest.mark.parametrize("user_agent", list(_NOT_ON_THE_LIST.values()), ids=list(_NOT_ON_THE_LIST))
def test_a_reader_not_on_the_ai_list_reads_licensed_content_unsigned(
    compose_stack: StackURLs, runtime: EdgeRuntime, user_agent: str
) -> None:
    """A browser, Googlebot and bingbot read a licensed page unsigned, with no discovery header."""
    reply = read(compose_stack, runtime, runtime.article, headers={"User-Agent": user_agent})
    assert_served(runtime, reply, runtime.article)
    assert reply.header(CONTENT_RULES) is None, f"{runtime}: {reply.headers}"
    assert reply.header(EXCHANGE_HINT) is None, f"{runtime}: {reply.headers}"


@obligation(Obligation.EDGEHINT_010)
@_EACH_RUNTIME
def test_a_valid_signed_url_is_served_whatever_the_user_agent(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """The buyer's bound URL, with a fresh proof each time, is served under every client class."""
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    for name, user_agent in _EACH_CLASS.items():
        headers = {**proof(url, fresh_agent), "User-Agent": user_agent}
        reply = read(compose_stack, runtime, url, headers=headers)
        assert reply.status != HTTPStatus.FORBIDDEN, f"{runtime}: refused the {name}: {reply.body}"
        assert_served(runtime, reply, url)


@obligation(Obligation.EDGEHINT_011, negative=True)
@_EACH_RUNTIME
def test_a_signed_url_that_fails_verification_is_refused_whatever_the_user_agent(
    compose_stack: StackURLs,
    exchange_key: ExchangeDeliveryKey,
    fresh_agent: FreshAgent,
    runtime: EdgeRuntime,
) -> None:
    """A tampered URL is refused for the browser too: the signature check never yields to the gate.

    Control: the untampered URL, with the buyer's proof, is served to a browser.
    Then the same URL with one character of its signature changed, each time with a
    fresh proof made for it, is refused 403 ``signature_mismatch`` under every
    client class.
    """
    url = bound_urls(runtime, fresh_agent, exchange_key)[0]
    control = read(
        compose_stack, runtime, url, headers={**proof(url, fresh_agent), "User-Agent": BROWSER_UA}
    )
    assert_served(runtime, control, url)

    tampered = tamper_query_param(url)
    for user_agent in _EACH_CLASS.values():
        headers = {**proof(tampered, fresh_agent), "User-Agent": user_agent}
        reply = read(compose_stack, runtime, tampered, headers=headers)
        assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, "signature_mismatch")


@obligation(Obligation.EDGEHINT_012)
@_EACH_RUNTIME
def test_the_manifest_is_served_to_every_client(
    compose_stack: StackURLs, runtime: EdgeRuntime
) -> None:
    """/.well-known/fora.json answers 200 with this publisher's manifest, to a bot as to anyone."""
    for headers in [{"User-Agent": ua} for ua in _EACH_CLASS.values()] + [{}]:
        reply = read(compose_stack, runtime, runtime.manifest_url, headers=headers)
        assert reply.status == HTTPStatus.OK, f"{runtime} {headers}: answered {reply.status}"
        manifest = parse_document(WellKnownManifest, reply.body)
        assert manifest.domain == runtime.publisher, f"{runtime}: {manifest.domain}"


@obligation(Obligation.EDGEHINT_014, negative=True)
@_EACH_RUNTIME
def test_an_unsigned_read_with_no_user_agent_is_refused(
    compose_stack: StackURLs, runtime: EdgeRuntime
) -> None:
    """A read that carries no User-Agent header at all is refused like an AI bot's.

    The control, a browser reading the same page, is
    ``test_a_reader_not_on_the_ai_list_reads_licensed_content_unsigned``.
    """
    reply = read(compose_stack, runtime, runtime.article, headers={})
    assert_refused(runtime, reply, HTTPStatus.FORBIDDEN, None)


@obligation(Obligation.EDGEHINT_015)
@_EACH_RUNTIME
def test_an_unsigned_post_from_an_ai_bot_is_not_gated(
    compose_stack: StackURLs, runtime: EdgeRuntime
) -> None:
    """An AI bot's unsigned POST to a licensed page goes to the origin, not to the gate.

    On Lambda@Edge the request is handed back to CloudFront. Over HTTP the answer is
    whatever the origin makes of a POST to a static page, and is not the edge's 403
    with the discovery headers.
    """
    reply = read(
        compose_stack,
        runtime,
        runtime.article,
        method="POST",
        headers={"User-Agent": AI_BOT_UA, "Content-Type": "application/json"},
        body=b"{}",
    )
    if not runtime.http:
        assert reply.forwarded, f"{runtime}: the POST was not handed to the origin: {reply}"
        return
    assert reply.status != HTTPStatus.FORBIDDEN, f"{runtime}: the POST was gated: {reply.body}"
    assert reply.header(CONTENT_RULES) is None, f"{runtime}: {reply.headers}"
