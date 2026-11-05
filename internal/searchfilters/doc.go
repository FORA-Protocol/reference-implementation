// Package searchfilters loads a deployment's search-filter schema: the JSON
// Schema that names the search_filters keys the Broker's catalog search applies
// and the format of each value. The operator writes it; the Broker validates a
// request's filters against it, and the identity service publishes it in the
// fora_discover tool, so agents see the vocabulary before they call. Both
// services load the same file through Load, so both refuse the same document
// with the same text.
//
// The document is compiled by the protocol SDK's registration-schema compiler,
// which bounds its size, depth and evaluation cost, refuses a reference that
// leaves the document, and admits only regular expressions every SDK language
// reads the same way. Load adds the rules that make the document a filter
// vocabulary rather than any schema:
//
//   - It is one flat object of keys: `type` is "object", `properties` names at
//     least one key, and `additionalProperties` is the literal false, so a key
//     outside the vocabulary is refused instead of ignored.
//   - It has no whole-object rule (`required`, `oneOf`, `allOf`, `anyOf`, `not`,
//     `minProperties` and the rest are not allowed at the top level). A filter
//     key is optional by nature: a request carrying only a query must pass, and a
//     failure that names no key cannot tell the agent what to change.
//   - It uses no reference machinery at all (`$ref`, `$dynamicRef`, `$defs`,
//     `$anchor`, `$dynamicAnchor`, `$id`). The identity service embeds the
//     document as one property of the tool's input schema, where a reference
//     resolves against the tool's schema instead of this document (JSON Schema
//     2020-12 §9.3.1), so a document using one would load in the Broker and fail
//     in the identity service.
//   - It gives no key a `default`. The identity service's tool would fill a
//     default into every call that lacks the key, URL-only and empty calls
//     included, while the Broker applies none to a request that reaches it
//     directly; and a default that breaks its own key's rule would stop the
//     tool from mounting.
//   - It is written in one dialect, one way: `$schema` is absent or exactly
//     "https://json-schema.org/draft/2020-12/schema". The MCP SDK reads any
//     value it does not know as 2020-12, so a second spelling could validate one
//     way in the Broker and another in the tool.
//   - Each key is dot-separated segments of letters, digits, `_` and `-`, which
//     is how the search template addresses it, and no key is a prefix of another
//     (`news` beside `news.language` would make filters.news both a value and an
//     object).
//
// `format` is an annotation in both validators, never an assertion: constrain a
// value's shape with `pattern` and `enum`.
package searchfilters
