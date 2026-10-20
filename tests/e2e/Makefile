.PHONY: deps fmt fmt-check lint typecheck deadcode test-fast

# Lint and format targets for the full-stack E2E harness (`tests/e2e/`) and
# the sibling real-AWS acceptance tree (`tests/e2e-aws/`). The two trees
# share one uv venv: tests/e2e/pyproject.toml carries the ruff config and
# the dev dependency, and `tests/e2e-aws/` is a single test file under the
# same lint scope (mirrors `make test-e2e-aws` at root, which invokes
# pytest with `--with-editable . pytest -q ../e2e-aws`).
#
# Root delegation: the project Makefile's `fmt` and `lint` targets descend
# here, closing the lint-coverage gap the build-tag pocket audit surfaced.
#
# STACK-DRIVING test execution lives in docker-compose (`make test-e2e` at root)
# and is intentionally absent from this Makefile — the runner container resolves
# its own venv via tests/e2e/Dockerfile. The `test-fast` target below is the
# exception: it runs ONLY the harness tests that need no stack at all.

deps:
	uv sync

fmt:
	uv run ruff format . ../e2e-aws/

fmt-check:
	uv run ruff format --check . ../e2e-aws/

# The second command is the harness bans of ADR-028 D8 (ruff-harness.toml): what a
# test module may not call. It walks this directory, which is what applies the
# file's test-module scope, and runs with --ignore-noqa, so no file can exempt
# itself. harness/test_harness_bans.py runs each ban on a sample violation and
# checks that this recipe still carries these arguments.
lint:
	uv run ruff check . ../e2e-aws/
	uv run ruff check --config ruff-harness.toml --ignore-noqa .

# The root `typecheck` target descends here. When the repo's only mypy
# invocation lived under src/mcp/, deleting that tree took the whole Python
# type gate with it, so `make quality` reported green with no Python type
# checking at all while the harness kept growing. This restores the gate.
#
# Not --strict: the harness is untyped in places and interacts with untyped
# third-party stubs, so strict mode would be a wall of errors rather than a
# gate anyone keeps green. It checks what it can and is wired into `quality`,
# which is the property that was actually missing. This narrower gate is
# deliberate: the alternative on offer was no Python type gate at all.
#
# Scope MATCHES `lint` above deliberately. A typecheck narrower than its sibling
# lint gate leaves files that look covered and are not: the standalone drivers
# (`demo_proof.py`, `register_staging_agent.py`, `smoke_staging.py`) and
# `../e2e-aws/` are all linted, and the drivers construct the harness's own
# types — exactly the arity/shape drift a type checker catches and a linter
# does not.
#
# The second command checks that mypy itself refuses a string passed to
# obligation(), using two probe modules that are deliberately outside the first
# command's scope: the bad probe exists to produce errors. typecheck_probes/check.py
# requires exactly the errors the bad probe marks and nothing else. It runs here
# rather than in test-fast because mypy already runs in this target, so the cache
# is warm. The checker itself is in the first command's scope.
#
# vulture_whitelist.py is checked too: it names each symbol it excuses, so an entry
# whose symbol was removed or renamed fails here instead of excusing nothing.
typecheck:
	uv run mypy harness/ demo_proof.py register_staging_agent.py smoke_staging.py ../e2e-aws/ \
		typecheck_probes/check.py vulture_whitelist.py
	uv run python typecheck_probes/check.py

# ADR-028 D8 H8: no dead harness code. vulture reads its scope, confidence and
# exclusions from [tool.vulture] in pyproject.toml, which says why each is there.
# harness/test_harness_deadcode.py runs the same configuration on sample dead code
# and checks that this recipe still runs vulture with no arguments of its own.
deadcode:
	uv run vulture

