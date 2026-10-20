//go:build integration

package mcp_test

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The endpoint is stateless: every POST is served on its own, with no session
// to open first and none kept afterwards. These tests drive that through the
// HTTP surface, for every protocol version the endpoint accepts.

const newProtocol = "2026-07-28"

// legacyVersions are the MCP versions older than the stateless one that the
// endpoint still serves.
var legacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// TestProtocolMatrix_EveryToolAnswersOnEverySupportedVersion lists the tools and
// calls all five on each supported version. A legacy version is driven twice:
// through the SDK client, which opens with initialize, and as bare POSTs with no
// handshake. The stateless version is driven through the SDK client, which opens
// with server/discover, and as bare POSTs carrying only per-request metadata.
func TestProtocolMatrix_EveryToolAnswersOnEverySupportedVersion(t *testing.T) {
	for _, v := range legacyVersions {
		t.Run(v+"/initialize", func(t *testing.T) { assertEveryToolAnswers(t, v, true) })
		t.Run(v+"/no-handshake", func(t *testing.T) { assertEveryToolAnswers(t, v, false) })
	}
	t.Run(newProtocol+"/discover", func(t *testing.T) { assertEveryToolAnswers(t, newProtocol, true) })
	t.Run(newProtocol+"/no-handshake", func(t *testing.T) { assertEveryToolAnswers(t, newProtocol, false) })
}

func assertEveryToolAnswers(t *testing.T, version string, viaSDK bool) {
	f := newFixture(t)
	calls := armEveryTool(t, f)
	a := f.provision(t, "dev-one")

	var c mcpClient = &rawClient{f: f, bearer: a.Token, version: version}
	if viaSDK {
		session := f.connectVia(t, bearerTransport{token: a.Token}, version)
		if got := session.InitializeResult().ProtocolVersion; got != version {
			t.Fatalf("the session negotiated %q, want %q", got, version)
		}
		c = sdkClient{session}
	}

	want := make([]string, 0, len(calls))
	for _, call := range calls {
		want = append(want, call.name)
	}
	got := c.tools(t)
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("tools/list = %v, want %v", got, want)
	}

	for _, call := range calls {
		var out struct {
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(c.call(t, call.name, call.args), &out); err != nil {
			t.Fatalf("%s: decode result: %v", call.name, err)
		}
		if out.RequestID == "" {
			t.Errorf("%s returned no request_id", call.name)
		}
	}
	if n := len(f.exchange.Calls()); n == 0 {
		t.Error("no call reached the Exchange; the tools answered without doing their work")
	}
	assertNoBearerLeaked(t, f.broker, f.exchange, f.issuer)
}

// TestDiscover_AdvertisesEverySupportedVersionAndTheTools sends the stateless
// protocol's server/discover, which a 2026-07-28 client calls before anything
// else, and checks what it learns: the versions it may use, that the server has
// tools, and which server it is talking to.
func TestDiscover_AdvertisesEverySupportedVersionAndTheTools(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")

	c := &rawClient{f: f, bearer: a.Token, version: newProtocol}
	var res struct {
		SupportedVersions []string `json:"supportedVersions"`
		Capabilities      struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
		Meta struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"io.modelcontextprotocol/serverInfo"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(c.rpc(t, "server/discover", nil), &res); err != nil {
		t.Fatalf("decode server/discover result: %v", err)
	}
	for _, v := range append([]string{newProtocol}, legacyVersions...) {
		if !slices.Contains(res.SupportedVersions, v) {
			t.Errorf("supportedVersions = %v, missing %s", res.SupportedVersions, v)
		}
	}
	if res.Capabilities.Tools == nil {
		t.Error("capabilities carry no tools entry; a client would not look for the tools")
	}
	if res.Meta.ServerInfo.Name != "fora-registry" {
		t.Errorf("serverInfo name = %q, want fora-registry", res.Meta.ServerInfo.Name)
	}
}

// TestTransport_IssuesNoSessionID checks that initialize, the request that
// opened a session before, no longer returns a session id. rawClient checks the
// same on every response, and sends none itself, so every other test in this
// file also shows that no session id is required.
func TestTransport_IssuesNoSessionID(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")

	c := &rawClient{f: f, bearer: a.Token, version: legacyVersions[0]}
	c.rpc(t, "initialize", map[string]any{
		"protocolVersion": legacyVersions[0],
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "raw", "version": "1"},
	})
}

