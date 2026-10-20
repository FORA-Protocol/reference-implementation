// Package db embeds the publisher console's migrations for execution at boot.
package db

import "embed"

// Migrations holds the embedded console schema migration files.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// MigrationsDir is the subdirectory inside Migrations containing .sql files.
const MigrationsDir = "migrations"

// MigrationsTable names the golang-migrate tracking table. It is distinct from
// the other services' tables because the console owns its own logical database:
// a shared table name would make two services' migration histories collide the
// day someone pointed them at one database.
const MigrationsTable = "schema_migrations_console"
