# FORA Exchange — Runbook

Operated by the Exchange Operator.

**Escalation.** If §3 does not resolve it, contact Postindustria over the
existing communication channel. Send the `request_id` of a failing request
together with the matching log lines from the Exchange, the Broker and the Edge.
Postindustria has no access to your infrastructure, so that correlation ID is
the only way the request can be traced.

> This runbook assumes the Exchange is already deployed. For installation,
> configuration values, applying or destroying the stack, and deploy-time
> verification, see [`DEPLOYMENT.md`](DEPLOYMENT.md) and
> [`CONFIGURATION.md`](CONFIGURATION.md).

---

## 1. Overview

The Exchange is the publisher's side of the system. It owns the catalog of
licensed content, prices access to it, signs the offers agents buy, and issues the
signed delivery URL the agent finally fetches. **It is the only component that
charges money** — no other component moves money, and no other component can tell
you what was charged.

**Its dependency on the Broker comes first, because it is the one operators
misread.** The Broker publishes the list of signing keys that have been
withdrawn, and the Exchange treats that list as the final word:

- The Exchange **refuses to start** without `EXCHANGE_BROKER_WELLKNOWN_URL`. The
  address alone satisfies the boot check — the document itself is fetched over
  public HTTPS at runtime, so a Broker that is down, or a hostname
  that does not resolve yet, does not stop the Exchange from starting (§3.3).
- While the document is unreachable, the Exchange **rejects signed requests**
  and logs `exchange.httpsig.broker_wellknown_unavailable` — it holds no local
  copy of the keys to fall back to, cannot prove a key has not been withdrawn,
  and so does not accept it. A Broker outage therefore looks like mass
  authentication failure on the Exchange, while its own health checks stay
  passing.

Withdrawing a key is a Broker procedure even though the Exchange enforces it; see
the Broker's runbook.

---

## 2. Monitoring

### 2.1 Health

**There are two probes, and they answer different questions.**

| | Checks | Answers |
|---|---|---|
| `/healthz` | The catalog database, and nothing else | Is the process alive? |
| `/readyz` | The catalog database **and the ledger**, when a ledger is configured | Can this instance serve paid transactions? |

**`/healthz` pings the catalog database and nothing else.** It stays green with
the ledger dead, the cache dead, the Broker unreachable and the **account
registry** unreachable — while every paid transaction fails. Read it as "the
process is up", not "the service works".

**`/readyz` adds the ledger.** It returns 503 when the billing ledger does not
answer within two seconds, so a load balancer stops sending paid traffic to an
instance that cannot bill and resumes on its own when the ledger returns. With
`FORA_BILLING_ADAPTER` at its `free` default, or `inmemory`, there is no ledger to
probe and `/readyz` matches `/healthz` exactly.

The split is deliberate: point a **readiness** probe at `/readyz` and a **liveness**
probe at `/healthz`. The other way round, a routine ledger restart would restart the
Exchange — and take down the free resources that need no ledger at all.

Neither probe covers the cache, the Broker or the account registry.

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://exchange.example/healthz
# Expect: 200

curl -s -o /dev/null -w '%{http_code}\n' https://exchange.example/readyz
# Expect: 200. A 503 means the catalog database or the ledger is unreachable;
#         the logs name which.

curl -s https://exchange.example/.well-known/fora.json
# Expect: "role":"ROLE_EXCHANGE", your own "domain", and an "endpoint"
#         equal to your EXCHANGE_PUBLIC_ORIGIN

