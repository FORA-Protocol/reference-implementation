// Package testutil hosts cross-cutting test helpers shared by services and
// internal packages. Helpers here MUST NOT import service-side code; they are
// consumed by both Exchange- and Broker-side test trees, so the dependency
// must flow inward only.
package testutil

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// DiscardLogger returns a *slog.Logger that drops every record — the standard
// quiet logger for tests that need a non-nil logger but no output. Replaces the
// `slog.New(slog.NewTextHandler(io.Discard, nil))` literal previously copy-pasted
// across the broker test tree (testing doctrine §7: shared fixtures, not
// copy-paste).
func DiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// LogCapture is a logger whose records a test can read back, for the case where
// what a service RECORDS is the property under test rather than a side effect of
// it.
//
// That case is real and not rare: a service that answers a caller one thing and
// records another has made a deliberate split, and only reading both halves
// shows the split held. A test that reads only the answer proves half of it.
//
// The buffer is guarded because the services under test log from whatever
// goroutine served the request, and the reader is the test's own.
type LogCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// NewLogCapture returns a capture and the logger that writes into it. JSON,
// because a test asserting on a specific attribute should look for the key
// rather than for a substring of a formatted line.
//
// It records every level, DEBUG included. A service can be run at DEBUG, so a
// record's level is something a test asserts by reading the "level" key — the
// capture must not hide a record just because it sits below INFO.
func NewLogCapture() (*LogCapture, *slog.Logger) {
	c := &LogCapture{}
	return c, slog.New(slog.NewJSONHandler(c, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// Write makes the capture the handler's destination. It is not called by tests.
func (c *LogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// Lines returns the records written so far, one JSON object per entry.
func (c *LogCapture) Lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Split(strings.TrimSuffix(c.buf.String(), "\n"), "\n")
}

// Find returns the captured records whose msg is exactly event. A test asserting
// on one log line says which line it means, rather than searching the whole
// stream for a substring that some other record might also carry.
func (c *LogCapture) Find(event string) []string {
	var out []string
	for _, line := range c.Lines() {
		if strings.Contains(line, `"msg":"`+event+`"`) {
			out = append(out, line)
		}
	}
	return out
}

// Records returns the captured records whose msg is exactly event, decoded, so
// a test reads a record's level and fields by key.
func (c *LogCapture) Records(tb testing.TB, event string) []map[string]any {
	tb.Helper()
	c.mu.Lock()
	raw := append([]byte(nil), c.buf.Bytes()...)
	c.mu.Unlock()
	var out []map[string]any
	for _, rec := range DecodeRecords(tb, raw) {
		if rec["msg"] == event {
			out = append(out, rec)
		}
	}
	return out
}

// AssertLoggedOnce fails the test unless exactly one captured record is event,
// logged at level, and returns that record so the caller can check its other
// fields. The count is part of the assertion: a duplicate fails as surely as a
// record at the wrong level.
func (c *LogCapture) AssertLoggedOnce(tb testing.TB, event, level string) map[string]any {
	tb.Helper()
	recs := c.Records(tb, event)
	if len(recs) != 1 {
		tb.Fatalf("%d %s records, want exactly 1: %v", len(recs), event, recs)
	}
	if recs[0]["level"] != level {
		tb.Errorf("%s logged at %v, want %s: %v", event, recs[0]["level"], level, recs[0])
	}
	return recs[0]
}

// DecodeRecords parses output as one JSON log record per line. A line that is
// not a JSON object fails the test: the services write nothing but records to
// their log stream, and any other line there is one an operator's log pipeline
// cannot parse.
func DecodeRecords(tb testing.TB, output []byte) []map[string]any {
	tb.Helper()
	var records []map[string]any
	for line := range strings.Lines(string(output)) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			tb.Fatalf("output line is not a JSON log record (%v): %q", err, line)
		}
		records = append(records, rec)
	}
	return records
}
