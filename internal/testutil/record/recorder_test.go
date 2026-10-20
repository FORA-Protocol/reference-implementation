package record_test

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil/record"
)

func TestRecorder_Empty(t *testing.T) {
	var r record.Recorder[string]
	if r.Len() != 0 {
		t.Errorf("r.Len() = %d, want 0", r.Len())
	}
	if items := r.Snapshot(); len(items) != 0 {
		t.Errorf("len(r.Snapshot()) = %d, want 0", len(items))
	}
}

func TestRecorder_RecordAndSnapshot(t *testing.T) {
	var r record.Recorder[int]
	r.Record(1)
	r.Record(2)
	r.Record(3)

	if r.Len() != 3 {
		t.Errorf("r.Len() = %d, want 3", r.Len())
	}

	snap := r.Snapshot()
	if len(snap) != 3 || snap[0] != 1 || snap[1] != 2 || snap[2] != 3 {
		t.Errorf("snap = %v, want [1 2 3]", snap)
	}

	// Mutating the returned snapshot must not affect the recorder.
	snap[0] = 999
	snap2 := r.Snapshot()
	if snap2[0] != 1 {
		t.Errorf("recorder items mutated: snap2[0] = %d, want 1", snap2[0])
	}
}

func TestRecorder_Concurrent(t *testing.T) {
	var r record.Recorder[int]
	const goroutines = 20
	const iterations = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(base int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				r.Record(base*iterations + j)
				_ = r.Len()
				_ = r.Snapshot()
			}
		}(i)
	}
	wg.Wait()

	const want = goroutines * iterations
	if r.Len() != want {
		t.Errorf("r.Len() = %d, want %d", r.Len(), want)
	}
	if len(r.Snapshot()) != want {
		t.Errorf("len(r.Snapshot()) = %d, want %d", len(r.Snapshot()), want)
	}
}

func TestCaptureResult_CheckReadErr(t *testing.T) {
	t.Run("no error", func(t *testing.T) {
		// A capture result with no read error must not fail.
		cr := &record.CaptureResult{}
		// This must not panic or fail.
		cr.CheckReadErr(t)
	})
	t.Run("with error", func(t *testing.T) {
		// A capture result with a read error must fail with a clear message.
		readErr := errors.New("connection reset")
		cr := &record.CaptureResult{ReadErr: readErr}

		// Use a fake TB to capture the fatal.
		fake := &fakeTB{fatalCalled: false}
		cr.CheckReadErr(fake)

		if !fake.fatalCalled {
			t.Error("CheckReadErr did not call Fatal for non-nil ReadErr")
		}
		if !strings.Contains(fake.fatalMsg, "connection reset") {
			t.Errorf("Fatal message %q does not contain the read error", fake.fatalMsg)
		}
	})
}

// fakeTB captures Fatal calls for testing.
type fakeTB struct {
	testing.TB
	fatalCalled bool
	fatalMsg    string
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.fatalCalled = true
	// Simulate the message that would be printed.
	f.fatalMsg = format
	if len(args) > 0 {
		f.fatalMsg = format + " (args: " + args[0].(error).Error() + ")"
	}
}

func (f *fakeTB) Helper() {}
