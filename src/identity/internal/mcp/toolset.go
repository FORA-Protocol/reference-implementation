package mcp

import (
	"log/slog"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/exchacct"
)

// The dependency set every tool handler is a method on, the helper they share,
// and the table that mounts them.
//
// This file is named for what it holds rather than for any tool, and that is the
// layout rule for the package: tools_<name>.go holds that tool and nothing else,
// so a reader looking for a tool opens its file and finds it. Anything five tools
// share belongs here; anything two of them share belongs in a file named for the
// pair. A mount table for five tools sitting under one tool's name sends the
// reader to the wrong file and hides the endpoint's surface in the last place
// they would look for it.

// toolset holds what the tool handlers need. It exists so the handlers are
// methods with one dependency set rather than closures capturing loose values.
type toolset struct {
	// purchases, discovery and reports are the outbound legs, one port per
	// operation; see ports.go for why the split is at the seam rather than behind
	// one interface.
	purchases purchaser
	discovery discoverer
	reports   reporter
	// accounts opens and reports the caller's Exchange accounts. The two account
	// tools are decode-call-render over it: the workflow, its ordering and its
	// local notes are the service's, not the handler's.
	accounts *exchacct.Service
	// exchangeAllowed is the deployment's Exchange policy, asked here so an agent
	// gets a sentence naming it rather than a routing failure.
	exchangeAllowed exchangeAllowed
	// content fetches the licensed bytes for a delivered item. It signs as the
	// caller, using the same custodied key the offer acceptance was signed with —
	// which is the key the Exchange bound the delivery URL to.
	content contentFetcher
	// callTimeout bounds one fora_execute's whole content leg, and
	// maxCallContentBytes what it may accumulate. Both are CALL-scoped, so they
	// live here rather than in the fetcher, which bounds one request and has no
	// notion of a batch.
	//
	// maxItemContentBytes mirrors the fetcher's own per-item cap. It is held here
	// only so the budget check can ask whether the NEXT body could still fit,
	// instead of admitting one and discovering the overrun afterwards.
	callTimeout         time.Duration
	maxCallContentBytes int64
	maxItemContentBytes int64
	// log is the construction-time logger, used only when a call arrives with no
	// request-scoped one. Handlers log through toolset.logger(ctx).
	log *slog.Logger
	// directory builds a caller's directory origin. It is the OUTBOUND SIGNER's own
	// function (agentsign), not a copy of its logic: the origin is what the signed
	// Signature-Agent header carries, and the Broker and the Exchange both derive
	// the caller's identity from that header, so the value this service sends and
	// the value it signs must be produced by the same code rather than merely
	// configured from the same setting.
	directory func(subdomain string) string
}

// agentDirectory is the caller's directory origin — its FORA identity, matching
// the Signature-Agent the outbound signature carries.
//
// The MCP adapter puts this origin in requester.id as well. That is a deliberate
// choice and not a requirement the Exchange imposes: requester.id is a free label
// the protocol lets an agent pick, and the Exchange derives the identity from the
// covered Signature-Agent instead. Sending the origin keeps the two agreeing, so
// an operator reading a transaction's evidence row sees the party that signed it.
// The Broker does not compare it either. What the Broker DOES check, on a
// purchase, is that requester.domain names the same host as the verified
// Signature-Agent, because the Exchange discovers the acceptance key from it.
func (t *toolset) agentDirectory(subdomain string) string {
	return t.directory(subdomain)
}