curl -s https://exchange.example/.well-known/http-message-signatures-directory
# Expect: a "keys" array with one Ed25519 key ("kty":"OKP","crv":"Ed25519").
# The key MUST be identical across a restart.
```

The two published documents are the real readiness signal. Neither proves the
Broker link is healthy — for that, watch the logs for
`exchange.httpsig.broker_wellknown_unavailable` (§2.3). No endpoint reports it.

### 2.2 Logs

JSON, one object per line, on stdout — `docker compose logs exchange`. **There is
no `/metrics` endpoint and no Prometheus support**, so alerting is on log events.

Everything in this section assumes the default `EXCHANGE_LOG_LEVEL` of `INFO`. Raised to
`WARN`, the service stops writing the `INFO` lines below, and several checks and
procedures in this runbook read those; lowered to `DEBUG`, it adds routine detail,
such as an `http request` line for every request. [`CONFIGURATION.md`](CONFIGURATION.md)
§2.1 says what each level hides.

Every request-path line carries `request_id`, and it is the same value across the
Edge, the Broker and the Exchange — grep it in all three to trace one request end
to end. Boot lines carry no `request_id` — nothing has made a request yet. Some are
free text and some use the same dotted `exchange.*` names as the request path; the
table below gives both.

| Event | Level | Means |
|---|---|---|
| `exchange.exit` | ERROR | Refused to start, or stopped. `err` names the cause. |
| `exchange.httpsig.broker_wellknown_unavailable` | WARN | The Broker's withdrawn-key document could not be read. **The request is rejected, not let through.** The lookup does not depend on the request that started it, so a caller that disconnects cannot cause this line: each one means the Broker's document really did not arrive, whether it was refused, failed, or took longer than the fetch timeout. |
| `exchange.httpsig.reject` | WARN | A request was rejected before it reached a handler. `outcome` is `signature`, `replay`, `broken_chain`, `hop_budget` or `body_too_large`. The first four are authentication outcomes and point at the caller's key, clock or relay chain, so a request carrying them was signed. `body_too_large` is not one of them and need not be signed at all — the request never reached verification, because its body passed the read cap, and the caller fixes it by sending less. See [`CONFIGURATION.md`](CONFIGURATION.md) §2.7. The line appears for a catalog push as well as for the other RPCs; the two mounts refuse an over-cap body by different means and write the same line. |
| `exchange.admin.ip_reject` | WARN | An admin call came from an address not in `ADMIN_ALLOWED_CIDRS`. Field `client_ip`. |
| `exchange.execute_transaction` | INFO on success, WARN on rejection | One line per purchase attempt. `outcome` is `VALIDATED`, `REJECTED_AUTHZ` or `REJECTED_REPORTING_OVERDUE`; `kind` and `err` carry the detail. A `REJECTED_REPORTING_OVERDUE` line does NOT mean the caller saw an HTTP error: the refusal travels in the response body as the item's `denial_reason`, under a 200. §3.1. |
| `exchange.execute_transaction` with `event=billing_record_failed_best_effort` | **ERROR** | The transaction committed and the agent has its URL, but the charge was never posted to the ledger — a silent under-charge. See §2.3. |
| `exchange.execute_transaction` with `event=release_hold_failed` | ERROR | A failed purchase's reservation could not be released. It expires on its own; money is not lost, but the agent's available balance is understated until it does. |
| `exchange.execute_transaction` with `event=denial_journal_failed` | ERROR | An item was refused and the agent received its denial, but the refusal could not be recorded in `fora.transaction_events`. No money is affected; the publisher's denial count in the console's revenue totals is one short. Carries `offer_id`, `publisher_id`, `denial_reason` and `err`. |
| `exchange.report_usage` | INFO on success, WARN on rejection | One line per usage report. `outcome` is `VALIDATED`, `REPLAY`, `REJECTED_AUTHZ`, `REJECTED_FIELDS`, `REJECTED_TOLERANCE`, `REJECTED_BILLING_ID`, `REJECTED_TIMESTAMP` or `REJECTED_ALREADY_REPORTED`. `REJECTED_ALREADY_REPORTED` means a second, different report arrived for an obligation that had already been accepted: nothing was written to the obligation row, and this line is the only record of the attempt. Repeated lines from one agent are worth looking at — a settled report cannot be revised, so the agent is either retrying past a success it did not notice or trying to change a record it is the subject of. `REJECTED_WINDOW` is no longer produced — a report is accepted whatever the time — but it survives on rows written before that changed. A report addressed to another Exchange never reaches this line — it is refused before the obligation is read, and appears as `foraaudience.refused` below. |
| `exchange.catalog_push.reject` | WARN | One line per catalog entry a push was refused for, whatever refused it. Carries `uri` and the machine-readable `reason` — the same token the publisher sees in the refusal message — plus `detail` when the refusing check has one. A push is all-or-nothing per submission, so any of these lines means the whole submission was refused and nothing from it was stored. This is what to grep when a publisher reports a refusal and you need to place it; §3.1 lists every reason with its fix. |
| `license term refused at ingest` | WARN | The second line a term refusal writes, one gate deeper than the row above. Carries `uri`, the SDK `rule` id, the entry-relative `path` (`terms[i].…`) and the offending `token`, so you can name the bad value without asking for the publisher's feed. Only the ingest-tier term check produces it; every other reason has the row above and nothing more. |
| `foraaudience.refused` | WARN, **ERROR** for the two below | A request named a recipient other than this Exchange and was refused before the handler ran, so it changed nothing. Carries `path` (the RPC), `verdict`, `self` (the identity this service answers to) and `reason`. `verdict` is `mismatch` (a well-formed domain naming somebody else), `empty` (no recipient at all) or `malformed` (not a bare domain). A steady stream of `mismatch` from one caller usually means their configured Exchange domain disagrees with `EXCHANGE_DOMAIN` here. At **ERROR** level it is not the caller's fault: `no_verdict` means this service's own identity is unusable and **every** caller is being refused, and any other verdict means a value this build does not understand. |
| `migrations applied` | INFO | Boot. Carries `version`, `dirty` and `table`. **Appears twice** — once per database. Read `table` to tell them apart: `schema_migrations_fora` is the catalog database, `schema_migrations_sor` the account registry. |
| `ed25519 signing key loaded` | INFO | Boot. Must always appear. |
| `rsa signing key loaded` | INFO | Boot. Appears only when an RSA key is configured. Without one, `no RSA signing key configured; requests for AWS_CLOUDFRONT_RSA sites will be refused` appears instead — fine for an all-Ed25519 deployment, a problem the moment a publisher uses CloudFront. |
| `billing adapter: tigerbeetle` | INFO | Boot. Carries `ledger`, `currency`, `address`. Its **absence** means the `free` adapter. |
| `billing adapter seeded` | INFO | Boot, `inmemory` adapter only. Carries the number of seeded agents. |
| `sor adapter: postgres` | INFO | Boot. The account registry is connected and migrated. Carries `cache_ttl`. **Not optional** — its absence means the Exchange is not running. |
| `default tenant not found yet — Register will fail until it is seeded` | WARN | Boot. No tenant row matches `EXCHANGE_DEFAULT_TENANT` (or `EXCHANGE_DOMAIN`). **No agent can register, so no agent can buy.** Boot continues anyway. Carries `default_tenant_domain`. |
| `could not verify default tenant at boot` | WARN | Boot. The check could not run, usually a brief database problem. Carries `default_tenant_domain` and `err`. Re-check by hand once the service is up. |
| `default agent credits configured` | INFO | Boot. The welcome credit parsed from `EXCHANGE_DEFAULT_AGENT_CREDIT`, which every first `Register` grants from until the next restart. Appears on every boot, whether or not the default tenant is seeded yet: a tenant created later grants it without a restart. Carries `default_agent_credits`, as `CODE=AMOUNT` pairs at 8 decimal places, or empty when no credit is configured. A malformed value never reaches this line — it stops the boot instead. |
| `granted default agent credit` | INFO | One line per welcome-credit grant at Register — the operator's visibility into credit drawn from the ledger's liquidity account, because the grant has no aggregate cap. Carries `billing_ref` and `amount`. |
| `exchange.execute_transaction` with `event=sor_active_check_failed` | WARN | The account registry could not be read during a purchase. The purchase is **allowed through** — the ledger balance still bounds spending — but an account you switched off may buy until the registry answers again. |
| `exchange.httpsig.replay_store_ready` | INFO | Boot. Redis-backed replay protection is live. Carries `addr`. |
| `exchange.httpsig.replay_store_disabled` | INFO | Boot. `REDIS_URL` is unset, so replay protection is per-process — safe at one instance, not above (§3.3). |
| `exchange.httpsig.wellknown_enabled` | INFO | Boot. Carries `well_known_url` and `poll_interval` — where to confirm the interval you configured took effect. |
| `exchange listening` / `admin listening` | INFO | Boot. Both must appear. |

### 2.3 Alerts

**These are recommendations, not configured alerts.** Nothing here ships an
alerting rule — the Exchange has no metrics endpoint, so these are log conditions
for you to wire into whatever monitoring you already run. **"Wake on-call" means
call the engineer on duty, at any hour.**

| Signal | Severity | First action |
|---|---|---|
| `event=billing_record_failed_best_effort` | **Wake on-call** | **The most important row here.** A committed transaction with no ledger posting: the agent has content it was never charged for. Every occurrence is revenue silently lost. §3.1. |
| `exchange.httpsig.broker_wellknown_unavailable` | **Wake on-call** | The Broker is unreachable, so every signed request is being rejected. Check the Broker before chasing key problems. |
| Billing authorization failing in bulk | **Wake on-call** | The ledger is unreachable or misconfigured. Every paid purchase is denied while `/healthz` stays green. |
| `exchange.exit` naming a migration | **Wake on-call** | A migration failed at boot; no instance will start. Do not clear the dirty flag by hand. |
| `/healthz` non-200 for 2 minutes | **Wake on-call** | The catalog database is unreachable. Note that `/healthz` says nothing about the account registry. |
| `exchange.httpsig.reject` rate spikes | Business hours | Key rotation, clocks that are out of sync, or a TLS proxy newly placed in front (§3.1). |
| `event=release_hold_failed` | Business hours | Reservations are not being released promptly. Self-correcting as holds expire. |
| `event=denial_journal_failed` | Business hours | Refused purchases are not being recorded, so the publishers' denial counts are short. Purchases themselves are unaffected. Check the catalog database. |
| `default tenant not found yet …` at boot | Business hours | No agent can register while this appears, so the deployment cannot take a single new customer. Create the tenant — §4.2. |
| `event=sor_active_check_failed` | Business hours | The account registry is unreadable, so switched-off accounts are not being enforced. Purchases continue. Check the registry database. |

**Do not alert on** transaction denials — an agent asked for something it may not
have and was told no; that is the system working. Nor on
`exchange.admin.ip_reject`: the admin listener refuses everything by default, by
design, and the internet will try it.

---

## 3. Troubleshooting

### 3.1 Symptom → cause → fix

| Symptom | Why | What to do |
|---|---|---|
| **Every signed request rejected, `outcome=signature`** | A proxy in front terminates HTTPS and forwards HTTP, so the URL the caller signed is not the URL the Exchange checks | Set `FORA_TRUST_PROXY_HEADERS=true` — [`CONFIGURATION.md`](CONFIGURATION.md) §4. Only behind a proxy you control, never on a directly-exposed Exchange. |
| A CloudFront publisher's purchases refused: `no RSA signing key` | An absent RSA key never stops the boot. The Exchange runs, serves every Ed25519 publisher, and refuses only requests for a CloudFront-scheme site — per request, with the `failed_precondition` code | Generate the key and supply it: [`DEPLOYMENT.md`](DEPLOYMENT.md) §5. No restart loop to worry about; add the key and restart once. |
| Refuses to start: `EXCHANGE_BROKER_WELLKNOWN_URL is required` | Refusing is deliberate — with nothing to check the withdrawn-key list against, a withdrawn key would keep working | Point it at the Broker's `/.well-known/fora.json`. |
| **A catalog push refused `resource_exhausted` (HTTP 413), same submission every time** | The ingester splits a feed by entry COUNT (256, the protocol's bound on one request), and the Exchange also bounds the request SIZE at 1 MiB. A feed of unusually large entries builds a submission under the count bound and over the size one, and re-running rebuilds it identically | Split the feed file so the offending range goes as smaller submissions — the report names the entry range that was refused. This refusal is the Exchange's own verdict, so it prints `REFUSED` and nothing from that range is stored; the ranges stored before it stay stored, so re-running the split feed is safe. |
| **Every catalog push rejected, `missing_resource_owner_id`** | The publisher's `fora.json` names no payee (the one who gets paid) for this Exchange — it does not "attest" one. The payee is never guessed, and there is no fallback to the tenant id | The manifest needs `exchanges[].ext.resource_owner_id` on the entry whose `domain` equals your `EXCHANGE_DOMAIN`. §4.2. |
| Every push rejected, `caller_not_in_catalog_contributors` | The publisher's `fora.json` does not list the pushing key's identifier, or could not be fetched at all | Add it to `catalog_contributors[].domain`, or make it equal the manifest's own `domain`. §4.2. |
| A contributor's first catalog push, or an agent's registration, answered `unavailable` (HTTP 503; 502 on `agents/register`) although its key directory is up | The Exchange reads the directory the way the protocol SDK does, and states a body that is not a key directory at all (not JSON, JSON of another shape, or over 1 MiB) as unavailable, the same as an unreachable host | Fetch `https://<host>/.well-known/http-message-signatures-directory` and check it is a key set (`{"keys":[...]}`) of at most 1 MiB. [`CONFIGURATION.md`](CONFIGURATION.md) §2.3 |
| Every push rejected, `publisher_domain_not_registered` | The entry's domain is not registered as a publisher site on this Exchange, so nothing here knows who owns it | Register the site under its publisher first. §4.2. |
| Every push rejected, `resource_owner_id_mismatch` | The site IS registered, but under a different publisher than the one its `fora.json` attests for this Exchange. Attaching a domain to a payee needs both halves to agree | Either correct `exchanges[].ext.resource_owner_id` in the manifest, or re-register the site under the publisher the manifest names. §4.2. |
| A publisher cannot save another domain: "you already have 10 domains that are not verified" | Their payee holds `consoleapi.MaxUnverifiedDomains` sites on this seat that are saved and not verified, the most one payee may hold without proving any | They finish setting one up, which verifies it and frees a place, or you release one they do not want with the recovery procedure in §4.2 |
| Every push rejected, `publisher_domain_listing_not_active` | This Exchange does not sell that site yet: either it has no listing for it, or the listing is still `PENDING_MANIFEST` -- saved in the console but not verified | Nothing in the feed fixes this. The publisher serves the manifest the console gave them, installs the package and finishes the domain's setup in the console; the console then verifies it and the listing goes `ACTIVE`. §4.2. |
| Every push rejected, `publisher_manifest_version_refused` | The publisher's `fora.json` carries a document `ver` this Exchange does not accept — another major, or none at all. The Exchange reads major 1 and any minor of it, and refuses the document before reading anything else in it | Serve `"ver": "1.0"` (or another `1.x`) in the manifest. Retrying without changing the document gives the same answer. |
| Every push rejected, `tenant_mismatch` | The push named a `tenant_id` that disagrees with the seat this Exchange sells as. The server resolves the seat from its own `EXCHANGE_DOMAIN` and does not honour a disagreeing client value | Send no `tenant_id`, or send the seat's own id. The server-resolved seat is authoritative either way, so omitting it is always correct. |
| A push rejected, `invalid_license_terms` | One of the entry's terms failed an ingest-tier check — a `pricing.unit` or `quota.metric` token that is not in the registry, or a restriction naming the same token as both permitted and prohibited once aliases and letter case are folded (`scrape` and `crawl` are one token, so are `CRAWL` and `crawl`). The wire-tier term rules are refused earlier and never reach this reason | The second line the refusal writes, `license term refused at ingest`, carries the `rule` id, the entry-relative `path` and the offending `token`. For an unregistered token, register it or prefix it `vendor:` to declare it custom. For a collision the `token` is the folded form both spellings resolve to; drop it from one of the two lists. `fora-ingest --check feed.jsonl` reports both readings of it before a push. |
| A push rejected, `invalid_entry` | The entry would not convert for storage: a missing `domain` or `path`, or terms and metadata that would not marshal. It is a backstop — the wire rules refuse an empty `domain` or `path` earlier, so seeing this reason means the entry got past them | Rare, and worth reporting rather than working around: it means the wire rules and the storage conversion disagree about the same entry. The `uri` names the line. |
| A push rejected, `uri_owned_by_other_resource` | The URI the entry materialises already belongs to a DIFFERENT catalog row, or two lines in the same push claim it under different keys. A URI belongs to exactly one row | Re-pushing the same resource is not a conflict — it updates in place. A real collision means two lines carry the same `domain` + `path` under different `content_id` values; correct the `content_id`. |
| Purchases denied with `currency mismatch` | The catalog's terms are priced in one currency and the ledger is configured for another. A deployment is single-currency | Compare `EXCHANGE_BILLING_LEDGER` (`978`=EUR, `840`=USD) against the currency in the catalog's `pricing`. §3.2. |
| **A committed transaction has no ledger posting** | The posting is best-effort: it happens after the transaction commits and its failure cannot fail the request. A crash, or a brief ledger failure, in that window leaves a charge unposted | Find it in the logs and put it right by hand — §3.2. Nothing does this automatically. |
| Admin API unreachable | The allowlist refuses everything by default **and** has no effect behind a proxy, so "too strict" and "not applied at all" look identical | [`DEPLOYMENT.md`](DEPLOYMENT.md) §7. Bind the port directly; read `exchange.admin.ip_reject` for the address actually seen. |
| **One agent's purchases all denied, `outcome=REJECTED_REPORTING_OVERDUE`** | The agent owes usage reports. It is refused while more than 10 obligations are overdue, or more than one in five of its due obligations is unreported — and with no history, the first missed report is one out of one, so it blocks | The agent files the missing reports through `ReportUsage`. They are accepted however late they are, and each accepted report clears one. Nothing needs doing on this side, and the deadline must never be edited in the database. The denial arrives as the item's `denial_reason` under a 200, not as an HTTP error, so an agent that only checks the status code will look like it is silently getting nothing. |
| **Offer prices are out of date** | The copy the Exchange keeps in memory is rebuilt at boot and after each catalog push — nothing else refreshes it, so a row edited directly in the database is invisible | Run the ingest tool again (§4.2), or restart. Prefer the ingest tool: it re-validates. |
| **Discovery fails for one listed URL instead of returning its offer, and takes the rest of the response with it** | The stored term names the same token as both permitted and prohibited once aliases and letter case are folded — the shape a push is refused for today, in a row written before that refusal existed. Stored terms ride out on offers and the Exchange validates its own responses, so it refuses to answer with that resource. A discovery call naming several URLs fails whole, so healthy resources in the same request stop answering too | The publisher re-pushes the resource with the token dropped from one of the two lists. A push upserts on the row's `resource_id`, which the entry's `content_id` decides, so the corrected push must carry the same `content_id` the stored row was written under; then the corrected term replaces it in place — no row is deleted and nothing is edited in the database. `fora-ingest --check feed.jsonl` names the offending token before a push, without a key or a running Exchange. |
| Discovery returns nothing for a URL you know is listed | The URL's domain has no tenant, or its stored form differs from the one asked for | Check the catalog rows for that prefix — §3.2. |
| **One agent is denied every paid purchase: `agent is not registered for paid content: call Register first`** | The agent has no account. It authenticates fine and can still fetch free content, but it has nothing to charge. Adding a row to `fora.agents` by hand does **not** fix this — that row's `billing_ref` stays empty | The agent must call the `Register` RPC itself. **Do not fund it** — there is no account to fund yet. §4.2. |
| One agent denied with `account is switched off: contact the operator to turn it back on` | The agent registered, but its account row in the registry has `active = false` | Switch it back on — §4.2. |
| One agent denied with `no billing account found for this agent: contact the operator` | The agent's `fora.agents` row carries a `billing_ref` that the registry does not know. The two databases disagree | Escalate. Do not delete the `billing_ref`: the ledger balance is keyed on it. §5. |
| Exchange will not start | `EXCHANGE_DSN` unset, the catalog database unreachable, or `EXCHANGE_LOG_LEVEL` set to something other than `DEBUG`, `INFO`, `WARN` or `ERROR` | Read the `exchange.exit` line; it names the cause. |
| Exchange will not start, `err` begins `sor:` | A **different** database. `EXCHANGE_SOR_DSN` is unset, or the account-registry database does not exist or is unreachable | [`DEPLOYMENT.md`](DEPLOYMENT.md) §8 lists each `sor:` message and its fix. This one does not fix itself. |

