.PHONY: help quality quality-ci fmt fmt-check lint typecheck test test-fast jscpd file-length install-tools sqlc
.PHONY: go-fmt go-fmt-check go-lint go-typecheck go-test test-integration test-zitadel test-e2e-collect
.PHONY: db-up db-down db-logs dev-keys adr-001-check adr-008-d3-check sdk-pin-check
.PHONY: stale-proto-names-check published-refs-check published-secrets-check test-terraform
.PHONY: test-production-db
.PHONY: delivery-event-check
.PHONY: image-version-check build-args-check sqlc-check harness-deadcode-check
.PHONY: zitadel-up zitadel-creds zitadel-logs zitadel-down buildx-check
.PHONY: edge-%
.PHONY: test-e2e e2e-run e2e-keys e2e-up e2e-down e2e-logs e2e-demo e2e-project ledger go-mod-download

# Go is a single module covering the Go services under src/ and the shared code
# under internal/. Edge (TS) has its own Makefile under src/; the Python e2e harness
# has one under tests/e2e/.

# Parallelism caps: balance concurrency across CPU cores without saturating
# memory bandwidth, thrashing CPU caches, or overwhelming the Docker daemon.
# GO_PARALLEL caps Go toolchain and linter concurrency (avoiding collision with GNU parallel's $PARALLEL).
# The default value 3 must align with:
#   - Dockerfile ARG GO_BUILD_P=3 (deliberate restatement for compose builds)
#   - Publish gate GO_BUILD_P default of 3 (same deliberate restatement)
GO_PARALLEL ?= 3
COMPOSE_PARALLEL ?= $(GO_PARALLEL)
# Only copy from PARALLEL if explicitly passed on the make command line (e.g. make PARALLEL=8),
# ignoring ambient environment variables where GNU parallel stores options like "--jobs 200%".
ifneq ($(filter command line,$(origin PARALLEL)),)
GO_PARALLEL := $(PARALLEL)
COMPOSE_PARALLEL := $(PARALLEL)
endif

# Default build knobs passed to in-image Go builders and Compose concurrency limit.
export GO_BUILD_P ?= $(GO_PARALLEL)
export COMPOSE_PARALLEL_LIMIT ?= $(COMPOSE_PARALLEL)

# TigerBeetle client static archive on macOS has alignment issues with newer Xcode ld_prime:
# "64-bit mach-o member 'compiler_rt.o' not 8-byte aligned in libtb_client_x86_64-macos.a".
# Classic linker flag is needed on Darwin to link CGO binaries cleanly (Xcode 15+ deprecates -ld_classic).
# Obtained via --print so Makefile appends only the delta flag, matching source mode behavior.
# IMPORTANT: Always APPEND to existing CGO_LDFLAGS, never overwrite caller's settings.
DARWIN_CGO_LDFLAGS := $(shell scripts/darwin-cgo-env.sh --print)
ifneq ($(DARWIN_CGO_LDFLAGS),)
export CGO_LDFLAGS := ${CGO_LDFLAGS} ${DARWIN_CGO_LDFLAGS}
endif



help:
	@echo "Root targets:"
	@echo "  make quality        - fmt + lint + typecheck + test-fast + jscpd"
	@echo "  make fmt            - format Go, edge, Python (e2e)"
	@echo "  make lint           - lint Go, edge, Python (e2e)"
	@echo "  make typecheck      - typecheck Go, edge, Python (e2e harness)"
	@echo "  make test-fast      - fast tests across all"
	@echo "  make jscpd          - copy-paste detection across src/, internal/ and the e2e harness"
	@echo "  make file-length    - enforce per-language file-length caps"
	@echo "  make sqlc           - regenerate sqlc Go code from queries + migrations"
	@echo "  make db-up          - start local Postgres + Redis via docker compose"
	@echo "  make db-down        - stop local Postgres + Redis"
	@echo "  make dev-keys       - generate gitignored local signing keys for go-run dev"
	@echo "  make zitadel-up     - start + provision the local Zitadel OIDC provider"
	@echo "  make zitadel-creds  - re-print its export lines (eval-safe)"
	@echo "  make zitadel-down   - stop it and wipe its volumes (issuer is baked in)"
	@echo "  make test-integration - run //go:build integration tests (spins testcontainers)"
	@echo "  make test-terraform - terraform module test suites + config drift checks (needs terraform)"
	@echo "  make test-production-db - operator scripts on real PostgreSQL 16 and Zitadel, the production database bootstrap too when present (needs Docker, psql, lsof, openssl)"
	@echo "  make published-secrets-check - scan the published file set for secrets (needs gitleaks)"
	@echo "  make ledger TX=<id> - render the evidence chain for one transaction"
	@echo "  make install-tools  - install sqlc, gitleaks, golangci-lint, gofumpt, biome, jscpd, wrangler"
	@echo ""
	@echo "E2E testing (tiered for reduced system load):"
	@echo "  make test-e2e-fast    - Fast tests (no stack, ~0.1s)"
	@echo "  make test-e2e-light   - Light tests (needs stack, ~30s)"
	@echo "  make test-e2e-heavy  - Heavy tests (full integration, ~2-5 min)"
	@echo "  make test-e2e-tiered - All tiers in sequence"
	@echo "  make test-e2e        - Original full suite"
	@echo ""
	@echo "Per-subproject: make edge-<target> (e.g. edge-lint, edge-test)"

