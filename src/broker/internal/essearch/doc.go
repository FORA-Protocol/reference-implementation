// Package essearch is the Broker's Elasticsearch catalog search. It turns a
// free-text query and the request's search filters into catalog URIs by running
// a search template the index owner wrote, against an index the index owner
// fills and keeps current. The Broker only reads it. No model is called and no
// catalog file is loaded.
//
// The query is configuration, not code. The template, stored in the cluster
// (Config.TemplateID) or sent with every search (Config.TemplateSource), decides
// which fields are searched, how they rank and how each filter applies. The
// Broker fills three parameters: query, the agent's text, left out when there
// is none; filters, the accepted filters nested by dot (news.language becomes
// filters.news.language); and limit. It reads one URL per hit at
// Config.HitURLPath.
//
// The filter vocabulary is the deployment's JSON Schema (Config.Filters, see
// internal/searchfilters). Parse refuses a key the schema does not declare as
// unsupported, and a value that breaks its key's rule as malformed, before any
// search runs; with no schema it refuses every key. The refusal never quotes a
// submitted value.
//
// Elasticsearch supplies candidate URLs and nothing else. Every URL then takes
// the Broker's normal per-URL route (publisher manifest, Exchange discovery,
// offer verification), so a stale hit cannot produce an offer the Exchange does
// not stand behind. That is also why a hit's URL must equal the Exchange's
// stored URI byte for byte: discovery matches on those bytes.
//
// The transport is plain HTTP to two endpoints, _search/template and
// _render/template, which every Elasticsearch since 6.x and OpenSearch serve.
// No client library is used, so no product check and no version lock apply.
//
// The package takes values, never the environment: cmd/server reads the
// deployment's settings and hands them over as a Config.
package essearch
