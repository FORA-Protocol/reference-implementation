//go:build integration

package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/app"
)

// These tests run fora_discover in a deployment that configured a search-filter
// schema: the example the repository ships for the Broker and this service,
// which declares news.language (two lower-case letters) and
// news.date_published_after (a UTC date-time to the second). The schema becomes
// the tool's input schema for search_filters, so the MCP SDK checks every call
// against it before the handler runs, and a refused call never reaches the
// Broker double.

// exampleFilterSchema is the filter schema the repository ships as the example
// for both services.
func exampleFilterSchema(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "broker", "search-filters.example.json"))
	if err != nil {
		t.Fatalf("read the example filter schema: %v", err)
	}
	return raw
}

// newFilteredFixture is newFixture for a deployment that configured the example
// filter schema.
func newFilteredFixture(t *testing.T) *fixture {
	t.Helper()
	doc := string(exampleFilterSchema(t))
	return newBoundedFixture(t, func(c *app.MCPConfig, _ peerSet) { c.SearchFiltersSchema = doc })
}

// listedFilterProperty returns the search_filters property of the tool's input
// schema, as the JSON the agent received in tools/list.
func listedFilterProperty(t *testing.T, tool *mcpsdk.Tool) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("encode the listed input schema: %v", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode the listed input schema %s: %v", raw, err)
	}
	property, ok := schema.Properties["search_filters"]
	if !ok {
		t.Fatalf("the listed input schema has no search_filters property: %s", raw)
	}
	return property
}

// assertRefusedBeforeTheBroker calls fora_discover with args, in a deployment
// with the example filter schema, and checks that the call was refused naming
// key and that nothing reached the Broker.
//
// The refusal must be a tool error from the MCP SDK's argument validation, which
// runs before the handler. IsError alone would not show that, because an error
// the handler returns after calling the Broker sets it too. Each check reports
// without stopping, so a call that went through still shows what reached the
// Broker.
func assertRefusedBeforeTheBroker(t *testing.T, args map[string]any, key string) {
	t.Helper()
	f := newFilteredFixture(t)
	a := f.provision(t, "dev-one")

	res, err := f.connect(t, a.Token).CallTool(t.Context(),
		&mcpsdk.CallToolParams{Name: "fora_discover", Arguments: args})
	if err != nil {
		t.Fatalf("call fora_discover: %v; want a tool result with isError", err)
	}
	text := textOf(res)
	if !res.IsError || !strings.HasPrefix(text, `validating "arguments"`) {
		t.Errorf("isError = %v, text = %q; want the SDK's argument-validation error", res.IsError, text)
	}
	if !strings.Contains(text, key) {
		t.Errorf("error text = %q, want it to name %s", text, key)
	}
	if calls := f.broker.Calls(); len(calls) != 0 {
		t.Errorf("the Broker received %d calls; the refusal must come before any", len(calls))
	}
}

// TestNew_RefusesAnUnusableFilterSchema starts the service through the
// composition root with a filter schema it must refuse. The start must fail with
// an error naming the stage that refused the document and the rule it broke, and
// it must not panic: the MCP SDK panics on an input schema it cannot resolve.
//
// The loader both services share refuses each of these, so both refuse the same
// file. A reference into the document's own $defs compiles in the protocol SDK
// and stops resolving once the document is embedded in the tool's schema; a
// default, valid or not, would be filled into every call by the MCP SDK, and one
// that breaks its own key's pattern would not resolve. The resolve this service
// runs after the loader is a backstop no accepted document is known to reach,
// and tools_discover_schema_internal_test.go drives it directly.
func TestNew_RefusesAnUnusableFilterSchema(t *testing.T) {
	const loader = "app: mcp: search filters schema: "
	cases := map[string]struct {
		doc       string
		refusedBy string
		rule      string
	}{
		"additionalProperties missing": {
			doc:       `{"type":"object","properties":{"news.language":{"type":"string"}}}`,
			refusedBy: loader,
			rule:      "additionalProperties must be false",
		},
		"a $ref into the document's own $defs": {
			doc: `{"type":"object","additionalProperties":false,"$defs":{"language":{"type":"string"}},` +
				`"properties":{"news.language":{"$ref":"#/$defs/language"}}}`,
			refusedBy: loader,
			rule:      "no reference keyword",
		},
		"a $ref to a missing $defs entry": {
			doc: `{"type":"object","additionalProperties":false,` +
				`"properties":{"news.language":{"$ref":"#/$defs/language"}}}`,
			refusedBy: loader,
			rule:      "refused by the protocol SDK's schema rules",
		},
		"a default its own pattern refuses": {
			doc: `{"type":"object","additionalProperties":false,` +
				`"properties":{"news.language":{"type":"string","pattern":"^[a-z]{2}$","default":"english"}}}`,
			refusedBy: loader,
			rule:      "uses default",
		},
		"a valid default": {
			doc: `{"type":"object","additionalProperties":false,` +
				`"properties":{"news.language":{"type":"string","pattern":"^[a-z]{2}$","default":"en"}}}`,
			refusedBy: loader,
			rule:      "uses default",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := buildFixture(t, func(m *app.MCPConfig, _ peerSet) { m.SearchFiltersSchema = c.doc })
			if err == nil {
				t.Fatal("the service started with a filter schema it must refuse")
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, c.refusedBy) || !strings.Contains(msg, c.rule) {
				t.Errorf("error = %q, want it to begin %q and name %q", msg, c.refusedBy, c.rule)
			}
		})
	}
}

