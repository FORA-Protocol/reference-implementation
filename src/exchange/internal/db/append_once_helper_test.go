//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// raiseExceptionSQLState is what a plpgsql RAISE EXCEPTION without an explicit
// SQLSTATE reports.
const raiseExceptionSQLState = "P0001"

// refusalFunction is the one function every append-once table's triggers call.
// Migration 000044 renamed it from prevent_evidence_mutation and made it name
// the table its trigger fired on.
const refusalFunction = "refuse_append_once_mutation"

// appendOnceGuard names what makes one fora table append-once: the row trigger
// that refuses UPDATE and DELETE, and the function both of its triggers call.
// Names are unqualified; every one of them lives in schema fora.
type appendOnceGuard struct {
	table      string
	rowTrigger string
	function   string
}

// evidenceGuard is fora.transaction_evidence's guard, bound to function.
func evidenceGuard(function string) appendOnceGuard {
	return appendOnceGuard{
		table:      "transaction_evidence",
		rowTrigger: "trg_transaction_evidence_no_row_mutation",
		function:   function,
	}
}

// assertTruncateRefused proves the guard function raises, and raises the exact
// message the operator documents quote. A BEFORE TRUNCATE statement trigger
// fires regardless of row count, so this needs no fixture.
func assertTruncateRefused(t *testing.T, ctx context.Context, dsn string, g appendOnceGuard) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, "TRUNCATE fora."+g.table)
	if err == nil {
		t.Fatalf("TRUNCATE fora.%s succeeded; the append-once trigger did not fire", g.table)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != raiseExceptionSQLState {
		t.Fatalf("TRUNCATE fora.%s rejected with %v, want SQLSTATE %s from the append-once guard",
			g.table, err, raiseExceptionSQLState)
	}
	if want := "fora." + g.table + " is append-once: TRUNCATE is prohibited"; pgErr.Message != want {
		t.Fatalf("TRUNCATE fora.%s refused with %q, want %q", g.table, pgErr.Message, want)
	}
}

// assertRowMutationTriggerCovers pins the row trigger to BOTH operations AND to
// the guard function. The pg_trigger.tgtype bits are ROW=1, BEFORE=2, INSERT=4,
// DELETE=8, UPDATE=16.
//
// tgfoid is the load-bearing predicate, not a detail: a trigger correctly wired
// to BEFORE/ROW/UPDATE/DELETE but pointing at a permissive function — one that
// returns NEW instead of raising — satisfies every operation-set assertion while
// silently allowing rows to be rewritten. Without it this probe proves the
// trigger is shaped right, not that it refuses anything.
func assertRowMutationTriggerCovers(t *testing.T, ctx context.Context, dsn string, g appendOnceGuard) {
	t.Helper()
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM pg_trigger
			WHERE tgrelid = ('fora.' || $1)::regclass
			  AND tgname = $2
			  AND NOT tgisinternal
			  AND (tgtype & 16) = 16   -- UPDATE
			  AND (tgtype & 8)  = 8    -- DELETE
			  AND (tgtype & 1)  = 1    -- FOR EACH ROW
			  AND (tgtype & 2)  = 2    -- BEFORE
			  AND tgfoid = ('fora.' || $3 || '()')::regprocedure
		)`
	if !schemaProbe.Exists(t, ctx, dsn, q, g.table, g.rowTrigger, g.function) {
		t.Fatalf("%s is not a BEFORE UPDATE OR DELETE row trigger on fora.%s bound to fora.%s()",
			g.rowTrigger, g.table, g.function)
	}
}

// openShapeTable creates a session-scoped TEMP table declared LIKE fora.like
// and returns the one connection that can see it, with the function that closes
// it. The caller closes it before stepping any migration, so the migrator holds
// the only session.
//
// INCLUDING CONSTRAINTS copies the CHECK constraints — the properties a shape
// test is after — and NOT NULL comes along unconditionally; INCLUDING DEFAULTS
// keeps any column default. Postgres never copies foreign keys or triggers
// through LIKE, which is what makes this work: no parent row is needed, and a
// rejected INSERT is unambiguously a CHECK firing rather than a foreign key or
// the append-once guard.
func openShapeTable(t *testing.T, ctx context.Context, dsn, name, like string) (*pgxpool.Conn, func()) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		pool.Close()
		t.Fatalf("acquire: %v", err)
	}
	closeFn := func() {
		conn.Release()
		pool.Close()
	}
	if _, err := conn.Exec(ctx,
		"CREATE TEMP TABLE "+name+" (LIKE fora."+like+" INCLUDING CONSTRAINTS INCLUDING DEFAULTS)"); err != nil {
		closeFn()
		t.Fatalf("create shape table %s: %v", name, err)
	}
	return conn, closeFn
}
