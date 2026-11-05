package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/searchfilters"
)

// What fora_discover tells an agent before it calls: its description and its
// input schema. Both turn on one deployment decision, whether a search-filter
// schema is configured, so they are built together, once, by New, before any
// tool is mounted.

// discoverTool is fora_discover as tools/list shows it. filters is the
// deployment's search-filter schema, or nil when none is configured.
func discoverTool(filters *searchfilters.Schema) (*mcpsdk.Tool, error) {
	schema, err := discoverInputSchema(filters.Raw())
	if err != nil {
		return nil, fmt.Errorf("mcp: fora_discover input schema: %w", err)
	}
	tool := &mcpsdk.Tool{Name: "fora_discover", Description: discoverDescription(filters != nil)}
	// Set only when one was built. A nil *jsonschema.Schema stored in the field is
	// a schema AddTool panics on, while an unset field is one it infers.
	if schema != nil {
		tool.InputSchema = schema
	}
	return tool, nil
}

// discoverInputSchema is fora_discover's input schema with the deployment's
// filter schema as the search_filters property. raw is that document, as
// searchfilters.Load accepted it. When raw is empty no schema is configured, and
// the result is nil: AddTool then infers the schema from discoverInput, which
// leaves search_filters an open object.
//
// With a schema, the MCP SDK checks each call's search_filters against it before
// handleDiscover runs, a call that names URLs included, although the Broker would
// not read that call's filters. A key the schema does not list, or a value that
// breaks its key's type, pattern or enum, comes back as a tool error whose text
// starts with `validating "arguments"`, and nothing is sent to the Broker. The
// Broker still checks every request it receives. It is the only check for a
// caller that uses a FORA SDK instead of this endpoint, and it decides everything
// a schema cannot state, such as what a well-formed value that no resource
// carries returns.
//
// AddTool resolves an input schema itself and panics when it cannot. So the
// result is resolved here first, with the same options, and a document that does
// not resolve stops the start-up with the reason instead. A reference would not
// resolve: once the document is embedded, it is read against the tool's schema
// instead of its own (JSON Schema 2020-12 §9.3.1), and Load refuses every
// reference keyword for that reason. Load also refuses every default: the MCP
// SDK fills each declared default into a call before it checks it, so the key
// would reach the Broker on every call, whether or not the agent sent it, and a
// default that broke its own key's rule would not resolve. No document Load
// accepts is known to fail here; the check stays so that one which did would
// stop the start-up with its reason rather than a panic.
func discoverInputSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	schema, err := jsonschema.For[discoverInput](nil)
	if err != nil {
		return nil, err
	}
	var filters jsonschema.Schema
	if err := json.Unmarshal(raw, &filters); err != nil {
		return nil, fmt.Errorf("decode the search filter schema: %w", err)
	}
	schema.Properties["search_filters"] = &filters
	// jsonschema-go allows a schema to be resolved more than once, so the schema
	// resolved here is the one AddTool receives and resolves again.
	if _, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true}); err != nil {
		return nil, fmt.Errorf("the search filter schema does not resolve as the search_filters property: %w", err)
	}
	return schema, nil
}

// discoverDescription is what an agent reads about fora_discover. The first part
// holds for every catalog search. The rest depends on the deployment: with a
// filter schema, the keys are in the input schema; without one, the text lists
// the fora-music-v1 keys and how the tag-based search applies them.
func discoverDescription(hasSchema bool) string {
	if hasSchema {
		return discoverCore + discoverSchemaKeys
	}
	return discoverCore + discoverMusicKeys
}

// discoverCore names no filter key and no search engine.
const discoverCore = "Find licensable offers for one or more URLs, or by a free-form query, search " +
	"filters or both. " +
	"Returns one group per requested URL, each carrying that URL's offers. A query returns " +
	"one group per resource it matched, best match first; whether a query can match " +
	"anything depends on how the Broker is configured, and a query that matches nothing " +
	"comes back with a reason. A URL with nothing licensable comes back as an empty group " +
	"with a reason, never silently dropped. When URLs are given, the URLs are used and " +
	"the query and search_filters are not. Pass an offer back unchanged to license it. " +
	"search_filters narrows a search with required constraints, with or without a query. " +
	"A malformed filter value is an error naming the key. A well-formed filter that nothing " +
	"matches comes back with the reason NOT_IN_CATALOG, not as an error. Some searches match " +
	"the query's keywords and do not interpret it, so state a constraint as a filter where " +
	"the search applies them. "

// discoverSchemaKeys is the rest of the text when a filter schema is configured.
// The schema itself lists the keys, so the text does not repeat them and cannot
// disagree with it. It says the check covers a call with URLs too, because the
// core text says such a call does not use its filters.
const discoverSchemaKeys = "The search_filters keys this deployment applies, and the format of each " +
	"value, are this tool's input schema under search_filters. Every call is checked against " +
	"it, including one that gives URLs, and a key the schema does not list is refused."

// discoverMusicKeys is the rest of the text when no filter schema is configured:
// the fora-music-v1 keys, and how the tag-based search applies them.
const discoverMusicKeys = "Whether the Broker applies search_filters depends on its configured " +
	"catalog search: a Broker whose search does not apply them refuses the request as " +
	"unimplemented. " +
	"fora-music-v1 keys: music.isrc (a 12-character upper-case ISRC without hyphens, " +
	"or an array of them); music.genre, music.mood, music.instruments, music.suitable_for " +
	"(a lower-case label or an array of labels); music.energy (low, medium or high, or an " +
	"array of them); music.vocals (true or false); music.bpm_range and " +
	"music.duration_seconds_range (a string \"MIN-MAX\", \"MIN-\" or \"-MAX\", " +
	"inclusive, e.g. \"90-120\"). An array means any of its values; every key must " +
	"match. A key the profile does not define is ignored by the tag-based search, and " +
	"by a Broker with no catalog search, which refuses only the profile's own keys. With " +
	"the tag-based search and a query: BPM and duration ranges in the query " +
	"are intersected with the filter ranges, and tracks the query excludes are removed. " +
	"If some filtered tracks match the query's wanted tags, only those matching the most " +
	"of them are returned; otherwise every track that passes the filters and the " +
	"query's exclusions and ranges is returned."
