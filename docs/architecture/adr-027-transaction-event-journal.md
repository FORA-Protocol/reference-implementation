# ADR-027 — Transaction Event Journal: Denied Transactions as Append-Only Events

**Status:** Proposed (2026-09-29)

**Relates to:** ADR-008 (D1, time is a port; the amendment on console absence reasons),
ADR-010 (the revenue totals route, amended by this ADR), ADR-011 (`transaction_log` as
the record of what was sold), ADR-012 (D1's `signed_url_hash`, corrected by this ADR),
ADR-019 (one `DenialReason` vocabulary; batch denials travel in the response body),
ADR-026 (the publisher is the resource owner; the console surface and its grant check).

---

## Context

When the Exchange refuses a transaction item, the refusal goes back to the agent and
nowhere else. `ExecuteTransaction` puts the reason in the item's `denial_reason` and
writes no row. A publisher therefore cannot see how often agents were refused for its
content, or why, and no dashboard can count it.

Three places look as if they could hold a denial. None of them can:

- **`fora.transaction_log.denial_reason`.** The column has existed since the first
  migration, and nothing has ever written it. The table cannot hold a denial without
  invented values: `signed_url_hash`, `expiry` and `agent_identity_hash` are NOT NULL,
  and a denied item has none of them. The table also has a second job that a denial row
  would break. It has one row per issued URL, and its UNIQUE `idempotency_key` is the
  replay source and the double-charge backstop: the replay path treats any row under a
  key as a successful transaction, and the duplicate-hold release decides from that row
  whether to release a ledger hold. `transaction_evidence` and `reporting_obligations`
  hold foreign keys to it, and ADR-011 treats it as the record of what was sold.
- **`fora.transaction_request_claims.response_payload`.** It holds the finalized
  response, denials included, as one protobuf blob keyed by `(agent_id,
  idempotency_key)`. It names no tenant, resource or publisher, so it cannot be counted
  per publisher. It is also written only for requests that carry the request-level
  proof.
- **The logs.** Some denials write a WARN line; most write nothing. Logs are not a
  store the console can read.

The protocol's transaction-log documentation (module `github.com/FORA-Protocol/protocol`
v1.0.7, `website/src/content/docs/components/transaction-log/event-types.mdx`)
describes a `BillingAuthorizationResult` event with `authorized=false` and a
`denial_reason` for exactly this case. The Exchange does not implement it.

---

## Decision

### D1 — A separate append-only journal; a denial is a `BILLING_AUTHORIZATION` event

A new table, `fora.transaction_events`, holds one row per attributed denied item. The
row's `event_type` is `BILLING_AUTHORIZATION`, a value of the new enum
`fora.transaction_event_type`. The label mirrors the protocol documentation's
`EVENT_BILLING_AUTHORIZATION`. The protocol defines no `EventType` enum in
`fora.proto`, so no parity test can pin the label; it is implementation-owned.

`authorized` is not a column. Every row the Exchange writes today is a refusal and
carries a `denial_reason`, so `authorized=false` holds for every row. Approvals are not
journaled (out of scope). Journaling them later needs a column that says which decision
a row records, and `denial_reason` then stops being NOT NULL.

`denial_reason` uses `fora.denial_reason`, the enum that already stores the proto
`DenialReason` value for value (ADR-019), with a parity test against the proto.

### D2 — The row

| Column | What it holds |
|---|---|
| `event_id` | Primary key. A UUIDv7 the Exchange mints. |
| `event_type` | `BILLING_AUTHORIZATION` (D1). |
| `created_at` | The decision time, read from the Exchange's clock (ADR-008 D1). It has no database default. `transaction_log.created_at` is a database default; both are UTC. |
| `tenant_id` | The seat whose catalog entry the offer resolved to. Foreign key to `fora.tenants`, `ON DELETE RESTRICT`. |
| `idempotency_key` | The derived per-item key: the request's idempotency key, `:`, and the offer id. It is the same value `transaction_log` and the billing adapter use for that item. |
| `offer_id` | The id of the signed offer the item presented. |
| `agent_id` | The agent the Exchange resolved for the request: the transport identity for a direct request, and the body-proven `Requester.domain` for a relayed one. Foreign key to `fora.agents`, `ON DELETE RESTRICT`. |
| `resource_id`, `resource_owner_id` | From the catalog entry at decision time. |
| `denial_reason` | The reason the agent was given. |

`resource_id` and `resource_owner_id` are recorded values, not foreign keys. A catalog
re-push can move a resource to another owner. The denial stays with the publisher that
owned the resource when the Exchange refused it.