### 3.2 Diagnostics

**Trace one request end to end.** The `request_id` is the same in all three
services; no line at all means the request never reached the Exchange, so look at
the Broker.

```bash
docker compose logs exchange | grep '"request_id":"<ID>"'
# Expect: the request-path lines for that request — an exchange.execute_transaction
#         or exchange.report_usage outcome line, plus any httpsig rejection
```

**Inspect a transaction and its evidence row.**

```sql
SELECT transaction_id, tenant_id, agent_id, resource_id, unit_cost, currency,
       billing_id, expiry, created_at
  FROM fora.transaction_log
 WHERE idempotency_key = '<request idempotency key>:<offer id>';
-- Expect: one row. Each item of a request is keyed by the request's
-- idempotency key, a colon, and the item's offer id. A row here is an item
-- that was delivered: a denied item never writes one.

SELECT transaction_id, offer_id, requester_id, request_id, created_at
  FROM fora.transaction_evidence WHERE transaction_id = '<transaction id>';
-- Expect: one row, holding the signed offer and both parties' signatures
-- verbatim, so a dispute can be settled from the row alone. Append-once (§5).
```

**Inspect the denials recorded for a request.**

```sql
SELECT created_at, denial_reason, offer_id, resource_id, resource_owner_id
  FROM fora.transaction_events
 WHERE event_type = 'BILLING_AUTHORIZATION'
   AND agent_id = '<agent id>'
   AND idempotency_key LIKE '<request idempotency key>:%'
 ORDER BY created_at;
-- Expect: one row per item the Exchange refused AND could attribute to a
-- publisher. An item refused for an offer signature that did not verify, or for
-- a resource no longer in the catalog, writes no row; neither does a request
-- that proved no possession of the agent key. The rules are in
-- docs/architecture/adr-027-transaction-event-journal.md. Append-once (§5).
```

**Find committed transactions with no ledger posting.** Take the ids out of the
error log, then confirm each one really committed:

```bash
docker compose logs exchange | grep 'billing_record_failed_best_effort' \
  | python3 -c "import sys,json;[print(json.loads(l)['transaction_id']) for l in sys.stdin]"
# Expect: one transaction id per unposted charge — empty output is the good case
```

```sql
SELECT transaction_id, agent_id, unit_cost, currency, created_at
  FROM fora.transaction_log WHERE transaction_id IN ('<id>', '<id>');
-- Expect: one row per id. Each is content delivered and not charged for;
-- put it right in the ledger by hand (§6).
```

**Confirm which billing adapter is live.**

```bash
docker compose logs exchange | grep 'billing adapter'
# Expect: {"level":"INFO","msg":"billing adapter: tigerbeetle","ledger":978,...}
# No output means the free adapter: every charge approved, nothing recorded.
```

**Read the served documents** — the two commands in §2.1. A `domain` that does not
match what the Broker registered, or an `endpoint` that does not match the
Broker's entry for you, produces empty offer lists with no error anywhere.

**List tenants, agents and catalog size.**