// register mounts every tool on srv. Adding a tool here is the ONLY way it
// becomes reachable, so this list is the endpoint's surface.
func (t *toolset) register(srv *mcpsdk.Server) {
	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "fora_register",
		Description: "Open this agent's account at one FORA Exchange, or return the existing one. " +
			"'exchange' is required: that Exchange's bare domain (\"exchange.example\" or " +
			"\"exchange.example:8081\"). 'fields' carries the registration details it asks " +
			"for, and an Exchange that asks for nothing needs none. " +
			"What to put in 'fields' is published by the Exchange itself, at " +
			"https://<exchange>/.well-known/fora.json under account_registration.data_schema — " +
			"a JSON Schema naming the required and optional members. Read it and send members " +
			"that match; an Exchange publishing no schema asks for nothing in particular. " +
			"Submitting a registration accepts that Exchange's terms, the document at its " +
			"terms_uri, in the revision its terms_digest names — this call echoes that digest, " +
			"so the acceptance records which revision was agreed. Nothing is filled in for you. " +
			"Safe to call more than once: a repeat returns the same account handle.",
	}, t.handleRegister)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "fora_status",
		Description: "List this agent's FORA accounts. The answer is always a list of entries with " +
			"the same shape, each carrying a 'source' marker saying where it came from — read that " +
			"field first. Pass 'exchange' (a bare domain) to ask that Exchange directly: one entry, " +
			"source \"exchange\", fetched now and authoritative, with 'registered' saying whether " +
			"an account exists there — registered false is a normal answer, not an error. Omit " +
			"'exchange' to get every Exchange this adapter has registered the agent at, each source " +
			"\"local_hint\": those come from a local note, make no network call, can be out of " +
			"date, and miss any registration made outside this adapter — re-ask with 'exchange' to " +
			"confirm one. 'active' is null wherever nobody authoritative was asked. 'balances' lists " +
			"what the account can spend now, one {amount, currency} entry per currency, with holds " +
			"for pending purchases subtracted; an empty list means the balance is not reported, " +
			"not that it is zero, and hints never carry one. This tool does " +
			"not report what an Exchange requires to register; that is published in the Exchange's " +
			"own /.well-known/fora.json.",
	}, t.handleStatus)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "fora_discover",
		Description: "Find licensable offers for one or more URLs, or by a free-form query, search " +
			"filters or both. " +
			"Returns one group per requested URL, each carrying that URL's offers. A query returns " +
			"one group per resource it matched, best match first; whether a query can match " +
			"anything depends on how the Broker is configured, and a query that matches nothing " +
			"comes back with a reason. A URL with nothing licensable comes back as an empty group " +
			"with a reason, never silently dropped. When URLs are given, the URLs are used and " +
			"the query and search_filters are not. Pass an offer back unchanged to license it. " +
			"search_filters narrows a search with required constraints, with or without a query. " +
			"fora-music-v1 keys: music.isrc (a 12-character upper-case ISRC without hyphens, " +
			"or an array of them); music.genre, music.mood, music.instruments, music.suitable_for " +
			"(a lower-case label or an array of labels); music.energy (low, medium or high, or an " +
			"array of them); music.vocals (true or false); music.bpm_range and " +
			"music.duration_seconds_range (a string \"MIN-MAX\", \"MIN-\" or \"-MAX\", " +
			"inclusive, e.g. \"90-120\"). An array means any of its values; every key must " +
			"match. A malformed value is an error naming the key; keys the profile does not " +
			"define are ignored. A well-formed filter that no track matches comes back with the reason " +
			"NOT_IN_CATALOG, not as an error. With a query: BPM and duration ranges in the query " +
			"are intersected with the filter ranges, and tracks the query excludes are removed. " +
			"If some filtered tracks match the query's wanted tags, only those matching the most " +
			"of them are returned; otherwise every track that passes the filters and the " +
			"query's exclusions and ranges is returned.",
	}, t.handleDiscover)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "fora_execute",
		Description: "License one or more discovered offers and get a signed delivery URL for each. " +
			"Pass the offers fora_discover returned, unchanged. A single offer is fine — it is just a " +
			"batch of one. Each result is licensed independently: a delivered item carries a delivery " +
			"URL and its transaction id, a refused one carries the reason. By default the content " +
			"itself comes back with the result, so there is nothing further to fetch. With " +
			"embed_content false, the content is not fetched and the result carries only the " +
			"short-lived delivery URL for each item.",
		InputSchema: executeInputSchema(),
	}, t.handleExecute)

	mcpsdk.AddTool(srv, &mcpsdk.Tool{
		Name: "fora_report",
		Description: "Report usage of licensed content to the Exchange that issued the offer. " +
			"Reporting is a licensing obligation: an offer's terms may require it, and overdue " +
			"reports can get later purchases denied.",
	}, t.handleReport)
}
