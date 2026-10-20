//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
)

// billingIDIndex is the object 000047 creates. A totals read narrowed to one
// resource or domain looks settlements up in the transaction log by billing id,
// one batch at a time; without this index each batch reads the whole log.
const billingIDIndex = "transaction_log_billing_id_idx"

// hasBillingIDIndex reports whether the index exists, is valid, and is on
// billing_id. indisvalid is checked for the reason the 000030 test gives: a
// build that fails part way leaves an INVALID index of the right name, which
// the planner will not use.
func hasBillingIDIndex(t *testing.T, ctx context.Context, dsn string) bool {
	t.Helper()
	return schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1
			  FROM pg_index AS i
			  JOIN pg_class AS c ON c.oid = i.indexrelid
			  JOIN pg_namespace AS n ON n.oid = c.relnamespace
			  JOIN pg_indexes AS x ON x.schemaname = n.nspname AND x.indexname = c.relname
			 WHERE n.nspname = 'fora' AND c.relname = $1 AND i.indisvalid
			   AND x.tablename = 'transaction_log' AND x.indexdef LIKE '%(billing_id)%'
		)`, billingIDIndex)
}

// TestTransactionLogBillingIDIndexMigration verifies 000047 both ways: at head
// the index exists on transaction_log (billing_id), and stepping down to
// version 46 removes it. Migrate(46) is an explicit version rather than
// Steps(-1) so later migrations stacking on top do not shift the math.
func TestTransactionLogBillingIDIndexMigration(t *testing.T) {
	ctx := t.Context()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	if !hasBillingIDIndex(t, ctx, dsn) {
		t.Fatalf("after up: %s is missing, invalid, or not on transaction_log (billing_id)", billingIDIndex)
	}
	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(46); err != nil {
		t.Fatalf("migrate to version 46 (reverse 000047): %v", err)
	}
	if hasBillingIDIndex(t, ctx, dsn) {
		t.Errorf("after down 000047: %s still exists", billingIDIndex)
	}
}
