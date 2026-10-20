//go:build integration

package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
)

// TestSessionCalls_EachCarryTheirOwnIdentityAndRequestID drives two agents, each
// on ONE reused client session, with their tool calls sent in alternation. Every
// HTTP request carries its own X-Request-ID, the session-opening request
// included. It runs once per way a client opens a session: initialize on a
// legacy protocol version, and server/discover on 2026-07-28.
//
// A fresh session per call would hide the case this pins: a tool handler whose
// context was captured when the session opened would still see the right id,
// because the session's first request and the call would be the same request.
// Reusing the session is what makes a stale id from initialize or from an
// earlier call observable.
//
// Each call is checked on every surface the id reaches: the response header, the
// id the tool reports, the outbound request the Exchange receives, and the log
// line the tool writes. The Exchange's signature check names the key that
// signed, which is how the test proves each call went out as its own agent.
func TestSessionCalls_EachCarryTheirOwnIdentityAndRequestID(t *testing.T) {
	for _, tc := range []struct{ version, opens string }{
		{"2025-11-25", "initialize"},
		{"2026-07-28", "server/discover"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			assertSessionCallsCorrelate(t, tc.version, tc.opens)
		})
	}
}

func assertSessionCallsCorrelate(t *testing.T, version, opens string) {
	f := newFixture(t)
	f.exchange.statusResp = &forav1.GetAccountStatusResponse{
		Ver: helpers.ProtocolVersion, BillingRef: "acct-1", Active: true,
	}
	one, two := f.provision(t, "dev-one"), f.provision(t, "dev-two")
	oneRT := &requestIDTransport{token: one.Token, prefix: "one"}
	twoRT := &requestIDTransport{token: two.Token, prefix: "two"}
	sessions := map[*requestIDTransport]*mcpsdk.ClientSession{
		oneRT: f.connectVia(t, oneRT, version),
		twoRT: f.connectVia(t, twoRT, version),
	}
	steps := []struct {
		who agent
		rt  *requestIDTransport
	}{{one, oneRT}, {two, twoRT}, {one, oneRT}, {two, twoRT}}

	callIDs := make([]string, 0, len(steps))
	for i, step := range steps {
		out := callTool[statusResult](t, sessions[step.rt], "fora_status", map[string]any{
			"exchange": f.exchange.Domain(t),
		})
		sent := step.rt.lastCall(t)
		callIDs = append(callIDs, sent.sent)

		if out.RequestID != sent.sent {
			t.Errorf("call %d: the tool reported request_id %q, want %q", i, out.RequestID, sent.sent)
		}
		calls := f.exchange.Calls()
		if len(calls) != i+1 {
			t.Fatalf("call %d: the Exchange saw %d calls, want %d", i, len(calls), i+1)
		}
		got := calls[i]
		if got.KeyID != step.who.Thumbprint {
			t.Errorf("call %d: signed with %q, want the caller's key %q", i, got.KeyID, step.who.Thumbprint)
		}
		if got.RequestID != sent.sent {
			t.Errorf("call %d: the outbound request carried %q, want %q", i, got.RequestID, sent.sent)
		}
	}

	for _, rt := range []*requestIDTransport{oneRT, twoRT} {
		rt.assertEchoed(t, opens)
	}
	assertStatusLogIDs(t, f, callIDs)
}

// assertStatusLogIDs checks that the status log lines carry exactly the tool
// calls' ids, in order, each with ONE request_id key. slog appends attributes
// rather than replacing them, so a logger built on a context that already held
// an id would write two request_id keys on one line; decoding into a map would
// keep only one of them, so the raw line is counted instead.
func assertStatusLogIDs(t *testing.T, f *fixture, want []string) {
	t.Helper()
	lines := f.logs.Find("identity.mcp.status")
	if len(lines) != len(want) {
		t.Fatalf("got %d identity.mcp.status lines, want %d: %v", len(lines), len(want), lines)
	}
	for i, line := range lines {
		if n := strings.Count(line, `"request_id":`); n != 1 {
			t.Errorf("status line %d has %d request_id keys, want 1: %s", i, n, line)
		}
		if !strings.Contains(line, fmt.Sprintf(`"request_id":%q`, want[i])) {
			t.Errorf("status line %d does not carry request_id %q: %s", i, want[i], line)
		}
	}
}

// exchangeRecord is one HTTP request of a client session: the JSON-RPC method it
// carried (or the HTTP method when it had no body), the X-Request-ID sent, and
// the X-Request-ID the server answered with.
type exchangeRecord struct {
	method string
	sent   string
	echoed string
}

// requestIDTransport authenticates as one agent and gives each HTTP request a
// distinct X-Request-ID, recording what was sent and what came back.
type requestIDTransport struct {
	token  string
	prefix string

	mu      sync.Mutex
	n       int
	records []exchangeRecord
}

func (r *requestIDTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+r.token)
	method := req.Method
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		clone.Body = io.NopCloser(bytes.NewReader(body))
		var msg struct {
			Method string `json:"method"`
		}
		if json.Unmarshal(body, &msg) == nil && msg.Method != "" {
			method = msg.Method
		}
	}

	r.mu.Lock()
	r.n++
	id := fmt.Sprintf("%s-%d", r.prefix, r.n)
	r.mu.Unlock()
	clone.Header.Set(reqctx.HeaderRequestID, id)

	resp, err := http.DefaultTransport.RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.records = append(r.records, exchangeRecord{
		method: method, sent: id, echoed: resp.Header.Get(reqctx.HeaderRequestID),
	})
	r.mu.Unlock()
	return resp, nil
}

// lastCall returns the most recent tools/call request of this session.
func (r *requestIDTransport) lastCall(t *testing.T) exchangeRecord {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.records) - 1; i >= 0; i-- {
		if r.records[i].method == "tools/call" {
			return r.records[i]
		}
	}
	t.Fatal("the session sent no tools/call request")
	return exchangeRecord{}
}

// assertEchoed checks that every request of the session, the session-opening
// request included, got its own id back in the response header.
func (r *requestIDTransport) assertEchoed(t *testing.T, opens string) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	sawOpen := false
	for _, rec := range r.records {
		sawOpen = sawOpen || rec.method == opens
		if rec.echoed != rec.sent {
			t.Errorf("%s: the response carried X-Request-ID %q, want %q", rec.method, rec.echoed, rec.sent)
		}
	}
	if !sawOpen {
		t.Errorf("session %s sent no %s request; the case under test needs one", r.prefix, opens)
	}
}
