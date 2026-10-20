//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
)

// TestTransactionEvidenceSchema covers what migration 000024 guarantees about
// fora.transaction_evidence: it is append-once (one of the two tables the shared
// refusal function guards, with fora.transaction_events), its columns refuse
// values that could never have been verified, and it reverses cleanly.
//
// It lives at the infra layer rather than beside the RPC tests because every
// property here is a DDL one: EvidenceRepo deliberately exposes no mutation and
// no way to write a malformed row, so no production surface can attempt either,
// and hand-rolling the attempt in a transport test would be exactly the raw-SQL
// arrange path the testing doctrine forbids. Schema-level assertions belong here,
// in the same category as Migrate (compare the sibling migration tests in this
// package).
//
// All of it runs against ONE container. The column-shape cases are subtests here
// rather than a top-level test of their own precisely so this package does not
// grow another per-test Postgres: the doctrine's per-test-container exemption is
// bounded by growth, and these migration tests step migrations up and back down,
// which a shared post-migration snapshot cannot serve.
//
// The append-once assertions need no fixture row:
//   - TRUNCATE fires the statement-level trigger even on an empty table, which
//     proves the guard function actually raises rather than merely existing;
//   - the row-level trigger is asserted from the catalog, because a trigger
//     wired to the wrong operation set (UPDATE but not DELETE, say) is precisely
//     the regression that would otherwise survive the entire suite.
func TestTransactionEvidenceSchema(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	if !schemaProbe.HasTable(t, ctx, dsn, "transaction_evidence") {
		t.Fatal("after up: fora.transaction_evidence is missing")
	}

	// At head the triggers call the shared refusal function, which 000044 renamed
	// from prevent_evidence_mutation. The message is the one 000024 raised.
	assertTruncateRefused(t, ctx, dsn, evidenceGuard(refusalFunction))
	assertRowMutationTriggerCovers(t, ctx, dsn, evidenceGuard(refusalFunction))
	assertEvidenceBoundToTransactionLog(t, ctx, dsn)
	assertCorrelationKeyIndexed(t, ctx, dsn)
	assertColumnShape(t, ctx, dsn)

	// The down migration must leave no orphaned trigger function behind. It runs
	// last because it drops the table every assertion above reads.
	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(23); err != nil {
		t.Fatalf("migrate to version 23 (reverse 000024): %v", err)
	}
	if schemaProbe.HasTable(t, ctx, dsn, "transaction_evidence") {
		t.Fatal("after down 000024: fora.transaction_evidence still exists")
	}
	if schemaProbe.HasFunction(t, ctx, dsn, "prevent_evidence_mutation") {
		t.Fatal("after down 000024: fora.prevent_evidence_mutation() was left behind")
	}
	if schemaProbe.HasFunction(t, ctx, dsn, refusalFunction) {
		t.Fatalf("after down 000024: fora.%s() was left behind", refusalFunction)
	}
}

// assertEvidenceBoundToTransactionLog pins the structural claim every
// evidence-absence assertion in the transport suite rests on: "no transaction_log
// row therefore no evidence row".
//
// That inference is currently carried by a comment. It holds because
// transaction_id is the evidence PRIMARY KEY (so never NULL) and carries a
// foreign key into transaction_log — an evidence row cannot exist without its
// parent. Asserting it here makes the claim a checked fact rather than prose, and
// keeps it honest if the schema ever loosens.
//
// A behavioural test of the same-transaction rollback (force the evidence write to
// fail, assert the log and obligation rows roll back with it) is NOT drivable
// through the public surface: transaction_id is minted server-side per execute, so
// a caller cannot provoke the primary-key conflict from outside. That coverage is
// filed as a follow-up rather than faked here.
func assertEvidenceBoundToTransactionLog(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	const fk = `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conrelid = 'fora.transaction_evidence'::regclass
			  AND contype  = 'f'
			  AND confrelid = 'fora.transaction_log'::regclass
			  AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (transaction_id) REFERENCES%'
		)`
	if !schemaProbe.Exists(t, ctx, dsn, fk) {
		t.Fatal("transaction_evidence.transaction_id has no foreign key into fora.transaction_log; " +
			"an evidence row could outlive or precede its transaction")
	}
	const pk = `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conrelid = 'fora.transaction_evidence'::regclass
			  AND contype  = 'p'
			  AND pg_get_constraintdef(oid) = 'PRIMARY KEY (transaction_id)'
		)`
	if !schemaProbe.Exists(t, ctx, dsn, pk) {
		t.Fatal("transaction_evidence is not keyed 1:1 on transaction_id")
	}
}

// assertCorrelationKeyIndexed pins the index behind request_id, the column the
// migration header names as the join key correlating an evidence row outward
// against the edge delivery log and the reconciliation sweep.
//
// It is asserted here rather than left to a query plan because the cost of its
// absence is invisible until the table is large: the sweep it exists to serve
// would seq-scan a table that is append-once and has no removal path, so it only
// ever grows. The leading column is what matters — a tenant-only index cannot
// serve a lookup keyed on the correlation id.
func assertCorrelationKeyIndexed(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			WHERE i.indrelid = 'fora.transaction_evidence'::regclass
			  AND c.relname = 'transaction_evidence_request_idx'
			  AND pg_get_indexdef(i.indexrelid) LIKE '%(request_id, created_at DESC)'
		)`
	if !schemaProbe.Exists(t, ctx, dsn, q) {
		t.Fatal("transaction_evidence has no (request_id, created_at DESC) index; " +
			"the correlation sweep the column exists for would seq-scan a never-pruned table")
	}
}