# The Python leg of the root `test-fast` (and so of `make quality`), which testing
# doctrine point 5 requires on every commit. It went missing when src/mcp was
# retired: `fmt`, `lint`, and `typecheck` each got a tests/e2e replacement for the
# deleted `-C src/mcp` call and this one did not, leaving the per-commit tier with
# no Python test execution at all.
#
# The file list is EXPLICIT, not a glob. Every other harness module needs the
# compose stack, and a glob would quietly pull the whole e2e suite into the
# per-commit tier the first time someone added a file. These are pure
# vector/primitive/source-scan tests — no stack, no Docker, ~0.1s — and they are
# exactly the kind whose breakage otherwise surfaces as a wall of unexplained
# signature rejections much later. Add a new file here only if it needs nothing
# brought up.
#
# The published-* and image-version guard modules earn their place for a
# different reason than the rest: they watch gates that `make quality` itself
# relies on. Running them in a later tier would mean a green quality run could
# keep asserting a property no longer being checked.
#
# The publish tool has three guard suites. Two were one module until their
# combined length passed the cap on a test file, and the third covers the paths it
# holds back. All three belong in this tier for the same reason, so a split that
# moved any one of them out would quietly remove part of the coverage this tier
# gives the publish tool.
#
# The workflow-script guard belongs here for a third reason: it is the only test
# that reads the real publishing workflow, and nothing else in this repository
# parses that file at all — there is no actionlint, no yamllint and no yq. Run it
# in a later tier and a green `make quality` would say nothing about the shell
# that publishes the images.
#
# The lambda-edge invoker guard belongs here for a fourth reason: the files it
# reads — the emulator's Dockerfile and its container probe — are not copied
# into the e2e runner image, so on that tier it can only skip. This is the one
# tier where it runs at all, and what it guards is a property whose breakage
# surfaces as an intermittent failure somewhere else entirely.
#
# The release-tool guard belongs here for the image-version reason: it drives the
# tool that performs the release edit, and that tool is what the image-version
# gate is protecting the tree from. The two share one fixture, so a change to
# either has to run both, and both run in the tier that gates every commit.
#
# The ver-wire guard is a pure AST scan of this tree: no stack, no Docker, well
# under a second, which is exactly the criterion above. It belongs in the
# per-commit tier because what it catches is invisible at run time — no service
# validates `ver` on the way in, and the two that read it echo it straight back
# to the caller, so a body that ships an empty or hand-written version passes the
# whole e2e suite. A guard whose subject cannot fail a test has to fail the commit.
# Note its three pure-scan siblings (single_offer_wire, denial_reason_key,
# no_redundant_or) meet the same criterion and are absent; adding them is a
# separate change, not a reason to leave this one out.
#
# The Web Bot Auth oracle suite belongs here because the conformance tests take
# its verdicts on trust. It pins the Python oracle to the draft's own test
# vectors and fires each of its named checks on the defect that check names: pure
# parsing and signatures, no stack, under a second. If the oracle drifts, every
# conformance verdict it gives is suspect, so the drift has to fail the commit.
#
# The self-contained-stack guard belongs here because what it catches passes
# every e2e run. It reads the e2e compose files and refuses a published host
# port, a written-out address, a fixed address outside the upper half of the
# stack's own /24, and a checkout folder mounted with write access. A stack that
# breaks one of these still comes up and passes when it runs alone. It fails only
# when a second stack shares the Docker daemon, and no gate starts two. The guard
# reads files and brings nothing up.
#
# The buildx guard belongs here for the lambda-edge invoker's reason, in its
# strongest form: the script it reads, scripts/check-buildx.sh, is not copied
# into the e2e runner image, so on that tier its skip mark fires and the module
# does not run. This is the one tier where it can run at all, and until it was
# listed here it ran on no tier — the gate could be inverted or emptied with
# every tier still passing. It meets the criterion above: a docker stub on PATH
# and one subprocess, no stack brought up.
#
# The identity-list guard belongs here for the lambda-edge invoker's reason: it
# reads scripts/gen-e2e-keys.sh, which the e2e runner image does not carry, so on
# that tier it skips. It is also what checks that every per-test agent's host
# resolves to the agent-directory service, a fault that would otherwise surface
# only as signature refusals in the money-path suites. It reads two files and
# needs nothing brought up.
#
# The publisher-sites guard belongs here for the identity-list guard's reasons:
# it checks that every per-test publisher site resolves to the sites service and
# that the stack's importer job matches the production compose template, which
# the e2e runner image does not carry, so on that tier the template checks skip.
# A broken zone shows up there only as "no CDN detected" in the publisher suites.
# It reads two files and needs nothing brought up.
#
# The two obligation-registry modules test the registry machinery itself, not an
# obligation: the parser, the enum and decorator, the quote check and the
# end-of-session report, each in temporary trees and pytester subprocesses. No
# stack, no Docker. The quote check over the real registry needs the go binary
# to find the pinned protocol module, which only the host has, so this is the
# one tier where that check runs.
#
# The harness-seams module drives the send seam's own checks against a loopback
# stub: the strict shape check refusing an unknown field, the declared-negative
# verdict check, and the lint ban that keeps ClientConfig in one module. Those
# checks never fire against a healthy stack, so this is where each is seen to
# fail. No stack, no Docker.
#
# The harness-bans module runs ruff with ruff-harness.toml on one sample
# violation per ban, for the same reason: a misconfigured ban passes silently.
# It needs ruff, which only the host has, so this is the one tier it runs in.
#
# The harness-deadcode module does the same for vulture (H8): the real
# [tool.vulture] table on sample dead code, and the recipes that run it. It needs
# vulture, which only the host has.
#
# The fixture-git-env guard belongs here because its subject is this tier. The
# pre-push hook runs these suites with a GIT_DIR naming the developer's
# repository, and the guard is what shows the suites above keep their git
# commands inside their own scratch repositories under that environment. It
# runs a sample of them in a child pytest, a few seconds per hook shape.
#
# The e2e-images guard belongs here for the lambda-edge invoker's reason: the
# service Dockerfiles, the publishing workflow and the push scripts it reads are
# not in the e2e runner image, so on that tier it skips. It is what keeps the
# e2e-only control service and the operator CLI out of the production images.
# It reads files and needs nothing brought up.
test-fast:
	uv run pytest -q harness/test_acceptance.py \
		harness/test_httpsig_signer.py \
		harness/test_guards_exchange_identity.py \
		harness/test_guards_self_contained_stack.py \
		harness/test_recipient.py \
		harness/test_guards_stale_proto.py \
		harness/test_guards_published_refs.py harness/test_guards_published_secrets.py \
		harness/test_guards_publish_gates.py harness/test_guards_publish_worktree.py \
		harness/test_guards_publish_held_back.py \
		harness/test_guards_image_version.py harness/test_guards_workflow_scripts.py \
		harness/test_guards_lambda_edge_invoker.py harness/test_guards_buildx.py \
		harness/test_guards_release_version.py harness/test_guards_deployment_docs.py \
		harness/test_guards_harness_collection.py harness/test_guards_ver_wire.py \
		harness/test_guards_sdk_pin.py harness/test_guards_sdk_pin_prerelease.py \
		harness/test_guards_gate_args.py harness/test_guards_identity_lists.py \
		harness/test_guards_publisher_sites.py harness/test_guards_e2e_images.py \
		harness/test_guards_fixture_git_env.py \
		harness/test_obligation_registry.py harness/test_obligation_report_plugin.py \
		harness/test_harness_seams.py harness/test_harness_bans.py \
		harness/test_harness_deadcode.py harness/test_wba_oracle.py
