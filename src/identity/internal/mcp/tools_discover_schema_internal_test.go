package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The resolve check in discoverInputSchema is a backstop: no document
// searchfilters.Load accepts is known to reach it. So the composition-root test,
// TestNew_RefusesAnUnusableFilterSchema, sees every unusable document refused by
// Load first, and never reaches the check. The documents here are ones Load
// refuses, references, because a reference is what stops resolving once the
// document is embedded. They are handed to the builder directly, to show that one
// that got past Load would still be an error rather than the panic the MCP SDK's
// AddTool raises on the same schema. Same reasoning as caller_internal_test.go.

// buildDiscoverSchema runs discoverInputSchema, failing the test on a panic.
func buildDiscoverSchema(t *testing.T, doc string) (*jsonschema.Schema, error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("discoverInputSchema panicked: %v", p)
		}
	}()
	return discoverInputSchema(json.RawMessage(doc))
}

func TestDiscoverInputSchema_RefusesADocumentThatDoesNotResolveEmbedded(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		doc string
		// cause is what the error must name: the reference that does not resolve.
		cause string
	}{
		// The pointer is read against the tool's schema, whose root has no $defs.
		"a $ref into the document's own $defs": {
			doc: `{"type":"object","additionalProperties":false,"$defs":{"lang":{"type":"string"}},` +
				`"properties":{"news.language":{"$ref":"#/$defs/lang"}}}`,
			cause: "/$defs/lang",
		},
		// An embedded resource needs an absolute $id, and the tool's schema gives no
		// base to resolve a relative one against.
		"a relative $id": {
			doc: `{"$id":"filters.json","type":"object","additionalProperties":false,` +
				`"properties":{"news.language":{"type":"string"}}}`,
			cause: "filters.json",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			schema, err := buildDiscoverSchema(t, c.doc)
			if err == nil {
				t.Fatalf("the schema was built (%v); want an error, because AddTool would panic on it", schema)
			}
			if !strings.Contains(err.Error(), c.cause) {
				t.Errorf("error = %q, want it to name %s", err, c.cause)
			}
		})
	}
}

// TestDiscoverInputSchema_EmbedsAFlatDocument is the control for the refusals
// above: a document with no reference resolves, sits under search_filters, and
// is a schema AddTool accepts.
func TestDiscoverInputSchema_EmbedsAFlatDocument(t *testing.T) {
	t.Parallel()
	const doc = `{"type":"object","additionalProperties":false,` +
		`"properties":{"news.language":{"type":"string","pattern":"^[a-z]{2}$"}}}`

	schema, err := buildDiscoverSchema(t, doc)
	if err != nil {
		t.Fatalf("discoverInputSchema: %v", err)
	}
	listed, err := json.Marshal(schema.Properties["search_filters"])
	if err != nil {
		t.Fatalf("encode search_filters: %v", err)
	}
	if !strings.Contains(string(listed), `"news.language"`) {
		t.Errorf("search_filters = %s, want the configured document", listed)
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "schema-check", Version: "1"}, nil)
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "fora_discover", InputSchema: schema},
		func(context.Context, *mcpsdk.CallToolRequest, discoverInput) (*mcpsdk.CallToolResult, discoverOutput, error) {
			return nil, discoverOutput{}, nil
		})
}

// TestDiscoverInputSchema_NoDocumentLeavesTheSchemaToAddTool covers the
// deployment with no filter schema: no schema is built, so AddTool infers it from
// discoverInput exactly as it did before filter schemas existed.
func TestDiscoverInputSchema_NoDocumentLeavesTheSchemaToAddTool(t *testing.T) {
	t.Parallel()
	schema, err := buildDiscoverSchema(t, "")
	if err != nil || schema != nil {
		t.Errorf("discoverInputSchema(empty) = %v, %v; want no schema and no error", schema, err)
	}
}
