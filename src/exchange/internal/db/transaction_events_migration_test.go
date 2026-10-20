//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
)

// eventsGuard is fora.transaction_events' append-once guard.
var eventsGuard = appendOnceGuard{
	table:      "transaction_events",
	rowTrigger: "trg_transaction_events_no_row_mutation",
	function:   refusalFunction,
}

// TestTransactionEventsSchema covers what migration 000044 guarantees about
// fora.transaction_events, the journal of denied transaction items:
//   - it is append-once, through the refusal function it shares with
//     fora.transaction_evidence, and that function names the table it refused;
//   - a denial is recorded once per agent and item key, and names a seat and an
//     agent that exist, while the resource and its owner are recorded values;
//   - every identifier refuses an empty value, and the two a caller influences
//     are length-bounded, because nothing ever removes a row;
//   - the one read has its index;
//   - the migration reverses cleanly — giving the evidence table back its own
//     function and message — and applies again.
//
// Every property is a DDL one, asserted from the catalog or against a TEMP copy
// of the table, for the reason TestTransactionEvidenceSchema gives: the
// journal's repository exposes no mutation, so no production surface can
// attempt one.
func TestTransactionEventsSchema(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)
	if !schemaProbe.HasTable(t, ctx, dsn, "transaction_events") {
		t.Fatal("after up: fora.transaction_events is missing")
	}

	assertTruncateRefused(t, ctx, dsn, eventsGuard)
	assertRowMutationTriggerCovers(t, ctx, dsn, eventsGuard)
	assertEventKeyedPerAgent(t, ctx, dsn)
	assertEventReferencesOnlySeatAndAgent(t, ctx, dsn)
	assertPublisherReadIndexed(t, ctx, dsn)
	assertEventColumnBounds(t, ctx, dsn)

	// Explicit versions rather than Steps, so migrations stacked above 000044
	// later do not shift the math.
	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(43); err != nil {
		t.Fatalf("migrate to version 43 (reverse 000044): %v", err)
	}
	if schemaProbe.HasTable(t, ctx, dsn, "transaction_events") {
		t.Fatal("after down 000044: fora.transaction_events still exists")
	}
	if hasForaType(t, ctx, dsn, "transaction_event_type") {
		t.Fatal("after down 000044: the fora.transaction_event_type enum was left behind")
	}
	if schemaProbe.HasFunction(t, ctx, dsn, refusalFunction) {
		t.Fatalf("after down 000044: fora.%s() was left behind", refusalFunction)
	}
	// The evidence table is guarded by its own function again, and still raises
	// the message 000024 gave it.
	assertTruncateRefused(t, ctx, dsn, evidenceGuard("prevent_evidence_mutation"))
	assertRowMutationTriggerCovers(t, ctx, dsn, evidenceGuard("prevent_evidence_mutation"))

	if err := m.Up(); err != nil {
		t.Fatalf("re-apply after down 000044: %v", err)
	}
	assertTruncateRefused(t, ctx, dsn, eventsGuard)
	assertRowMutationTriggerCovers(t, ctx, dsn, evidenceGuard(refusalFunction))
}

