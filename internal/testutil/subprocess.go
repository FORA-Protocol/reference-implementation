package testutil

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"
)

// Some behaviour can only be observed from outside the process: code that ends
// in os.Exit, or that installs process-wide state no test can undo. Such tests
// re-execute their own test binary (the stdlib helper-process pattern): the
// parent runs one helper test in a child and asserts on what the child left
// behind.

// helperProcessEnv tells a re-executed test binary which helper test it was
// started to run. InHelperProcess opens only when the value is the running
// test's own name, so an ordinary `go test` — which never sets it — skips every
// helper body instead of calling a main() that ends in os.Exit.
const helperProcessEnv = "FORA_TEST_HELPER_PROCESS"

// helperRunFlag is the -test.run argument that makes a re-executed test binary
// run exactly the named test and nothing else.
func helperRunFlag(test string) string {
	return "-test.run=^" + test + "$"
}

// HelperResult is what a helper process left behind. The two streams are kept
// apart: a service writes its log records to stdout, and the test runtime's own
// messages (the race detector, coverage) go to stderr.
type HelperResult struct {
	Code   int
	Stdout []byte
	Stderr []byte
}

// RunHelper runs cmd as the helper test named helperTest and returns what the
// child left behind. cmd must re-execute the current test binary with no
// arguments of its own, exec.CommandContext(ctx, os.Args[0]); RunHelper adds the
// flag that selects helperTest. The caller builds the command in its own
// _test.go file, so the exec call stays in test code and this function only
// wires it up.
//
// The child's environment is exactly env plus the helper marker. Nothing is
// inherited from the parent: a child that saw the developer's shell would pick
// up whatever DSN happened to be exported there, and a test of a service's
// start-up would connect to that database and migrate it.
func RunHelper(tb testing.TB, cmd *exec.Cmd, helperTest string, env ...string) HelperResult {
	tb.Helper()
	cmd.Args = append(cmd.Args, helperRunFlag(helperTest))
	cmd.Env = append([]string{helperProcessEnv + "=" + helperTest}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	var res HelperResult
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			tb.Fatalf("run helper process %s: %v", helperTest, err)
		}
		res.Code = exitErr.ExitCode()
	}
	res.Stdout, res.Stderr = stdout.Bytes(), stderr.Bytes()
	return res
}

// InHelperProcess reports whether this test binary was started by RunHelper to
// run tb. A helper test calls it first and skips when it is false.
func InHelperProcess(tb testing.TB) bool {
	return os.Getenv(helperProcessEnv) == tb.Name()
}
