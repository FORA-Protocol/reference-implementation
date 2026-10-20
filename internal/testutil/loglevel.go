package testutil

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// mainReturnedExit marks a child whose main() returned instead of exiting.
// None of the cases below lets a service boot, so every main() they run must
// end in os.Exit(1); this code turns one that returned into a visible failure
// rather than an exit status that happens to match.
const mainReturnedExit = 42

// RunMainAsHelper is the body of the helper test through which
// AssertLogLevelContract runs a service's main(). Outside a helper process it
// skips, so a normal test run never calls main() in-process.
func RunMainAsHelper(t *testing.T, main func()) {
	if !InHelperProcess(t) {
		t.Skip("helper process for AssertLogLevelContract; run via subprocess only")
	}
	os.Args = os.Args[:1]
	main()
	os.Exit(mainReturnedExit)
}

// LogLevelContract describes one service binary's log-level setting.
type LogLevelContract struct {
	// Command re-executes the current test binary with no arguments of its own,
	// exec.CommandContext(ctx, os.Args[0]). The caller builds it in its own
	// _test.go file (see RunHelper).
	Command func(ctx context.Context) *exec.Cmd
	// Helper is the test whose body is RunMainAsHelper(t, main).
	Helper string
	// Var is the level variable, such as BROKER_LOG_LEVEL.
	Var string
	// ExitEvent is the record main() writes when the service refuses to start,
	// such as broker.exit.
	ExitEvent string
	// DSNVar is the service's database variable. Every child runs with it
	// empty, so each boot that gets past the level setting stops at the missing
	// database, deterministically and without touching a network.
	DSNVar string
	// BootInfoEvent is an INFO record the service's boot writes before it stops
	// at the missing database, when there is one. It lets the contract see the
	// INFO threshold itself: the record is required when the variable is unset,
	// and ERROR must hide it. Leave it empty for a service whose boot writes
	// nothing between DEBUG and ERROR before that stop.
	BootInfoEvent string
}

// AssertLogLevelContract drives a service's main() in child processes and pins
// the part of the level setting that belongs to that binary: main reads its own
// variable, refuses a bad value before anything else runs, and applies the
// level it read.
//
//   - a value that is not a level stops start-up before anything else runs:
//     exit 1 and exactly one record, the exit record, naming the variable;
//   - unset writes no DEBUG record, and writes BootInfoEvent when there is one;
//   - debug, in any case, adds DEBUG records;
//   - ERROR writes only ERROR records, so BootInfoEvent is gone too.
//
// Which records each level keeps is pinned once, by the internal/logging
// tests; the contract does not repeat that mapping per service. On a boot that
// writes nothing between DEBUG and ERROR before its stop, the unset and ERROR
// cases can only see the DEBUG record, which is why BootInfoEvent exists.
//
// The DEBUG cases depend on the one DEBUG record every service writes before
// its boot stops at the missing database: the database helper's "database
// disabled: no DSN set". If that line ever goes, the debug case fails rather
// than passing on a stream that happened to hold no DEBUG line.
func AssertLogLevelContract(t *testing.T, c LogLevelContract) {
	t.Helper()
	t.Run("value that is not a level stops start-up", func(t *testing.T) {
		t.Parallel()
		code, records := runServiceMain(t, c, c.Var+"=WARNING")
		if code != 1 || len(records) != 1 {
			t.Fatalf("exit %d with %d records, want exit 1 with only the exit record: %v", code, len(records), records)
		}
		rec := records[0]
		errText, _ := rec["err"].(string)
		if rec["msg"] != c.ExitEvent || rec["level"] != "ERROR" ||
			!strings.HasPrefix(errText, c.Var+": ") || !strings.Contains(errText, `"WARNING"`) {
			t.Errorf("record = %v, want an ERROR %s naming %s and the refused value", rec, c.ExitEvent, c.Var)
		}
	})
	t.Run("unset writes no DEBUG record", func(t *testing.T) {
		t.Parallel()
		records := assertStoppedAtDatabase(t, c)
		if slices.ContainsFunc(records, isDebugRecord) {
			t.Errorf("unset %s wrote a DEBUG record: %v", c.Var, records)
		}
		if c.BootInfoEvent != "" && !slices.ContainsFunc(records, func(rec map[string]any) bool {
			return rec["msg"] == c.BootInfoEvent && rec["level"] == "INFO"
		}) {
			t.Errorf("unset %s did not write the INFO record %q: %v", c.Var, c.BootInfoEvent, records)
		}
	})
	t.Run("debug in any case adds DEBUG records", func(t *testing.T) {
		t.Parallel()
		records := assertStoppedAtDatabase(t, c, c.Var+"=debug")
		if !slices.ContainsFunc(records, isDebugRecord) {
			t.Errorf("%s=debug wrote no DEBUG record: %v", c.Var, records)
		}
	})
	t.Run("ERROR writes only ERROR records", func(t *testing.T) {
		t.Parallel()
		for _, rec := range assertStoppedAtDatabase(t, c, c.Var+"=ERROR") {
			if rec["level"] != "ERROR" {
				t.Errorf("%s=ERROR wrote a %v record: %v", c.Var, rec["level"], rec)
			}
		}
	})
}

// assertStoppedAtDatabase runs main() with env and fails unless the boot got
// past the level setting and stopped at the missing database, which the exit
// record names. It returns every record the child wrote.
func assertStoppedAtDatabase(t *testing.T, c LogLevelContract, env ...string) []map[string]any {
	t.Helper()
	code, records := runServiceMain(t, c, env...)
	if code != 1 || len(records) == 0 {
		t.Fatalf("exit %d with %d records, want exit 1 ending in %s: %v", code, len(records), c.ExitEvent, records)
	}
	last := records[len(records)-1]
	errText, _ := last["err"].(string)
	if last["msg"] != c.ExitEvent || !strings.Contains(errText, c.DSNVar+" is required") {
		t.Fatalf("last record = %v, want %s reporting %s is required", last, c.ExitEvent, c.DSNVar)
	}
	return records
}

// runServiceMain runs the service's main() in a child with the database
// variable empty plus env, and returns its exit code and log records.
func runServiceMain(t *testing.T, c LogLevelContract, env ...string) (int, []map[string]any) {
	t.Helper()
	res := RunHelper(t, c.Command(t.Context()), c.Helper, append([]string{c.DSNVar + "="}, env...)...)
	return res.Code, DecodeRecords(t, res.Stdout)
}

func isDebugRecord(rec map[string]any) bool {
	return rec["level"] == "DEBUG"
}
