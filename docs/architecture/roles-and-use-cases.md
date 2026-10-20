# FORA Exchange — Roles and Use-Cases Catalog

**Status**: Living catalog. Updated when new use cases land, when status of an existing one changes, or when a new role is introduced.

**Purpose**: Enumerate the use cases each role of the FORA Exchange has, plus the implementation status of each ("Today"). ADRs reference IDs from this catalog (e.g. AC-2, PC-1) to scope the surface they make decisions about; this document is the single denormalised place where the surface is named.

**Scope**: This is a *catalog*, not an ADR. It has no decision / consequences / alternatives sections. It describes what the system supports, in what state, and where the next ADR-level work would land. If a use case requires a decision before it can be implemented, the "Pulls on" column points at the relevant (sometimes still-forthcoming) ADR.

**Status legend**:

- `✓ implemented` — wired end-to-end, has integration coverage.
- `partial` — some surface exists (e.g. a DB column, a manual code path) but the role-facing flow is incomplete.
- `✗ not yet` — no production code path exists.
- `n/a` — out of scope for the current phase.

---

## Roles

The Exchange recognises four primary roles. The fifth — *Broker* — is not a role on the Exchange's surface; it is an intermediary that consumes the Agent surface on behalf of agents. See ADR-006 for the Broker model.

- **Agent** — consumes content. Authenticated via Ed25519 keys served from the Web Bot Auth directory at `/.well-known/http-message-signatures-directory` on the agent's domain (FORA-native self-signup, no admin onboarding required).
- **Publisher** — the business that owns the content and gets paid for it. Its id is `resource_owner_id`: the Exchange creates it at registration, and the domain's manifest repeats it (ADR-026 D2). A publisher authenticates in two ways: catalog pushes are signed with the contributor key from a Web Bot Auth directory the publisher controls; the publisher console uses an OIDC login (ADR-026 D4, D5). A publisher is not a Tenant (below).
- **Exchange admin** — the operator of the deployment. The admin plane (ADR-022) is a small typed service (`fora.admin.v1`) on a separate internal listener. It has two setters, tenant fee rate and reporting policy, and no per-operator login. Everything else is still SQL against the database or future tooling. The old public `/admin/*` REST endpoints stay removed (see `transport/admin_removed_e2e_test.go`).
- **Edge / delivery node** — the entity that serves bytes to the agent. Verifies signed URLs at request time. Today the Edge is a thin TS worker (`src/edge/`); the audit-grade "third witness" delivery log does not exist.

**Tenant vs Publisher (ADR-026 D1, D7).** A **Tenant** is a seat on the Exchange: one Exchange identity that sells content. It is named by its hostname (`tenants.domain`). It holds the seller settings: commission default (`fee_rate_bps`), reporting policy, broker relay, agent activation, and the keys that sign delivery URLs. A **Publisher** is the business that gets paid, keyed by `resource_owner_id`. A publisher owns one or more **publisher domains** (sites). Each site has its own signing scheme and CDN key reference. One publisher can sell through many tenants, and one tenant can sell for many publishers. Everything that belongs to one such pair (the listing state, the commission override, who may open the console) is stored per `(tenant, publisher)`, so two tenants selling the same publisher share nothing. Today there is one tenant, the operator's own. A second tenant on the same deployment needs the `Host`-based lookup that ADR-026 D1 defers. Do not merge tenant and domain back into one row: the site-specific columns were moved out of `tenants` for exactly this reason.


---

## Agent (AC-*)

| # | Use case | Today | Pulls on |
|---|---|---|---|
| AC-1 | Discover available content | ✓ `fora.v1.ExchangeService/DiscoverResources` | — |
| AC-2 | Accept an offer and receive a signed URL | ✓ `ExecuteTransaction` → Authorize → URL mint (Ed25519 or RSA CloudFront per tenant) | ADR-009 |
| AC-3 | Fetch content from the Edge using the URL | ✓ Edge serves; URL signature verified via `@fora-protocol/sdk/verify` | ADR-012 |
| AC-4 | Report usage of the content | ✓ `ReportUsage` (pure audit endpoint, post-MR-2; no money movement) | ADR-009 |
| AC-5 | Dispute a transaction | ✗ `DisputeTransaction` not implemented | ADR-011 |
| AC-6 | View own balance, quota, transaction history | ✗ no admin / self-service surface (billing adapter exposes `GetBalance` but it is not wired to any RPC) | ADR-010 (admin UI / self-service plane) |
| AC-7 | Top up balance / fund the account | partial — a newly registered agent receives the operator-configured one-time default credit at Register (`EXCHANGE_DEFAULT_AGENT_CREDIT`, unset = disabled; ADR-009 amendment 2026-08-13); ongoing top-up stays out-of-protocol (operator funding scripts / billing-provider action) | ADR-010 |
| AC-8 | Maintain own identity (well-known documents, key rotation) | partial — self-signup against the agent's own key directory is implemented (`agent_self_signup_e2e_test.go`); key rotation is TBD | ADR-009 |
| AC-9 | Receive a refund (passive) | ✗ no Refund adapter method yet (incoming via the refund follow-up) | ADR-011 (output side) |

---

## Publisher (PC-*)