// TestDiscover_InputSchemaCarriesTheConfiguredFilters checks what an agent reads
// in tools/list: the configured document itself as the search_filters property,
// and a description that sends the agent there instead of listing keys.
func TestDiscover_InputSchemaCarriesTheConfiguredFilters(t *testing.T) {
	f := newFilteredFixture(t)
	a := f.provision(t, "dev-one")

	tool := listedTool(t, f.connect(t, a.Token), "fora_discover")

	listed := listedFilterProperty(t, tool)
	var got, want any
	if err := json.Unmarshal(listed, &got); err != nil {
		t.Fatalf("decode the listed search_filters property: %v", err)
	}
	if err := json.Unmarshal(exampleFilterSchema(t), &want); err != nil {
		t.Fatalf("decode the example filter schema: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("search_filters = %s, want the configured document", listed)
	}
	// Spelled out as well, so the properties the agent relies on are named here
	// rather than inherited from whatever the example file says.
	var schema struct {
		AdditionalProperties *bool `json:"additionalProperties"`
		Properties           map[string]struct {
			Pattern     string `json:"pattern"`
			Description string `json:"description"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(listed, &schema); err != nil {
		t.Fatalf("decode the listed search_filters property: %v", err)
	}
	if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Error("search_filters does not refuse a key it does not list")
	}
	for _, key := range []string{"news.language", "news.date_published_after"} {
		if p, ok := schema.Properties[key]; !ok || p.Pattern == "" || p.Description == "" {
			t.Errorf("search_filters.%s = %+v, want it declared with a pattern and a description", key, p)
		}
	}
	if !strings.Contains(tool.Description, "this tool's input schema under search_filters") {
		t.Errorf("fora_discover description does not point at the input schema: %q", tool.Description)
	}
	for _, unwanted := range []string{"music.", "tag-based search"} {
		if strings.Contains(tool.Description, unwanted) {
			t.Errorf("fora_discover description carries %q, which this deployment does not apply: %q",
				unwanted, tool.Description)
		}
	}
}

// TestDiscover_UnknownFilterKeyIsRefusedBeforeTheBroker sends a key the schema
// does not list. That includes a fora-music-v1 key, which a deployment with a
// schema does not apply, and a call that names a URL: the schema checks the
// arguments, so the Broker's rule that a named URL leaves the filters unread
// never comes into play.
func TestDiscover_UnknownFilterKeyIsRefusedBeforeTheBroker(t *testing.T) {
	cases := map[string]struct {
		args map[string]any
		key  string
	}{
		"a key the schema does not list": {
			args: map[string]any{"search_filters": map[string]any{"news.author": "Plato"}},
			key:  "news.author",
		},
		"a fora-music-v1 key": {
			args: map[string]any{"search_filters": map[string]any{"music.genre": "jazz"}},
			key:  "music.genre",
		},
		"beside a named URL": {
			args: map[string]any{
				"uris":           []any{"https://publisher.example/a"},
				"search_filters": map[string]any{"news.author": "Plato"},
			},
			key: "news.author",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { assertRefusedBeforeTheBroker(t, c.args, c.key) })
	}
}

// TestDiscover_MalformedFilterValueIsRefusedBeforeTheBroker sends declared keys
// with values their rule refuses: the wrong type, and a string the pattern does
// not match.
func TestDiscover_MalformedFilterValueIsRefusedBeforeTheBroker(t *testing.T) {
	cases := map[string]struct {
		key   string
		value any
	}{
		"a language that is not two letters": {key: "news.language", value: "english"},
		"a language that is not a string":    {key: "news.language", value: 5},
		"a date with no time":                {key: "news.date_published_after", value: "2026-01-05"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			assertRefusedBeforeTheBroker(t, map[string]any{
				"query":          "Socrates",
				"search_filters": map[string]any{c.key: c.value},
			}, c.key)
		})
	}
}

// TestDiscover_DeclaredFiltersAreForwardedUnchanged is the other half of the two
// refusals above: filters the schema allows reach the Broker as the agent wrote
// them, beside the query.
func TestDiscover_DeclaredFiltersAreForwardedUnchanged(t *testing.T) {
	f := newFilteredFixture(t)
	a := f.provision(t, "dev-one")
	filters := map[string]any{
		"news.language":             "en",
		"news.date_published_after": "2026-01-05T00:00:00Z",
	}

	callTool[discoverResult](t, f.connect(t, a.Token), "fora_discover", map[string]any{
		"query":          "Socrates",
		"search_filters": filters,
	})

	sent := f.broker.LastDiscovery()
	if sent == nil {
		t.Fatal("a discovery with declared filters never reached the Broker")
	}
	if got := sent.GetSearchFilters().AsMap(); !reflect.DeepEqual(got, filters) {
		t.Errorf("search_filters sent = %v, want %v", got, filters)
	}
	if got := sent.GetQuery(); got != "Socrates" {
		t.Errorf("query sent = %q, want the agent's query", got)
	}
}
