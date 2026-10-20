//go:build integration

package db_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// TestPoolTrace_ACallerEndedStatementIsDebugAndADatabaseFaultIsError pins the
// one place the query tracer departs from pgx's own levels, against a real
// Postgres and through the pool the production constructor builds.
//
// A failure caused by the statement's own context ending — the caller
// cancelled, or its deadline passed — is DEBUG: the caller went away and the
// code that issued the statement records its own outcome. A failure the
// database side produces while the caller is still waiting stays ERROR,
// because that is the record database alerting counts. Each leg names the
// record it leaves; the errors are the ones pgx itself produces on both the
// Acquire and the Query path.
func TestPoolTrace_ACallerEndedStatementIsDebugAndADatabaseFaultIsError(t *testing.T) {
	ctx := t.Context()
	logs, logger := testutil.NewLogCapture()
	pool, err := db.Open(ctx, db.Config{DSN: db.StartPostgres(t, ctx)}, logger)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)

	// Acquire, cancelled before it starts: an Acquire record at DEBUG.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := db.Ping(cancelled, pool); err == nil {
		t.Fatal("ping with a cancelled context succeeded")
	}
	// Acquire, with a deadline already passed: an Acquire record at DEBUG.
	expired, cancelExpired := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer cancelExpired()
	if err := db.Ping(expired, pool); err == nil {
		t.Fatal("ping past its deadline succeeded")
	}
	// A running statement the caller abandons, first by cancelling and then by
	// letting its deadline pass: a Query record at DEBUG each time.
	// Acquire before starting each timer: reconnecting after cancellation can
	// consume the deadline on a busy runner and exercise Acquire, not Query.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire for cancellation: %v", err)
	}
	abandon, cancelAbandon := context.WithCancel(ctx)
	defer cancelAbandon()
	timer := time.AfterFunc(100*time.Millisecond, cancelAbandon)
	_, err = conn.Exec(abandon, "SELECT pg_sleep(5)")
	timer.Stop()
	conn.Release()
	if err == nil {
		t.Fatal("a cancelled statement completed")
	}
	conn, err = pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire for deadline: %v", err)
	}
	overrun, cancelOverrun := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelOverrun()
	_, err = conn.Exec(overrun, "SELECT pg_sleep(5)")
	conn.Release()
	if err == nil {
		t.Fatal("a statement past its deadline completed")
	}
	// A fault the database reports while the caller waits: a Query record at ERROR.
	if _, err := pool.Exec(ctx, "SELECT 1/0"); err == nil {
		t.Fatal("division by zero succeeded")
	}
	// A fault on this side with the caller still waiting: an Acquire record at ERROR.
	pool.Close()
	if err := db.Ping(ctx, pool); err == nil {
		t.Fatal("ping on a closed pool succeeded")
	}

	assertLevels(t, logs, "Acquire", "DEBUG", "DEBUG", "ERROR")
	assertLevels(t, logs, "Query", "DEBUG", "DEBUG", "ERROR")
}

// assertLevels fails unless the captured event records carry exactly the
// levels want, in order.
func assertLevels(t *testing.T, logs *testutil.LogCapture, event string, want ...string) {
	t.Helper()
	records := logs.Records(t, event)
	got := make([]string, 0, len(records))
	for _, rec := range records {
		level, _ := rec["level"].(string)
		got = append(got, level)
	}
	if !slices.Equal(got, want) {
		t.Errorf("%s record levels = %v, want %v: %v", event, got, want, records)
	}
}
