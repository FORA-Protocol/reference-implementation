// Package record provides a generic, thread-safe slice recorder for test fixtures and assertions.
package record

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Recorder is a generic, thread-safe slice recorder for test fixtures and assertions.
// The zero value is ready for use.
type Recorder[T any] struct {
	mu    sync.Mutex
	items []T
}

// Record appends an item to the recorded sequence.
func (r *Recorder[T]) Record(item T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item)
}

// Snapshot returns a point-in-time copy of all recorded items.
func (r *Recorder[T]) Snapshot() []T {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]T, len(r.items))
	copy(out, r.items)
	return out
}

// Len returns the count of items recorded so far.
func (r *Recorder[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.items)
}

// CaptureResult holds a captured request and any error from reading its body.
type CaptureResult struct {
	Req     *http.Request
	ReadErr error
}

// CheckReadErr fatals if reading the request body failed. A request whose
// body could not be read carries an empty body and a non-nil ReadErr;
// signature verification would then fail misleadingly. Every caller that
// uses the request body should call CheckReadErr first.
func (r *CaptureResult) CheckReadErr(tb testing.TB) {
	tb.Helper()
	if r.ReadErr != nil {
		tb.Fatalf("captured request body could not be read: %v", r.ReadErr)
	}
}

// CaptureServer returns an httptest.Server that records every request it receives
// (body restored) and an associated Recorder for assertions. The server is closed
// on test cleanup.
func CaptureServer(t *testing.T) (*httptest.Server, *Recorder[*CaptureResult]) {
	t.Helper()
	seen := &Recorder[*CaptureResult]{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clone := r.Clone(context.Background())
		body, err := io.ReadAll(r.Body)
		if err != nil {
			// Record the error alongside the request so the test fails with a clear message
			// instead of a misleading "signature did not verify" later.
			clone.Body = io.NopCloser(bytes.NewReader(nil))
			seen.Record(&CaptureResult{Req: clone, ReadErr: err})
		} else {
			clone.Body = io.NopCloser(bytes.NewReader(body))
			seen.Record(&CaptureResult{Req: clone})
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}
