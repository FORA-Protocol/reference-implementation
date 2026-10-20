-- NOT A DEPLOYMENT STEP. This file sits beside the PostgreSQL operator documents
-- but is not part of them: it exists only for the automated end-to-end test stack,
-- which runs four Exchanges at once. Do not run it against a deployment — see
-- CONFIGURATION.md §3 for the databases a real deployment does need.
--
-- How it runs: docker-compose.e2e.yml bind-mounts this file into
-- /docker-entrypoint-initdb.d/, so postgres executes it ONCE while initialising the
-- data directory — before the TCP socket opens for application connections. That is
-- why the services below, which wait for `depends_on: postgres condition:
-- service_healthy`, never find a missing database. `make e2e-down -v` wipes the
-- volume, so the next `up` re-runs it. Nothing here is created later, on demand:
-- every service that uses one of these databases runs its own migrations at boot but
-- never issues CREATE DATABASE, so the database has to exist before it connects.

-- Catalog databases. Each Exchange instance owns a SEPARATE catalog database, so
-- DiscoverResources (which reads the whole catalog without filtering by tenant —
-- exchange/internal/service/catalog.go) cannot expose one publisher's catalog on
-- another Exchange. exchange-a uses the default POSTGRES_DB (fora); exchange-b,
-- exchange-c and exchange-d need their own.
CREATE DATABASE fora_b;
CREATE DATABASE fora_c;
CREATE DATABASE fora_d;

-- System-of-Record (SoR) databases. Each Exchange keeps its account registry
-- (schema `sor`, table `sor.agent_accounts`) in a SEPARATE logical database from its
-- catalog DB above: the Postgres SoR adapter opens a SECOND pool via
-- EXCHANGE_SOR_DSN and runs its own migrations at boot. billing_ref is
-- one-per-Exchange (ADR-021), so the registry is per-exchange, mirroring the
-- per-exchange catalog split above.
CREATE DATABASE fora_sor;
CREATE DATABASE fora_sor_b;
CREATE DATABASE fora_sor_c;
CREATE DATABASE fora_sor_d;

-- Identity Service (registry / MCP adapter). It migrates at boot against
-- IDENTITY_DSN, so its database is created here alongside the exchange/SoR ones.
CREATE DATABASE identity;

-- The publisher console's own database. It holds one table -- who has signed in
-- -- and nothing a publisher owns, which all lives in the Exchange. It is a
-- separate database rather than a schema inside fora because the two services
-- run their own migrations and a shared migration table would collide.
CREATE DATABASE console;
