package main

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// TestHelperMain runs main() in a child process for TestLogLevelSetting.
func TestHelperMain(t *testing.T) { testutil.RunMainAsHelper(t, main) }

// TestLogLevelSetting pins CONSOLE_LOG_LEVEL through the binary's own main(): the
// variable main reads, a value that is not a level refusing the boot before
// anything runs, and the level it read being applied. AssertLogLevelContract
// says what each case can observe on this boot path.
func TestLogLevelSetting(t *testing.T) {
	t.Parallel()
	testutil.AssertLogLevelContract(t, testutil.LogLevelContract{
		Command: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, os.Args[0])
		},
		Helper:    "TestHelperMain",
		Var:       "CONSOLE_LOG_LEVEL",
		ExitEvent: "console.exit",
		DSNVar:    "CONSOLE_DSN",
		// No BootInfoEvent: the database is the first thing the console's boot
		// reads, so nothing between DEBUG and ERROR is written before it stops.
	})
}
