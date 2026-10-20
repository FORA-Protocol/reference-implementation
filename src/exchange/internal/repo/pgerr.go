package repo

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres SQLSTATE codes this package translates into domain sentinels. One
// home for all of them: a second copy of "23505" somewhere else in the package
// is a constant that can drift from the branch that reads it.
const (
	// pgUniqueViolation is returned when an INSERT collides with a primary key
	// or a UNIQUE constraint.
	pgUniqueViolation = "23505"
	// pgForeignKeyViolation is returned when a row references a key that is not
	// there.
	pgForeignKeyViolation = "23503"
	// pgCheckViolation is returned when a value violates a CHECK constraint
	// (e.g. fee_rate_bps outside [0, 10000)).
	pgCheckViolation = "23514"
)

// pgCode returns the SQLSTATE of err, or "" if err is not a Postgres error.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// mapCheckWriteErr maps the error from a guarded write: a Postgres CHECK
// violation becomes the caller's sentinel, and any other error is wrapped with
// op for context. The Go-side guard runs before the round-trip and names the
// specific cause; the CHECK is the in-store backstop, so a violation that slips
// past the guard still surfaces as the caller's domain sentinel rather than a
// raw pgconn.PgError. Returns nil for a nil err so callers can tail-return it.
// The read-back count, when there is one, is the caller's to return alongside.
// Shared by every guarded write port (fee rates, default agent credit).
func mapCheckWriteErr(err error, op string, sentinel error) error {
	if err == nil {
		return nil
	}
	if pgCode(err) == pgCheckViolation {
		return sentinel
	}
	return fmt.Errorf("%s: %w", op, err)
}

// fkSentinel pairs one NAMED foreign key with the domain error a violation of it
// means. The name is the constraint's, as the migration that created it spells
// it -- Postgres reports that name, and it is the only thing that tells two
// violations on one table apart.
type fkSentinel struct {
	constraint string
	err        error
}

// mapInsertErr maps the error from an INSERT to the caller's domain errors: a
// unique violation becomes duplicate, and a foreign-key violation becomes the
// sentinel paired with the constraint Postgres named.
//
// Anything that matches nothing is wrapped with op, which is deliberate rather
// than a gap: a table with two foreign keys can fail on either, and mapping an
// unrecognized one onto the sentinel for the other would tell the caller a
// specific untruth about which reference was missing. A wrapped error at least
// carries the real constraint name. Returns nil for a nil err.
func mapInsertErr(err error, op string, duplicate error, fks ...fkSentinel) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("%s: %w", op, err)
	}
	if pgErr.Code == pgUniqueViolation && duplicate != nil {
		return duplicate
	}
	if pgErr.Code == pgForeignKeyViolation {
		for _, fk := range fks {
			if fk.constraint == pgErr.ConstraintName {
				return fk.err
			}
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}
