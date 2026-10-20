//go:build integration

package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// These tests drive fora_execute's embed_content parameter over the MCP wire,
// against the enforcing edge double. The edge's hit count is what tells "the
// content was fetched" apart from "the content was not fetched", so every case
// asserts it.

// embedCase sets up one delivered item behind an enforcing edge and returns the
// session, the edge and the delivery URL the relay hands back.
func embedCase(t *testing.T) (*mcpsdk.ClientSession, *testutil.EdgeDouble, string, *fixture) {
	t.Helper()
	f := newFixture(t)
	a := f.provision(t, "dev-one")
	edge := testutil.NewEdgeDouble(t, time.Now().Unix())
	edge.SetBody([]byte("<html>the licensed article</html>"), "text/html")
	endpoint := f.exchange.SignDelivery(t, edge.URLFor(a.Thumbprint))
	f.broker.executeResp = &forav1.BrokerTransactionResponse{
		Ver:       helpers.ProtocolVersion,
		Exchanges: []*forav1.ExchangeOutcome{{AgentIdentityHash: a.Thumbprint}},
		Items:     []*forav1.TransactionResultItem{deliveredItem("offer-1", endpoint)},
	}
	return f.connect(t, a.Token), edge, endpoint, f
}

// structuredText decodes content[0], which must be the structured text block.
func structuredText(t *testing.T, res *mcpsdk.CallToolResult) deliveryResult {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("no content blocks at all")
	}
	first, ok := res.Content[0].(*mcpsdk.TextContent)
	if !ok {
		t.Fatalf("content[0] is %T, want the structured text block first", res.Content[0])
	}
	var decoded deliveryResult
	if err := json.Unmarshal([]byte(first.Text), &decoded); err != nil {
		t.Fatalf("content[0] is not the structured output: %v", err)
	}
	return decoded
}

// TestExecute_EmbedsContentByDefaultAndWhenTrue: an absent embed_content gets
// the schema default true, so it behaves exactly like an explicit true.
func TestExecute_EmbedsContentByDefaultAndWhenTrue(t *testing.T) {
	cases := map[string]map[string]any{
		"absent":        {},
		"explicit true": {"embed_content": true},
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			session, edge, _, f := embedCase(t)
			args := map[string]any{"offers": []map[string]any{signedOffer("offer-1", f.exchange.Domain(t))}}
			for k, v := range extra {
				args[k] = v
			}

			res := callExecuteRaw(t, session, args)

			if len(structuredText(t, res).Items) != 1 {
				t.Error("the structured text block does not carry the one item")
			}
			resources := resourceBlocks(res)
			if len(resources) != 1 || string(resources[0].Blob) != "<html>the licensed article</html>" {
				t.Fatalf("got %d embedded resources, want the one served document", len(resources))
			}
			if hits := edge.Hits(); hits != 1 {
				t.Errorf("edge hits = %d, want 1", hits)
			}
		})
	}
}

// TestExecute_EmbedContentFalseReturnsOnlyTheURL: with false the purchase still
// happens, but the edge is never contacted and the result is only the text block
// with the delivery URL and the transaction id.
func TestExecute_EmbedContentFalseReturnsOnlyTheURL(t *testing.T) {
	session, edge, endpoint, f := embedCase(t)

	res := callExecuteRaw(t, session, map[string]any{
		"offers":        []map[string]any{signedOffer("offer-1", f.exchange.Domain(t))},
		"embed_content": false,
	})

	if f.broker.LastExecute() == nil {
		t.Fatal("the Broker received no Execute call; the purchase must still happen")
	}
	if len(res.Content) != 1 {
		t.Errorf("got %d content blocks, want only the structured text block", len(res.Content))
	}
	decoded := structuredText(t, res)
	if len(decoded.Items) != 1 {
		t.Fatalf("the text block carries %d items, want 1", len(decoded.Items))
	}
	if got, _ := decoded.Items[0]["retrieval_endpoint"].(string); got != endpoint {
		t.Errorf("retrieval_endpoint = %q, want %q", got, endpoint)
	}
	if got, _ := decoded.Items[0]["transaction_id"].(string); got != "tx-offer-1" {
		t.Errorf("transaction_id = %q, want tx-offer-1", got)
	}
	if len(decoded.DeliveryFailures) != 0 {
		t.Errorf("delivery_failures = %v, want none: nothing was fetched", decoded.DeliveryFailures)
	}
	if hits := edge.Hits(); hits != 0 {
		t.Errorf("edge hits = %d, want 0: false must not fetch", hits)
	}
}

// TestExecute_EmbedContentRefusesNonBooleans: a value that is not a JSON boolean
// is refused by the SDK's schema validation, before the handler runs, so the
// Broker is never asked to execute and nothing is charged.
//
// isError alone would not prove that: a handler error sets it too, after the
// purchase. The SDK's validation prefix and the parameter name in the text are
// what show where the call was refused.
func TestExecute_EmbedContentRefusesNonBooleans(t *testing.T) {
	cases := map[string]any{"null": nil, "string": "false", "number": 0}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			session, edge, _, f := embedCase(t)

			res, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{
				Name: "fora_execute",
				Arguments: map[string]any{
					"offers":        []map[string]any{signedOffer("offer-1", f.exchange.Domain(t))},
					"embed_content": value,
				},
			})
			if err != nil {
				t.Fatalf("call fora_execute: %v; want a tool result with isError", err)
			}
			if !res.IsError {
				t.Fatal("the call succeeded; want a refusal")
			}
			text := textOf(res)
			if !strings.HasPrefix(text, `validating "arguments"`) || !strings.Contains(text, "embed_content") {
				t.Errorf("error text = %q, want the SDK's schema-validation error naming embed_content", text)
			}
			if f.broker.LastExecute() != nil {
				t.Error("the Broker received an Execute call; the refusal must come before any purchase")
			}
			if hits := edge.Hits(); hits != 0 {
				t.Errorf("edge hits = %d, want 0", hits)
			}
		})
	}
}

// TestExecute_AdvertisesEmbedContent: the agent chooses embed_content from the
// tool listing alone, so the listing must carry the default and both warnings.
func TestExecute_AdvertisesEmbedContent(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")
	session := f.connect(t, a.Token)

	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var tool *mcpsdk.Tool
	for _, candidate := range listed.Tools {
		if candidate.Name == "fora_execute" {
			tool = candidate
		}
	}
	if tool == nil {
		t.Fatal("fora_execute is not listed")
	}
	if !strings.Contains(tool.Description, "embed_content false") {
		t.Errorf("tool description does not explain embed_content false: %q", tool.Description)
	}

	var schema struct {
		Properties map[string]struct {
			Type        json.RawMessage `json:"type"`
			Default     json.RawMessage `json:"default"`
			Description string          `json:"description"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	raw, err := json.Marshal(tool.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode input schema %s: %v", raw, err)
	}
	embed, ok := schema.Properties["embed_content"]
	if !ok {
		t.Fatalf("input schema has no embed_content: %s", raw)
	}
	if string(embed.Type) != `"boolean"` || string(embed.Default) != "true" {
		t.Errorf("embed_content = type %s default %s, want boolean default true", embed.Type, embed.Default)
	}
	for _, name := range schema.Required {
		if name == "embed_content" {
			t.Error("embed_content is listed as required; it is optional")
		}
	}
	for _, want := range []string{"bearer credential", "enforce the key binding"} {
		if !strings.Contains(embed.Description, want) {
			t.Errorf("embed_content description lacks %q: %q", want, embed.Description)
		}
	}
}
