// Package essearchtest is the HTTP stand-in for Elasticsearch: the external
// system faked at the adapter boundary, for tests whose subject is the Broker's
// behaviour rather than Elasticsearch's. It answers _search/template and
// _render/template as scripted, answers anything else 404, and records every
// request. Rendering, ranking and authentication are proven against a real node
// by the integration tests in the essearch package.
package essearchtest

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/essearch"
)

// Key is the API key Config authenticates with. It is not a credential: the
// stand-in records it and checks nothing.
const Key = "stand-in-key"

// Index and TemplateID are what Config names.
const (
	Index      = "catalog"
	TemplateID = "fora-catalog"
)

// Request is one request the stand-in received.
type Request struct {
	Method        string
	Path          string
	Body          []byte
	OpaqueID      string
	Authorization string
}

// AssertBody fails tb unless the request body is the JSON document want.
func (r Request) AssertBody(tb testing.TB, want string) {
	tb.Helper()
	var got, expected any
	if err := json.Unmarshal(r.Body, &got); err != nil {
		tb.Fatalf("request body %q: %v", r.Body, err)
	}
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		tb.Fatalf("expected body %q: %v", want, err)
	}
	if !reflect.DeepEqual(got, expected) {
		tb.Errorf("request body = %s, want %s", r.Body, want)
	}
}

// Server is a running stand-in.
type Server struct {
	srv *httptest.Server

	mu     sync.Mutex
	search Reply
	render Reply
	reqs   []Request
}

// NewServer starts a plain-http stand-in that tb closes at the end of the test.
func NewServer(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	tb.Cleanup(s.srv.Close)
	return s
}

// NewTLSServer starts an https stand-in and returns it with the PEM of the
// certificate it serves, which is its own trust root.
func NewTLSServer(tb testing.TB) (*Server, []byte) {
	tb.Helper()
	s := &Server{}
	s.srv = httptest.NewUnstartedServer(http.HandlerFunc(s.handle))
	s.srv.StartTLS()
	tb.Cleanup(s.srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.srv.Certificate().Raw})
	return s, caPEM
}

// URL is the base URL of the stand-in.
func (s *Server) URL() string { return s.srv.URL }

// Close stops the stand-in, so the next request fails to connect.
func (s *Server) Close() { s.srv.Close() }

// Script sets the answer to every following _search/template request.
func (s *Server) Script(r Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.search = r
}

// ScriptRender sets the answer to every following _render/template request.
func (s *Server) ScriptRender(r Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.render = r
}

// Requests returns the requests received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

// Config returns settings that reach this stand-in: its index, a stored
// template, the URL at _source.url, an API key, and plain http allowed.
func (s *Server) Config() essearch.Config {
	return essearch.Config{
		Endpoint:   s.URL(),
		Index:      Index,
		TemplateID: TemplateID,
		HitURLPath: "_source.url",
		Limit:      10,
		Timeout:    2 * time.Second,
		Auth:       essearch.AuthAPIKey,
		APIKey:     Key,
		AllowHTTP:  true,
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		Body:          body,
		OpaqueID:      r.Header.Get("X-Opaque-Id"),
		Authorization: r.Header.Get("Authorization"),
	})
	search, render := s.search, s.render
	s.mu.Unlock()

	var reply Reply
	var answer func(Reply) string
	switch {
	case strings.HasSuffix(r.URL.Path, "/_render/template"):
		reply, answer = render, Reply.renderBody
	case strings.HasSuffix(r.URL.Path, "/_search/template"):
		reply, answer = search, Reply.searchBody
	default:
		reply = Reply{Status: http.StatusNotFound, ErrorType: "resource_not_found_exception", ErrorReason: "no handler"}
		answer = Reply.renderBody
	}
	respond(w, r, reply, answer)
}

// respond waits out the reply's delay, unless the caller gives up first, and
// writes it.
func respond(w http.ResponseWriter, r *http.Request, reply Reply, answer func(Reply) string) {
	select {
	case <-time.After(reply.Delay):
	case <-r.Context().Done():
		return
	}
	if reply.RedirectTo != "" {
		http.Redirect(w, r, reply.RedirectTo, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if reply.Status != 0 {
		w.WriteHeader(reply.Status)
	}
	_, _ = io.WriteString(w, answer(reply))
}