```sql
SELECT tenant_id, domain, fee_rate_bps,
       activate_new_agents_by_default FROM fora.tenants;
-- Expect: activate_new_agents_by_default is TRUE unless you changed it. Only the
-- default tenant's copy is read (§4.2) — it decides whether a newly registered
-- agent starts able to buy.

SELECT agent_id, billing_ref IS NULL AS cannot_buy, registered_at
  FROM fora.agents ORDER BY registered_at DESC LIMIT 20;
-- Expect: cannot_buy = f for every agent that has called Register.
-- cannot_buy = t is the single most useful diagnostic here: that agent has no
-- account, so every paid purchase it attempts is denied (§3.1). Funding it does
-- not help; it must call Register.

SELECT tenant_id, count(*) AS rows, max(updated_at) FROM fora.catalog
 GROUP BY tenant_id;
SELECT resource_id, uri, resource_owner_id, pricing FROM fora.catalog
 WHERE uri LIKE 'https://www.publisher.example/%' LIMIT 5;
-- Expect: pricing carries the unit cost and currency — the value to compare
-- against EXCHANGE_BILLING_LEDGER when purchases fail on currency mismatch.
```

### 3.3 Gotchas

- **On day one the Exchange starts, but rejects every signed request until the
  Broker answers.** The Broker's document is fetched at runtime, not at boot;
  until DNS, certificates and the Broker are all live the Exchange runs with
  passing health checks and logs
  `exchange.httpsig.broker_wellknown_unavailable` on each rejected request. It
  fixes itself the moment the document is reachable. A process that instead
  exits and restarts has a configuration fault — see
  [`DEPLOYMENT.md`](DEPLOYMENT.md) §8 for both shapes.
- **`ADMIN_ADDR` defaults to `:8082`, which is the Broker's published port.**
  Running both on one host with host networking collides. Change one.
- **A proxy in front makes the admin allowlist useless.** It reads the
  connection's own source address and never `X-Forwarded-For`, so behind a proxy
  every caller looks like the proxy. Bind the admin port directly.
- **Redis-less mode is announced, not silent — but only once, at boot.** With
  `REDIS_URL` unset the Exchange logs `exchange.httpsig.replay_store_disabled` and
  carries on. Requests then succeed exactly as they would with Redis, so that single
  line is the whole signal. Correct at one instance, unsafe above.
- **The image is amd64 only.** There is no ARM build.
- **Every Exchange owns two databases: one catalog database and one account
  registry.** The Exchange builds its in-memory catalog by reading the whole
  catalog table without filtering by tenant, so every row in that database is
  discoverable through this Exchange. Two publishers who must not appear in each
  other's discovery results need two Exchanges with two catalog databases, not one
  Exchange with two tenants — and each of those Exchanges needs its own account
  registry as well, because an agent's `billing_ref` is issued by one Exchange and
  means nothing to another.
- **There is no shared key file.** The Exchange learns every verification key
  from the key owner's own published documents: the Broker's directory for the
  relay key and the withdrawn-key list, and each signer's own directory for
  that signer's key.

---

## 4. Procedures

### 4.1 Routine operations

**Restart.**

```bash
docker compose restart exchange
# Expect: the boot sequence from DEPLOYMENT.md §6, ending in
#         "exchange listening" and "admin listening"
```

It costs about ten seconds and loses nothing durable, but three things happen
worth knowing: the in-memory catalog is rebuilt from the database, in-process
replay memory is discarded if `REDIS_URL` is unset, and any purchase in progress
fails — its ledger reservation is not released explicitly and expires on its own
about six minutes later. Delivery URLs already issued keep working; the CDN
verifies them without asking the Exchange.

**Configuration needs a restart.** There is no live reload of any setting: not
the signing keys, not the Broker URL, not the billing configuration. Two
exceptions: a fee rate or reporting policy set through the admin API (§4.2)
takes effect immediately, and **verification keys were never configuration** —
they are fetched from the key owners' published documents and refresh on their
own as the cached copies expire (`EXCHANGE_DIRECTORY_TTL`,
`EXCHANGE_REVOCATION_POLL_INTERVAL`), with no restart involved.

**Running more than one instance.** Above one instance `REDIS_URL` is mandatory
and every instance must use the **same** Redis. Check each instance's boot log for
`exchange.httpsig.replay_store_ready`; a `replay_store_disabled` line on any of them
means that instance is replay-protecting itself alone.
Start one instance first so it applies migrations alone, confirm `/healthz`, then
add the rest.

**Read a tenant's configuration by SQL.** The admin API only writes values — there
is no RPC that reads them, and that is deliberate: the write calls return the
values they saved, so tooling confirms a write without a separate read call.

```sql
SELECT tenant_id, domain, fee_rate_bps, fee_rate_notes,
       reporting_policy, activate_new_agents_by_default
  FROM fora.tenants WHERE tenant_id = '<tenant>';
-- Expect: one row. fee_rate_bps is basis points — 1500 means 15%.
-- activate_new_agents_by_default decides whether a newly registered agent can buy
-- straight away (TRUE) or waits for you to switch it on (FALSE). It is read from
-- the default tenant only (§4.2); on any other row it is never read.
```

### 4.2 Publisher, catalog and key procedures

#### Onboard a publisher

**1. Create the seat, once per deployment.** There is no RPC for this; it is a
database row. The seat is this Exchange's own selling identity, not a publisher,
so you create exactly one and every publisher you onboard afterwards is sold
through it.

```sql
INSERT INTO fora.tenants (tenant_id, domain, ed25519_key_ref, rsa_key_ref,
                          fee_rate_bps, activate_new_agents_by_default)
VALUES ('seat', 'exchange.example', 'exchange-primary', 'cf-rsa-primary',
        1500, TRUE);
-- Expect: INSERT 0 1
```

`domain` is the hostname this Exchange answers on — the value of your
`EXCHANGE_DOMAIN`, not a publisher's site. A push whose `EXCHANGE_DOMAIN` matches
no seat row is refused outright with `failed_precondition`.

`ed25519_key_ref` and `rsa_key_ref` point at the PRIVATE keys this Exchange signs
delivery URLs with, one per scheme. They stay here whatever a publisher's CDN is.
`rsa_key_ref` may be left NULL if no site you sell is behind CloudFront.

`fee_rate_bps` is your commission and can be changed later without a restart.

`activate_new_agents_by_default` decides whether a newly registered agent can buy
immediately. **Name it explicitly, as above.** Omit it and the row still succeeds,
silently taking `TRUE` — every new agent is admitted the moment it registers, with
no review. Set it to `FALSE` if you want to approve each agent by hand (see
"Register an agent" below).

**Only one tenant's copy of that column is ever read.** The Exchange takes the
policy from the tenant named by `EXCHANGE_DEFAULT_TENANT`, or by `EXCHANGE_DOMAIN`
if that is unset. With one seat per deployment those name the same row. And until
that row exists, **every** agent registration fails.

**1b. Register the publisher, its site, and the listing.** Three more rows, and
all three are required before a push for that domain is accepted.

**Prefer the publisher console.** When this deployment runs one (see
`EXCHANGE_CONSOLE_WELLKNOWN_URL` in [`CONFIGURATION.md`](CONFIGURATION.md)), the
publisher creates all three themselves: they sign in, register their business,
save the site, publish the document the console gives them, install the
package, and press the button that verifies it. Nothing below is needed, nobody
touches the database, and the listing goes live only once the site itself
proves it authorized you.
The SQL that follows is for a deployment with no console, and for recovering one
publisher whose registration went wrong.

```sql
-- The payee. One per publisher, whatever number of sites they run.
INSERT INTO fora.publishers (publisher_id, display_name)
VALUES ('pub_0c4f…', 'Stoa Press');

-- The site. cdn is the integration in front of it: cloudflare, fastly or
-- cloudfront. All three verify the delivery address inside an edge function the
-- publisher deploys, so all three sign with ED25519 and none of them needs a
-- CloudFront key-pair id. The AWS_CLOUDFRONT_RSA scheme remains for sites an
-- operator built the other way, where CloudFront itself checks the address
-- against a trusted key group; those also need cloudfront_key_pair_id, which the
-- publisher reads out of their own AWS account after uploading the public key.
INSERT INTO fora.publisher_domains (domain, publisher_id, signing_scheme, cdn)
VALUES ('www.publisher.example', 'pub_0c4f…', 'ED25519', 'cloudflare');

-- The listing: this seat offers that site. It starts PENDING_MANIFEST and only
-- an ACTIVE listing accepts a push, so a first onboarding sets it here.
-- terms is the licensing this seat sells the site under: which uses are
-- permitted, at what price, and whether attribution is required. sitemap_url
-- is where the automatic catalog import reads the site's pages from; left out,
-- it is empty and the site is never imported automatically.
-- import_replaces_manual_terms, left out, is false: the import leaves alone the
-- pages of the site that another source pushed (see Automatic catalog import).
INSERT INTO fora.publisher_domain_listings (tenant_id, domain, terms, sitemap_url,
                                            state, verified_at, last_checked_at)
VALUES ('seat', 'www.publisher.example',
        '{"pricing_model":"free","rate":"","permitted_functions":["search","ai-input"],'
        '"attribution_required":false}'::jsonb,
        'https://www.publisher.example/sitemap.xml',
        'ACTIVE', NOW(), NOW());
```

