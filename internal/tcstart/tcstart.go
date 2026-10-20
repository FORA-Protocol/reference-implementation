//go:build integration

// Package tcstart starts integration-test containers so that a host port taken
// at the wrong moment does not fail the package.
//
// Every fixture publishes its container's ports on ephemeral host ports, which
// Docker picks. On a rootless Docker daemon that pick races the host: the daemon
// chooses a port that is free inside its own network namespace, and RootlessKit
// must then bind the same number on the host, where an outgoing connection may
// already hold it. The container start then fails with "bind: address already
// in use" although nothing in the test asked for that port. A second start
// draws another port, so the start is retried, a bounded number of times, for
// that one error and no other.
package tcstart

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/testcontainers/testcontainers-go"
)

// attempts bounds the retries. Each collision is with one port among thousands,
// so a run that fails this many times in a row has a different problem.
const attempts = 5

// portRace is the daemon's error when the host-side bind of a published port
// fails. Only this error is retried; any other start failure is returned as is.
const portRace = "address already in use"

// Run calls start until it returns a container without a host-port collision,
// terminating the container a failed start leaves behind before the next try.
func Run[C testcontainers.Container](ctx context.Context, start func(context.Context) (C, error)) (C, error) {
	var (
		c   C
		err error
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		c, err = start(ctx)
		if err == nil || !strings.Contains(err.Error(), portRace) {
			return c, err
		}
		// Said on stderr, as the other fixtures' startup diagnostics are: a
		// TestMain logger is usually a discard logger, and a retry nobody can see
		// would hide how often the box takes a port.
		fmt.Fprintf(os.Stderr, "tcstart: start %d of %d lost its host port: %v\n", attempt, attempts, err)
		// Best-effort; the testcontainers reaper removes anything left over.
		_ = testcontainers.TerminateContainer(c)
		if ctx.Err() != nil {
			break
		}
	}
	return c, fmt.Errorf("host port collided on each of %d starts: %w", attempts, err)
}
