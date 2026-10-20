package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// executeInput licenses one or more discovered offers. A single offer is not
// special-cased — it is the degenerate 1-element batch.
type executeInput struct {
	// Offers are the offers to license, each the FULL object fora_discover
	// returned, handed back unchanged. Their signatures cover those bytes, so an
	// edited offer will not verify at the Exchange.
	Offers []map[string]any `json:"offers"`
	// IdempotencyKey makes a retried purchase safe: the Exchange dedupes on it and
	// returns the original result rather than charging twice. Optional — a fresh
	// key is minted when absent, so a plain call always executes; supply the same
	// value to retry the same purchase.
	//
	// NOTE the deliberate asymmetry with fora_report, which REQUIRES this field.
	// The two tools take opposite positions on the same field name because the
	// cost of an absent key differs: a report without one is a silent
	// double-count, whereas a purchase without one is a caller who did not intend
	// a retry at all, and refusing it would make the common single-shot call
	// impossible to write. A caller that retries MUST pass the original key —
	// omitting it on a retry is a second purchase, and it will be charged as one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// EmbedContent chooses whether this service fetches the content and returns
	// it inside the result. The schema default is true (see executeInputSchema),
	// and the SDK applies it before validation, so an absent value arrives here as
	// true and a false here always means the caller sent false.
	EmbedContent bool `json:"embed_content,omitempty"`
}

// embedContentDescription is the embed_content parameter's description. It
// states both limits of false, because the agent decides with nothing else in
// front of it.
const embedContentDescription = "Default true: the content is fetched and returned inside " +
	"the result. Set false to skip the fetch and get only each item's delivery URL. " +
	"The URL is a bearer credential: it stays visible in the chat and in logs until it " +
	"expires, about 5 minutes after the purchase. The URL alone works only when the edge " +
	"does not enforce the key binding, as on the demo deployment; where the binding is " +
	"enforced, only this service can fetch it, so keep the default."

// executeInputSchema is the inferred schema for executeInput with one change:
// embed_content gets an explicit default of true.
//
// The field is a plain bool rather than *bool on purpose. The inferred schema of
// a pointer allows null, and null is not a choice the caller can mean. With a
// plain bool the schema is {"type": "boolean"}, so null, "false" and 0 are all
// refused by the SDK before the handler runs, and therefore before anything is
// bought. The default is what makes an absent value true: the pinned SDK applies
// schema defaults before it validates the arguments.
func executeInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[executeInput](nil)
	if err != nil {
		// A static type that does not infer is a programming error, and AddTool
		// panics on the same condition.
		panic(fmt.Sprintf("fora_execute input schema: %v", err))
	}
	embed := schema.Properties["embed_content"]
	embed.Default = json.RawMessage("true")
	embed.Description = embedContentDescription
	return schema
}

// executeOutput is the BrokerTransactionResponse projection.
type executeOutput struct {
	// Items holds one result per offer, in the order they were submitted, each the
	// protocol's own TransactionResultItem encoding. There is deliberately no
	// per-item "licensed" boolean: an item is the protocol's own object and adding
	// a field of ours to it would corrupt an encoding the agent may hand back. Read
	// it as: a DELIVERED item carries retrieval_endpoint (and expires_at); an item
	// the Exchange DENIED carries denial_reason; an item whose Exchange refused the
	// Broker's whole sub-request, or did not answer it, carries refusal — the
	// Exchange, the code it answered with and its typed reason. Exactly one of the
	// three is always present.
	Items []map[string]any `json:"items"`
	// Exchanges holds one entry per Exchange the Broker contacted, the protocol's
	// ExchangeOutcome encoding: the offers sent there and the agent_identity_hash
	// each of that Exchange's delivery URLs is bound to — the caller's own key.
	Exchanges []map[string]any `json:"exchanges,omitempty"`
	// Totals holds the charged totals, one per currency, in the protocol's Cost
	// encoding. An item is charged when it carries neither denial_reason nor
	// refusal. Absent when nothing was charged.
	Totals []map[string]any `json:"totals,omitempty"`
	// DeliveryFailures reports the delivered items whose content this service could
	// not fetch. An entry appears ONLY for a failure — a success is evident from the
	// embedded resource carrying the bytes — and it sits outside items[] for the
	// same reason there is no per-item "licensed" boolean above.
	//
	// A failure here is not a failed purchase: the item was licensed and charged
	// for, and its retrieval_endpoint is still valid, so an agent that can reach the
	// edge itself may simply fetch it.
	DeliveryFailures []deliveryFailure `json:"delivery_failures,omitempty"`
	// RequestID is the correlation id this call ran under, quotable in a bug report.
	RequestID string `json:"request_id,omitempty"`
}