quality: fmt lint typecheck test-fast harness-deadcode-check jscpd file-length adr-001-check adr-008-d3-check sdk-pin-check stale-proto-names-check delivery-event-check published-refs-check published-secrets-check image-version-check build-args-check sqlc-check
	@echo "All quality gates passed!"

# CI variant of `quality`: formatting is CHECKED, never written. CI must not
# mutate the tree, and a check FAILS on violations instead of the local `fmt`
# silently auto-fixing them. (Go + edge formatting is also enforced by their
# linters; tests/e2e Python formatting is enforced ONLY here.) Local
# `make quality` keeps auto-formatting via `fmt`.
quality-ci: fmt-check lint typecheck test-fast harness-deadcode-check jscpd file-length adr-001-check adr-008-d3-check sdk-pin-check stale-proto-names-check delivery-event-check published-refs-check published-secrets-check image-version-check build-args-check sqlc-check
	@echo "All quality gates passed (CI, check-only fmt)!"

file-length:
	@python3 scripts/check-file-length.py

# Enforces ADR-028 D8 H8: no dead code in the e2e harness. vulture runs over the
# harness's own scope; tests/e2e/pyproject.toml carries that scope and the reason
# for every exclusion.
harness-deadcode-check:
	@$(MAKE) -C tests/e2e deadcode

# Enforces ADR-001: Exchange authz code (service/ + transport/) MUST NOT read
# JWT claims. See docs/architecture/adr-001-three-layer-auth.md.
adr-001-check:
	@scripts/check-jwt-in-exchange-authz.sh

# Enforces ADR-008 D3: lenient pytest xfail markers (strict=False) are forbidden
# under tests/. See docs/architecture/adr-008-testing-surface-isolation.md.
adr-008-d3-check:
	@scripts/check-xfail-strict.sh

# Enforces FORA protocol/SDK pin agreement: one repository, one revision, across
# the four manifests and three lockfiles that name it — even though Go and Python
# now name a release and TypeScript still names a commit. The script's header
# states the invariants; this comment deliberately does not restate them, because
# the last copy here listed four of the eight and went stale in the same commit
# that rewrote it.
sdk-pin-check:
	@scripts/check-sdk-pin-consistency.sh

# Forbids pre-unification RAMPRequest/RAMPResponse and accidental FORA spellings
# (renamed DiscoveryRequest/DiscoveryResponse) in comments and prose, where the
# compiler cannot see them.
stale-proto-names-check:
	@scripts/check-stale-proto-names.sh

# Keeps the edge worker's delivery-record name and the ledger's search for it in
# agreement. They are written in two languages, and drift renders every evidence
# chain's delivery row as "not recorded" while both sides still pass their own
# tests.
delivery-event-check:
	@scripts/check-delivery-event-name.sh

# Terraform module test suites (offline: plan mode + mocked providers) plus
# config drift checks (compatibility_date vs wrangler.toml). Not part of
# `quality`: it needs the terraform binary, which not every dev machine has —
# CI runs it as its own job with zero tolerance.
test-terraform:
	@deploy/terraform/scripts/test-terraform.sh
	python3 deploy/terraform/scripts/tests/ledger-id-derivation-test.py
	python3 deploy/terraform/scripts/tests/currency-table-consistency-test.py

# Operator-script tests. Most run against real containers: the drop of a
# retired issuer's publishers on the real Exchange and console schemas, the
# pinned Zitadel on its scoped database, and on a real Zitadel the bootstrap
# provisioning and then reconciling the developer instance, and the publisher
# instance created through the System API, provisioned, and sending its mail.
# A tree that carries the production deployment adds its own suite: the
# database bootstrap, the host's SQL client and the seed on PostgreSQL 16. The
# suite travels with the production stack, and test-terraform.sh refuses a
# tree that carries one without the other.
# Not part of `quality`: the target needs Docker, psql, lsof and openssl. CI's
# integration job runs it.
# published-ref-allow: the target runs the production suite when the tree carries it
PRODUCTION_SUITE := deploy/terraform/scripts/tests/production-suite.sh
test-production-db:
	python3 deploy/terraform/scripts/tests/drop-issuer-publishers-test.py
	python3 deploy/terraform/scripts/tests/zitadel-bootstrap-reconcile-test.py
	python3 deploy/terraform/scripts/tests/zitadel-instances-test.py
	@if [ -f $(PRODUCTION_SUITE) ]; then $(PRODUCTION_SUITE) db; fi

