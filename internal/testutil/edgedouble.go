package testutil

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// EdgeDouble is a publisher edge that ENFORCES delivery-URL binding (ADR-013
// D1-D3), for tests of the fetching side.
//
// It runs the real check rather than looking for the headers: it rebuilds the
// RFC 9421 signature base from the request as received, verifies the Ed25519
// signature against the presented key, and enforces the three-way identity
// agent_id == keyid == thumbprint(presented key). A double that only checked the
// headers were present would pass against a signer emitting the wrong bytes,
// which is the one failure a fetcher test has to catch — at the real edge it
// surfaces as an undifferentiated 403.
//
// The verify side is written out here (edgedouble_proof.go) rather than calling
// the SDK's own verifier. Two implementations that agree only because they share
// code prove nothing about interoperability, and the party this must
// interoperate with is the SDK's TypeScript verifier, which the edge runs.
//
// That verifier — sdk/ts/src/pop.ts in github.com/FORA-Protocol/protocol, with
// Go's helpers.VerifyAgentBinding as its parity twin — is the behaviour this
// double tracks. The SDK's shared proof corpus (testdata/pop-sign-vectors.json)
// pins the two together: edgedouble_test.go replays every vector through the
// double, which must accept exactly the ones the corpus marks valid. Where they
// knowingly differ, the difference is commented at the site. Independence is the
// point, so the ORDER of the checks is deliberately not copied; only the
// accept/reject boundaries have to agree, because those are what a test asserts
// on.
//
// Nothing is bound at construction: like the real edge, the double reads the
// expected identity out of the URL's own agent_id parameter, so one double
// serves any number of agents.
type EdgeDouble struct {
	server *httptest.Server
	hits   atomic.Int32

	// now is fixed at construction, so verify and freshness read it without the
	// lock below.
	now int64

	mu  sync.RWMutex
	set settings
	// remotes is the set of client addresses requests arrived from. Guarded by
	// the same mutex as set, for the same reason: the test goroutine reads it
	// while the server's goroutine writes it.
	remotes map[string]struct{}
	// requestIDs is the correlation id each request carried, in arrival order,
	// with an absent header recorded as the empty string rather than skipped —
	// "no id" is the failure worth seeing, so it must not look like "no request".
	requestIDs []string
}

// settings is the double's programmable behaviour, held together so the handler
// goroutine can take it in one consistent read.
//
// The test goroutine writes these through the Set* methods and the httptest
// server's goroutine reads them, with no happens-before edge between the two.
// Guarding them matches the sibling double in the MCP suite, which reads its
// programmable responses under its own mutex for the same reason.
type settings struct {
	body          []byte
	contentType   string
	refusalStatus int
	refusalBody   string
	handler       http.HandlerFunc
}

// NewEdgeDouble starts an enforcing edge and registers its shutdown.
func NewEdgeDouble(tb testing.TB, now int64) *EdgeDouble {
	tb.Helper()
	e := &EdgeDouble{
		now: now,
		set: settings{
			body:        []byte("<html>licensed</html>"),
			contentType: "text/html; charset=utf-8",
		},
		remotes: map[string]struct{}{},
	}
	e.server = httptest.NewServer(http.HandlerFunc(e.serve))
	tb.Cleanup(e.server.Close)
	return e
}

// Origin is the double's scheme and authority.
func (e *EdgeDouble) Origin() string { return e.server.URL }

// Now is the instant the double verifies freshness against, so a test can build a
// signing window that lands on a chosen side of the expiry boundary without
// having to thread the same value through two constructors.
func (e *EdgeDouble) Now() int64 { return e.now }

// URLFor mints a delivery URL bound to agentID, shaped like one the Exchange
// signs: the reserved parameters plus agent_id, which is what the binding check
// reads.
func (e *EdgeDouble) URLFor(agentID string) string {
	return e.server.URL + "/article?exp=4102444800&kid=exchange&agent_id=" + url.QueryEscape(agentID)
}