// TestTransport_AcceptsOnlyPOST checks that an authenticated GET, which opened
// the server-to-client event stream before, and an authenticated DELETE, which
// closed a session before, are both refused with 405 and Allow: POST. The
// DELETE carries a session id to show that one is not looked up.
func TestTransport_AcceptsOnlyPOST(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")

	for _, tc := range []struct {
		method string
		header http.Header
	}{
		{http.MethodGet, http.Header{"Accept": {"text/event-stream"}}},
		{http.MethodDelete, http.Header{"Mcp-Session-Id": {"any-session"}}},
	} {
		resp := sendMCP(t, f, tc.method, a.Token, tc.header, "")
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", tc.method, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
			t.Errorf("%s: Allow = %q, want POST", tc.method, allow)
		}
	}
}

// TestTransport_RefusesAnUnsupportedProtocolVersion sends a version older than
// any the endpoint serves. The SDK refuses it with 400 and names the versions it
// does support, and no tool runs.
func TestTransport_RefusesAnUnsupportedProtocolVersion(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")

	resp := sendMCP(t, f, http.MethodPost, a.Token,
		http.Header{"Mcp-Protocol-Version": {"2024-01-01"}},
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "Unsupported protocol version") ||
		!strings.Contains(string(body), newProtocol) {
		t.Errorf("body = %q, want the SDK's unsupported-version error naming %s", body, newProtocol)
	}
}

// mcpClient is the two ways these tests speak MCP: the SDK client, and bare POSTs.
type mcpClient interface {
	tools(t *testing.T) []string
	// call runs one tool, fails the test if the tool reported an error, and
	// returns its structured result.
	call(t *testing.T, name string, args map[string]any) json.RawMessage
}

type sdkClient struct{ session *mcpsdk.ClientSession }

func (c sdkClient) tools(t *testing.T) []string {
	t.Helper()
	res, err := c.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func (c sdkClient) call(t *testing.T, name string, args map[string]any) json.RawMessage {
	t.Helper()
	res, err := c.session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s reported an error: %s", name, textOf(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal %s result: %v", name, err)
	}
	return raw
}

// rawClient speaks MCP as bare JSON-RPC POSTs at one protocol version: no
// initialize, no session id. On the stateless version each request carries the
// per-request metadata that version requires instead of a handshake.
type rawClient struct {
	f       *fixture
	bearer  string
	version string
	n       int
}

func (c *rawClient) tools(t *testing.T) []string {
	t.Helper()
	var res struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(c.rpc(t, "tools/list", nil), &res); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func (c *rawClient) call(t *testing.T, name string, args map[string]any) json.RawMessage {
	t.Helper()
	var res struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		Content           json.RawMessage `json:"content"`
	}
	raw := c.rpc(t, "tools/call", map[string]any{"name": name, "arguments": args})
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode %s result: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s reported an error: %s", name, res.Content)
	}
	return res.StructuredContent
}

// rpc sends one request and returns its result. It fails the test on any HTTP
// or JSON-RPC error, and on a response that carries a session id.
func (c *rawClient) rpc(t *testing.T, method string, params map[string]any) json.RawMessage {
	t.Helper()
	c.n++
	if params == nil {
		params = map[string]any{}
	}
	header := http.Header{"Mcp-Protocol-Version": {c.version}}
	if c.version >= newProtocol {
		// The stateless version requires the method, and a tool call's name, as
		// HTTP headers too, so a load balancer can route without reading the body.
		header.Set("Mcp-Method", method)
		if name, ok := params["name"].(string); ok {
			header.Set("Mcp-Name", name)
		}
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion":    c.version,
			"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "raw", "version": "1"},
			"io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.n, "method": method, "params": params})
	if err != nil {
		t.Fatalf("marshal %s: %v", method, err)
	}
	resp := sendMCP(t, c.f, http.MethodPost, c.bearer, header, string(body))
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s at %s: status %d: %s", method, c.version, resp.StatusCode, respBody)
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.Errorf("%s at %s: the response issued session id %q; a stateless endpoint issues none",
			method, c.version, sid)
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		t.Fatalf("%s at %s: decode response %q: %v", method, c.version, respBody, err)
	}
	if out.Error != nil {
		t.Fatalf("%s at %s: JSON-RPC error %d: %s", method, c.version, out.Error.Code, out.Error.Message)
	}
	return out.Result
}
