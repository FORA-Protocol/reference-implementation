# ADR-028 — E2E Obligation Harness: Registry, SDK-Driven Requests, Universal Shape Check

**Status:** Proposed (2026-10-02)

**Relates to:** ADR-008 (D1, time is a port; D3, strict xfail markers; D5, declared test isolation, which this ADR extends to every e2e test).

---

## Context

The e2e suite under `tests/e2e/` cannot yet show that FORA behaves as the protocol and the requirements say, nor that a failing test means a product defect rather than a harness defect.

- **No traceability from requirements to tests.** A few dozen tests quote obligation bullets, with nothing tying a bullet to its test. The normative statements in `fora.proto`, the protocol documentation, these ADRs and the acceptance criteria of shipped work are not systematically turned into tests.
- **The harness builds and reads the wire by hand.**
  - Request bodies are hand-written dictionaries, some carrying fields the pinned protocol does not define.
  - Responses are read as raw dictionaries, and refusals are matched on message text.
  - A request is signed and sent in several unrelated ways.
  - Negative tests start from bodies that are invalid for reasons other than the one under test, so they pass whether or not the check they target works.
- **Tests observe state by reading the database**, and some pass only on a freshly started stack.

The protocol already provides what a precise harness needs. The Python SDK sends every protocol call, validates responses into the generated models, verifies offer signatures, and raises one typed error carrying the Connect code and the decoded `ErrorDetail`. The protocol's build generates protovalidate-aware JSON Schemas from `fora.proto`.

## Decision

### D1 — Obligations are written once; links and status are derived

Each fact about an obligation is written in exactly one place. Everything else is derived from it, so no two records can disagree and nothing has to keep them in agreement.

- **The obligation itself lives in the registry.** `tests/e2e/obligations/<area>.yaml` holds one entry per obligation:
  - `id` (`AREA-NNN`, assigned once, never renumbered);
  - `title` and `level` (`MUST`, `SHOULD`, `MAY`, `DECISION`);
  - `actor` and `surface` (D2);
  - `source`: `cite` and a verbatim `quote`;
  - `positive` and `negative`, each negative naming the expected code, the typed reason and the side effect that must not occur. A negative is a refusal with a typed error response: a Connect code or HTTP status with its typed reason, or a typed per-item denial or absence reason in the body. An answer that refuses nothing but must leave something out is not a negative: that check is a "must not" in `positive`, and an entry with no refusal lists `negative: []`;
  - optionally `question` (an open clarification) and `retired: true`.

  The registry holds no list of tests and no status.
- **The link between a test and an obligation lives only on the test.** The registry loads into an `Obligation` enum, one member per ID, and a test declares `@obligation(Obligation.RELAY_004)`. A mistyped ID fails at import. A raw string cannot be passed, because the decorator takes enum members and type checking enforces it.
- **Status is computed from the tests, never stored.** At the end of each session a report derives each obligation's state from the tests that claim it:
  - *open*, when `question` is set;
  - *untested*, when no test claims it;
  - *skipped*, when every claiming test skipped at run time; the report gives each skip's reason;
  - *xfail*, when every claiming test that did not skip is a strict xfail (ADR-008 D3);
  - *partial*, when some are and some are not;
  - *tested*, when none is, except that an entry listing `negative` cases stays *partial* until a claiming test declares that it drives one, with `@obligation(..., negative=True)`.

  The report records state; it never fails a run.
- **Quotes are checked against their sources.** A host-side check verifies each `quote` against the cited file, in the pinned protocol module or this repository. Citations name the pinned protocol module, published source paths or these ADRs, so a public reader can open them.

### D2 — Each obligation is tested at the surface of its actor

| Actor | Surface | Driven by |
|---|---|---|
| Broker, Exchange, publisher edge | `sdk` | The protocol SDK's client verbs, as a self-custody agent. This is that agent's production path |
| Identity service: key custody, MCP tools, signing as the agent, delivery fetch | `mcp` | MCP `tools/call` against the identity service, as a custodial agent |
| Publisher console, and the Exchange routes and the catalog importer a publisher reaches only through it | `console` | The console's pages and wizard calls, as a signed-in publisher with a login of the test's own |
| A plain JSON API the SDK has no client for: the onboarding preview | `http` | One harness module per API, which sends each request and reads every answer through models that refuse a field the API's own document does not name |

- **A protocol rule is proven once, at the `sdk` surface.**
- **A thin `mcp` layer proves the custodial wiring:** one journey per `fora_*` tool, plus that tool's failure modes.
- **Rules where conformance is critical** are also checked by independent third-party implementations, so a defect shared by the SDK and the services cannot hide.

### D3 — One send seam; every response passes the universal shape check first

