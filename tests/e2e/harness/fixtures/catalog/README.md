# E2E catalog feeds

The four publisher feeds this harness seeds its catalog from. They are owned by
the test suite. Change them when a test needs a term shape that is missing, and
do not change them to make a demo look better.

The first three started as a copy of the demo feeds under `deploy/fixtures/demo/`.
That directory is now demo-only. `archive` was written for this suite and has no
demo counterpart. The two sets are free to diverge, and are expected
to: a demo wants one currency and a tidy story, a test suite wants the awkward
combinations.

## How they are used

`seed.py` shells out to the production `fora-ingest` binary once per feed, each
a single signed `PushResources` RPC. There is no Python re-implementation of the
mapper and no direct catalog write. That is the property the suite depends on,
and it comes from the ingest path, not from which feed is on the other end of it.

Paths are resolved from the harness package, not from the repository root. The
runner image copies this tree to `/runner/harness`, so a repo-root-relative path
would not exist inside the container.

## Invariants the tests rely on

Change any of these and expect failures well away from the file you edited.

- **One feed per publisher domain, one edge runtime each.** `philosophy` is
  Cloudflare, `music` is Fastly, `sfx` is AWS. `sfx` is also the only
  `AWS_CLOUDFRONT_RSA` tenant, so it is the only feed that exercises RSA signing.
  `archive` is the exception: it has no edge and nothing fetches its content. It
  is the catalog of exchange-d, the one seat that refuses purchases the Broker
  relays, so it exists to be discovered and bought, not delivered. It keeps one
  FREE and one priced USD term, so a direct purchase there moves money and a
  refused relayed one visibly does not.
- **Currency spread: 23 EUR, 11 USD, 6 GBP across 40 terms in 28 records.** The
  suite needs more than one currency. Every Exchange in the e2e stack bills in
  USD and EUR, one TigerBeetle ledger each, so USD and EUR terms are the ones a
  funded buyer can transact — priced-path coverage depends on them existing. No
  Exchange bills in GBP, so a GBP priced term is a currency-mismatch case, which
  is also worth keeping.
- **At least one FREE term per feed.** A zero charge never reaches the
  billing gate, so a FREE term is reachable by an agent that holds no money.
- **Term shapes stay varied:** flat and per-unit pricing, quotas, obligations,
  prohibited functions, geo and user-type restrictions, and a subscription /
  reference-only entry. Each backs at least one scenario.
- **Four `music` tracks carry `music.<field>` tags in `ext` and three alternative terms
  each** (online creator, broadcast, advertising). The Broker's catalog search
  indexes them, and the query discovery tests expect exact results from them:
  change a tag and those tests change with it. Their `vocals` and
  `suitable_for` values belong to this suite, not to the demo feed.
- **Every record with a per-unit term states its estimate.** A metered offer may
  carry an `estimated_quantity` or none, and a publisher may state it once for the
  resource, as the record's `estimated_quantity` (the feed carries no estimate on a
  term). An estimate the publisher states is carried onto the offer, so these
  records are where that obligation is tested. The value is in the metered term's
  own unit: one access or stream, the text's length in characters or tokens, the
  track's length in minutes. A metered offer with no estimate at all is pushed by
  the tests that need one.
- **Every record stays VALID.** Malformed prices, unknown vocabulary tokens and
  other rejection cases belong in per-test inputs. One bad record here makes the
  whole seed fail, and every test with it.

## Content bodies

The feeds point at content served from `deploy/content/demo`, which
`docker-compose.e2e.yml` mounts into the origin container. Currency and licence
terms are catalog metadata; the article bodies are neutral, so they are still
shared. The retrieval canary the tests assert on lives in that content, so an
edit to the demo copy can break a test here. Split the content tree when that
starts happening, or when a test needs a body of its own.

## The Elasticsearch index

The `es-seed` one-shot (`tests/e2e/es-seed/seed.py`) builds the index `broker-es`
searches from `philosophy.jsonl`, the feed exchange-a sells, and it stands in for
the index owner: nothing else in the stack writes to Elasticsearch. Each record
becomes one document, whose `url` is the catalog URI the Exchange stores for it,
whose `title` is the record's title and whose `body` is the article text under
`deploy/content/demo/stoa-press/articles/philosophers/`. One more document, titled
Diogenes, is in the index and in no feed, so a search can hit a URL the Exchange
does not hold; it is the last document.

Each document also carries a `language` and a `date_published` for the example
filter schema to select on. The seed assigns them by the document's position, in
feed order with Diogenes last, and does not read them from the article: an even
position is `en`, an odd one `el`, and position *i* was published on
2026-01-(*i*+1). The seed also stores the example search template
(`deploy/broker/search-template.example.mustache`) under the id `broker-es` names.
`test_broker_es_query.py` expects exact results from all of this: change a title,
an article, the feed order or the rule, and those tests may change with it.
