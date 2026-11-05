# FORA Broker — Writing the Search Template

This guide is for whoever owns the Elasticsearch index a Broker searches. On the
`elasticsearch` catalog search provider the Broker does not write a query of its
own: it runs **your search template**, and you decide which fields are searched,
how hits rank, how each filter applies and where a hit carries its URL. The
Broker's own settings are in [`CONFIGURATION.md`](CONFIGURATION.md) §7.

A working pair to start from ships with the Broker:
`deploy/broker/search-template.example.mustache` (the template) and
`deploy/broker/search-filters.example.json` (the filter schema it applies). The
Broker's tests run both against a real node.

## 1. What the Broker sends

Every search is one call:

```
POST /<BROKER_ES_INDEX>/_search/template
{"id": "<BROKER_ES_TEMPLATE_ID>", "params": {"query": "...", "filters": {...}, "limit": 50}}
```

With `BROKER_ES_TEMPLATE_FILE` the body carries `"source": "<the file's text>"`
instead of `"id"`. The request id travels as `X-Opaque-Id`, so the cluster's logs
correlate with the Broker's.

| Parameter | What it holds | When it is present |
|---|---|---|
| `query` | The agent's free text, exactly as sent. | Only when the request has query text. A request with filters and no query sends no `query` at all. |
| `filters` | The filters the schema accepted, nested by dot: `news.language` arrives as `filters.news.language`. | Only when at least one filter was accepted. |
| `limit` | `BROKER_ES_RESULT_LIMIT`, the most URLs the Broker takes from one search. | Always. |

`query` is left out rather than sent empty because the template engine treats any
string, the empty one included, as present: an absent parameter is the only one a
section such as `{{#query}}` can test for.

The Broker reads **one URL per hit**, at `BROKER_ES_HIT_URL_PATH`, keeps the hits'
order, drops a URL it has already seen, and stops at `limit` even if the template
returns more. A hit without a usable URL at that path fails the whole search.

## 2. Writing the template

The template is Mustache text that renders to a search request body. The example:

```
{
  "query": {
    "bool": {
      "must": [
        {{#query}}
        {"multi_match": {"query": "{{query}}", "type": "best_fields", "fields": ["title^3", "body"]}}
        {{/query}}
        {{^query}}
        {"match_all": {}}
        {{/query}}
      ],
      "filter": [
        {{#filters.news.language}}
        {"term": {"language": "{{filters.news.language}}"}},
        {{/filters.news.language}}
        {{#filters.news.date_published_after}}
        {"range": {"date_published": {"gte": "{{filters.news.date_published_after}}"}}},
        {{/filters.news.date_published_after}}
        {"match_all": {}}
      ]
    }
  },
  "size": "{{limit}}",
  "_source": ["url"],
  "track_total_hits": false,
  "sort": [{"_score": "desc"}, {"url": "asc"}]
}
```

- **Insert agent input with `{{name}}` only.** The cluster escapes such a value as
  a JSON string, so text shaped like the end of a clause stays text. `{{{name}}}`
  and `{{&name}}` insert a value unescaped, `{{#join}}name{{/join}}` writes each
  value it joins unescaped, and with agent input they let the agent write query
  syntax. Keep the default delimiters too: after `{{=<% %>=}}`, `<%&name%>` inserts
  a value unescaped as well. The Broker refuses a template file that uses any of
  these. It cannot read a stored template (that needs a cluster privilege its key
  does not have), so for a stored one the rule is yours to keep.
- **Handle the absent query.** A filter-only request sends no `query`. The example
  pairs `{{#query}}` with `{{^query}}` so that such a request matches everything the
  filters let through. A template that needs a query can leave the `{{^query}}`
  branch out; a filter-only request then matches nothing.
- **One section per filter key.** `{{#filters.news.language}}` renders only when the
  agent sent that filter. End a list of optional clauses with a clause that is
  always there, as the example's last `{"match_all": {}}` is, so no rendering leaves
  a trailing comma.
- **An array value needs `{{#toJson}}`.** `{{#toJson}}filters.news.tags{{/toJson}}`
  renders an array as JSON, each value escaped, for a `terms` clause. `{{#join}}`
  would write the values unescaped.