Rows are never removed (D7), so their size is bounded. Every text column must be
non-empty. `offer_id` is at most 255 bytes: the Exchange mints it as a UUID, and only
offers whose Exchange signature verified are journaled (D3). `idempotency_key` is at
most 512 bytes: the 256-byte cap the Exchange puts on a request key, the separator, and
the offer id.

One index serves the one read: `(tenant_id, resource_owner_id, created_at)`.

### D3 — Attribution: only offers the Exchange signed and the catalog resolved

A denial is attributed to a publisher only when the Exchange signature on the offer
verified **and** the offer's catalog entry resolved. An unattributed denial writes no
row.

| Denial | Attributed? | Why |
|---|---|---|
| `SIGNATURE_INVALID` on the offer | No | The agent named the resource; nothing the Exchange signed supports it. |
| `CONTENT_UNAVAILABLE` | No | The resource is gone from the catalog, so there is no entry. |
| `OFFER_EXPIRED` | Yes, when the entry resolves | The expired offer is re-checked with a signature-only verification, and its signed `canonical_url` is looked up in the catalog. |
| `SIGNATURE_INVALID` on the agent's acceptance | Yes | Raised after the entry resolved. |
| `REPORTING_OVERDUE` | Yes | Raised after the entry resolved. |
| `ACCOUNT_NOT_REGISTERED`, `ACCOUNT_INACTIVE`, `INSUFFICIENT_BALANCE` | Yes | Raised after the entry resolved. |
| `ENTITLEMENT_*`, `SUBSCRIPTION_LAPSED` | Yes, by construction | They would be raised after the entry resolved. No Exchange code raises them today. |

The expired-offer lookup is the same one the broker-relay gate already uses to find an
item's seat without checking expiry. It verifies the Exchange signature itself instead
of relying on the order in which the SDK checks signature and expiry. An expired offer
whose resource has since left the catalog stays `OFFER_EXPIRED` and is not attributed.

A request refused as a whole — a request-level proof that does not verify, or a relayed
request with an item acceptance that does not verify — is a transport error raised
before any item runs. No item is denied, so no row is written.

The proto also defines `RATE_LIMITED`, `RESTRICTION_NOT_SATISFIED`, `QUOTA_EXCEEDED`,
`DELEGATION_INVALID` and `SCOPE_INSUFFICIENT`. The Exchange produces none of them.

The denial error carries the resolved catalog entry to the point where the row is
written. It is an unexported wrapper around the domain error: it changes neither the
error kind nor what reaches the wire.

### D4 — Only requests that proved key possession write events

The Exchange decides once per request, at admission, whether the caller proved
possession of the agent key. There are two ways:

- the request-level proof (`agent_request_acceptance`) verifies. The request then also
  holds a request-level claim;
- the request carries no request-level proof, and at least one item's acceptance
  verifies. An older client sends this shape; it gets no claim.

A request that proves neither writes no row, even though its items still run and are
denied in the response.

This follows the protocol's split between request-level and per-item state. The
`agent_request_acceptance` field of `TransactionRequest` in `fora.proto` (module
`github.com/FORA-Protocol/protocol` v1.0.7) says an Exchange verifies the request-level proof "before
creating or serving request-level idempotency state", and that "older clients that omit
it retain per-item execution semantics but receive no request-level claim". A journal
row is per-item state, keyed by the per-item key, like the `transaction_log` row that
an older client's successful item already writes. The request-level claim keeps its
stricter rule.

The one case where the agent identity is not proven is a relayed request without the
request-level proof in which no item verifies. There the agent is the unproven
`Requester.domain`. Possession is not proven, so nothing is written.

### D5 — One event per agent and item key; retries write nothing new

The table has `UNIQUE (event_type, agent_id, idempotency_key)`, and the insert is
`ON CONFLICT DO NOTHING`.

An exact retry of a request whose response was finalized onto its claim runs no item
code, so it cannot write a second row. The same denial can still be raised twice for
one key: an older client's retry re-executes, a claim whose request never finalized
re-runs, and the losing copy of a concurrent duplicate re-runs once its wait expires.
The constraint makes each of those a no-op.

The constraint is scoped per agent. The protocol scopes idempotency the same way: the
`idempotency_key` comment on `TransactionRequest` says uniqueness "is scoped to the
verified RFC 9421 signer: the server dedupes per (authenticated caller, key), never
globally". `transaction_log` keys its UNIQUE on the derived key alone; that is a known
limitation of that table, and the journal does not copy it. Without `agent_id`, a
second agent presenting the same key and offer would have its denial silently dropped.

The first decision for a key is the one recorded. A retry denied for a different reason
adds nothing. A retry that later succeeds is recorded in `transaction_log`, and both
rows remain.

