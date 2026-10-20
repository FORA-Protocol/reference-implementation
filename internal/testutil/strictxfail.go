package testutil

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// StrictXFail runs body as a strict expected failure: a test that states what
// the specification requires of a product that does not do it yet. It is the Go
// form of the strict pytest xfail ADR-008 D3 requires of the Python suites, and
// it holds only for its own defect:
//
//   - body fails, and every failure message matches signature: the real test
//     passes and logs "XFAIL <defect>";
//   - body fails for any other reason, including a failure with no message, a
//     panic, or one matching failure next to one that does not: the real test
//     fails with those failures;
//   - body passes: the real test fails with "XPASS(strict): <defect> is fixed;
//     remove the marker", so the marker cannot outlive the defect;
//   - body skips without failing: the real test is skipped.
//
// defect names the off-spec behavior in plain words. signature is the defect's
// own failure, matched against each failure message without its file:line. A
// marker without a defect, without a signature, or with a signature that matches
// the empty string (and so any message at all) is refused, because it would hold
// for every failure. A defect that carries a tracker key is refused too: the
// tracker entry points at the test, never the other way round.
//
// body runs on its own goroutine against a recording testing.TB, which captures
// failures instead of reporting them. Fatal, Fatalf, FailNow and the Skip family
// stop body the way they stop a test, through runtime.Goexit. body must use the
// tb it is handed, never the enclosing test's t: a FailNow on t from body's
// goroutine is a misuse the testing package reports as a panic.
//
// Cleanup functions body registers pass to the real test and run when it ends,
// not when body returns, so a resource body creates and caches on a shared
// fixture outlives body exactly as it would have without the marker. A failure
// those functions report once the verdict is in is reported on the real test.
// Log output passes through at once, prefixed with the caller's file:line.
func StrictXFail(tb testing.TB, defect string, signature *regexp.Regexp, body func(tb testing.TB)) {
	tb.Helper()
	if err := checkMarker(defect, signature); err != nil {
		tb.Fatalf("StrictXFail: %v", err)
	}
	rec := runRecorded(tb, body)
	result := rec.seal()
	switch judge(result, signature) {
	case xfailHeld:
		tb.Logf("XFAIL %s: %s", defect, strings.Join(result.messages(), "; "))
	case xfailPassed:
		tb.Fatalf("XPASS(strict): %s is fixed; remove the marker", defect)
	case xfailSkipped:
		tb.Skipf("skipped inside StrictXFail(%s): %s", defect, result.skipReason)
	case xfailUnexpected:
		reportUnexpected(tb, defect, result)
	}
}