- **Project only the URL.** The Exchange supplies the title, the price and the
  terms, so nothing else needs to leave the index: `"_source": ["url"]`.
- **Break ties.** Equal scores in a stable order make the answers repeatable; the
  example sorts on the URL after the score, which needs the URL field to be a
  `keyword`.
- **Size the search with `{{limit}}`.** The Broker cuts at the limit either way, but
  asking for more hits than it reads costs the cluster for nothing.

## 3. Where the URL comes from

`BROKER_ES_HIT_URL_PATH` names where each hit carries its URL, as dotted names into
the hit as Elasticsearch returns it. An array at any step stands for its first
element.

| The URL is | The template | `BROKER_ES_HIT_URL_PATH` |
|---|---|---|
| a stored field | `"_source": ["url"]` | `_source.url` |
| a stored field, projected | `"_source": false, "fields": ["url"]` | `fields.url` |
| computed from other fields | `"_source": false, "script_fields": {"url": {"script": {"source": "params.prefix + doc['slug'].value", "params": {"prefix": "https://publisher.example/articles/"}}}}` | `fields.url` |

Whichever way it is built, **the URL must equal the URI the Exchange stores for
that resource, byte for byte**: the Exchange's `EXCHANGE_CATALOG_URI_SCHEME`, then
`://`, the domain and the path, as the publisher's feed gave them. The Exchange
looks a URL up by its bytes, with nothing decoded or case-folded, so a document
whose URL differs in one character can never produce an offer.

## 4. The filter schema

The template applies filters; the schema says which ones exist. The Broker
validates every request's `search_filters` against it before any search: a key the
schema does not declare is refused with `UNIMPLEMENTED` and a value that breaks its
key's rule with `INVALID_ARGUMENT`, each naming `search_filters.<key>`. So a
filter reaches the template only in a shape the template expects.

Keep the two in step: every key the schema declares needs a section in the
template, or the filter is accepted and has no effect. The identity service
publishes the same schema in its `fora_discover` tool, so give it the same file.
The rules the schema must follow are in [`CONFIGURATION.md`](CONFIGURATION.md) §7.
In short: one flat object of keys with `additionalProperties: false`, no reference
keyword, no `default`, no rule over the whole object, and `pattern` or `enum` for a
value's shape, because `format` is not checked. A filter the agent did not send is
never applied, so a template section for it simply does not render.

## 5. Stored or sent with every search

| | Stored (`BROKER_ES_TEMPLATE_ID`) | File (`BROKER_ES_TEMPLATE_FILE`) |
|---|---|---|
| Who changes it | You, with `PUT _scripts/<id>`, and the Broker sees the change at the next search | The Broker's operator, and the Broker reads it at start-up |
| What the cluster must allow | `script.allowed_types` includes `stored` | `script.allowed_types` includes `inline` |
| Checked by the Broker for `{{{ }}}`, `{{& }}`, `{{#join}}` and `{{= }}` | No: the Broker cannot read it | Yes, at start-up |

To store the example:

```
curl -u <admin> -H 'Content-Type: application/json' -X PUT \
  'https://search.example:9243/_scripts/fora-catalog' \
  --data-binary @<(jq -Rs '{script: {lang: "mustache", source: .}}' search-template.example.mustache)
```

## 6. Trying it before the Broker does

Render the template without searching, with the parameters the Broker would send.
The Broker's own start-up check makes the same call:

```
curl -u <reader> -H 'Content-Type: application/json' -X POST \
  'https://search.example:9243/_render/template' \
  -d '{"id": "fora-catalog", "params": {"query": "central bank", "filters": {"news": {"language": "en"}}, "limit": 5}}'
```

The answer's `template_output` must be a JSON object: it is the request body the
search will run. Then run it:

```
curl -u <reader> -H 'Content-Type: application/json' -X POST \
  'https://search.example:9243/catalog/_search/template' \
  -d '{"id": "fora-catalog", "params": {"query": "central bank", "limit": 5}}'
```

Check that every hit carries a URL at the path the Broker reads, and that the URL
is one the Exchange sells. Both calls need only the `read` privilege on the index,
which is all the Broker's key should have.