- **Every call goes through one seam.** An `sdk`-surface call is an SDK client verb; an `mcp`-surface call is an MCP tool call.
- **Inside the seam, before any other assertion, every response is checked against a published contract:**
  - **SDK calls:** the strict JSON Schema of the response message, error responses included. The protocol publishes these as release artifacts, generated with the protovalidate-aware generator, so they carry the `buf.validate` constraints, with a strict variant that refuses unknown fields.
  - **MCP calls:** the tool result's `structuredContent`, against the tool's declared `outputSchema`.
- **The check is not optional.** A test cannot reach a response without it.
- **Request bodies built outside a verb** come from one factory per protocol message over the generated models, serialized by the SDK's wire serializer. Hand-written protocol dictionaries are not allowed.

### D4 — Responses and refusals are read through the SDK's types

- **Assertions read the SDK's typed return values.**
- **A refusal is asserted only as the SDK's typed error:** the Connect code and the decoded typed reason. Message text is never asserted.

### D5 — A negative is a mutation of the request the SDK would send

- **How a negative is built.** The test lets the SDK build, stamp and validate the request, then patches it in the SDK's pre-signing hook. The SDK signs the patched bytes and decodes the reply as usual.
- **Declaration.** Each mutation is declared at the call site with `malformed(kind, why)`. Before sending, the seam runs the strict schema and requires its verdict to agree with the declaration: a request declared malformed must fail the schema, and an undeclared one must pass it.
- **Control request.** The same test sends the unpatched control request, so the mutation is the only difference between the accepted call and the refused one.

### D6 — E2E-only control surfaces exist only under the `e2e` build tag

- **Existing admin RPCs are used where they exist.** For example, the reporting window is set per tenant through `fora.admin.v1.SetReportingPolicy` (`window_seconds`).
- **State with no admin RPC is set and observed through an e2e-only control service** in each Go service, compiled only under the `e2e` build tag:
  - the tenant's transaction log;
  - the tenant relay policy;
  - per-tenant offer and delivery-URL lifetimes.
- **The surface cannot reach a production binary.**
  - Every file of the control package carries `//go:build e2e`, and only `e2e`-tagged wiring imports it.
  - Production images are built without the tag. A production build that imports the package fails to compile, because the build constraints exclude all of its files.
  - The `make quality` build runs without the tag, so a misplaced import breaks the gate. No custom guard is needed.
- **There is no per-service test clock** (ADR-008 D1 keeps time a port for integration tests). Shifting one service's clock in a multi-service stack breaks the signature windows between services. Time-dependent behaviour is tested by shortening the window and polling until it passes.

### D7 — Every test owns its identities

- **Fresh identities.** A `fresh_agent()` fixture mints a key, a WBA directory entry and, optionally, an Exchange account per test, through the SDK's identity helpers.
- **Fixed identities** are allowed only in fixtures that need a pre-registered party.
- **Every e2e test declares its isolation contract**, extending ADR-008 D5 beyond the obligation tests.
- **The suite must pass twice in a row against one stack.**

### D8 — Harness standards, enforced by structure first, standard linters second

A rule is enforced, in order of preference:
1. by making the violation impossible to express;
2. by a standard linter's configuration;
3. by a custom check, only where neither can state the rule.

Each linter ban carries a companion test that runs the real linter on a sample violation and expects it to fire, so a misconfigured rule cannot pass silently.

| # | Standard | Enforcement |
|---|---|---|
| H1 | Requests go through SDK verbs or MCP tool calls; other bodies come from the message factories | **Structural:** the seam accepts only generated-model instances or factory output and serializes them itself, so a dictionary cannot reach the wire |
| H2 | Every response passes the universal shape check (D3) | **Structural:** the check runs inside the seam, and the seam is the only way to send |
| H3 | Responses and refusals are read through the SDK's types (D4) | **Structural:** the seam returns only the typed result or raises the SDK's typed error, and never exposes the raw response. **Linter:** ruff `banned-api` refuses `httpx` in test modules |
| H4 | Negatives are declared mutations with a control request (D5) | **Structural:** `malformed()` is the only way to alter a request, and it takes the control request as an argument and sends it first. The schema-verdict check runs inside it |
| H5 | Signing happens only inside the SDK, apart from the third-party conformance oracles | **Structural:** the harness keeps no signer module. **Linter:** ruff `banned-api` refuses the SDK's low-level signing functions and raw Ed25519 signing outside the oracle modules |
| H6 | No sleeps; waiting uses `poll_until(predicate, deadline)` | **Linter:** ruff `banned-api` refuses `time.sleep` and `asyncio.sleep` outside the poller |
| H7 | RPC paths, enum values and the protocol version come from the SDK and the generated models | **Structural:** the seam takes an SDK verb or a generated service descriptor, never a path string |
| H8 | No dead harness code | **Linter:** vulture in `make quality`. Harness modules declare no `__all__`: vulture counts every name listed there as used, so a listed definition could never be reported dead. No linter rule can state that, so review enforces it |
| H9 | One factory per message; one identity helper, one poller, one address resolver | **Linter:** jscpd at threshold 0 over `src/`, `internal/` and the harness. It reports copies of at least 20 lines and 100 tokens, so a smaller second helper is caught in review |
| H10 | Tests never touch the database (D6) | **Linter:** ruff `banned-api` refuses, in test modules, the database and cache drivers `psycopg`, `psycopg2`, `asyncpg`, `pg8000`, `sqlalchemy`, `sqlite3` and `redis`, and `harness.operator_writes`: the one harness module that writes a database, for the seed's writes of seats, publishers and account approvals that no product surface can make yet |
| H11 | Every test owns its identities; the suite passes twice per stack (D7) | **Structural:** `fresh_agent()` is the only identity source in tests. **CI:** the suite runs twice against one stack. **Config:** `xfail_strict = true` |
| H12 | Every sent request is captured with its bytes | **Structural:** the stack's capture proxy records every request sent to the parties it fronts: the Broker, the Exchanges, the console, the publisher edges, the static key-directory hosts and the wildcard zones. It is the stack's one TLS endpoint: the services serve plain http, and every port the proxy listens on answers both http and https, under a certificate from the local CA the stack mints at every start. The services and the runner resolve those parties' names to it, and it forwards each request over plain http with its Host, end-to-end headers and body bytes unchanged. The runner's own requests are also captured at its transport |