# Forbids references from published files to anything the publish does not
# ship — such a pointer is unopenable for whoever reads the public repo. The
# script header lists what counts; a deliberate exception takes a
# `published-ref-allow:` comment on the same or preceding line.
published-refs-check:
	@scripts/check-published-refs.sh

# Runs the secret scanner over the tracked files the publish ships, so a key on
# a published path fails the branch instead of the release. The publish tool
# scans the curated tree it is about to push; this is that check moved forward
# to the commit that would cause it. Needs gitleaks — `make install-tools`.
published-secrets-check:
	@scripts/check-published-secrets.sh

# Keeps the published image version declared once per deployment document and
# identical across the three. A release edits one line per document; a partial
# edit would otherwise leave a document naming a tag nobody published, which
# reads as correct right up to the "not found".
image-version-check:
	@scripts/check-image-version.sh

# Verifies Dockerfile ARG defaults match canonical values from go-mod-fetch.sh.
# Catches drift between the single-source-of-truth script and copied ARG lines.
build-args-check:
	@scripts/check-build-args.sh

# src/console/js lints and tests the publisher console's browser scripts,
# which are served as they are from src/console/internal/web/static.
# tests/e2e/wba-oracle-ts is the e2e stack's Web Bot Auth oracle sidecar; its
# TypeScript is held to the edge worker's biome rules and tsc strictness flags.
fmt: go-fmt
	@$(MAKE) -C src/edge fmt
	@$(MAKE) -C src/console/js fmt
	@$(MAKE) -C tests/e2e/wba-oracle-ts fmt
	@$(MAKE) -C tests/e2e fmt

# Check-only formatting (no writes); fails if anything is unformatted.
fmt-check: go-fmt-check
	@$(MAKE) -C src/edge fmt-check
	@$(MAKE) -C src/console/js fmt-check
	@$(MAKE) -C tests/e2e/wba-oracle-ts fmt-check
	@$(MAKE) -C tests/e2e fmt-check

lint: go-lint
	@$(MAKE) -C src/edge lint
	@$(MAKE) -C src/console/js lint
	@$(MAKE) -C tests/e2e/wba-oracle-ts lint
	@$(MAKE) -C tests/e2e lint

typecheck: go-typecheck
	@$(MAKE) -C src/edge typecheck
	@$(MAKE) -C tests/e2e/wba-oracle-ts typecheck
	@$(MAKE) -C tests/e2e typecheck

test test-fast: go-test
	@$(MAKE) -C src/edge test-fast
	@$(MAKE) -C src/console/js test-fast
	@$(MAKE) -C tests/e2e test-fast

go-mod-download:
	@scripts/go-mod-fetch.sh

# Use golangci-lint's formatter (gofumpt + goimports per .golangci.yml), NOT a
# standalone gofumpt binary: the two disagree even at the same version, and
# golangci-lint is the authoritative gate (the `lint` step). This keeps write,
# check, and lint on one formatter.
go-fmt: | go-mod-download
	@echo "==> go fmt (golangci-lint fmt)"
	golangci-lint fmt

# Check-only: `fmt --diff` prints the diff and exits non-zero when unformatted.
go-fmt-check: | go-mod-download
	@echo "==> go fmt check (golangci-lint fmt --diff)"
	golangci-lint fmt --diff

# One lint pass per build-tag combination, the same combinations go-typecheck
# vets: a plain `golangci-lint run` does not load a file behind a build tag, so
# the integration and Zitadel tiers (and every depguard rule scoped to them)
# would otherwise never be linted. The e2e tag selects the e2e-only control
# service (ADR-028 D6), which no untagged pass loads either.
go-lint: | go-mod-download
	@echo "==> go lint (golangci-lint --concurrency $(GO_PARALLEL))"
	golangci-lint run --concurrency $(GO_PARALLEL) ./...
	@echo "==> go lint --build-tags integration"
	golangci-lint run --concurrency $(GO_PARALLEL) --build-tags integration ./...
	@echo "==> go lint --build-tags integration,zitadel"
	golangci-lint run --concurrency $(GO_PARALLEL) --build-tags integration,zitadel ./...
	@echo "==> go lint --build-tags e2e"
	golangci-lint run --concurrency $(GO_PARALLEL) --build-tags e2e ./...

go-typecheck: | go-mod-download
	@echo "==> go build (-p $(GO_PARALLEL))"
	go build -p $(GO_PARALLEL) ./...
# Build-tagged test files are outside every other Go gate: `go build` does not
# compile test files at all, and both `go lint` and `go test` run untagged, so a
# file behind `integration` or `integration && zitadel` can stop compiling and
# every gate stays green. `go vet` does typecheck test files, so one vet per tag
# combination puts those tiers back under the build. The two combinations cover
# all three tag expressions in the tree: `integration` also selects
# `integration && !zitadel`, and `integration zitadel` also selects
# `integration && zitadel`.
	@echo "==> go vet -tags integration (-p $(GO_PARALLEL))"
	go vet -p $(GO_PARALLEL) -tags "integration" ./...
	@echo "==> go vet -tags 'integration zitadel' (-p $(GO_PARALLEL))"
	go vet -p $(GO_PARALLEL) -tags "integration zitadel" ./...