### D6 — The write is synchronous and best-effort

The row is written on the denial path, before the response returns. If the write
fails, the agent still receives its denial, and the Exchange logs
`exchange.execute_transaction` with `event=denial_journal_failed` at ERROR.

The write does not fail the request. Items in a batch run one after another, and an
earlier item may already be charged and committed when a later item is denied.
Aborting the request at that point leaves the claim without a finalized response. The
agent's retry is then refused as a partial replay, and the agent has paid for an item
whose URL it never receives. A reporting record must not cause that.

The write does not inherit the caller's cancellation; it has its own short deadline.
An agent that hangs up still leaves its denial behind.

Consequence: the counts are a lower bound. Every failed write leaves an ERROR line that
names the offer, the publisher and the reason.

### D7 — Append-once, enforced by one shared function

`fora.transaction_events` refuses `UPDATE`, `DELETE` and `TRUNCATE` for every role,
through a row trigger and a statement trigger, exactly as `fora.transaction_evidence`
does. Both tables use one function, `fora.refuse_append_once_mutation()`, which names
the table it refused:

```
ERROR:  fora.transaction_events is append-once: DELETE is prohibited
```

The function is the evidence table's former `fora.prevent_evidence_mutation()`, renamed
and generalized. The evidence triggers stay bound to it, and their message is unchanged.
There is no `REVOKE`: every environment connects as the database owner, which a
`REVOKE` does not restrict.

Nothing removes rows. One row is written per attributed denied item, and
`ExecuteTransaction` has no rate limit, so an agent that repeats a refused purchase
under new keys adds a row each time. Partitioning is the planned retention mechanism
for every table, and it is not built.

### D8 — The publisher totals carry a denial count by reason

`POST /exchange/v1/console/revenue/totals` gains `denials`: a list of `{reason, count}`
for the whole period, sorted by the protocol's enum number, and `[]` when there are
none.

- **The period, not the buckets.** The count answers "how often, and why" for the
  period asked.
- **Not scoped by currency.** The protocol's `BillingAuthorizationResult` carries a
  `currency` only as the currency of an authorized amount, so a denial has none. The
  console's lists that carry no money, such as `transactions/list`, are not filtered by
  currency either. A ledger that does not keep an offer's currency refuses it as
  `INSUFFICIENT_BALANCE`; filtered by currency, those denials could never be seen,
  because their currency is the one the ledger rejects. So every supported currency's
  answer carries the same denials.
- **Absence reasons.** A currency the deployment keeps no ledger in is answered
  `UNSUPPORTED_CURRENCY` before anything is read, so `denials` is `[]`. When the ledger
  cannot be read (`INTERNAL_ERROR`), the denials are still reported, like the
  transaction counts. Denials never clear `NO_TRANSACTIONS_IN_PERIOD`, which describes
  sales.
- **Who a denial belongs to.** Transactions are attributed through
  `fora.publisher_transactions`, which follows the catalog entry's *current* owner. A
  denial keeps the owner at decision time (D2).
- **Spelling.** `reason` is the protocol's own name, such as
  `DENIAL_REASON_OFFER_EXPIRED` — the same token an agent receives. The console package
  defines no constants for it. The ADR-008 amendment's rule — console wire values are
  constants in the shared package — covers absence reasons, which are console
  vocabulary. Denial reasons are protocol vocabulary, and ADR-019 allows no parallel
  copy of it.
- **A lower bound** (D6).

Access is unchanged: the route answers only an actor with a grant for the publisher,
and every read filters on the seat's `tenant_id` and the publisher.

### D9 — `transaction_log.denial_reason` is dropped

The column was never written, and denials now live in the journal. The
`fora.denial_reason` enum stays, because the journal uses it.

Dropping a column makes the release impossible to roll back. The older binary names the
column in its insert and reads it in every query on `transaction_log`, so it fails on
the first one. A deployment that has run this release fixes forward.

---

## Deviations from the protocol's transaction-log documentation

The documentation of module `github.com/FORA-Protocol/protocol` v1.0.7
(`website/src/content/docs/components/transaction-log/`) describes the full design.
This Exchange implements one event of it, differently:

