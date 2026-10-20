package logging_test

import (
	"log"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/logging"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// TestParseLevel pins the whole vocabulary of a level variable: the four names
// in any case, empty as the INFO default, and a refusal for everything else,
// including names other tools accept and the offset forms slog's own parser
// takes.
func TestParseLevel(t *testing.T) {
	t.Parallel()
	accepted := map[string]slog.Level{
		"":      slog.LevelInfo,
		"DEBUG": slog.LevelDebug, "debug": slog.LevelDebug,
		"INFO": slog.LevelInfo, "Info": slog.LevelInfo,
		"WARN": slog.LevelWarn, "wArN": slog.LevelWarn,
		"ERROR": slog.LevelError, "error": slog.LevelError,
	}
	for raw, want := range accepted {
		if got, err := logging.ParseLevel(raw); err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"WARNING", "TRACE", "FATAL", "INFO+2", "DEBUG-4", "0", "verbose"} {
		_, err := logging.ParseLevel(raw)
		if err == nil {
			t.Errorf("ParseLevel(%q) accepted a value outside the four names", raw)
			continue
		}
		// The message reaches an operator in the exit record, so it names the
		// value that was refused and every value that would have been accepted.
		if msg := err.Error(); !strings.Contains(msg, `"`+raw+`"`) || !strings.Contains(msg, "DEBUG, INFO, WARN or ERROR") {
			t.Errorf("ParseLevel(%q) error = %q; want the value and the four accepted names", raw, msg)
		}
	}
}

// Setup changes process-wide state: slog.SetDefault also rewires the log
// package, and putting the old default back does not undo that. So Setup runs
// only in a child process. The child logs through the three paths a service's
// records take (the logger Setup returned, slog.Default(), and the log
// package), and the parent reads what reached stdout.

const (
	setupHelper   = "TestHelperSetup"
	setupLevelVar = "FORA_TEST_LOG_LEVEL"
	// setupHelperExit proves the child ran the helper body: a helper that
	// skipped would exit 0.
	setupHelperExit = 3
)

// TestHelperSetup is the subprocess body, not a standalone test.
func TestHelperSetup(t *testing.T) {
	if !testutil.InHelperProcess(t) {
		t.Skip("helper process for TestSetup_*; run via subprocess only")
	}
	logger, err := logging.Setup(os.Stdout, setupLevelVar, runhttp.EnvTrimmed(setupLevelVar))
	if err != nil {
		logger.Error("setup.refused", "err", err.Error())
	}
	logger.Debug("returned.debug")
	logger.Info("returned.info")
	logger.Warn("returned.warn")
	slog.Info("default.info")
	slog.Warn("default.warn")
	log.Print("bridged")
	os.Exit(setupHelperExit)
}

// runSetup runs the helper with env and returns the records it wrote to stdout.
func runSetup(t *testing.T, env ...string) []map[string]any {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0])
	res := testutil.RunHelper(t, cmd, setupHelper, env...)
	if res.Code != setupHelperExit {
		t.Fatalf("helper exited %d, want %d; stderr: %s", res.Code, setupHelperExit, res.Stderr)
	}
	return testutil.DecodeRecords(t, res.Stdout)
}

// assertLevels fails unless records are exactly want, read as message → level.
// Exactly, so a record that should have been filtered out fails the test as
// surely as one that is missing.
func assertLevels(t *testing.T, records []map[string]any, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(records))
	for _, rec := range records {
		msg, _ := rec["msg"].(string)
		got[msg], _ = rec["level"].(string)
	}
	if !maps.Equal(got, want) {
		t.Errorf("records (msg → level) = %v, want %v", got, want)
	}
}

// TestSetup_UnsetMeansInfo pins the default: with nothing set, a service logs
// exactly as it did before the setting existed — INFO and above, through the
// logger it was handed and through the process default alike.
func TestSetup_UnsetMeansInfo(t *testing.T) {
	t.Parallel()
	assertLevels(t, runSetup(t), map[string]string{
		"returned.info": "INFO", "returned.warn": "WARN",
		"default.info": "INFO", "default.warn": "WARN",
		"bridged": "ERROR",
	})
}

// TestSetup_AppliesTheLevelToEveryPath pins that the chosen level governs the
// process default as well as the returned logger, read in any case with
// surrounding spaces ignored — and that the standard library's own lines stay
// visible even at ERROR.
func TestSetup_AppliesTheLevelToEveryPath(t *testing.T) {
	t.Parallel()
	assertLevels(t, runSetup(t, setupLevelVar+"= Error "), map[string]string{"bridged": "ERROR"})
	assertLevels(t, runSetup(t, setupLevelVar+"=debug"), map[string]string{
		"returned.debug": "DEBUG", "returned.info": "INFO", "returned.warn": "WARN",
		"default.info": "INFO", "default.warn": "WARN",
		"bridged": "ERROR",
	})
}

// TestSetup_RefusesAnUnknownLevelAndInstallsNothing pins the failure half: the
// error names the variable, the value and the accepted names, the returned
// logger still reports it at INFO, and nothing was installed — the process
// default and the log package never reach the service's stdout.
func TestSetup_RefusesAnUnknownLevelAndInstallsNothing(t *testing.T) {
	t.Parallel()
	records := runSetup(t, setupLevelVar+"=WARNING")
	assertLevels(t, records, map[string]string{
		"setup.refused": "ERROR", "returned.info": "INFO", "returned.warn": "WARN",
	})
	const want = `FORA_TEST_LOG_LEVEL: "WARNING" is not a log level (want DEBUG, INFO, WARN or ERROR, in any case)`
	for _, rec := range records {
		if rec["msg"] == "setup.refused" && rec["err"] != want {
			t.Errorf("refusal err = %v, want %q", rec["err"], want)
		}
	}
}