| # | Use case | Today | Pulls on |
|---|---|---|---|
| PC-1 | Publish offers (push catalog) | ✓ `fora.v1.CatalogService/PushResources` with RFC 9421 httpsig (`publisher_onboarding_e2e_test.go`) | — |
| PC-2 | Configure pricing, tolerance, required fields, reporting window | ✓ `tenants.reporting_policy` JSONB drives `reporting_obligations.required_fields` / `quantity_tolerance` (migration 000008) | — |
| PC-3 | Receive payouts | ✗ not designed | ADR-010 |
| PC-4 | View revenue accrual per period | ✓ the console route `revenue/totals` returns gross, platform fee and net per day, ISO week or month, read from the TigerBeetle ledger for the one publisher the caller holds a grant for, optionally narrowed to one resource or domain (`console_reads_totals_integration_test.go`), and how many of the publisher's items were refused in the period, by reason (`console_reads_denials_integration_test.go`). The console's Overview (`/overview`) shows the period's totals and a chart of transactions per day, the month so far by default (`dashboard_overview_integration_test.go`). Its Income report (`/income`) shows a row per day with the transaction count, gross, fee and net and the period's total, filterable by period, domain and resource and showing only the days that earned by default, reachable from each catalog entry, and downloadable as CSV (`dashboard_income_integration_test.go`) | ADR-010 (amendments 2026-09-25, 2026-09-29 and 2026-09-30), ADR-026 D6, ADR-027 |
| PC-5 | View transaction history per offer | ✓ the console routes `transactions/list` (paged, filterable by resource, domain and reporting status, with each transaction's reporting status) and `transactions/detail` (the signed offer, the agent's signed acceptance and the reporting obligation) (`console_reads_transactions_integration_test.go`). The console's Transactions page lists them with Previous and Next paging and the Exchange's reporting status, filtered by domain and by report status (`dashboard_domain_filter_integration_test.go`, `dashboard_status_filter_integration_test.go`), and the detail page verifies both signatures in the browser (`dashboard_transactions_integration_test.go`) | ADR-010 (amendment 2026-09-25), ADR-026 D6 |
| PC-6 | Respond to disputes (acknowledge / contest) | ✗ not designed | ADR-011 |
| PC-7 | Maintain own identity (offer-signing keys) | partial — offer signing is implemented (`signing.OfferSigner`); explicit key rotation flow is TBD | ADR-009 |
| PC-8 | Configure delivery (which Edge serves what `content_hash`) | ✗ not designed | ADR-012 |

---

## Exchange admin (EC-*)

| # | Use case | Today | Pulls on |
|---|---|---|---|
| EC-1 | Onboard a publisher | supported — a publisher registers themselves through the publisher console: they sign in, register the business, save the site, publish the generated manifest, install the package and have the site verified. The operator seeds nothing. The registration surface is the Exchange's own signed JSON routes under `/exchange/v1/console/`, called by the console on the acting person's behalf (ADR-026, as amended 2026-09-21) | ADR-026 |
| EC-2 | Onboard an agent (verify domain → public key chain) | partial — `agentreg.Registry` exists with lazy key-directory resolution; explicit operator-driven ceremony is TBD | ADR-009 |
| EC-3 | View transactions across tenants | ✗ no admin surface (DB rows queryable via SQL; nothing role-facing) | ADR-010 |
| EC-4 | Manually credit / debit an agent balance | partial — no admin surface for arbitrary per-agent credit/debit (operator funding scripts remain the tool); the default credit for new agents is deployment configuration (`EXCHANGE_DEFAULT_AGENT_CREDIT` is the sole channel, read once at boot, unset means no grant — no admin RPC and no database column) | ADR-010 |
| EC-5 | Investigate a dispute (review the three signed sources: offer, transaction, delivery) | ✗ no admin surface; the third witness (signed delivery log) does not exist | ADR-011 + ADR-012 |
| EC-6 | Trigger / monitor reconciliation runs | ✗ not designed | ADR-011 |
| EC-7 | View FX position / treasury (Phase 3 only) | n/a | future |
| EC-8 | System health (revenue, error rates, latency) | partial — structured logs (`slog`) exist on all three services; no dashboard | (ops) |

---

## Edge / delivery node (EDC-*)

| # | Use case | Today | Pulls on |
|---|---|---|---|
| EDC-1 | Verify signed URL on incoming fetch | ✓ Ed25519 verification implemented via `@fora-protocol/sdk/verify`; RSA CloudFront signed URLs verified natively by CloudFront | — |
| EDC-2 | Serve content when URL valid | ✓ basic origin-proxy serve path (`src/edge/src/app.ts`) | — |
| EDC-3 | Sign and log delivery record (the third witness) | ✗ this is the gap — Edge currently neither signs nor records deliveries | ADR-012 |
| EDC-4 | Publish delivery logs to a queryable store | ✗ not designed | ADR-012 |
| EDC-5 | Verify `content_hash` on outbound bytes | ✗ depends on whether the offer signs the `content_hash` over the served object | ADR-012 |

---

## Cross-references

- **ADR-001 .. ADR-008** — existing ADRs in this directory. ADR-006 (Broker intermediation) is the primary upstream for the Agent surface.
- **ADR-009 (forthcoming)** — identity & key lifecycle (agent + publisher onboarding, key rotation). Pulls on AC-2, AC-4, AC-8, PC-7, EC-1, EC-2.
- **ADR-010 (forthcoming)** — admin / self-service surface (balance, history, payouts). Pulls on AC-6, AC-7, PC-3, PC-4, PC-5, EC-3, EC-4.
- **ADR-011 (forthcoming)** — dispute & refund flow. Pulls on AC-5, AC-9, PC-6, EC-5, EC-6.
- **ADR-012 (forthcoming)** — Edge delivery record (the third signed witness). Pulls on AC-3, PC-8, EC-5, EDC-3, EDC-4, EDC-5.
- **ADR-022** — the admin transport plane (`fora.admin.v1`, separate internal listener). Pulls on EC-1 (fee rate, reporting policy setters only).
- **ADR-026** — publisher registration model and the publisher console. Tenant = seat, publisher = payee, many-to-many. The manifest proves domain control. Registration and publisher reads are the Exchange's own signed routes, not a protocol package (amended 2026-09-21). Pulls on PC-4, PC-5, PC-7, EC-1.