- **One table per concern, not one "fat event" table.** The documentation stores every
  event type in one wide `transaction_events` table. Here, `transaction_log` remains
  the record of issued URLs (the documentation's `SignedURLIssued`), and
  `transaction_events` holds only denials.
- **Synchronous and best-effort, not an asynchronous write-ahead log with eventual
  durability.** No write-ahead log exists (D6).
- **Refusals made before billing is asked are recorded too.** The documentation logs
  `BillingAuthorization` after the billing adapter decides. Here, any attributed denial
  is a `BILLING_AUTHORIZATION` event, including `OFFER_EXPIRED`,
  `REPORTING_OVERDUE`, the acceptance-signature denial and `ACCOUNT_NOT_REGISTERED`,
  which are decided before the adapter is called.
- **The derived per-item key, deduplicated per agent.** The documentation's
  `idempotency_key` is the request's key; here it is the per-item key (D2), with the
  per-agent constraint of D5.
- **`agent_id`, not `buyer_lid`.** The documentation names the requester by its account
  handle (`billing_ref`). An `ACCOUNT_NOT_REGISTERED` agent has no account, so the row
  names the agent.
- **Fields left out:** `billing_latency_ms`, `exchange_id`, `subscription_id`,
  `authorized` (D1), and the hash chain and signature. The fields the documentation
  sets only on approval (`billing_id`, `transaction_id`, `authorized_amount`,
  `currency`) do not apply to a denial. There is no partitioning and no retention (D7).
- **Two timestamps.** The documentation's `timestamp` is `created_at` here: the
  decision time on the Exchange's clock. The UUIDv7 `event_id` embeds the wall clock at
  minting; nothing reads it as a time.

---

## Consequences

### Positive

- A publisher can see how often agents were refused for its content, and why, per
  period, through the route it already uses for revenue.
- Every recorded denial names a verified offer, a resolved catalog entry and an agent
  whose key the request proved, so a caller cannot charge a denial to a publisher it
  did not buy from.
- `transaction_log` keeps one meaning: a row is a sale.
- Evidence and journal refuse changes through one function, not two copies.

### Negative

- The table grows forever, one row per attributed denied item (D7).
- The counts are a lower bound; a failed write is visible only in the logs (D6).
- A second append-once table constrains restores the same way the evidence table does:
  a data-only restore into a populated database fails.
- The release cannot be rolled back (D9).
- Individual events have no read route yet; only counts are exposed.

---

## Rejected alternatives

- **Write denials to `transaction_log`.** Needs invented values for NOT NULL columns,
  and turns the replay source and the double-charge backstop into a table where a row
  can mean "refused".
- **Count denials from the claim's stored response.** It is a protobuf blob with no
  tenant, resource or publisher, and it exists only for requests with the request-level
  proof.
- **Journal only requests that hold a claim.** Denial counts would then depend on the
  version of the agent's SDK, not on what happened (D4).
- **Journal a denial only when that item's own acceptance verified.** It contradicts
  the attribution of the acceptance-signature denial, and it would count the same
  denial differently for older and newer clients.
- **Record a currency and filter by it.** Hides every currency-mismatch denial (D8).
- **`UNIQUE (event_type, idempotency_key)`.** Deduplicates globally, which the protocol
  rules out (D5).
- **Fail the request when the write fails.** Can leave an agent charged for an item it
  never receives (D6).
- **Foreign keys to `fora.catalog` and `fora.publishers`.** The row records who owned the
  resource when it was refused, not a reference to today's entry (D2).
- **A separate denial-counts route.** The totals route already answers "what happened
  in this period" for a publisher, with the same access check.

---

## Out of scope

- Journaling approvals, and the protocol's other event types.
- A route that lists individual denial events.
- Retention, partitioning and archival.
- Integration tests for `ENTITLEMENT_*` and `SUBSCRIPTION_LAPSED`, which nothing raises.
- Changing `transaction_log`'s global uniqueness on the derived key.

---

## Amendments to earlier ADRs

- **ADR-010** (amendment of 2026-09-25, the revenue report as a console route): the
  totals also carry a denial count by reason, as D8 describes. Recorded as a dated note
  on ADR-010.
- **ADR-012** D1 says the delivery record's `signed_url_hash` matches
  `transaction_events.signed_url_hash`, the protocol documentation's table. In this
  Exchange the issued URL's hash lives on `transaction_log`, and `transaction_events`
  holds denials only. Corrected inline in ADR-012.

---

## References

- Module `github.com/FORA-Protocol/protocol` v1.0.7:
  `fora.proto` (`TransactionRequest.idempotency_key`,
  `TransactionRequest.agent_request_acceptance`, `DenialReason`) and
  `website/src/content/docs/components/transaction-log/` (`overview.mdx`,
  `event-types.mdx`, `write-ahead-logging.mdx`, `storage-backends.mdx`).
- ADR-008 D1, ADR-010, ADR-011, ADR-012 D1, ADR-019, ADR-026 D6.
- Migrations `000044_transaction_events` and
  `000045_drop_transaction_log_denial_reason` in `src/exchange/internal/db/migrations/`.
