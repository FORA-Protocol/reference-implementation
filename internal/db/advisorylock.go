package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TryAdvisoryLock opens a dedicated session on dsn and takes the session-level
// advisory lock key on it, without waiting. ok is false when another session
// holds the lock; the session is closed and nothing is held. When ok is true,
// the caller holds the lock for as long as it keeps the session, and release
// closes the session, which is what Postgres releases a session lock on.
//
// The session is dedicated on purpose: a lock taken on a pooled connection is
// released whenever that connection is returned, which is not a lifetime a
// caller controls. An error is a database failure, distinct from contention,
// and a caller must treat it as one.
func TryAdvisoryLock(ctx context.Context, dsn string, key int64) (release func(), ok bool, err error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, false, fmt.Errorf("advisory lock: connect: %w", err)
	}
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&ok); err != nil {
		_ = conn.Close(ctx)
		return nil, false, fmt.Errorf("advisory lock: acquire: %w", err)
	}
	if !ok {
		_ = conn.Close(ctx)
		return nil, false, nil
	}
	return func() { _ = conn.Close(context.WithoutCancel(ctx)) }, true, nil
}