// trackerKey matches an issue-tracker key such as PROJ-123.
var trackerKey = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-[0-9]+\b`)

// checkMarker refuses a marker that would hold for every failure, and a defect
// named by a tracker key instead of by its behavior.
func checkMarker(defect string, signature *regexp.Regexp) error {
	switch {
	case strings.TrimSpace(defect) == "":
		return errors.New("the defect must be named")
	case trackerKey.MatchString(defect):
		return fmt.Errorf("defect %q carries a tracker key; name the behavior instead", defect)
	case signature == nil:
		return fmt.Errorf("defect %q needs the signature of its own failure", defect)
	case signature.MatchString(""):
		return fmt.Errorf("signature %q of defect %q matches the empty string, so it matches any failure",
			signature, defect)
	}
	return nil
}

// reportUnexpected fails tb with every failure body recorded, so the reader sees
// the failure exactly as it would have been reported without the marker.
func reportUnexpected(tb testing.TB, defect string, result runResult) {
	tb.Helper()
	if result.panicked != "" {
		tb.Errorf("body panicked inside StrictXFail(%s): %s", defect, result.panicked)
	}
	if len(result.failures) == 0 && result.panicked == "" {
		tb.Errorf("body failed without a message inside StrictXFail(%s), which cannot be its signature", defect)
	}
	for _, f := range result.failures {
		tb.Errorf("failure is not the expected defect (%s): %s: %s", defect, f.where, f.msg)
	}
	tb.FailNow()
}

// xfailOutcome is what one run of body means for the real test.
type xfailOutcome int

const (
	xfailHeld xfailOutcome = iota
	xfailPassed
	xfailSkipped
	xfailUnexpected
)

// judge decides the outcome of one recorded run. It is pure so every branch is
// unit-tested without a test binary that is meant to fail.
func judge(r runResult, signature *regexp.Regexp) xfailOutcome {
	switch {
	case r.panicked != "":
		return xfailUnexpected
	case !r.failed && r.skipped:
		return xfailSkipped
	case !r.failed:
		return xfailPassed
	case len(r.failures) == 0:
		return xfailUnexpected
	}
	for _, f := range r.failures {
		if !signature.MatchString(f.msg) {
			return xfailUnexpected
		}
	}
	return xfailHeld
}

// failure is one failure body reported, with the file:line that reported it.
type failure struct {
	where string
	msg   string
}

// runResult is what body did, read once body has stopped.
type runResult struct {
	failed     bool
	skipped    bool
	skipReason string
	panicked   string
	failures   []failure
}

func (r runResult) messages() []string {
	out := make([]string, 0, len(r.failures))
	for _, f := range r.failures {
		out = append(out, f.msg)
	}
	return out
}

// runRecorded runs body to completion on its own goroutine against a recorder
// over parent, and returns that recorder once body has returned, stopped
// through runtime.Goexit, or panicked.
func runRecorded(parent testing.TB, body func(tb testing.TB)) *xfailRecorder {
	rec := &xfailRecorder{TB: parent, helpers: map[string]bool{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				rec.mu.Lock()
				rec.result.panicked = fmt.Sprintf("%v\n%s", p, debug.Stack())
				rec.mu.Unlock()
			}
		}()
		body(rec)
	}()
	<-done
	return rec
}

// xfailRecorder is the testing.TB body runs against. testing.TB has an unexported
// method, so a recorder can only be one by embedding a real TB: every method it
// does not declare below (Name, Cleanup, TempDir, Setenv, Context, Output and
// the rest) passes through to the real test. It declares every method that
// fails, skips or attributes a line, and records instead of reporting.
//
// Once sealed, after the verdict, it forwards failures to the real test: they
// can only come from a Cleanup function body registered, which runs on the real
// test's goroutine when it ends.
type xfailRecorder struct {
	testing.TB

	mu      sync.Mutex
	sealed  bool
	result  runResult
	helpers map[string]bool
}

// callerSkip is the number of frames between where's runtime.Callers and the
// code that called a recorder method: runtime.Callers, where, record, and the
// recorder method itself.
const callerSkip = 4

// record stores one failure, or forwards it once the recorder is sealed. It
// must be called directly from the public recorder method, so callerSkip holds.
func (r *xfailRecorder) record(msg string) bool {
	where := r.where()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return false
	}
	r.result.failed = true
	r.result.failures = append(r.result.failures, failure{where: where, msg: msg})
	return true
}

// where is the file:line of the first caller that has not marked itself a
// helper, the line testing.T would have printed.
func (r *xfailRecorder) where() string {
	pcs := make([]uintptr, 64)
	n := runtime.Callers(callerSkip, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		f, more := frames.Next()
		if !r.helpers[f.Function] || !more {
			return fmt.Sprintf("%s:%d", filepath.Base(f.File), f.Line)
		}
	}
}

// seal ends the recording and returns what body did.
func (r *xfailRecorder) seal() runResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sealed = true
	return r.result
}

func (r *xfailRecorder) isSealed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sealed
}

// Helper marks the calling function as a helper, so a failure it reports is
// attributed to its caller.
func (r *xfailRecorder) Helper() {
	pc, _, _, ok := runtime.Caller(1)
	if !ok {
		return
	}
	fn := runtime.FuncForPC(pc).Name()
	r.mu.Lock()
	r.helpers[fn] = true
	r.mu.Unlock()
}

// Error records a failure and lets body continue.
func (r *xfailRecorder) Error(args ...any) {
	if !r.record(fmt.Sprint(args...)) {
		r.TB.Error(args...)
	}
}

// Errorf records a failure and lets body continue.
func (r *xfailRecorder) Errorf(format string, args ...any) {
	if !r.record(fmt.Sprintf(format, args...)) {
		r.TB.Errorf(format, args...)
	}
}

// Fatal records a failure and stops body.
func (r *xfailRecorder) Fatal(args ...any) {
	if !r.record(fmt.Sprint(args...)) {
		r.TB.Fatal(args...)
	}
	runtime.Goexit()
}

// Fatalf records a failure and stops body.
func (r *xfailRecorder) Fatalf(format string, args ...any) {
	if !r.record(fmt.Sprintf(format, args...)) {
		r.TB.Fatalf(format, args...)
	}
	runtime.Goexit()
}

// Fail marks body failed without a message and lets it continue.
func (r *xfailRecorder) Fail() {
	if r.isSealed() {
		r.TB.Fail()
		return
	}
	r.mu.Lock()
	r.result.failed = true
	r.mu.Unlock()
}

// FailNow marks body failed without a message and stops it.
func (r *xfailRecorder) FailNow() {
	if r.isSealed() {
		r.TB.FailNow()
	}
	r.Fail()
	runtime.Goexit()
}

// Failed reports whether body has failed so far.
func (r *xfailRecorder) Failed() bool {
	if r.isSealed() {
		return r.TB.Failed()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result.failed
}

// Skip records a skip with its reason and stops body.
func (r *xfailRecorder) Skip(args ...any) { r.skip(fmt.Sprint(args...)) }

// Skipf records a skip with its reason and stops body.
func (r *xfailRecorder) Skipf(format string, args ...any) { r.skip(fmt.Sprintf(format, args...)) }

// SkipNow records a skip and stops body.
func (r *xfailRecorder) SkipNow() { r.skip("") }

func (r *xfailRecorder) skip(reason string) {
	if r.isSealed() {
		r.TB.Skip(reason)
	}
	r.mu.Lock()
	r.result.skipped = true
	r.result.skipReason = reason
	r.mu.Unlock()
	runtime.Goexit()
}

// Skipped reports whether body has skipped.
func (r *xfailRecorder) Skipped() bool {
	if r.isSealed() {
		return r.TB.Skipped()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result.skipped
}

// Log writes to the real test's output at once, prefixed with the caller's line.
func (r *xfailRecorder) Log(args ...any) { r.log(fmt.Sprintln(args...)) }

// Logf writes to the real test's output at once, prefixed with the caller's line.
func (r *xfailRecorder) Logf(format string, args ...any) { r.log(fmt.Sprintf(format, args...)) }

func (r *xfailRecorder) log(msg string) {
	where := r.where()
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	_, _ = fmt.Fprintf(r.Output(), "%s: %s", where, msg)
}