`publisher_id` is the value the publisher's manifest must attest as
`ext.resource_owner_id` for this Exchange. If the two disagree the push is
refused with `resource_owner_id_mismatch`, and nothing is stored.

**2. Generate the catalog-contributor key** — the key the ingest tool signs
pushes with, not the Exchange's own key:

```bash
scripts/gen-contributor-key.sh <contributor-id>
# → deploy/publisher-keys/<contributor-id>-key.json
```

The contributor id is the hostname of the contributor's key directory. The
public half must be published in the contributor's own well-known directory,
served at `https://<contributor-id>/.well-known/http-message-signatures-directory`
under the media type `application/http-message-signatures-directory+json` with
a response signed by that key (the Exchange learns it by fetching that
directory — there is no shared key file to update); the private half is what
you pass to `--key` below, so treat it as a secret and keep it out of the
repository. Every push names `https://<contributor-id>` as its
`Signature-Agent`, and the Exchange reads the key from that same directory, so
it must be served there.

**3. Have the publisher publish `/.well-known/fora.json`.** The document's shape,
with every field traced to the setting that produces it, is in
[`../../deploy/publisher-wellknown/`](../../deploy/publisher-wellknown/); the copy
there is the one the test suite pins to the Edge Worker's output. Two fields
matter from this side, and both fail silently when wrong:

- `exchanges[].domain` must equal your `EXCHANGE_DOMAIN` **exactly**, and that
  entry's `ext.resource_owner_id` is the **payee** — the one who gets paid. Every
  euro of this publisher's revenue goes to it. Absent or empty, every push is
  rejected. Present but wrong, nothing is rejected: the value is stored and paid
  out as written.
- `catalog_contributors[].domain` must contain the kid from step 2 — unless that
  kid equals the manifest's own `domain`, which is also accepted.

**4. Verify end to end.** Load the catalog (below), then confirm a real agent can
discover and buy. The Edge and Broker each own a half of that path — see
`src/edge/RUNBOOK.md` §4.2 and `src/broker/RUNBOOK.md` §4.2 rather than repeating
their checks here. On the Exchange side:

```bash
curl -s https://www.publisher.example/.well-known/fora.json
# Expect: the document described above — matching the reference copy in
#         deploy/publisher-wellknown/ — served over HTTPS with a valid certificate
```

```sql
SELECT count(*) FROM fora.catalog WHERE resource_owner_id = 'pub_0c4f…';
-- Expect: the number of entries you pushed for that publisher's sites.
-- Count by resource_owner_id, not by tenant_id: every publisher you sell shares
-- the one seat, so tenant_id counts the whole catalog.
```

#### Register an agent, and switch its account on or off

An agent must have an **account** before it can buy anything. Free content is
unaffected — an agent with no account can still fetch it — but every paid
purchase is denied.

The account is created once, by the agent, through the `Register` RPC. It cannot
be created by you, and it cannot be created with SQL: the Exchange takes the
agent's identity from the signature on the request, so only the agent itself can
ask. What one `Register` call produces:

| Step | Where | What lands |
|---|---|---|
| 1 | Account registry (`EXCHANGE_SOR_DSN`) | A row in `sor.agent_accounts`, keyed on a fresh `billing_ref`. Its `active` column is copied from the default tenant's `activate_new_agents_by_default`. |
| 2 | Ledger | An account for that same `billing_ref`. This is where the agent's money lives. |
| 3 | Catalog database (`EXCHANGE_DSN`) | The `billing_ref` is written onto the agent's `fora.agents` row. |

`billing_ref` is a random identifier the Exchange creates. It is the one string
that ties the three together. Calling `Register` again returns the same
`billing_ref` and changes nothing.

**Adding a row to `fora.agents` by hand does not register an agent.** Such an
agent authenticates normally and can fetch free content, but its `billing_ref` is
empty, so every paid purchase is denied with `agent is not registered for paid
content: call Register first`. There is nothing you can do from your side; the
agent has to call `Register`.

Check whether an agent is registered:

```sql
SELECT agent_id, billing_ref FROM fora.agents WHERE agent_id = '<agent>';
-- Expect: one row. A NULL billing_ref means not registered — it cannot buy.
```

Then look up the account itself, in the **other** database:

```sql
SELECT billing_ref, subdomain, active, legal_entity, email
  FROM sor.agent_accounts WHERE billing_ref = '<billing_ref from above>';
-- Expect: one row. subdomain is the agent's identity; active decides whether it
-- may buy right now.
```

**Switching an account off and on again** is a direct update in the registry
database. **There is no RPC and no admin endpoint for this yet.**

```sql
UPDATE sor.agent_accounts SET active = FALSE WHERE billing_ref = '<billing_ref>';
-- Expect: UPDATE 1
```

The change takes effect within **30 seconds** — the Exchange re-reads the status
at most that often, tuned by `EXCHANGE_SOR_CACHE_TTL`. No restart is needed. A
switched-off agent is then denied with `account is switched off: contact the
operator to turn it back on`. Set `active = TRUE` to reverse it.

Two behaviours worth knowing. If the registry is **unreadable** during a
purchase, the Exchange lets the purchase through and logs
`event=sor_active_check_failed` — a switched-off agent may buy until the registry
answers again; the ledger balance is what still limits it. And the columns
holding the agent's company details (`legal_entity`, the address columns, `email`)
are all optional: the Exchange stores whatever the agent sent and checks none of
it. Deciding that a registration is complete enough is your judgement, expressed
through `active`.

#### Run the ingestion pipeline

`fora-ingest` reads a JSON-L feed (one JSON object per line), maps each record to
a catalog entry, signs the batch and pushes it over the Exchange's catalog RPC.
There is no way to write to the catalog directly by SQL.
**This binary is not built into the Exchange image** (the automatic importer,
`fora-import`, is — see the next procedure), so build it first, then run it
with its **three required flags** — none has a default, and there is no
committed contributor key to fall back on:

```bash
go build ./src/exchange/cmd/fora-ingest
# Expect: no output, and a ./fora-ingest binary

./fora-ingest \
  --exchange-url https://exchange.example \
  --tenant publisher \
  --key /secrets/catalog-contributor.json \
  feed.jsonl
# Expect: push: accepted=1284 warnings=3
#         followed, for a feed larger than one submission, by one line per
#         submission naming the entry range it carried and whether it was stored
```

The feed path is an optional positional argument; omit it and the feed is read
from standard input, so piping `feed.jsonl` in works identically.

When the endpoint host differs from the Exchange's published identity (for
example, a local port forward), add `--exchange exchange.example`, matching
the server's `EXCHANGE_DOMAIN`. Otherwise the recipient defaults to the URL host.
`--exchange-domain` is also accepted as an alias for `--exchange`.

**Read the result as pass or fail: exit status 0 is the only passing result.**
The Exchange stores or refuses a submission whole — never part of one — and the
binary exits non-zero on the first failure, so a partial catalog never passes
silently. A feed larger than the wire bound on one submission (256 entries at
the pinned protocol revision) is sent as several submissions in feed order; on a
failure the report says which entry ranges were stored, which submission the run
stopped at and why, and which entries were not sent, and re-running the same
feed is safe — the catalog push is an upsert, so entries already stored are
stored again unchanged.

**A submission the run stopped at is printed one of two ways, and they mean
different things.** `REFUSED` is the Exchange's own verdict: the request reached
it, it refused the submission whole, and **nothing from that range is stored**.
`NOT CONFIRMED` means no verdict came back — the call timed out, the connection
dropped, or the Exchange answered success over a count that disagreed with what
was sent — so **that range may or may not be stored**, and only a re-run
settles it. Do not read `NOT CONFIRMED` as "absent"; re-running is safe either
way, because the push upserts. A rerun clears a refusal a later run would not repeat: a
transient failure, or an entry you have corrected. It does not clear one the
same feed reproduces — see the `resource_exhausted` row in §3.1. Warnings are
notes about small problems and do not block. Each refusal names its URL and
reason; the common ones are in §3.1.

Before pushing, `./fora-ingest --check feed.jsonl` runs the same parse and
mapping plus the SDK's entry validation locally — no key, no Exchange, no
tenant — and exits 1 listing every violation with its record, rule id and field
path, so a feed is fixed in one round rather than one refusal at a time.

`fora-ingest` is the manual path: a feed a publisher prepared, pushed by hand.
A site registered with a sitemap is imported and refreshed on a schedule
instead, priced from the licensing chosen for the site — see the next
procedure.

#### Automatic catalog import

`fora-import` builds and refreshes a publisher's catalog from its sitemap
without the publisher preparing a feed or running anything. It ships in the
Exchange image as a second binary and runs as the `importer` compose service,
started by a systemd timer on the host.

