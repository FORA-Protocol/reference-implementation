.PHONY: deps fmt fmt-check lint typecheck

# Lint, format and typecheck for the WBA oracle sidecar. The root `fmt`,
# `fmt-check`, `lint` and `typecheck` targets descend here, so this TypeScript
# is held to the edge worker's biome rules and tsc strictness flags. It has no test
# target of its own: what it does is exercised through the e2e harness, by
# tests/e2e/harness/test_wba_oracle_ts.py against the running container.

deps:
	npm ci

fmt:
	npm run fmt

fmt-check:
	npm run fmt:check

lint:
	npm run lint

typecheck:
	npm run typecheck