One rule has no linter equivalent: each registry quote still appears at its citation. A host-side test checks it. The link between tests and obligations needs no check at all, because it is written in one place and type-checked (D1).

The harness bans live in a ruff configuration of their own (`tests/e2e/ruff-harness.toml`), scoped to the test modules and run with `--ignore-noqa`, so no file can exempt itself. Type checking runs with `check_untyped_defs`, so a call into the seam's typed API is checked even in an unannotated test.

### D9 — The workflow per area, and when an obligation is done

1. **Derive.** Extract every normative statement for the area into the registry with a verbatim quote.
2. **Clarify.** Collect ambiguous, contradictory or silent statements, and ask the protocol author in one batch, each question with a recommended answer. Each answer is recorded as a `DECISION` entry.
3. **Plan.** Name each entry's surface and the harness capabilities its test needs. A missing capability is built first. A gap in the SDK or the published schemas is closed in the protocol repository, not worked around in the harness.
4. **Implement.** Write the tests to D3–D8. An unmet obligation is a strict xfail (ADR-008 D3) whose reason names the defect.
5. **Prove.** A strict xfail must fail under `--runxfail` on the assertion its reason names. A passing test must be seen to fail when the behaviour under test is broken: the change's test-verify gate runs a mutation tool over the changed product or SDK code, and every surviving mutant is killed or marked equivalent with its reason. The evidence lives in that gate's record, not in the registry.
6. **Close.** The full suite passes twice against one stack, and `make quality` passes.

An obligation is `tested` when:
- its positive and negative paths run through its surface on the seam;
- the negative asserts the exact refusal and the absence of the side effect;
- the test-verify gate for the change that added it is GRADED.

## Consequences

### Positive

- A failing e2e test points at the product: requests, responses and refusals are built and read by the protocol's own code, and checked against its own contract.
- Gaps in the SDK and the published schemas surface while the harness is built, and are fixed where every implementer benefits.
- Coverage is measurable as a report derived from the tests, and each obligation traces to a source a public reader can open.

### Negative

- The harness depends on SDK capabilities and schema artifacts that must be released first: the pre-signing hook, strict decoding, typed error decoding, identity helpers and the strict JSON Schemas.
- Migrating the existing harness onto the seam is a large one-time change.
- Each Go service carries an e2e-only control surface. The `e2e` build tag keeps it out of production structurally: an untagged build cannot compile an import of it.

## Rejected alternatives

- **Every obligation through MCP.** It would pay for a sign-up and an MCP session on every protocol rule, and it gives no control over the request bytes, so mutation-based negatives would be impossible.
- **Hand-built payloads with a schema check on the way out.** The SDK already builds, signs and decodes correctly. A second builder is a second implementation to keep in agreement with the first.
- **A per-service test clock.** It skews signature windows between services. Configurable windows do not.
- **Test endpoints behind a runtime flag.** A runtime flag ships the surface in every production binary. A build tag does not.
- **Custom AST guard tests for harness rules.** A rule that structure or a standard linter can express is enforced there (D8); a guard is the last resort.
- **Observing side effects through the database.** It couples tests to the schema and bypasses the layers under test.

## References

- ADR-008 — Testing Surface as a First-Class Subsystem.
- `fora.proto` and the Python SDK, in the pinned protocol module `github.com/FORA-Protocol/protocol`.
- RFC 9421, HTTP Message Signatures; draft-ietf-webbotauth-httpsig-protocol-00.