**What it does.** One run works through every **eligible listing** of the seat
— a `fora.publisher_domain_listings` row that is `ACTIVE` and has a
`sitemap_url`. Registration records the sitemap together with the licensing
the publisher chose (`terms`); a listing created by hand (step 1b of **Onboard
a publisher**) has one only if its INSERT sets `sitemap_url`. A listing with no
sitemap recorded is skipped, never imported from a guess. For each listing it builds the listing's license
terms (see **Pricing** below), walks the sitemap (or sitemap index) within the
limits below, keeps the pages on the listing's domain and inside its
`content_path_scope`, builds one catalog entry per page carrying those terms,
validates the whole listing locally, and pushes it through the signed catalog
RPC as the contributor identity below — the same path `fora-ingest` takes, in
the same batches. It pushes only the pages that are its own (see **Pages
another source pushed** below), and a page it pushes again goes back under the
identity it is stored under, so a rerun updates every page without duplicates
-- as long as the deployment's catalog URI scheme stays what it was when the
pages were stored (see **Keep the catalog URI scheme** below).
A sitemap URL with a query string (`/news/a?utm_source=x`)
is left out: a catalog path cannot carry a query, and the importer never
strips one, because `/article?id=42` is not the page at `/article`. The other
pages of that sitemap still import.

**Which pages the scope takes.** A page is inside `content_path_scope` by the
rule the edge applies to every request it gates: the page's path is
percent-decoded, repeated slashes are merged, and it is compared with each
entry as a plain prefix, without regard to case. So `/News/` takes `/news/a`,
`/café/` takes `/caf%C3%A9/b`, and `/news` also takes `/newsletter/x`. An
entry is lower-cased but never decoded: write `/café/`, not `/caf%C3%A9/`. An
entry written percent-encoded does not match the pages it names, at the edge or
here, so a listing with one imports nothing under it. A page whose path cannot
be decoded, or holds a backslash or a `.` or `..` segment, is left out whenever
the listing has a scope: the edge counts such a request as licensed only because
it cannot place it. A listing with no scope offers the whole site. The edge's
rule and the importer's copy of it (`internal/licensedscope`) are pinned to one
set of cases, `testdata/licensed-scope-vectors.json`. Only membership is decided
on the decoded path: a page is stored and sold under its path exactly as the
sitemap spells it (see **Pages another source pushed** below).

**Pricing.** The terms every imported entry carries are built on **every
run** from the licensing the publisher chose at registration — free or charged
per access, the rate, the permitted and prohibited uses, attribution — priced
in the deployment's denomination: `EXCHANGE_TERMS_CURRENCY` (default `EUR`)
and `EXCHANGE_TERMS_UNIT` (default `fetches`), the same two settings the
console previews terms in. The compose stack gives both services the same
values from its `exchange_terms_currency` and `exchange_terms_unit` inputs.
**Changing either setting re-prices every imported page of every listing at
the next run**: the rate stays the number the publisher entered and only the
currency or the unit changes, so change them only deliberately.

The built terms are checked before the sitemap is fetched, with the same rules
a push applies. Licensing that cannot be read or built into terms, or a
denomination the Exchange refuses (an unregistered unit, say), makes that one
listing `failed` (the reason says which, and names the rule) and touches
nothing else; the other listings of the run go ahead.

**It only adds and updates.** A page that disappears from a later sitemap
keeps its catalog entry; nothing is ever deleted by the importer. `complete`
below means "every page the configured sitemap lists is in the catalog", not
"the catalog equals the website": a sitemap that omits pages leaves them out,
and one that drops pages leaves their entries in place. Removing content is a
separate, manual operation.

**Which catalog rows are the importer's.** Every push the importer makes
carries the request header `X-Fora-Catalog-Source: auto-import`, and the
Exchange records on each row a push stores whether that push carried it:
`fora.catalog.auto_imported` is `true` for the rows of a push with the header
and `false` for the rows of any other push, so the last push of a row decides.
`fora-ingest` never sends the header, so a page pushed by hand is recorded as
not the importer's even though it is signed with the same contributor key.
Every row that existed before the column did reads `false`. The header is a
label, not a credential: the push's signature does not cover it, and any
contributor a site authorizes could send it on its own push. All that would do
is mark the rows of that same push as the importer's. It authorizes nothing.

**Pages another source pushed.** Before it pushes a listing, the importer
reads the rows the catalog already holds for the site, and decides for each
page it built from the sitemap:

| Row for the page | `auto_imported` | Listing's `import_replaces_manual_terms` | What the importer does |
|---|---|---|---|
| None | — | — | Pushes the page with the listing's terms |
| Yes | `true` | either | Refreshes it: pushes the listing's terms, carrying the row's content id, title and metadata back unchanged |
| Yes | `false` | `false` | Skips it: the page keeps its own terms, title and metadata, and counts in `last_import_skipped` |
| Yes | `false` | `true` | Takes it over: as a refresh, and the row is the importer's from then on |

So a page pushed by hand — including one the operator pushes with
`fora-ingest` — keeps its own terms unless the listing's
`import_replaces_manual_terms` is `true`. That column is the publisher's
answer, given with the site's terms, to whether the import may take over the
pages already in the catalog; it is `false` unless they said yes. The console
asks it on the last step of adding a domain, as **Replace the terms of pages
that are already in the catalog**, and shows the answer on the domain's page.
Either way the importer never clears a page's title or metadata, and a page
stored under a content id keeps it, so its URI stays with its row. Pages
compare on domain and path exactly as pushed: `/news/caf%C3%A9` and
`/news/café` are two different pages.

Two kinds of row are always skipped, whatever the listing says, because the
importer cannot push the page again under the row's own identity: a row whose
resource id predates the current identity scheme, and a page with two rows, one
per URI scheme (see **Keep the catalog URI scheme**). They count in
`last_import_skipped` with the pages left to their source, and nothing in the
row or the log tells the two causes apart: compare the site's rows with the
table above.

**Keep the catalog URI scheme.** A catalog URI starts with the scheme in the
Exchange's `EXCHANGE_CATALOG_URI_SCHEME` (`https` unless set), and a stored row
keeps the URI it was stored under. Changing the setting once pages are stored
turns every later push of a page stored under a content id into a URI move,
which the Exchange refuses, whether `fora-ingest` or the importer sends it. The
importer matches pages without the scheme, so after such a change a page it
may push is pushed under the new scheme: one stored under a content id gets its
whole submission refused on every run, and one stored without gets a second
row, after which the page is skipped. Do not change the setting on a deployment
whose catalog holds pages; if it has to change, reset the catalog and push it
again.

On a stack where the importer ran before `auto_imported` existed (development or
staging), the pages it wrote read `false` like every other old row: they are
skipped from then on, or taken over where the listing allows it. Reset that
stack's catalog if they should be refreshed again; nothing guesses which rows an
earlier import wrote.

**Pages pushed by hand while a run pushes.** The importer reads a site's rows
once, before its first submission, and decides every page from that read; the
window until its last submission is as long as the listing's run, up to
`IMPORT_LISTING_TIMEOUT`. A page it was going to push that is pushed by hand
inside that window ends one of two ways:

- If the page already had a row, or is pushed by hand without a content id, the
  importer's push overwrites the hand-pushed version and records the page as
  the importer's again, so the next run refreshes it instead of skipping it.
  Push that page by hand again to restore it.
- If the page had no row and is pushed by hand with a content id, the
  importer's entry for it claims a URI another resource now owns, and the
  Exchange refuses that entry's whole submission: the run ends `partial` or
  `failed`. The next run finds the row and treats it as the table says.

The window is accepted: closing it would need a conditional overwrite on the
Exchange side, which would change how a submission is counted. Avoid pushing a
site's pages by hand while its import runs.

**Large stored metadata.** The importer splits a listing into submissions of at
most 256 entries, as `fora-ingest` does, and never by size. A page it pushes
again carries its stored title and metadata, so 256 such pages with unusually
large metadata can make one submission larger than the Exchange accepts. That
submission is refused with `resource_exhausted` (see §3.1) on every run, and the
importer has no setting to split smaller. Push those pages again by hand with
less metadata.

**Stored data that no longer validates.** A page the importer pushes again is
checked, with the content id, title and metadata carried over, by the rules
every push applies, before anything is signed. A page whose stored metadata
cannot be read or no longer passes — only a row stored under older rules can be
in that state — fails the whole listing before anything is sent: `failed`,
every count zero, and the reason names the page and what is wrong with it
(the rule it breaks, or why it cannot be read). Nothing changes,
and the listing fails on every run until the page is fixed: push that page by
hand with metadata that validates. It is then a page pushed by hand, skipped or
taken over on the next run as the listing says.

**The contributor identity.** The importer signs as an operator-held
catalog-contributor key, distinct from the Exchange's own signing keys. Its
`kid` is the contributor hostname the stack provisions
(`catalog-contributor.<domain>` by default — the `catalog_contributor_hostname`
Terraform output), where Caddy serves the public key at
`/.well-known/http-message-signatures-directory`. Each publisher must list that
hostname in its manifest's `catalog_contributors[]`; a manifest that does not
refuses every push from the importer, which is the publisher's way to stop it.
When the Exchange runs the console, that hostname must be the one in
`EXCHANGE_CATALOG_CONTRIBUTOR_HOST`: the console writes that host into every
manifest it generates and the domain check requires it, so a key with any
other `kid` is refused on every push.
On the host the private key is `/opt/fora/keys/importer/contributor-key.json`,
root-owned, mounted read-only into the `importer` service alone — the
`exchange` container has no such file, by design.