// assertEventKeyedPerAgent pins the constraint the journal's ON CONFLICT
// targets. agent_id is in it because the protocol scopes idempotency per
// authenticated caller: without it, a second agent presenting the same
// request key and offer would have its denial silently dropped.
func assertEventKeyedPerAgent(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM pg_constraint
			WHERE conrelid = 'fora.transaction_events'::regclass
			  AND contype  = 'u'
			  AND pg_get_constraintdef(oid) = 'UNIQUE (event_type, agent_id, idempotency_key)'
		)`
	if !schemaProbe.Exists(t, ctx, dsn, q) {
		t.Fatal("fora.transaction_events is not UNIQUE on (event_type, agent_id, idempotency_key)")
	}
}

// assertEventReferencesOnlySeatAndAgent pins which columns are references and
// which are values. tenant_id and agent_id must name rows that exist, and an
// event must never be orphaned by removing either. resource_id and
// resource_owner_id are copied from the catalog entry at decision time and must
// NOT be foreign keys: the denial stays with the publisher that owned the
// resource when it was refused, whatever the catalog says later.
func assertEventReferencesOnlySeatAndAgent(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	// confdeltype 'r' is ON DELETE RESTRICT. The referenced table is matched by
	// regclass, not by the rendered definition, whose schema prefix depends on
	// the session's search_path.
	const q = `
		SELECT count(*) = 2
		   AND bool_and(confdeltype = 'r')
		   AND bool_or(confrelid = 'fora.tenants'::regclass
		               AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (tenant_id) REFERENCES %')
		   AND bool_or(confrelid = 'fora.agents'::regclass
		               AND pg_get_constraintdef(oid) LIKE 'FOREIGN KEY (agent_id) REFERENCES %')
		  FROM pg_constraint
		 WHERE conrelid = 'fora.transaction_events'::regclass
		   AND contype  = 'f'`
	if !schemaProbe.Exists(t, ctx, dsn, q) {
		t.Fatal("fora.transaction_events foreign keys are not exactly tenant_id → fora.tenants " +
			"and agent_id → fora.agents, both ON DELETE RESTRICT")
	}
}

// assertPublisherReadIndexed pins the index behind the journal's only read: one
// publisher's denials on one seat over a period. The table only ever grows, so
// its absence would surface as a slow read long after any test could notice.
func assertPublisherReadIndexed(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	const q = `
		SELECT EXISTS (
			SELECT 1 FROM pg_index i
			JOIN pg_class c ON c.oid = i.indexrelid
			WHERE i.indrelid = 'fora.transaction_events'::regclass
			  AND c.relname = 'transaction_events_publisher_idx'
			  AND pg_get_indexdef(i.indexrelid) LIKE '%(tenant_id, resource_owner_id, created_at)'
		)`
	if !schemaProbe.Exists(t, ctx, dsn, q) {
		t.Fatal("fora.transaction_events has no (tenant_id, resource_owner_id, created_at) index")
	}
}

// eventShapeRow is one row for the TEMP copy of fora.transaction_events. Only
// the text columns vary; the rest are fixed valid values.
type eventShapeRow struct {
	eventID, idempotencyKey, offerID, tenantID, agentID, resourceID, resourceOwnerID string
}

// validEventShapeRow is a row every CHECK accepts. Each case varies one column
// from it, so a rejection can come from nothing else.
func validEventShapeRow() eventShapeRow {
	return eventShapeRow{
		eventID: "event-shape", idempotencyKey: "idem-shape:offer-shape", offerID: "offer-shape",
		tenantID: "tenant-shape", agentID: "agent.example", resourceID: "resource-shape",
		resourceOwnerID: "pub_shape",
	}
}

// assertEventColumnBounds covers every value-shape constraint 000044 puts on
// the journal. The bounds are exact: the largest accepted value is accepted and
// one byte more is refused. offer_id is at most 255 bytes, and idempotency_key,
// the 256-byte request key plus ':' plus the offer id, at most 512.
func assertEventColumnBounds(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	conn, done := openShapeTable(t, ctx, dsn, "event_shape", "transaction_events")
	defer done()

	cases := []struct {
		name         string
		edit         func(*eventShapeRow)
		wantRejected bool
	}{
		{"baseline", func(*eventShapeRow) {}, false},
		{"empty event_id", func(r *eventShapeRow) { r.eventID = "" }, true},
		{"empty idempotency_key", func(r *eventShapeRow) { r.idempotencyKey = "" }, true},
		{"512-byte idempotency_key", func(r *eventShapeRow) { r.idempotencyKey = strings.Repeat("k", 512) }, false},
		{"513-byte idempotency_key", func(r *eventShapeRow) { r.idempotencyKey = strings.Repeat("k", 513) }, true},
		{"empty offer_id", func(r *eventShapeRow) { r.offerID = "" }, true},
		{"255-byte offer_id", func(r *eventShapeRow) { r.offerID = strings.Repeat("o", 255) }, false},
		{"256-byte offer_id", func(r *eventShapeRow) { r.offerID = strings.Repeat("o", 256) }, true},
		{"empty tenant_id", func(r *eventShapeRow) { r.tenantID = "" }, true},
		{"empty agent_id", func(r *eventShapeRow) { r.agentID = "" }, true},
		{"empty resource_id", func(r *eventShapeRow) { r.resourceID = "" }, true},
		{"empty resource_owner_id", func(r *eventShapeRow) { r.resourceOwnerID = "" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := validEventShapeRow()
			tc.edit(&row)
			assertCheckOutcome(t, insertEventShapeRow(ctx, conn, row), tc.wantRejected)
		})
	}
}

// insertEventShapeRow writes one row into the TEMP shape table and returns the
// raw error so the caller can classify it. The copy carries no UNIQUE
// constraint, so cases stay independent without any cleanup.
func insertEventShapeRow(ctx context.Context, conn *pgxpool.Conn, r eventShapeRow) error {
	const q = `
		INSERT INTO event_shape (
			event_id, event_type, created_at, tenant_id, idempotency_key,
			offer_id, agent_id, resource_id, resource_owner_id, denial_reason
		) VALUES ($1, 'BILLING_AUTHORIZATION', now(), $2, $3, $4, $5, $6, $7, 'OFFER_EXPIRED')`
	_, err := conn.Exec(ctx, q,
		r.eventID, r.tenantID, r.idempotencyKey, r.offerID, r.agentID, r.resourceID, r.resourceOwnerID)
	return err
}

// hasForaType reports whether a type of that name exists in schema fora.
func hasForaType(t *testing.T, ctx context.Context, dsn, name string) bool {
	t.Helper()
	return schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM pg_type ty
			JOIN pg_namespace n ON n.oid = ty.typnamespace
			WHERE n.nspname = 'fora' AND ty.typname = $1
		)`, name)
}