# The e2e tag builds the binaries the e2e stack runs, control service included
# (ADR-028 D6). The untagged `go build` above is the production build, which
# cannot compile an import of the control package.
	@echo "==> go build -tags e2e (-p $(GO_PARALLEL))"
	go build -p $(GO_PARALLEL) -tags e2e ./...
	@echo "==> go vet -tags e2e (-p $(GO_PARALLEL))"
	go vet -p $(GO_PARALLEL) -tags e2e ./...

# The second run is the e2e control service's own tests, which compile only
# under the e2e tag.
go-test: | go-mod-download
	@echo "==> go test (-p $(GO_PARALLEL))"
	go test -race -count=1 -p $(GO_PARALLEL) ./...
	@echo "==> go test -tags e2e (e2e control service)"
	go test -tags e2e -race -count=1 -p $(GO_PARALLEL) ./src/exchange/internal/e2econtrol/...

jscpd:
	@echo "==> jscpd"
	@jscpd --config .jscpd.json

sqlc:
	sqlc generate

# sqlc is included in the CI toolchain and make install-tools. Compare without
# overwriting files: stale generated code must fail the quality gate.
sqlc-check:
	sqlc diff

db-up:
	docker compose up -d postgres redis

db-down:
	docker compose down

db-logs:
	docker compose logs -f postgres

# ── Local Zitadel: the upstream OIDC provider, a developer and a publisher instance ──
# These live behind the `zitadel` compose profile, so `make db-down` does NOT stop
# them — use `make zitadel-down`.
COMPOSE_ZITADEL = docker compose --profile zitadel

# Export GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET first to configure Google
# sign-in; without them the bootstrap skips it and the alice login still works.
zitadel-up:
	$(COMPOSE_ZITADEL) up -d zitadel-db mailpit zitadel zitadel-login
	@echo "==> provisioning the developer instance (project, login policy, OIDC client, demo users, google IdP)"
	$(COMPOSE_ZITADEL) run --rm --no-deps zitadel-init
	@echo "==> creating and provisioning the publisher instance at 127.0.0.1 (console client, admin, alice)"
	$(COMPOSE_ZITADEL) run --rm --no-deps zitadel-system-key
	$(COMPOSE_ZITADEL) run --rm --no-deps zitadel-publishers-init
	@echo ""
	@echo "Both instances' mail (sign-up codes, password resets): http://127.0.0.1:58025"
	@echo "Wire a shell for the identity service and the console with:"
	@echo "  eval \"\$$(make zitadel-creds)\""

# Re-print an earlier run's exports for a new shell: the identity service's
# client from the developer instance, the console's from the publisher one. The
# format is not restated here — --emit-exports is the same script that wrote
# them, and it touches neither Zitadel nor a token. eval-safe.
zitadel-creds:
	@$(COMPOSE_ZITADEL) run --rm --no-deps zitadel-init --emit-exports
	@$(COMPOSE_ZITADEL) run --rm --no-deps zitadel-publishers-init --emit-exports

zitadel-logs:
	$(COMPOSE_ZITADEL) logs -f zitadel

# Compose's default project name is the directory basename unless overridden.
COMPOSE_PROJECT ?= $(if $(COMPOSE_PROJECT_NAME),$(COMPOSE_PROJECT_NAME),$(notdir $(CURDIR)))

# Volumes go too: the issuer is baked at init, so reconfiguring is a wipe. Only the
# zitadel volumes — `down -v` would take pgdata/redisdata/tbdata with it. The label
# filter keeps a sibling compose project out of the blast radius.
zitadel-down:
	$(COMPOSE_ZITADEL) rm -sfv zitadel-login zitadel zitadel-db mailpit zitadel-init zitadel-system-key zitadel-publishers-init
	@vols=$$(docker volume ls -q \
		--filter label=com.docker.compose.project=$(COMPOSE_PROJECT) \
		--filter name=zitadel); \
	if [ -n "$$vols" ]; then docker volume rm $$vols; else echo "no zitadel volumes to remove"; fi

# Generate gitignored local signing keys for running the Exchange via `go run`.
# The binary fails closed when no key is configured (src/exchange/cmd/server/keys.go),
# so dev provisions keys via files the same way production and e2e do.
dev-keys:
	@mkdir -p deploy/dev-keys
	@openssl genpkey -algorithm ED25519 -out deploy/dev-keys/ed25519-private.pem
	@openssl genrsa -out deploy/dev-keys/rsa-private.pem 2048
	@echo "Wrote deploy/dev-keys/ed25519-private.pem + rsa-private.pem (gitignored)."
	@echo "Export before running the exchange via go run:"
	@echo "  export FORA_ED25519_PRIVATE_PEM_FILE=$(CURDIR)/deploy/dev-keys/ed25519-private.pem"
	@echo "  export FORA_RSA_PRIVATE_PEM_FILE=$(CURDIR)/deploy/dev-keys/rsa-private.pem"

