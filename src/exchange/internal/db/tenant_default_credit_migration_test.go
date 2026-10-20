//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
)

// TestTenantDefaultCreditMigration verifies the three welcome-credit schema
// changes and that each down is the exact inverse of its up:
//
//   - 000026 added fora.tenants.default_agent_credit, one NUMERIC amount.
//   - 000037 replaced it with default_agent_credits, a JSONB map of one amount
//     per currency, because the Exchange now bills on one ledger per currency.
//   - 000050 dropped the map: the Exchange keeps the welcome credit in its own
//     configuration, parsed once at boot, instead of copying it onto the seat.
//
// So at head neither column exists; stepping down to 48 brings the map back;
// stepping down to 36 swaps it for the NUMERIC one; stepping down to 25 removes
// both. Each step names an explicit version rather than Steps(-1), so migrations
// stacked on top later do not shift the math.
func TestTenantDefaultCreditMigration(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	// Head: no welcome-credit column of either shape.
	if schemaProbe.HasColumn(t, ctx, dsn, "tenants", "default_agent_credits") {
		t.Fatal("after up: fora.tenants still has the default_agent_credits column 000050 drops")
	}
	if schemaProbe.HasColumn(t, ctx, dsn, "tenants", "default_agent_credit") {
		t.Fatal("after up: the single-amount default_agent_credit column survived 000037")
	}

	m := schemaProbe.Migrator(t, dsn)

	// Migrate(49) reverses 000050: the map comes back in the shape 000037 made.
	if err := m.Migrate(49); err != nil {
		t.Fatalf("migrate to version 49 (reverse 000050): %v", err)
	}
	if !schemaProbe.HasColumn(t, ctx, dsn, "tenants", "default_agent_credits") {
		t.Fatal("after down 000050: fora.tenants is missing the restored default_agent_credits column")
	}
	if !schemaProbe.HasColumnType(t, ctx, dsn, "tenants", "default_agent_credits", "jsonb") {
		t.Error("after down 000050: default_agent_credits is not JSONB")
	}
	if !schemaProbe.HasColumnNotNull(t, ctx, dsn, "tenants", "default_agent_credits") {
		t.Error("after down 000050: default_agent_credits is nullable")
	}

	// Migrate(36) reverses 000037: the map goes, the single amount comes back.
	if err := m.Migrate(36); err != nil {
		t.Fatalf("migrate to version 36 (reverse 000037): %v", err)
	}
	if schemaProbe.HasColumn(t, ctx, dsn, "tenants", "default_agent_credits") {
		t.Error("after down 000037: fora.tenants still has the default_agent_credits column")
	}
	if !schemaProbe.HasColumn(t, ctx, dsn, "tenants", "default_agent_credit") {
		t.Fatal("after down 000037: fora.tenants is missing the restored default_agent_credit column")
	}

	// Migrate(25) reverses the 000026 ADD COLUMN.
	if err := m.Migrate(25); err != nil {
		t.Fatalf("migrate to version 25 (reverse 000026): %v", err)
	}
	if schemaProbe.HasColumn(t, ctx, dsn, "tenants", "default_agent_credit") {
		t.Fatal("after down 000026: fora.tenants still has the default_agent_credit column")
	}
}

// TestTenantDefaultCreditsMigration_InitializesRetainedRows: the rollout that
// introduced 000037 KEEPS tenant rows (only agents, transactions, evidence and
// the ledger are recreated), so the migration has live rows to convert. Every
// retained row must come out with an empty map — "no grant" — rather than NULL,
// which the reader would refuse, or a carried-over amount in a currency the
// column can no longer name.
func TestTenantDefaultCreditsMigration_InitializesRetainedRows(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	m := schemaProbe.Migrator(t, dsn)
	// Step back to the schema the rollout starts from: the single NUMERIC amount.
	if err := m.Migrate(36); err != nil {
		t.Fatalf("migrate to version 36: %v", err)
	}
	// A tenant row carrying a configured amount, the state a live deployment is
	// in when the migration runs. Seeded through raw SQL because no repository
	// port inserts tenants and the head-generated query cannot run against this
	// older schema; the whole file is schema-level testing, the same category as
	// the information_schema reads schemaProbe performs.
	if !schemaProbe.Exists(t, ctx, dsn, `
		WITH inserted AS (
			INSERT INTO fora.tenants (
				tenant_id, domain, ed25519_key_ref, reporting_policy, default_agent_credit
			) VALUES (
				't_credits_retained', 't-credits-retained.example',
				'secret://ed25519/t_credits_retained', '{}'::jsonb, 50
			)
			RETURNING 1
		)
		SELECT EXISTS (SELECT 1 FROM inserted)`) {
		t.Fatal("could not seed a tenant row at version 36")
	}

	if err := m.Migrate(37); err != nil {
		t.Fatalf("migrate to version 37 with one tenant row: %v", err)
	}
	// The retained row has the empty map, not NULL and not a converted amount.
	if !schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM fora.tenants
			 WHERE tenant_id = 't_credits_retained'
			   AND default_agent_credits = '{}'::jsonb
		)`) {
		t.Error("after up 000037: the retained tenant row was not initialized to an empty map")
	}
}

// TestDropTenantDefaultCreditsMigration_DownRestoresEmptyMap: reversing 000050
// on a deployment that has seats must give every seat the empty map, which is
// "no grant", rather than NULL, which a reader of that schema refuses.
func TestDropTenantDefaultCreditsMigration_DownRestoresEmptyMap(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	// Seeded through raw SQL for the same reason as the test above: no
	// repository port inserts tenants, and this file tests the schema itself.
	if !schemaProbe.Exists(t, ctx, dsn, `
		WITH inserted AS (
			INSERT INTO fora.tenants (tenant_id, domain, ed25519_key_ref, reporting_policy)
			VALUES ('t_credits_dropped', 't-credits-dropped.example',
			        'secret://ed25519/t_credits_dropped', '{}'::jsonb)
			RETURNING 1
		)
		SELECT EXISTS (SELECT 1 FROM inserted)`) {
		t.Fatal("could not seed a tenant row at head")
	}

	if err := schemaProbe.Migrator(t, dsn).Migrate(49); err != nil {
		t.Fatalf("migrate to version 49 with one tenant row: %v", err)
	}
	if !schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
			SELECT 1 FROM fora.tenants
			 WHERE tenant_id = 't_credits_dropped'
			   AND default_agent_credits = '{}'::jsonb
		)`) {
		t.Error("after down 000050: the tenant row did not come back with an empty map")
	}
}
