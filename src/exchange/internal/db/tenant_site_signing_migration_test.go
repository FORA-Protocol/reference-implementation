//go:build integration

package db_test

import (
	"context"
	"testing"

	sharedb "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// TestTenantSiteSigningColumnsMigration verifies the 000036 cutover both ways.
//
// Up: fora.tenants no longer carries signing_scheme or cloudfront_key_pair_id,
// and the CHECK that spanned them is gone. rsa_key_ref stays, because the seat
// keeps the private-key references whatever scheme a site uses. The
// fora.signing_scheme enum type also stays, because fora.publisher_domains uses
// it — dropping it would take that column with it.
//
// Down: both columns come back as 000002 declared them, and so does the
// constraint. The step down runs against a populated table so the NOT NULL on
// the restored signing_scheme is exercised against a real row rather than an
// empty one, where Postgres would accept a column definition that cannot hold.
//
// Migrate(35) is an explicit version rather than Steps(-1) so migrations stacked
// above 000036 later do not shift the math.
func TestTenantSiteSigningColumnsMigration(t *testing.T) {
	ctx := context.Background()
	dsn := sharedb.AcquireTestDSN(t, ctx, sharedPG)

	for _, col := range []string{"signing_scheme", "cloudfront_key_pair_id"} {
		if schemaProbe.HasColumn(t, ctx, dsn, "tenants", col) {
			t.Errorf("after up: fora.tenants still has the %s column", col)
		}
	}
	if !schemaProbe.HasColumn(t, ctx, dsn, "tenants", "rsa_key_ref") {
		t.Error("after up: fora.tenants lost rsa_key_ref; the seat keeps the private-key references")
	}
	if hasTenantsRSACheck(t, ctx, dsn) {
		t.Error("after up: the tenants_rsa_fields_required CHECK survived the columns it spans")
	}
	// The enum type is shared with fora.publisher_domains, so the cutover must
	// leave it in place.
	if !hasSigningSchemeEnum(t, ctx, dsn, "publisher_domains") {
		t.Error("after up: fora.publisher_domains.signing_scheme is no longer the fora.signing_scheme enum")
	}

	// Seed one seat so the down migration's restored NOT NULL column is added to
	// a populated table. The row is arranged through the sqlc layer because there
	// is no tenant creation RPC and TenantWriteRepo has no create surface. The
	// pool is closed before the schema is stepped so the migrator holds the only
	// session.
	const tenantID = "t_site_signing_down"
	pool := sharedb.OpenForTest(t, ctx, dsn)
	if _, err := sqlc.New(pool).InsertTenant(ctx, sqlc.InsertTenantParams{
		TenantID:        tenantID,
		Domain:          tenantID + ".example",
		Ed25519KeyRef:   "secret://ed25519/" + tenantID,
		ReportingPolicy: []byte(`{}`),
	}); err != nil {
		t.Fatalf("InsertTenant: %v", err)
	}
	pool.Close()

	m := schemaProbe.Migrator(t, dsn)
	if err := m.Migrate(35); err != nil {
		t.Fatalf("migrate to version 35 (reverse 000036) with one seat row: %v", err)
	}

	if !hasSigningSchemeEnum(t, ctx, dsn, "tenants") {
		t.Error("after down 000036: tenants.signing_scheme is not the fora.signing_scheme enum")
	}
	if !schemaProbe.HasColumnNotNull(t, ctx, dsn, "tenants", "signing_scheme") {
		t.Error("after down 000036: tenants.signing_scheme is nullable")
	}
	// The restored row must actually hold the default, which is the half a
	// column-shape check cannot see: ADD COLUMN ... DEFAULT backfills every
	// existing row, and a down file that dropped the DEFAULT would leave the seat
	// seeded above with nothing in the column it just declared NOT NULL.
	if !schemaProbe.Exists(t, ctx, dsn,
		`SELECT EXISTS (SELECT 1 FROM fora.tenants WHERE tenant_id = $1 AND signing_scheme = 'ED25519')`,
		tenantID) {
		t.Error("after down 000036: the existing seat row was not backfilled with the ED25519 default")
	}
	if !schemaProbe.HasColumnType(t, ctx, dsn, "tenants", "cloudfront_key_pair_id", "text") {
		t.Error("after down 000036: tenants.cloudfront_key_pair_id is not TEXT")
	}
	if !hasTenantsRSACheck(t, ctx, dsn) {
		t.Error("after down 000036: the tenants_rsa_fields_required CHECK was not restored")
	}
}

// hasSigningSchemeEnum reports whether the named table's signing_scheme column
// is the fora.signing_scheme enum.
//
// It reads udt_schema and udt_name rather than data_type, which renders every
// enum as "USER-DEFINED" and so cannot tell one from another. The enum's
// identity is the point in both directions: the up migration must leave the type
// behind for fora.publisher_domains, and the down migration must restore the
// column as that same type rather than as text.
func hasSigningSchemeEnum(t *testing.T, ctx context.Context, dsn, table string) bool {
	t.Helper()
	return schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
		  SELECT 1 FROM information_schema.columns
		   WHERE table_schema = 'fora' AND table_name = $1
		     AND column_name = 'signing_scheme'
		     AND udt_schema = 'fora' AND udt_name = 'signing_scheme'
		)`, table)
}

// hasTenantsRSACheck reports whether the CHECK that pairs an RSA signing scheme
// with its key references is present on fora.tenants. It is asserted in both
// directions because a down file that restored the two columns without it would
// leave a schema that differs from the one it claims to restore while a
// column-only check still passed.
func hasTenantsRSACheck(t *testing.T, ctx context.Context, dsn string) bool {
	t.Helper()
	return schemaProbe.Exists(t, ctx, dsn, `
		SELECT EXISTS (
		  SELECT 1
		    FROM pg_constraint c
		    JOIN pg_class      t ON t.oid = c.conrelid
		    JOIN pg_namespace  n ON n.oid = t.relnamespace
		   WHERE n.nspname = 'fora'
		     AND t.relname = 'tenants'
		     AND c.conname = 'tenants_rsa_fields_required'
		)`)
}