# Per-project isolated dev stack (random ports, named compose project).
# Call pattern for scripts and parallel executors:
#   eval "$$(PROJECT=my-executor make devstack-up)"
#   ... work ...
#   PROJECT=my-executor make devstack-down
devstack-up:
	@if [ -z "$(PROJECT)" ]; then echo "PROJECT=<name> required" >&2; exit 2; fi
	@scripts/devstack.sh up $(PROJECT)

devstack-down:
	@if [ -z "$(PROJECT)" ]; then echo "PROJECT=<name> required" >&2; exit 2; fi
	@scripts/devstack.sh down $(PROJECT)

devstack-status:
	@if [ -z "$(PROJECT)" ]; then echo "PROJECT=<name> required" >&2; exit 2; fi
	@scripts/devstack.sh status $(PROJECT)

# INTEGRATION_PARALLEL bounds how many package test binaries run concurrently.
# Each integration package spins its own testcontainers (Postgres/Redis), so the
# default `-p` (= GOMAXPROCS, often 14) starts a burst of dozens of containers at
# once and saturates the Docker daemon on a busy/shared host — inspect calls then
# time out ("context deadline exceeded" / mapped-port failures). 4 keeps the
# daemon responsive while still overlapping the long-pole packages. Override on a
# dedicated CI Docker for more speed: `make test-integration INTEGRATION_PARALLEL=8`.
INTEGRATION_PARALLEL ?= 4

# -timeout 25m: the per-package go-test deadline (default 10m). The
# exchange/internal/transport integration package alone runs ~10-12m under CI
# docker-in-docker + -p contention (≈190s locally serial, but dind is ~3x slower
# and concurrent containers compete), tripping the 10m default. 25m is ample
# headroom for the slowest package without masking a genuine hang.
#
# TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT: Ryuk, the testcontainers reaper one
# `go test` run shares across its packages, removes itself 10s (the default)
# after its last client disconnects. A gap that long between packages happens
# on a loaded box, and the next package to start then finds the reaper being
# removed and fails with "unexpected container status removing". Five minutes
# outlasts such a gap; the reaper still cleans up once the run has ended.
RYUK_RECONNECTION_TIMEOUT ?= 5m

test-integration:
	@echo "==> go test -tags integration (-p $(INTEGRATION_PARALLEL))"
	TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT=$(RYUK_RECONNECTION_TIMEOUT) \
		go test -tags integration -race -count=1 -timeout 25m -p $(INTEGRATION_PARALLEL) ./...

# Real-Zitadel tier (developer sign-up). Brings up a real Zitadel
# v3.4.9 + its Postgres as a per-package testcontainer and drives the sign-up
# flow through the real upstream-OIDC client (real discovery, headless login,
# JWKS ID-token verification) — the mock is kept only for the upstream-failure
# case a real Zitadel cannot stage. Deliberately OUT of test-integration
# (pre-push): the ~30-45s Zitadel boot + Login-V1 HTML scrape must not gate
# every push; run it nightly / on demand. Needs Docker; it holds no fixed host
# port (Zitadel is published through an ephemeral one), so it runs alongside
# anything else on the daemon.
test-zitadel:
	@echo "==> go test -tags 'integration zitadel' (real Zitadel)"
	go test -tags "integration zitadel" -race -count=1 -timeout 25m \
		-run 'TestAuthFlow' ./src/identity/internal/transport/