To rotate the key: delete `keys/contributor-key.json` in the stack directory,
re-run `gen-keys.sh` for the stack (the `kid` stays the hostname, so no
publisher manifest changes), and apply the stack. On the staging and demo
stacks that replaces the VM, because the key travels in user data; a stack that
delivers its bundle as a secret rolls the new bundle out instead.

**Schedule.** The timer is `fora-import.timer`, hourly by default, set by the
`importer_schedule` input of the compose-stack module (a systemd `OnCalendar`
expression). A change takes effect on the next apply; for an immediate change
on a running host, `systemctl edit fora-import.timer`, knowing the next apply
restores the module's value. A run missed while the host was down is made up
at boot (`Persistent=true`).

```bash
systemctl status fora-import.timer
# Expect: active (waiting), with the next trigger time
journalctl -u fora-import.service -n 50
# Expect: one importer.listing line per listing, then importer.done
```

Every `importer.*` line of one run carries the same `request_id`, and every
push of that run sends it as `X-Request-ID`, so the Exchange's log lines for
a scheduled push are found by that id.

**Manual rerun** — the same command the timer runs:

```bash
sudo docker compose -p fora -f /opt/fora/docker-compose.yml --profile importer run --rm importer
# Expect: one JSON log line per listing, "importer.done", and exit status 0
#         when every listing is complete
```

**Runs never overlap.** The process holds an advisory lock on the Exchange
database for its whole run. A second run that finds the lock held — a manual
rerun overlapping the timer, or the timer firing during a long manual run —
logs `importer.skipped`, writes nothing and **exits 0**: every listing's
recorded outcome is exactly what it was, and the systemd unit does not show as
failed. This is a run-level event, not a listing outcome. A database error
while taking the lock is a failed run and exits non-zero.

**Limits**, all environment on the `importer` service in the compose file
(the defaults are written there):

| Variable | Default | Bounds | Stops the walk as |
|---|---|---|---|
| `IMPORT_REQUEST_TIMEOUT` | `10s` | One sitemap fetch, reading the document included | `request-timeout` |
| `IMPORT_MAX_DOC_BYTES` | `1048576` | One sitemap document; a larger one fails the listing | `size` |
| `IMPORT_MAX_DOCUMENTS` | `100` | Sitemap documents fetched per listing, the root and every child of an index | `documents` |
| `IMPORT_MAX_RESOURCES` | `10000` | Unique pages per listing; one more fails the listing | `resources` |
| `IMPORT_LISTING_TIMEOUT` | `5m` | Walk plus submission for one listing | `deadline`, when it runs out during the walk; when it runs out during submission the run stops sending, and its outcome says what was stored (see the outcomes below) |

A walk that hits any of these limits is recorded `failed` with the reason and
**nothing is submitted** for the listing: the catalog is never rebuilt from a
partial reading of the sitemap, and the entries it already has stay. Any other
fetch failure — an unreachable or refused address, a refused redirect, an
answer other than 200 — is `source`, and a document that is not a well-formed
sitemap is `parser`. The reason appears as
`sitemap walk incomplete (<reason>): …` in `last_import_failure_reason` and on
the `importer.listing` log line, so `request-timeout` points at
`IMPORT_REQUEST_TIMEOUT` (or a slow site) and `deadline` at
`IMPORT_LISTING_TIMEOUT`. An address a sitemap lists never appears in a
failure reason or on an `importer.*` log line with its credentials: the user
part shows as `xxxxx`, and an address that is not a well-formed http or https
URL is described rather than repeated, together with the sitemap that listed
it.

**Reading the outcome.** Each run writes its result to the listing row it
worked on:

```sql
SELECT domain, last_import_attempt_at, last_import_success_at, last_import_outcome,
       last_import_entries, last_import_accepted, last_import_refused,
       last_import_unconfirmed, last_import_unsent, last_import_skipped,
       last_import_failure_reason
  FROM fora.publisher_domain_listings
 WHERE tenant_id = 'seat'
 ORDER BY domain;
-- Expect: one row per listing; last_import_outcome is NULL for a listing
--         never imported (not eligible yet, or no run since it became so)
```

| Outcome | Meaning |
|---|---|
| `complete` | Every entry built from the sitemap and not skipped was accepted. A valid empty sitemap is `complete` with zero entries, and so is a run that skipped every page, which sends nothing. Only this outcome moves `last_import_success_at`. |
| `failed` | Nothing was accepted: the listing's licensing could not be read or built into license terms, or the built terms failed validation (the reason names the rule), the sitemap could not be walked, an entry failed validation (the reason names the page), the site's catalog rows could not be read, a page it would push again has stored data that fails validation (the reason names the page), or the Exchange refused the first submission — an unauthorized contributor shows here. |
| `partial` | At least one submission was accepted, then a later one was refused or never sent. The accepted entries are in the catalog; a rerun resubmits everything and converges if the cause is gone. |
| `unconfirmed` | At least one submission got no verdict (a timeout, a dropped connection). Its entries may or may not be stored — a rerun settles it, and is safe because the push upserts. |

The six counts partition `last_import_entries` (every entry built):
`skipped` counts the pages never sent, because another source pushed them or
because they cannot be pushed again under their row's identity;
`accepted` is the Exchange's own count over the submissions it stored;
`refused` counts only submissions the Exchange answered with a verdict;
`unconfirmed` those without one; `unsent` the entries that never left the
importer, including everything after the submission a run stopped at.
Refused is never derived as "entries minus accepted"; each count comes from
what its submission reported. The entry ranges a failure reason names
(`submission 2 of 3 (entries 256-511)`) count only the entries sent, in
sitemap order; skipped pages are not in them. The exit status of the run is
non-zero when any listing is not `complete`, so a failing timer unit is the
first signal, and the row says which listing and why.

#### Rotate the tenant signing key

This is the Exchange's own Ed25519 key — the one that signs offers and Ed25519
delivery URLs.

```bash
openssl genpkey -algorithm ED25519 -out ed25519-private-v2.pem
# Expect: a file beginning -----BEGIN PRIVATE KEY-----

docker compose up -d exchange     # live reload is not implemented yet
# Expect: {"level":"INFO","msg":"ed25519 signing key loaded"}

curl -s https://exchange.example/.well-known/http-message-signatures-directory
# Expect: a "keys" array whose single key is the NEW one
```

Then fetch one real delivery URL end to end before declaring it done.

**Two things outlast the old key, and both last only a limited time.** Delivery
URLs already issued stay valid for **five minutes** and were signed with the old
key, so the Edge must still verify them; and the Edge caches the key directory for
**one hour**, so until that expires a worker may hold only the old key and URLs
signed with the *new* one can fail. Rotate at a quiet time and expect a period
where one side or the other of that overlap fails. There is no way to run the old
and new key side by side for a while.

#### Override a fee rate or reporting policy at runtime

Both take effect immediately, without a restart. Reach the admin listener from an
address inside `ADMIN_ALLOWED_CIDRS` — over an SSH tunnel or from a jump host (a
bastion) in the range, never through a proxy (§3.3).

```bash
# Fee rate: basis points, 0 <= bps < 10000. 1500 is 15%.
curl -s -X POST http://10.0.4.11:8082/fora.admin.v1.AdminService/SetTenantFeeRate \
  -H 'Content-Type: application/json' \
  -d '{"ver":"1.0","rate":{"tenant_id":"publisher","fee_rate_bps":1500,"notes":"rate change"}}'
# Expect: {"ver":"1.0","rate":{"tenant_id":"publisher","fee_rate_bps":1500,...}}
#         — the response returns the values exactly as saved

# Reporting policy: a FULL REPLACE, not a merge. Any field you omit is cleared
# and the Exchange's own defaults then apply.
curl -s -X POST http://10.0.4.11:8082/fora.admin.v1.AdminService/SetReportingPolicy \
  -H 'Content-Type: application/json' \
  -d '{"ver":"1.0","policy":{"tenant_id":"publisher","required_fields":["consumed_quantity"],"quantity_tolerance":0.05,"window_seconds":86400}}'
# Expect: the policy echoed back exactly as sent
```

Every successful write call adds an audit row in the same transaction as the
change, so a row exists if and only if the change landed:

```sql
SELECT created_at, action, source_addr, request_id, detail FROM fora.audit_log
 WHERE tenant_id = 'publisher' ORDER BY created_at DESC LIMIT 5;
-- Expect: a row whose action is SetTenantFeeRate or SetReportingPolicy and whose
-- detail JSON carries the values you sent.
```

A call naming an unknown tenant changes nothing and writes no audit row. Read
current values with the query in §4.1. **Two overrides are not implemented yet**:
a per-resource price override (the price comes from the catalog entry, so change
it by re-ingesting) and a delivery-witness override.

#### Recovering an abandoned or wrongly-claimed site registration

A site saved through the console sits at `PENDING_MANIFEST` -- "not verified"
in the console -- until it is verified: its document proves it is the
publisher's, its package answers, and its terms are set. The console saves a
site as soon as the publisher presses the wizard's last button, whether or not
those are in place yet, and saving holds the hostname. Two states need you:

- **Abandoned.** Somebody saved a site and never finished setting it up. The
  row is harmless but it holds the hostname: nobody else can register it, and
  the real owner arriving later is told the site belongs to another publisher.
