//go:build integration

package transport_test

// What a test puts at an Exchange's address when the Exchange's answer is not
// what the test is about.
//
// A Go test in this module cannot start the real Exchange, and no test may
// emulate one. So every test here that does not depend on an Exchange's answer
// stands up a wireExchange instead. A wireExchange serves the two things
// every registered Exchange publishes for the Broker's routing and health
// checks: its /.well-known/fora.json, advertising the origin that served it and
// naming that origin's /healthz as its health_endpoint, and the liveness answer
// at /healthz. A test can mount further liveness paths, unmount /healthz, and
// make the manifest itself fail. It never decodes a FORA request and never
// builds a FORA answer. Every other request gets the network condition the
// test chose:
//
//   - the sink fails the test, because the test asserts the Broker sent nothing;
//   - an unavailable endpoint answers 503 with no body, so a test can observe
//     that the Broker did forward, without anything answering for an Exchange;
//   - a silent endpoint closes the connection without a byte of response;
//   - a short-answer endpoint answers 200 with an empty body, which decodes to
//     a TransactionResponse with no items whatever was sent;
//   - a recording endpoint answers like the unavailable one and keeps the raw
//     bytes of every request it was sent, so a test can read what the Broker
//     put on the wire. The endpoint stores bytes only; the test decodes them.
//
// The behaviours that need a real Exchange are proven end to end by the suite
// under tests/e2e/harness/.

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	rwtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

// wireAnswer is what a wireExchange does with a request that is neither its
// manifest nor one of its liveness paths.
type wireAnswer func(t testing.TB, w http.ResponseWriter, r *http.Request)

// wireExchange is a network endpoint registered as an Exchange. See the file
// comment for what it serves.
type wireExchange struct {
	url    string
	domain string

	mu sync.Mutex
	// health maps each mounted liveness path to the status it answers, zero
	// meaning 200. A path absent from the map is not mounted, so a request for
	// it reaches the test's answer like any other request.
	health       map[string]int
	healthProbes map[string]int
	// manifestStatus, when non-zero, is what the manifest path answers instead
	// of the manifest.
	manifestStatus int
	manifests      int
	// arrived counts the requests that were neither the manifest nor a
	// liveness path: the sub-requests the Broker sent.
	arrived int
}

// startExchangeSink stands up an endpoint that fails the test if any request
// other than its manifest or its liveness probe reaches it.
func startExchangeSink(t testing.TB) *wireExchange {
	t.Helper()
	return startWireExchange(t, healthManifest, failTheTest)
}

// startExchangeSinkWithManifest is startExchangeSink serving the manifest the
// caller builds for the host that served it. /healthz is still mounted, but
// the Broker probes it only when that manifest names it.
func startExchangeSinkWithManifest(t testing.TB, manifest func(host string) []byte) *wireExchange {
	t.Helper()
	return startWireExchange(t, manifest, failTheTest)
}

// startUnavailableExchange stands up an endpoint that answers every forwarded
// request 503 with no body, the answer an overloaded proxy in front of an
// Exchange gives.
func startUnavailableExchange(t testing.TB) *wireExchange {
	t.Helper()
	return startWireExchange(t, healthManifest, answerUnavailable)
}

// answerUnavailable answers 503 with no body.
func answerUnavailable(_ testing.TB, w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusServiceUnavailable)
}

// forwardRecorder keeps every request a recording endpoint was sent, in
// arrival order.
type forwardRecorder struct {
	mu   sync.Mutex
	sent []forwarded
}

// forwarded is one request as it arrived: the host it was addressed to, its
// path, its content type, its headers and its body, undecoded.
type forwarded struct {
	host        string
	path        string
	contentType string
	header      http.Header
	body        []byte
}

// startRecordingExchange stands up an endpoint that keeps every forwarded
// request and answers it 503 with no body, the answer startUnavailableExchange
// gives. Nothing about it depends on what the request says.
func startRecordingExchange(t testing.TB) (*wireExchange, *forwardRecorder) {
	t.Helper()
	rec := &forwardRecorder{}
	ex := startWireExchange(t, healthManifest, func(t testing.TB, w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read the forwarded body: %v", err)
		}
		rec.mu.Lock()
		rec.sent = append(rec.sent, forwarded{
			host: r.Host, path: r.URL.Path, contentType: r.Header.Get("Content-Type"),
			header: r.Header.Clone(), body: body,
		})
		rec.mu.Unlock()
		answerUnavailable(t, w, r)
	})
	return ex, rec
}

// requests returns a copy of what the endpoint was sent so far.
func (r *forwardRecorder) requests() []forwarded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sent)
}