// handleExecute licenses the offers through the Broker's ExecuteTransaction.
//
// The agent's binding travels as a detached acceptance per item — signed with
// the caller's custodied key inside the SDK client — so each Exchange verifies
// the agent although the Broker re-packages the request and signs the copy it
// sends. An Exchange that refuses its whole share comes back as a refusal on
// those items, not as a failed call: the other Exchanges' items were still
// bought.
//
// It returns the signed delivery URL per item AND the content itself: the URL is
// bound to the caller's custodied key, which lives in Vault and never reaches the
// agent, so this service is the only party that can satisfy the edge's
// proof-of-possession check (ADR-023). The bytes ride back as embedded resources
// on the tool result; nothing is stored, because the fetch happens inside the
// call that pays for it.
//
// With embed_content false the fetch is skipped entirely: the result is the same
// structured output with the delivery URLs, and the edge is never contacted.
func (t *toolset) handleExecute(
	ctx context.Context, req *mcpsdk.CallToolRequest, in executeInput,
) (*mcpsdk.CallToolResult, executeOutput, error) {
	who, err := callerFrom(ctx, req)
	if err != nil {
		return nil, executeOutput{}, err
	}
	if len(in.Offers) == 0 {
		return nil, executeOutput{}, errors.New("fora_execute needs at least one offer")
	}
	rpc, err := buildTransactionRequest(t.requester(who.subdomain), in)
	if err != nil {
		return nil, executeOutput{}, err
	}
	resp, err := t.purchases.Execute(ctx, rpc)
	if err != nil {
		return nil, executeOutput{}, t.failed(ctx, who, "fora_execute", err)
	}
	out, err := projectTransaction(resp)
	if err != nil {
		return nil, executeOutput{}, err
	}
	out.RequestID = who.requestID
	log := t.logger(ctx)
	var blocks []mcpsdk.Content
	if in.EmbedContent {
		blocks = t.deliver(ctx, log, who, in, &out)
	} else {
		blocks = structuredFallback(out)
	}
	log.InfoContext(ctx, "identity.mcp.execute",
		"subdomain", who.subdomain, "offers", len(in.Offers), "items", len(out.Items),
		"embed_content", in.EmbedContent, "delivery_failures", len(out.DeliveryFailures))
	return &mcpsdk.CallToolResult{Content: blocks}, out, nil
}

// buildTransactionRequest assembles the batch from the caller's offers. requester
// is the caller's FORA identity, built from the authenticated context and never
// from the input. The acceptances themselves are signed later, inside the FORA
// client, where the caller's private key lives.
func buildTransactionRequest(
	requester *forav1.Requester, in executeInput,
) (*forav1.TransactionRequest, error) {
	items := make([]*forav1.TransactionItem, 0, len(in.Offers))
	for i, raw := range in.Offers {
		offer, err := mapToOffer(raw)
		if err != nil {
			return nil, fmt.Errorf("fora_execute: offer %d is malformed: %w", i, err)
		}
		items = append(items, &forav1.TransactionItem{Offer: offer})
	}
	key := in.IdempotencyKey
	if key == "" {
		key = "idem-" + uuid.NewString()
	}
	return &forav1.TransactionRequest{
		Ver:            helpers.ProtocolVersion,
		IdempotencyKey: key,
		Requester:      requester,
		Items:          items,
	}, nil
}

// mapToOffer turns the generic JSON object fora_discover handed the agent back
// into an Offer. Round-tripping through the map rather than re-modelling the offer
// is deliberate: the offer's signature covers its bytes, so reconstructing it
// field by field risks dropping one and invalidating the signature.
func mapToOffer(raw map[string]any) (*forav1.Offer, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("re-encode offer: %w", err)
	}
	var offer forav1.Offer
	if err = protojson.Unmarshal(encoded, &offer); err != nil {
		return nil, fmt.Errorf("decode offer: %w", err)
	}
	return &offer, nil
}

// projectTransaction renders the Broker's answer for the agent, carrying each
// result item, Exchange outcome and total across as the protocol's own encoding
// rather than a hand-written mirror that would have to track every field change.
func projectTransaction(resp *forav1.BrokerTransactionResponse) (executeOutput, error) {
	items, err := protoMaps(resp.GetItems(), "result item")
	if err != nil {
		return executeOutput{}, err
	}
	exchanges, err := protoMaps(resp.GetExchanges(), "exchange outcome")
	if err != nil {
		return executeOutput{}, err
	}
	totals, err := protoMaps(resp.GetTotals(), "total")
	if err != nil {
		return executeOutput{}, err
	}
	return executeOutput{Items: items, Exchanges: exchanges, Totals: totals}, nil
}

// protoMaps renders each message as the protocol's own JSON object. what names
// the kind of message for an error.
func protoMaps[M proto.Message](msgs []M, what string) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(msgs))
	for i, msg := range msgs {
		obj, err := protoToMap(msg)
		if err != nil {
			return nil, fmt.Errorf("fora_execute: render %s %d: %w", what, i, err)
		}
		out = append(out, obj)
	}
	return out, nil
}