// Hits counts requests that reached the handler, so a test can assert a redirect
// was NOT followed.
func (e *EdgeDouble) Hits() int32 { return e.hits.Load() }

// DistinctRemotes is how many client addresses requests arrived from.
//
// It is how a test tells a reused connection from a fresh one, which is not
// otherwise observable from this side. Go's HTTP client returns an idle
// connection to its pool and takes it back for the next request to the same
// host, so a batch fetched over one client arrives on one address. A batch that
// builds a client per item cannot reuse anything — each client has its own pool —
// and every item arrives on its own ephemeral port, having paid its own
// handshake.
func (e *EdgeDouble) DistinctRemotes() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.remotes)
}

// noteRemote records the address one request arrived from.
func (e *EdgeDouble) noteRemote(addr string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.remotes[addr] = struct{}{}
}

// RequestIDs is the correlation id each request carried, in arrival order.
//
// The real edge mints one when the header is absent and writes it into every log
// record, so a fetch that arrived without one is diagnosed under a value the
// caller has never seen. Recording what actually arrived is the only way this
// side can tell that apart from a fetch that arrived correlated.
func (e *EdgeDouble) RequestIDs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]string(nil), e.requestIDs...)
}

// noteRequestID records the correlation id one request carried, or the empty
// string when it carried none.
func (e *EdgeDouble) noteRequestID(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requestIDs = append(e.requestIDs, id)
}

// Close stops the double while leaving its URL valid to mint from, so a test can
// drive the dial-failure path against an address that is genuinely refusing
// rather than a hostname that resolves nowhere. Safe to call again — the
// constructor's cleanup does too.
func (e *EdgeDouble) Close() { e.server.Close() }

// SetBody sets what a passing fetch receives.
func (e *EdgeDouble) SetBody(body []byte, contentType string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.set.body, e.set.contentType = body, contentType
}

// SetRefusal makes every request fail with this status and body, without running
// the binding check.
func (e *EdgeDouble) SetRefusal(status int, body string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.set.refusalStatus, e.set.refusalBody = status, body
}

// SetHandler replaces the whole behaviour, for cases the knobs do not cover.
func (e *EdgeDouble) SetHandler(fn http.HandlerFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.set.handler = fn
}

// snapshot takes the programmable behaviour in one read and RETURNS it rather
// than leaving the lock held. serve goes on to run a caller-supplied handler and
// write a response, and some tests install handlers that block until cleanup —
// holding the mutex across that would serialise or deadlock the double.
func (e *EdgeDouble) snapshot() settings {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.set
}

func (e *EdgeDouble) serve(w http.ResponseWriter, r *http.Request) {
	e.hits.Add(1)
	e.noteRemote(r.RemoteAddr)
	e.noteRequestID(r.Header.Get(helpers.RequestIDHeader))
	set := e.snapshot()
	if set.handler != nil {
		set.handler(w, r)
		return
	}
	if set.refusalStatus != 0 {
		w.WriteHeader(set.refusalStatus)
		_, _ = w.Write([]byte(set.refusalBody))
		return
	}
	if reason := e.verify(r); reason != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprintf(w, `{"error":"Agent binding check failed","reason":%q}`, reason)
		return
	}
	w.Header().Set("Content-Type", set.contentType)
	_, _ = w.Write(set.body)
}

// verify returns the edge's own refusal token, or "" when the proof holds. The
// tokens match the production vocabulary so a test can assert on the exact
// string an agent would see.
func (e *EdgeDouble) verify(r *http.Request) string {
	// The target URI is rebuilt from the request AS RECEIVED — the raw request
	// line, never a re-parsed URL — which is what makes this a real interop check.
	return verifyProof(e.now, proof{
		method:    r.Method,
		targetURI: "http://" + r.Host + r.RequestURI,
		agentID:   r.URL.Query().Get("agent_id"),
		header:    r.Header,
	})
}
