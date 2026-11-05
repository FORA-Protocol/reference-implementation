# ADR-030 — A Broker's Elasticsearch Catalog Search Is Configured, Not Compiled

**Status:** Proposed (2026-11-03)

**Relates to:** ADR-006 (the Broker intermediates discovery), ADR-014 (the Exchange filters by resource and scope only), ADR-019 (error response contract: refusals carry a typed field).

---

## Context

A Broker's catalog search turns an agent's free-text query, and its
`search_filters`, into candidate catalog URLs. Each URL then takes the normal
per-URL route: the publisher's `fora.json`, the Exchange's offers, the Broker's
signature checks. A search can only propose a URL.

The `elasticsearch` provider searches an index someone else owns. For a text
publisher that index already exists, with its own fields, its own idea of
relevance and its own filter vocabulary (language, publication date, section),
and it is maintained by a team that knows Elasticsearch and does not write Go.
A first version of the provider wrote the query in Go: one `multi_match` over
configured text fields, a top-level keyword URL field it sorted on, and no
filters at all. That fixed what the owner could rank by and how the URL was
stored, and any change to either meant a Broker release. It also used the
official Elasticsearch client, which only talks to a cluster of its own major
version and refuses an answer without the product header, so a deployment on an
older cluster or on OpenSearch could not use it.

The protocol defines `search_filters` keys only for `fora-music-v1`. A text
publisher's filter keys are its deployment's choice, and agents need to learn
them.

## Decision

**D1 — The query is the index owner's search template.** The Broker runs an
Elasticsearch search template, stored in the cluster or sent with each search
from a file, and fills three parameters: `query` (absent when there is no query
text), `filters` (the accepted filters, nested by dot) and `limit`. It reads one
URL per hit at a configured path, so the URL can be stored or computed. The
template is the owner's to write, test and change with Elasticsearch's own tools;
the Broker never interprets a query.

**D2 — The filter vocabulary is a schema the deployment declares.** A JSON Schema
file names the `search_filters` keys the template applies and the shape of each
value. The Broker validates every request against it before any search: a key it
does not declare is `UNIMPLEMENTED` and a value it refuses is `INVALID_ARGUMENT`,
each on the field `search_filters.<key>`, and with no schema every key is refused.
A filter is never dropped, because an answer that ignored one would answer a
different question. The schema is compiled by the protocol SDK's registration
schema compiler, with its size, depth, cost and pattern bounds, plus rules that
keep it one flat list of keys: no rule over the whole object, no reference
keyword and no `default`.

**D3 — The identity service publishes the same schema.** Its `fora_discover`
tool embeds the schema as the input schema of `search_filters`, so an agent sees
the accepted keys and formats before it calls. The MCP library then checks every
call against it before the identity service sends anything to the Broker, a call
that names URLs included, although the Broker reads no filter on such a call.
This check is stricter than the Broker by design, and the tool's description
says so; a request sent straight to the Broker meets the Broker's checks alone.
Both services load the file through one loader, so both accept and refuse the
same schema documents. References are refused because an embedded document
resolves a reference against the tool's schema instead of its own. Defaults are
refused because the MCP library fills a declared default into every call, so a
tool call would carry a filter the agent never sent, which a request straight to
the Broker would not.

**D4 — Plain HTTP to two endpoints.** The Broker calls `_search/template` and
`_render/template` and nothing else, with the standard library's HTTP client: no
client library, no product check, no server-version lock. It works with
Elasticsearch 6.x and later and with OpenSearch, and is tested on 7.15.0. It
follows no redirect and retries nothing.

**D5 — Every risky choice is stated.** Authentication is `none`, `api_key` or
`basic`, with no default. Plain `http` has its own opt-in, separate from the
stack-wide development switch. Both log a warning at start-up while in use.

**D6 — Nothing else changes.** The protocol, the SDKs and the Exchange are
untouched; the provider is one more implementation of the Broker's catalog
search.

## Consequences

- The owner changes ranking, fields and filter semantics without a Broker
  release, and checks a template with `_render/template` before the Broker runs
  it. The Broker checks it the same way at start-up.
- The Broker's guarantees stay its own: the URL comes from the configured path and
  is routed like any other, a failed or partial search is
  `TEMPORARILY_UNAVAILABLE`, and named URLs never reach the index.
- The template inserts agent text. The cluster escapes a `{{value}}` as a JSON
  string, which keeps the text inside its clause. The forms that insert it
  unescaped (`{{{ }}}`, `{{& }}`, the join lambda) and a change of delimiters,
  under which they can be written another way, are refused in a template file. A
  stored template cannot be read with the Broker's read-only key, so for a stored
  one that rule is the owner's to keep.
- The schema file is mounted in two services. A mismatch shows at once: the
  Broker refuses a key the identity service advertised, naming it.
- An agent using the protocol directly, not through the identity service, learns
  the vocabulary from the deployment's documentation; the Broker's refusals name
  the key either way.

## Accepted gap

The end-to-end stack's identity service talks to the other Broker and loads no
schema, so publishing the schema through the MCP tool is proven by the identity
service's own tests, through the MCP SDK's real validation path, and not end to
end. The step that closes it is a second identity service in front of the
Elasticsearch Broker.

## Rejected alternatives

- **A default query driven by settings** (fields, weights, a URL field). It is the
  first version again, with every new ranking need a new setting.
- **One client library per engine.** The library pins the server's major version
  and refuses answers without its product header, which excludes older clusters
  and OpenSearch, and adds a dependency chain the Broker does not otherwise need.
- **A script engine inside the Broker** (for example a JavaScript runtime running
  owner-supplied functions). It puts a language runtime into the service that
  signs requests, and turns a query the owner can test with Elasticsearch's tools
  into code only the Broker can run.
- **A generic external search contract**, where the Broker calls the operator's
  own search service. A marketplace implements Broker discovery as suits it, and
  a configurable endpoint contract would constrain implementations rather than
  enable them.
- **A fork, or an operator-built image.** Every Broker release would need merging,
  and the deployment would no longer run the image this project publishes.
- **A WebAssembly plugin.** The same objections as a script engine, plus a build
  toolchain the index owner would have to adopt.