- **Wrongly claimed.** Somebody saved a hostname they do not control. They
  can never activate it — activation needs a document served from that host —
  but the hostname stays held.

There is deliberately no automatic release and no dispute flow. Deleting
somebody's registration is an operator decision, so it is your call, made with
the evidence in front of you.

First read what is actually there:

```sql
SELECT l.domain, l.state, l.created_at, l.last_checked_at, d.publisher_id, p.display_name
  FROM fora.publisher_domain_listings l
  JOIN fora.publisher_domains d USING (domain)
  JOIN fora.publishers p USING (publisher_id)
 WHERE l.state = 'PENDING_MANIFEST'
   AND l.created_at < NOW() - INTERVAL '30 days'
 ORDER BY l.created_at;
```

A row whose `last_checked_at` is null was never checked once. The console asks
the Exchange to check a site only after the site's package answers, so a null
says the publisher never got that far, not that nobody is working on it. A row
checked repeatedly is somebody still trying, and is not abandoned.

Then release the hostname, listing first:

```sql
BEGIN;
DELETE FROM fora.publisher_domain_listings
 WHERE tenant_id = 'seat' AND domain = 'www.publisher.example';
DELETE FROM fora.publisher_domains
 WHERE domain = 'www.publisher.example';
COMMIT;
```

Order matters, and the schema enforces it: the listing refers to the site, so
the site cannot go first. The site refers to the publisher with a restricting
reference, so deleting a site never removes the payee behind it — which is what
you want, because that publisher may have other sites, and the catalog rows
already settled to them must keep pointing at a payee that exists.

**Never delete a row whose state is `ACTIVE` this way.** An active listing has
catalog entries settling through it. Withdraw the content first, with the
catalog removal procedure above, and only then release the registration.

The publisher can register the hostname again afterwards, through the console,
with no further action from you.

### 4.3 Upgrade and rollback

**Always deploy a specific image tag, never `latest`.** The migration a release
carries is applied automatically at boot, so an unpinned tag means you cannot tell
which schema version you are about to move to.

**Upgrade:** pull the new tag, stop the container, start the new one. Migrations
are applied on start, so **start one instance first**, confirm `/healthz` and the
migration line, then start the rest.

```bash
docker compose logs exchange | grep 'migrations applied'
# Expect: TWO lines, one per database. Check the "table" field, not just the
#         version — the two sequences are numbered independently.
#   {"level":"INFO","msg":"migrations applied","version":<new>,"dirty":false,"table":"schema_migrations_fora"}
#   {"level":"INFO","msg":"migrations applied","version":<new>,"dirty":false,"table":"schema_migrations_sor"}
```

**Roll back:** start the previous tag. The schema stays where the newer version
left it — the binary never migrates backwards. Whether the older image can run
against it depends on what the release's migrations did:

| The migration | Result |
|---|---|
| **Added** a table, a column or an enum value | **Safe** — the older code never mentions the new object |
| **Renamed or dropped** a column | **Not safe** — the older code still queries the old name |

Ten migrations in this schema are of the second kind. `000016` renamed
`tx_request_id` to `idempotency_key` and `000021` renamed `manifest_url` to
`discovery_url`; `000006`, `000007`, `000011`, `000031`, `000036`, `000037`,
`000039` and `000045` dropped columns. Rolled back across any of them, the older
image fails on the first query that touches the column — loudly, at boot or on
that query, not silently. Across `000031` that is every tenant read, so no
offer, catalog lookup or signed URL is served. Across `000045`, which dropped
`transaction_log.denial_reason`, it is every read and write of
`fora.transaction_log`, so no purchase and no replay succeeds. If you cannot tell
which kind a release contained, treat it as the second.

**Do not reverse a migration by hand.** Down-migration files exist in the source
tree and ship inside the image, but nothing runs them, and reverting the migration
that created `fora.transaction_evidence` would drop the table — the signed offer
and both parties' signatures with it. If a rollback needs the schema moved
backwards, escalate; the reasons are in
[`deploy/storage/postgres/RUNBOOK.md`](../../deploy/storage/postgres/RUNBOOK.md) §4.4.

---

## 5. Backup and recovery

**PostgreSQL is the only durable store this service owns — but it owns two
databases in it.** Back up both: the catalog database (`EXCHANGE_DSN`) and the
account registry (`EXCHANGE_SOR_DSN`). A backup job written before the registry
existed will silently cover only the first. The Exchange holds no other state on
disk beyond the key files you mounted, which live in your secret store. Redis is a
cache, and TigerBeetle is backed up separately. Schedules, retention and restore
steps for the databases are in
[`deploy/storage/postgres/RUNBOOK.md`](../../deploy/storage/postgres/RUNBOOK.md).

**The one non-obvious step is restoring into a schema carrying the append-once
triggers.** `fora.transaction_evidence` and `fora.transaction_events` each have
`BEFORE UPDATE OR DELETE` and `BEFORE TRUNCATE` triggers that raise an exception
unconditionally; inserts are unaffected. So **restore into a fresh, empty
database** — the schema is created first and rows are then inserted, and the
triggers never fire. A data-only restore into a database that already holds the
tables **will fail**, because the restore truncates the target first and the
trigger refuses. Do not disable the triggers to force one through: that removes
the guarantee the tables exist to provide, and a restore is exactly when the
evidence matters.

| What | Where | If you lose it |
|---|---|---|
| Catalog | `fora.catalog` | Re-ingest it (§4.2). **No financial impact** — pricing is re-derived from the feed. Discovery returns nothing until the ingest completes. |
| Tenants | `fora.tenants` | Re-create the rows (§4.2). Fee rates and policies set through the admin API are reconstructible from `fora.audit_log`, if that survived. |
| Transaction log and evidence | `fora.transaction_log`, `fora.transaction_evidence` | **Financial impact.** Reconstructible: the amounts, from the ledger's own postings, which TigerBeetle holds independently. Not reconstructible: the signed offers and both parties' signatures — the proof of what was agreed. A dispute after this loss cannot be settled from your records. Back these up first. |
| Refused purchases | `fora.transaction_events` | The only record of the purchases the Exchange refused and could attribute to a publisher, which the console's revenue totals count. No financial impact; the counts for the period lost read zero. |
| Audit log | `fora.audit_log` | The only record of who changed a fee rate or policy and when. No functional impact; nothing reads it at runtime. |
| **Agent accounts** | `sor.agent_accounts` — in the **other** database | **Financial impact, and not reconstructible.** This table holds the mapping from each agent's identity to its `billing_ref`. The ledger stores no identity of its own: it derives each account from a one-way hash of the `billing_ref` text, which lives only here. Lose this table and no agent's balance can be reached — the money is still in the ledger, but nothing can say whose it is. Re-registering an agent creates a **new** `billing_ref` and therefore a new, empty ledger account. Back this up alongside the transaction log. |
| Replay records | Redis | A **five-minute window** in which an old signed request could be replayed. Losing Redis entirely is safe — an empty replay store is a safe starting state. |

`fora.transaction_log`, `fora.transaction_evidence`, `fora.transaction_events` and
`fora.audit_log` all grow forever: nothing removes old rows and there is no
retention setting. `fora.transaction_events` grows with refused purchases, which
have no upper bound: an agent that repeats a refused purchase under a new
idempotency key adds a row each time. Size the database accordingly.

---

## 6. Limitations

- **No dispute operation.** The evidence to settle a dispute is stored
  (`fora.transaction_evidence`), but there is no RPC, tool or workflow that acts
  on it. Disputes are handled outside the platform.
- **Delivery URLs are not single-use.** A URL is valid for five minutes and may be
  fetched any number of times within that window by anyone holding it.
- **No live signing-key reload.** Changing either signing key requires a restart.
- **No per-resource price override and no delivery-witness override.** Prices come
  from the catalog and change by re-ingesting.
- **No admin read API and no per-operator admin accounts.** The admin listener can
  only write values, never read them; read current values by SQL (§4.1). There is
  no operator login — being able to reach the port is the only check, and the
  audit log records a source address rather than a person.
- **No API for agent accounts.** Switching an agent's account on or off is a
  direct `UPDATE` in the account-registry database (§4.2), and it writes no audit
  row. Listing or searching accounts is SQL as well.
- **One seat decides the activation policy for every agent.** The policy is read
  from the single tenant named by `EXCHANGE_DEFAULT_TENANT`, whichever publisher
  the agent goes on to buy from. A per-publisher activation policy is not
  implemented yet — and the seat is not a publisher, so there is nowhere to put
  one today.
- **No self-service reporting and no dashboards.** Every question about revenue,
  volume or agent behaviour is answered with SQL against the tables in §3.2.
- **One currency per deployment.** `EXCHANGE_BILLING_LEDGER` fixes it, and a
  purchase priced in any other currency is denied. A second currency means a
  second Exchange.
- **The job that would find and re-post missed charges is not built.** Nothing
  re-posts a charge that failed after its transaction committed; finding and
  correcting those is manual (§3.2).
- **Behind a TLS-terminating proxy, `FORA_TRUST_PROXY_HEADERS=true` is
  required** — and forbidden anywhere else.
  [`CONFIGURATION.md`](CONFIGURATION.md) §4.
