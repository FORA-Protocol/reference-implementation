//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
)

// TestTransactionLogDenialReasonMigration verifies the 000045 DROP both ways:
//   - at head fora.transaction_log no longer carries denial_reason, and the
//     fora.denial_reason enum survives, because fora.transaction_events uses it;
//   - stepping down to version 44 restores the column as 000001 declared it — a
//     nullable fora.denial_reason — appended after result_payload rather than in
//     its original position;
//   - migrating up again removes it.
//
// The restored column is what the older down files need: 000027, 000022 and
// 000014 each ALTER its type, so every sibling test that steps below 27 also
// passes through this down file.
//
// Migrate(44) is an explicit version rather than Steps(-1) so migrations stacked
// above 000045 later do not shift the math. No row is seeded: the column is
// nullable, so the down file has nothing to backfill.
func TestTransactionLogDenialReasonMigration(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	if schemaProbe.HasColumn(t, ctx, dsn, "transaction_log", "denial_reason") {
		t.Fatal("after up: fora.transaction_log still has the denial_reason column")
	}
	if !hasForaType(t, ctx, dsn, "denial_reason") {
		t.Fatal("after up: the fora.denial_reason enum is gone, but fora.transaction_events uses it")
	}

	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(44); err != nil {
		t.Fatalf("migrate to version 44 (reverse 000045): %v", err)
	}
	if !schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			 WHERE table_schema = 'fora' AND table_name = 'transaction_log'
			   AND column_name = 'denial_reason'
			   AND udt_schema = 'fora' AND udt_name = 'denial_reason'
			   AND is_nullable = 'YES'
		)`) {
		t.Fatal("after down 000045: transaction_log.denial_reason is not back as a nullable fora.denial_reason")
	}
	// POSITIONS. The down file says the column comes back appended, after
	// result_payload, and that this is cosmetic. Pin the observation so the
	// comment and the schema cannot drift apart.
	if !schemaProbe.ColumnComesAfter(t, ctx, dsn, "transaction_log", "denial_reason", "result_payload") {
		t.Error("after down 000045: denial_reason did not come back appended after result_payload")
	}

	if err := m.Up(); err != nil {
		t.Fatalf("re-apply after down 000045: %v", err)
	}
	if schemaProbe.HasColumn(t, ctx, dsn, "transaction_log", "denial_reason") {
		t.Fatal("after re-apply: fora.transaction_log still has the denial_reason column")
	}
}