# The e2e stack's compose project, which also names its network. Every stack is
# self-contained (no host port, a network and subnet of its own; see
# scripts/e2e-stack.sh), so stacks under different names never meet.
#
# `make e2e-up` / `e2e-down` / `e2e-logs` on their own default to one stable
# name per checkout, so a developer can bring a stack up and take it down again
# by hand. `make test-e2e` runs a fresh stack under a name of its own, unique to
# the invocation, so two runs from one checkout cannot collide either. Naming a
# project (E2E_PROJECT=...) makes test-e2e use that name instead, which is how
# CI keeps the name its failure dump needs.
ifeq ($(origin E2E_PROJECT),undefined)
E2E_PROJECT := $(shell printf '%s' '$(COMPOSE_PROJECT)-e2e' | tr 'A-Z' 'a-z' | tr -c 'a-z0-9_-' '-')
E2E_RUN_PROJECT := $(E2E_PROJECT)-$(shell od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
else
E2E_RUN_PROJECT := $(E2E_PROJECT)
endif

# Compose invocation for the e2e stack, through the script that hands compose the
# stack's network. CI appends a registry-cache overlay
# (`-f docker-compose.e2e.cache.yml`) via E2E_COMPOSE_EXTRA so the image builds
# push/pull a BuildKit layer cache in Artifact Registry. Empty by default, so a
# plain local `make e2e-up` stays auth-free (no registry contact).
#
# E2E_HOST_PORTS=1 adds docker-compose.e2e.ports.yml, which publishes the
# services on loopback host ports for debugging from the host. No gate sets it.
E2E_HOST_PORTS_FILE = $(if $(filter 1,$(E2E_HOST_PORTS)),-f docker-compose.e2e.ports.yml)
COMPOSE_E2E = scripts/e2e-stack.sh compose $(E2E_PROJECT) $(E2E_HOST_PORTS_FILE) $(E2E_COMPOSE_EXTRA)

# The commands that reach a stack a run leaves up: its logs, and its removal
# (containers, network, volumes and the images built for it). $(1) is the
# stack's project name.
E2E_KEPT_HINT = echo "    logs:   make e2e-logs E2E_PROJECT=$(1)"; echo "    remove: make e2e-down E2E_PROJECT=$(1)"

# Bring a fresh stack up under its own project name, run the pytest E2E inside
# its network via the dedicated `runner` service, tear down. A run that fails at
# any step leaves what it started up for inspection, and prints the commands
# that read its logs and remove it; a CI job that fails dumps the service logs
# from that stack. Set FORA_E2E_KEEP_UP=1 to keep the stack after a passing run
# too; the same commands are printed.
#
# E2E_PASSES runs the runner that many times against the one stack before it
# comes down. CI sets 2: ADR-028 D7 requires the suite to pass a second time on a
# stack it has already run against, so state one pass leaves behind (balances,
# reporting obligations, replay keys) cannot decide what the next pass sees.
# Every pass runs even when an earlier one fails, and the target fails when any
# pass does.
E2E_PASSES ?= 1

test-e2e:
	@$(MAKE) --no-print-directory e2e-run E2E_PROJECT=$(E2E_RUN_PROJECT) || { rc=$$?; \
		echo "==> e2e run failed; anything it started is still up for inspection (project $(E2E_RUN_PROJECT)):"; \
		$(call E2E_KEPT_HINT,$(E2E_RUN_PROJECT)); exit $$rc; }

# Tiered e2e testing (reduces system load)
# Usage: make test-e2e-fast (no stack), test-e2e-light, test-e2e-heavy
.PHONY: test-e2e-fast test-e2e-light test-e2e-heavy test-e2e-tiered

test-e2e-fast:
	@echo "==> Fast e2e tests (no Docker stack needed)"
	@cd tests/e2e && uv run pytest \
		harness/test_wba_vectors.py \
		harness/test_wba_directories.py \
		harness/test_guards_*.py \
		-v --tb=short

test-e2e-light:
	@echo "==> Light e2e tests (needs stack, minimal load)"
	@cd tests/e2e && FORA_E2E_REUSE_STACK=1 uv run pytest \
		harness/test_obligation_report.py \
		harness/test_mcp_endpoint.py \
		-v --tb=short -n 2

test-e2e-heavy:
	@echo "==> Heavy e2e tests (full integration, single-threaded)"
	@cd tests/e2e && FORA_E2E_REUSE_STACK=1 uv run pytest \
		harness/obligations/ \
		-v --tb=short -n 1

test-e2e-tiered:
	@echo "==> Running ALL e2e tests in tiers (fast → light → heavy)"
	@$(MAKE) test-e2e-fast
	@$(MAKE) --no-print-directory e2e-up
	@$(MAKE) test-e2e-light
	@$(MAKE) test-e2e-heavy
	@echo "==> All tiers passed! Cleaning up..."
	@$(MAKE) --no-print-directory e2e-down

e2e-run: e2e-up
	@echo "==> full-stack E2E (runner, project $(E2E_PROJECT), $(E2E_PASSES) pass(es) on one stack)"
	$(COMPOSE_E2E) --profile test build runner
	# --no-deps: the stack is already up from e2e-up. Without this flag compose
	# re-reconciles every dependency and re-runs non-idempotent init one-shots
	# against the already-bootstrapped stack.
	@rc=0; for pass in $$(seq 1 $(E2E_PASSES)); do \
		echo "==> e2e pass $$pass of $(E2E_PASSES)"; \
		$(COMPOSE_E2E) --profile test run --rm --no-deps runner || rc=1; \
	done; exit $$rc
	@if [ -z "$${FORA_E2E_KEEP_UP}" ]; then \
		$(MAKE) --no-print-directory e2e-down E2E_PROJECT=$(E2E_PROJECT); \
	else \
		echo "==> stack left up (FORA_E2E_KEEP_UP), project $(E2E_PROJECT):"; \
		$(call E2E_KEPT_HINT,$(E2E_PROJECT)); \
	fi

# Generate every signing key the E2E stack needs BEFORE `docker compose up`, so
# a fresh checkout needs zero manual keygen (no private material is
# committed). Mints:
#   * the e2e identities (contributor/agents/broker-relay/test-signer/…) —
#     private fixtures under tests/e2e/harness/fixtures/
#     (scripts/gen-e2e-keys.sh). There is no shared key registry file: each
#     identity's pubkey is served only by that identity's own well-known host
#     in docker-compose.e2e.yml. The broker-relay private fixture this mints is
#     the one the broker container mounts (BROKER_RELAY_KEY_FILE); the broker
#     publishes its pubkey in its own WBA directory at boot.
#   * the demo subscription-publisher key the publisher-jwks service signs
#     /.well-known/fora.json with (scripts/gen-examplenews-publisher-key.sh).
#   * each publisher edge's pre-signed key directory, appended to its
#     deploy/edge-keys/<service>.env as WBA_SIGNED_DIRECTORY_JSON
#     (scripts/gen-e2e-signed-directories.sh). The signing tool runs on the edge
#     package's pinned SDK, so src/edge is installed first when it is not.
# Idempotent: safe to re-run before every `make e2e-up`.
e2e-keys:
	@echo "==> generating e2e signing keys (gitignored private material)"
	@scripts/gen-e2e-keys.sh
	@scripts/gen-examplenews-publisher-key.sh
	@test -d src/edge/node_modules || npm ci --prefix src/edge
	@scripts/gen-e2e-signed-directories.sh

# --wait blocks until every service is healthy; --wait-timeout caps the
# wait at E2E_WAIT_TIMEOUT seconds, 120 by default (Wave 0 acceptance bound).
# CI passes a longer value: its runner host is shared with other jobs, and
# there the daemon has taken minutes to start a container of a stack that was
# healthy. e2e-keys runs first so a clean checkout (no committed keys)
# comes up with zero manual steps.
#
# buildx-check runs before either: without the plugin compose builds these 19
# images on the legacy builder and says so only in a warning nobody reads, which
# turns a ~75s stack-up into ~39 minutes. Failing here costs a second and names
# the cause.
#
# The network goes first: the stack's subnet is allocated per stack, and the
# compose file reads its prefix (scripts/e2e-stack.sh).
E2E_WAIT_TIMEOUT ?= 120
e2e-up: buildx-check e2e-keys
	scripts/e2e-stack.sh network-up $(E2E_PROJECT)
	$(COMPOSE_E2E) up -d --build --wait --wait-timeout $(E2E_WAIT_TIMEOUT)

buildx-check:
	@scripts/check-buildx.sh

# Phase-2a demo-catalog proof (items 2-5): register the three demo tenants,
# ingest all three demo feeds via the production cmd/fora-ingest binary,
# DiscoverResources for a demo resource, and fetch the canary article through an
# edge. Re-runnable against an already-up stack brought up with host ports
# (`make e2e-up E2E_HOST_PORTS=1` first): the script runs on the host. It is
# handed E2E_PROJECT, so it reads the ports of the stack `make e2e-up` brought up
# under that name, whatever other stacks run on the daemon.
e2e-demo:
	E2E_PROJECT=$(E2E_PROJECT) uv run --project tests/e2e python tests/e2e/demo_proof.py

# Render the cryptographic evidence chain for one executed transaction. Reads
# the Exchange admin plane, the Broker's selection audit, and the edge delivery
# logs, then re-verifies both Ed25519 signatures offline.
#
# The Exchange admin plane and the Broker database are reachable from inside a
# deployment only, so both are normally addressed through a tunnel. Every
# endpoint and credential comes from the environment; nothing is defaulted to a
# live host here. Run `go run ./src/broker/cmd/fora-ledger -h` for the full set.
#
#   FORA_ADMIN_URL   Exchange admin base URL (required)
#   BROKER_DB_URL    Broker Postgres DSN; unset skips the routing leg
#   FORA_EDGE_LOG_GROUP  edge CloudWatch log group; unset skips the delivery leg
#
#   make ledger TX=<transaction-id>                     text to the terminal
#   make ledger TX=<transaction-id> FORMAT=html         self-contained report,
#                                                       opened in the browser
#   make ledger TX=<tx> FORMAT=html OUTPUT=report.html  choose the file
#   make ledger TX=<tx> FORMAT=html OPEN=0              save, do not open
ledger: go-mod-download
	@test -n "$(TX)" || { echo "usage: make ledger TX=<transaction-id> [FORMAT=html] [OUTPUT=file] [OPEN=0]" >&2; exit 2; }
	@go run ./src/broker/cmd/fora-ledger -tx "$(TX)" -format "$(or $(FORMAT),text)" \
		$(if $(OUTPUT),-output "$(OUTPUT)") $(if $(filter 0,$(OPEN)),-open=false)

# Removes the stack: its containers, network and volumes, and the images built
# for it. Unless E2E_PROJECT names one, every `make test-e2e` run gets a project
# name of its own, so built images left behind would pile up under a new name
# each run, and each would keep its build's layers on disk. The next build still
# hits Docker's build cache once they are removed. Downloaded images (Postgres,
# Redis, Zitadel) stay. --profile test takes in the runner, which only that
# profile names.
e2e-down:
	$(COMPOSE_E2E) --profile test down -v --rmi local
	scripts/e2e-stack.sh network-down $(E2E_PROJECT)

# Prints the project name e2e-up/e2e-down use, for driving the same stack with
# scripts/e2e-stack.sh compose "$(make -s e2e-project)" ...
e2e-project:
	@echo $(E2E_PROJECT)

e2e-logs:
	$(COMPOSE_E2E) logs -f

# Bare-pytest collection check for the E2E harness — runs `uv run pytest
# --collect-only` from inside `tests/e2e/` so the e2e venv (and its
# declared deps (e.g. `biscuit-python`) resolve. Use this
# during the audit-and-flip workflow when you need to enumerate
# xfail/xpass without spinning the full compose stack.
# Forward extra args via E2E_PYTEST_ARGS, e.g.:
#   make test-e2e-collect E2E_PYTEST_ARGS='harness/obligations/'
test-e2e-collect:
	@cd tests/e2e && uv run pytest --collect-only $(E2E_PYTEST_ARGS)

# ───── AWS demo deploy helpers (retired ECS demo; kept for the tooling sweep) ─
aws-rotate-keys:
	@scripts/rotate-keys.sh generate

aws-publish-keys:
	@scripts/rotate-keys.sh publish

aws-push:
	@scripts/ecr-push.sh

aws-wire-secrets:
	@scripts/wire-secrets.sh

aws-roll-services:
	@for svc in exchange broker identity; do \
		aws ecs update-service --cluster fora-demo --service fora-demo-$$svc --force-new-deployment >/dev/null \
		  && echo "  rolled fora-demo-$$svc" ; \
	done

# Real-AWS acceptance suite (noop unless FORA_E2E_AWS=1).
test-e2e-aws:
	@echo "==> AWS acceptance pytest"
	cd tests/e2e && uv run --with-editable . pytest -q ../e2e-aws

# golangci-lint installs from its official prebuilt binary, not `go install`.
# Pinned for reproducibility; bump deliberately and re-run `make quality`.
GOLANGCI_LINT_VERSION ?= v2.12.2

# The secret scanner behind `published-secrets-check`. Pinned for the same reason
# the linters are, and one more: its rule set decides what the gate reports, and
# the reviewed exemptions in .gitleaks.toml were verified against this version.
# Bumping it means re-checking those exemptions, not just the version string.
GITLEAKS_VERSION ?= v8.18.2

install-tools:
	go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go install github.com/zricethezav/gitleaks/v8@$(GITLEAKS_VERSION)
	@if ! golangci-lint version 2>/dev/null | grep -q "$(GOLANGCI_LINT_VERSION:v%=%)"; then \
		echo "==> installing golangci-lint $(GOLANGCI_LINT_VERSION) (prebuilt binary)"; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/$(GOLANGCI_LINT_VERSION)/install.sh \
			| sh -s -- -b $$(go env GOPATH)/bin $(GOLANGCI_LINT_VERSION); \
	fi
	go install mvdan.cc/gofumpt@latest
	npm install -g @biomejs/biome jscpd@^3.5.10 wrangler
	@echo "Installed: sqlc, gitleaks, golangci-lint, gofumpt, biome, jscpd, wrangler"

# ───── Prebuilt CI image (deploy/ci/Dockerfile) ────────────────────────────
# Bakes the toolchain the GitLab before_scripts apt-install on every run. Built
# + pushed MANUALLY (changes rarely); CI jobs pull it (runner has read access).
# Auth once before pushing:
#   gcloud auth login
#   gcloud auth configure-docker europe-west2-docker.pkg.dev
CI_IMAGE     ?= europe-west2-docker.pkg.dev/pi-infra/prebid-agentic-content-access/ci
CI_IMAGE_TAG ?= $(shell date +%Y%m%d)

.PHONY: ci-image ci-image-push
# --platform linux/amd64: the GitLab runners are amd64; an image built natively
# on an Apple Silicon machine ships arm64 binaries and every job dies with
# "exec format error" at container start.
ci-image:
	docker build --platform linux/amd64 \
		--build-arg GOLANGCI_LINT_VERSION=$(GOLANGCI_LINT_VERSION) \
		-t $(CI_IMAGE):$(CI_IMAGE_TAG) -t $(CI_IMAGE):latest deploy/ci

ci-image-push: ci-image
	docker push $(CI_IMAGE):$(CI_IMAGE_TAG)
	docker push $(CI_IMAGE):latest
	@echo "Pushed $(CI_IMAGE):$(CI_IMAGE_TAG) and :latest"

# Per-subproject dispatch for Edge (e.g. make edge-dev, make edge-run)
edge-%:
	$(MAKE) -C src/edge $*
