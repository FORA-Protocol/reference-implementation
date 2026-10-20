package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
)

const (
	// maxRecords bounds how many requests are kept. The Exchanges poll the
	// Broker's revocation list once a second each, so this holds hours of a run.
	maxRecords = 100_000
	// maxRecordBytes bounds the body bytes kept across every record.
	maxRecordBytes = 256 << 20
	// callerNameTTL is how long the names of a caller's address are reused. An
	// address moves to another container only when one restarts.
	callerNameTTL = 15 * time.Second
	// callerLookupTimeout bounds one reverse lookup of a caller's address.
	callerLookupTimeout = 500 * time.Millisecond
)

// Record is one request as it reached the proxy.
type Record struct {
	// Seq numbers the records in arrival order, from 1.
	Seq  int64     `json:"seq"`
	Time time.Time `json:"time"`
	// Scheme is the leg the request arrived on: "http", or "https" on the TLS
	// listener.
	Scheme string `json:"scheme"`
	// Target is the Host the caller sent the request to, as host:port.
	Target string `json:"target"`
	// CallerAddress is the address the request came from, and CallerNames the
	// names the network's DNS gives it: the caller's container.
	CallerAddress string   `json:"caller_address"`
	CallerNames   []string `json:"caller_names"`
	Method        string   `json:"method"`
	// Path is the request target exactly as it arrived, query included.
	Path    string      `json:"path"`
	Headers [][2]string `json:"headers"`
	Body    []byte      `json:"body"`
	// MessageType and Message are the FORA request message the body carries,
	// rendered as snake_case proto-JSON, for a request to a FORA RPC.
	// DecodeProblem says why a body sent to a FORA RPC was not decoded.
	MessageType   string          `json:"message_type,omitempty"`
	Message       json.RawMessage `json:"message,omitempty"`
	DecodeProblem string          `json:"decode_problem,omitempty"`
}

// size is what a record counts against maxRecordBytes.
func (r *Record) size() int { return len(r.Body) + len(r.Message) }

// matches reports whether needle occurs in the record's request target, body,
// header values or decoded message.
func (r *Record) matches(needle string) bool {
	if needle == "" || strings.Contains(r.Path, needle) {
		return true
	}
	if bytes.Contains(r.Body, []byte(needle)) || bytes.Contains(r.Message, []byte(needle)) {
		return true
	}
	for _, pair := range r.Headers {
		if strings.Contains(pair[1], needle) {
			return true
		}
	}
	return false
}

// store keeps the most recent records in arrival order.
type store struct {
	mu         sync.Mutex
	clk        clock.Clock
	records    []Record
	bytes      int
	next       int64
	maxRecords int
	maxBytes   int
}

func newStore(clk clock.Clock, maxRecords, maxBytes int) *store {
	return &store{clk: clk, maxRecords: maxRecords, maxBytes: maxBytes}
}

// add numbers and keeps r, evicting the oldest records past either bound.
func (s *store) add(r Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	r.Seq = s.next
	r.Time = s.clk.Now()
	s.records = append(s.records, r)
	s.bytes += r.size()
	for len(s.records) > 1 && (len(s.records) > s.maxRecords || s.bytes > s.maxBytes) {
		s.bytes -= s.records[0].size()
		s.records = s.records[1:]
	}
}

// query is one read of the store.
type query struct {
	after    int64
	contains string
}

// window is the answer to a query: the matching records, and the sequence
// numbers of the oldest record kept and the latest one taken.
type window struct {
	Oldest  int64    `json:"oldest"`
	Latest  int64    `json:"latest"`
	Records []Record `json:"records"`
}

// latest is the sequence number of the last record taken, 0 before the first.
func (s *store) latest() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.next
}

func (s *store) read(q query) window {
	s.mu.Lock()
	defer s.mu.Unlock()
	w := window{Latest: s.next, Oldest: s.next + 1, Records: []Record{}}
	if len(s.records) > 0 {
		w.Oldest = s.records[0].Seq
	}
	for i := range s.records {
		r := &s.records[i]
		if r.Seq <= q.after || !r.matches(q.contains) {
			continue
		}
		w.Records = append(w.Records, *r)
	}
	return w
}

// callerNames remembers, for a short while, the names the network's DNS gives
// each caller address.
type callerNames struct {
	mu    sync.Mutex
	clk   clock.Clock
	known map[string]namedAt
}

type namedAt struct {
	names []string
	at    time.Time
}

func newCallerNames(clk clock.Clock) *callerNames {
	return &callerNames{clk: clk, known: make(map[string]namedAt)}
}

// lookup returns the names of address, without trailing dots; none when the
// network's DNS has none for it.
func (c *callerNames) lookup(ctx context.Context, address string) []string {
	now := c.clk.Now()
	c.mu.Lock()
	cached, ok := c.known[address]
	c.mu.Unlock()
	if ok && now.Sub(cached.at) < callerNameTTL {
		return cached.names
	}
	lookupCtx, cancel := context.WithTimeout(ctx, callerLookupTimeout)
	defer cancel()
	found, err := net.DefaultResolver.LookupAddr(lookupCtx, address)
	names := make([]string, 0, len(found))
	if err == nil {
		for _, name := range found {
			names = append(names, strings.TrimSuffix(name, "."))
		}
	}
	c.mu.Lock()
	c.known[address] = namedAt{names: names, at: now}
	c.mu.Unlock()
	return names
}
