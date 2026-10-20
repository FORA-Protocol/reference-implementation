package testutil

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The tests below observe StrictXFail's verdict by running it, in turn, against
// a recorder: the outer recorder captures what StrictXFail reports on "the real
// test" without failing this one. The recorder itself is checked directly first,
// because every verdict depends on it capturing failures and stopping body.

const testDefect = "the Exchange refuses a report against an offer with no estimate"

var testSignature = regexp.MustCompile(`reported against zero-estimate obligation`)

const matchingFailure = "consumed quantity 3 reported against zero-estimate obligation"

// verdictOf runs StrictXFail over body and returns what it reported on the
// test it was handed.
func verdictOf(t *testing.T, body func(tb testing.TB)) runResult {
	t.Helper()
	return runRecorded(t, func(tb testing.TB) {
		StrictXFail(tb, testDefect, testSignature, body)
	}).seal()
}

func TestStrictXFail_Verdicts(t *testing.T) {
	cases := []struct {
		name string
		body func(tb testing.TB)
		// wantFail is "" when the real test must pass, else a substring of the
		// failure it must report.
		wantFail string
		wantSkip bool
	}{
		{
			name:     "expected failure matched passes as XFAIL",
			body:     func(tb testing.TB) { tb.Fatalf("got: %s", matchingFailure) },
			wantFail: "",
		},
		{
			name:     "every failure matching passes as XFAIL",
			body:     func(tb testing.TB) { tb.Error(matchingFailure); tb.Errorf("again: %s", matchingFailure) },
			wantFail: "",
		},
		{
			name:     "failure with another signature fails with that failure",
			body:     func(tb testing.TB) { tb.Fatalf("build harness: connection refused") },
			wantFail: "build harness: connection refused",
		},
		{
			name: "one matching failure next to another fails",
			body: func(tb testing.TB) {
				tb.Error(matchingFailure)
				tb.Error("accepted report returned no report_id")
			},
			wantFail: "accepted report returned no report_id",
		},
		{
			name:     "unexpected pass fails as XPASS(strict)",
			body:     func(tb testing.TB) { tb.Log("the report was accepted") },
			wantFail: "XPASS(strict): " + testDefect + " is fixed; remove the marker",
		},
		{
			name:     "failure without a message fails",
			body:     func(tb testing.TB) { tb.FailNow() },
			wantFail: "failed without a message",
		},
		{
			name:     "panic fails even when its value matches the signature",
			body:     func(testing.TB) { panic(matchingFailure) },
			wantFail: "body panicked",
		},
		{
			name:     "skip without failure skips",
			body:     func(tb testing.TB) { tb.Skip("no backend") },
			wantSkip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verdictOf(t, tc.body)
			if got.skipped != tc.wantSkip {
				t.Errorf("skipped = %v, want %v", got.skipped, tc.wantSkip)
			}
			if tc.wantFail == "" {
				if got.failed {
					t.Fatalf("real test failed, want it to pass: %v", got.messages())
				}
				return
			}
			if !got.failed || !strings.Contains(strings.Join(got.messages(), "\n"), tc.wantFail) {
				t.Fatalf("real test reported %v (failed=%v), want a failure containing %q",
					got.messages(), got.failed, tc.wantFail)
			}
		})
	}
}

func TestStrictXFail_FatalStopsBody(t *testing.T) {
	reached := false
	got := verdictOf(t, func(tb testing.TB) {
		tb.Fatal(matchingFailure)
		reached = true
	})
	if reached {
		t.Error("body ran past Fatal")
	}
	if got.failed {
		t.Errorf("a Fatal matching the signature must hold as XFAIL, got %v", got.messages())
	}
}