// startSilentExchange stands up an endpoint that drops every forwarded request's
// connection without a byte of response.
func startSilentExchange(t testing.TB) *wireExchange {
	t.Helper()
	return startWireExchange(t, healthManifest, hangUp)
}

// startShortAnswerExchange stands up an endpoint that answers every forwarded
// request 200 with an empty body in the request's own content type. To the
// Broker's Connect client that is an OK answer carrying no items at all.
func startShortAnswerExchange(t testing.TB) *wireExchange {
	t.Helper()
	return startWireExchange(t, healthManifest, func(_ testing.TB, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
		w.WriteHeader(http.StatusOK)
	})
}

func startWireExchange(t testing.TB, manifest func(host string) []byte, answer wireAnswer) *wireExchange {
	t.Helper()
	ex := &wireExchange{
		health:       map[string]int{healthzPath: 0},
		healthProbes: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc(forawellknown.Path, func(w http.ResponseWriter, r *http.Request) {
		ex.mu.Lock()
		ex.manifests++
		status := ex.manifestStatus
		ex.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write(manifest(r.Host))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if status, ok := ex.liveness(r); ok {
			w.WriteHeader(status)
			return
		}
		ex.mu.Lock()
		ex.arrived++
		ex.mu.Unlock()
		answer(t, w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ex.url = srv.URL
	ex.domain = strings.TrimPrefix(srv.URL, "http://")
	return ex
}

// healthzPath is the liveness path every wireExchange mounts unless a test
// unmounts it, and the one its own manifest names.
const healthzPath = "/healthz"

// liveness answers r when it is a GET for a mounted liveness path, counting the
// probe, and reports whether it did.
func (e *wireExchange) liveness(r *http.Request) (int, bool) {
	if r.Method != http.MethodGet {
		return 0, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	status, mounted := e.health[r.URL.Path]
	if !mounted {
		return 0, false
	}
	e.healthProbes[r.URL.Path]++
	if status == 0 {
		status = http.StatusOK
	}
	return status, true
}

// ownManifest is the manifest a working Exchange publishes: one advertising
// the origin that served it. It names no health endpoint, so a Broker judges
// the Exchange by the manifest fetch alone.
func ownManifest(host string) []byte {
	return rwtestutil.ExchangeManifest(host, "http://"+host)
}

// healthManifest is ownManifest naming the origin's /healthz as its
// health_endpoint. It is what the reference Exchange publishes, and what a
// wireExchange serves unless a test chose another manifest.
func healthManifest(host string) []byte {
	return rwtestutil.ExchangeManifestWithHealth(host, "http://"+host, "http://"+host+healthzPath)
}

// failTheTest is the sink's answer: the request should never have been sent.
func failTheTest(t testing.TB, w http.ResponseWriter, r *http.Request) {
	t.Errorf("the Broker sent %s %s to an Exchange; this test asserts it sends nothing", r.Method, r.URL.Path)
	w.WriteHeader(http.StatusInternalServerError)
}

// hangUp closes the connection at once, with no response and no graceful close.
func hangUp(t testing.TB, w http.ResponseWriter, _ *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		t.Error("the test server does not support hijacking")
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	_ = conn.Close()
}

// setHealthz sets what /healthz answers. The registry health refresher reads it
// on its next pass, when the manifest names it.
func (e *wireExchange) setHealthz(status int) {
	e.setHealth(healthzPath, status)
}

// setHealth mounts path as a liveness path answering status, or changes what
// an already mounted one answers.
func (e *wireExchange) setHealth(path string, status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.health[path] = status
}

// unmountHealth removes the liveness path, so a request for it is answered as
// any other request is and counted as an arrival.
func (e *wireExchange) unmountHealth(path string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.health, path)
}

// setManifestStatus makes the manifest path answer status with no document.
// Zero serves the manifest again.
func (e *wireExchange) setManifestStatus(status int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.manifestStatus = status
}

// arrivals reports how many forwarded requests reached the endpoint.
func (e *wireExchange) arrivals() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.arrived
}

// healthzProbeCount reports how many /healthz requests reached the endpoint
// while /healthz was mounted.
func (e *wireExchange) healthzProbeCount() int {
	return e.healthProbeCount(healthzPath)
}

// healthProbeCount reports how many requests reached the liveness path while it
// was mounted.
func (e *wireExchange) healthProbeCount(path string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.healthProbes[path]
}

// manifestFetches reports how many times the endpoint served its manifest.
func (e *wireExchange) manifestFetches() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.manifests
}