func TestStrictXFail_CleanupRunsAfterBodyStops(t *testing.T) {
	ran := false
	// Registered first, so it runs last: after the cleanup body registered.
	t.Cleanup(func() {
		if !ran {
			t.Error("cleanup registered inside body did not run")
		}
	})
	verdictOf(t, func(tb testing.TB) {
		tb.Cleanup(func() { ran = true })
		tb.Fatal(matchingFailure)
	})
	if ran {
		t.Error("cleanup ran when body stopped; it must run when the real test ends")
	}
}

func TestStrictXFail_RefusesAMarkerThatHoldsForAnyFailure(t *testing.T) {
	body := func(tb testing.TB) { tb.Fatal(matchingFailure) }
	cases := map[string]struct {
		defect    string
		signature *regexp.Regexp
		want      string
	}{
		"no defect":           {"  ", testSignature, "the defect must be named"},
		"no signature":        {testDefect, nil, "needs the signature"},
		"tracker key":         {"PROJ-123 " + testDefect, testSignature, "carries a tracker key"},
		"catch-all signature": {testDefect, regexp.MustCompile(`.*`), "matches the empty string"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := runRecorded(t, func(tb testing.TB) {
				StrictXFail(tb, tc.defect, tc.signature, body)
			}).seal()
			if !got.failed || !strings.Contains(strings.Join(got.messages(), "\n"), tc.want) {
				t.Fatalf("got %v, want a refusal containing %q", got.messages(), tc.want)
			}
		})
	}
}

// failViaHelper reports a failure from a function that marked itself a helper.
func failViaHelper(tb testing.TB) {
	tb.Helper()
	tb.Error("from a helper")
}

func TestRecorder_AttributesAFailureToTheHelpersCaller(t *testing.T) {
	var wantLine int
	got := runRecorded(t, func(tb testing.TB) {
		_, _, wantLine, _ = runtime.Caller(0)
		failViaHelper(tb) // must stay on the line after runtime.Caller
	}).seal()
	if len(got.failures) != 1 {
		t.Fatalf("got %d failures, want 1", len(got.failures))
	}
	want := "strictxfail_test.go:" + strconv.Itoa(wantLine+1)
	if got.failures[0].where != want {
		t.Errorf("failure attributed to %s, want %s", got.failures[0].where, want)
	}
}

func TestRecorder_ReportsStateAsTheBodySeesIt(t *testing.T) {
	var failedBefore, failedAfter bool
	got := runRecorded(t, func(tb testing.TB) {
		failedBefore = tb.Failed()
		tb.Fail()
		failedAfter = tb.Failed()
	}).seal()
	if failedBefore || !failedAfter || !got.failed {
		t.Errorf("Failed() before=%v after=%v, recorded=%v; want false, true, true",
			failedBefore, failedAfter, got.failed)
	}
	if t.Failed() {
		t.Error("a failure inside the recorder reached the real test")
	}
}

func TestJudge(t *testing.T) {
	match := failure{msg: matchingFailure}
	other := failure{msg: "something else"}
	cases := []struct {
		name string
		in   runResult
		want xfailOutcome
	}{
		{"pass", runResult{}, xfailPassed},
		{"skip", runResult{skipped: true}, xfailSkipped},
		{"fail then skip judges the failure", runResult{failed: true, skipped: true, failures: []failure{match}}, xfailHeld},
		{"matching", runResult{failed: true, failures: []failure{match, match}}, xfailHeld},
		{"other", runResult{failed: true, failures: []failure{other}}, xfailUnexpected},
		{"mixed", runResult{failed: true, failures: []failure{match, other}}, xfailUnexpected},
		{"no message", runResult{failed: true}, xfailUnexpected},
		{"panic", runResult{panicked: matchingFailure}, xfailUnexpected},
	}
	for _, tc := range cases {
		if got := judge(tc.in, testSignature); got != tc.want {
			t.Errorf("%s: judge = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestCheckMarker_AcceptsANamedDefectWithItsSignature(t *testing.T) {
	if err := checkMarker(testDefect, testSignature); err != nil {
		t.Fatalf("checkMarker: %v", err)
	}
}
