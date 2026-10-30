# FORA Edge Worker — Configuration Reference

This document lists every setting the Worker understands, what each one does, and
what happens if you leave it out. It is a reference, not a procedure — for the
step-by-step install see [`DEPLOYMENT.md`](DEPLOYMENT.md), and for day-to-day
operation see [`RUNBOOK.md`](RUNBOOK.md).

Audience: you, the DevOps engineer deploying the Worker. You do not need to read
the source code. Words that may be new are explained the first time they appear.

---

## 1. What the Worker needs to run

The Worker is one small program that Cloudflare runs at its edge, in front of the
publisher's article pages. It needs:

| Dependency | Required? | Why |
|---|---|---|
| A Cloudflare zone with proxied DNS | **Yes** | A "zone" is a domain in Cloudflare. The Worker only runs on proxied traffic. |
| A reachable origin | **Yes** | The real backend holding the article pages, and the publisher's own `/.well-known/fora.json` (and `/rsl.txt` if it has one), which the Worker passes through to it — §3.4. Reached either through the zone (`SAME_ZONE_ORIGIN`) or at an explicit address (`ORIGIN_URL`) — §3.1. |
| The Exchange's key directory | Yes, at runtime | Fetched over the public internet to get the public keys that signed URLs are checked against (`EXCHANGE_WBA_URL`). |

Before you start, note six things that operators often look for; most of them
do not exist here:

- **There are no secrets.** Every value below is public configuration — no
  password, no private key. So you do not need `wrangler secret`, a secret store,
  or an encrypted variable; plain Cloudflare `vars` are enough. (The private
  signing key stays inside the Exchange and is never given to Cloudflare or to the
  publisher.)
- **There are no command-line flags.** Configuration is 100% environment
  variables — in Cloudflare these are called "vars".
- **There is no `LOG_LEVEL`, by design.** The Worker writes structured records
  (machine-readable data, not free text) to Workers Logs at three levels, and
  none of them can be switched off safely:
  - `info` is the delivery record, `edge.deliver.authorized`: one per signed
    request the Worker authorized and served. It is written once the origin has
    answered, or, where the CDN fetches from the origin itself, when the Worker
    hands the request back to it; a delivery whose origin fetch failed writes
    the `error` record for that failure instead. The record is the Edge's
    witness in the evidence chain
    ([ADR-012](../../docs/architecture/adr-012-edge-delivery-log.md)).
    `fora-ledger` finds it by name, and today reads it only from the
    function's CloudWatch logs on Lambda@Edge; nothing reads it back on
    Cloudflare or Fastly yet. A `warn` threshold would remove every delivery
    from that chain.
  - `warn` is reserved for expected denials. Every unpaid bot produces one, and
    the `edge.deny.signature` alert ([`RUNBOOK.md`](RUNBOOK.md) §2.3) and the
    checks in [`DEPLOYMENT.md`](DEPLOYMENT.md) read these records.
  - `error` is anything that needs attention. Because `warn` means an expected
    denial here, an outage the Go services would record as a warning, such as
    an unreachable key directory, is an `error` on the Edge.

  There are no `debug` records to switch on either. And the Worker reads its
  settings when a request arrives (on Lambda@Edge they are written into the
  package's `index.mjs` when the package is built), so on Cloudflare and Fastly a mistyped level would not
  stop anything at deploy time — it would fail every request. Do not reduce
  the volume by sampling instead: keep `head_sampling_rate` at `1`
  ([`DEPLOYMENT.md`](DEPLOYMENT.md) §6.4). Sampling drops every record of a
  request that is not sampled, whatever its level, so it drops
  `edge.deliver.authorized` records too. The Broker, Exchange and Identity
  services, which run as long-lived processes, do take a level setting.
- **There is no `/metrics` endpoint** and no Prometheus support. Alerting is done
  on log events — see [`RUNBOOK.md`](RUNBOOK.md) §2.3.
- **The Worker is stateless** and needs **zero bindings**. (A "binding" is a
  connection from a Worker to a Cloudflare storage product.) No KV, no D1, no R2,
  no Durable Objects, no Queues.
- **`FORA_ENFORCE_BINDING` defaults to on.** ("Binding" here means a second
  thing: a delivery address tied to one agent.) The Worker requires
  cryptographic proof that the caller holds the key the address was issued
  to — §3.6.

---

## 2. Environment variables

"Required" means the Worker cannot serve without it. Nothing is checked at deploy
time — the Worker reads its variables when the first request arrives — so a
missing required variable passes `wrangler deploy` and then fails with an error
on **every** request, article traffic included. It is not a partial failure.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `EXCHANGE_URL` | **Required** | Base address of the Exchange. Its **only** runtime effect is the `X-FORA-Exchange` header on a bot denial — a hint telling the bot where to negotiate. The Worker never calls this address. | `https://exchange.example` |
| `EXCHANGE_WBA_URL` | **Required** | The Exchange's public-key directory. This is the address the Worker actually fetches, to get the keys that verify signed URLs. | `https://exchange.example/.well-known/http-message-signatures-directory` |
| `SAME_ZONE_ORIGIN` | One of the two, see §3.1 | `"true"` forwards to the zone's own configured origin. Cloudflare-only. | `true` |
| `ORIGIN_URL` | One of the two, see §3.1 | Explicit address of a dedicated backend. | `https://origin.publisher.example` |
| `FORA_VERIFY_KEYS` | Optional | JSON list of public keys to pin, skipping the network fetch — §3.2. | `[{"kty":"OKP","crv":"Ed25519","x":"<43-char base64url>"}]` |
| `WBA_KEYS_JSON` | Optional | JSON list of the **publisher's own** public signing keys, served in its key directory. Unset is a legitimate configuration — §3.4. | see §3.4 |
| `WBA_REVOCATION_URL` | Optional | Address where verifiers check whether the publisher's own keys were withdrawn. Ignored unless `WBA_KEYS_JSON` is also set. | `https://www.publisher.example/key-revocations` |
| `WBA_SIGNED_DIRECTORY_JSON` | Optional | The key directory pre-signed for this host, `{body, content_digest, signature_input, signature}`, made offline with `scripts/sign-wba-directory.mjs` by whoever holds the key. Served verbatim in place of the unsigned directory. When `WBA_KEYS_JSON` is also set, the body must list exactly those keys. | the output of the signing tool |
| `BOT_UA_ALLOW_JSON` | Optional | Replaces the built-in search-crawler list — §3.3. | `["Googlebot","bingbot"]` |
| `BOT_UA_DENY_JSON` | Optional | Replaces the built-in AI-bot list — §3.3. | `["GPTBot","ClaudeBot"]` |
| `LICENSED_PATHS_JSON` | Optional | JSON list of the path prefixes the publisher licenses, each starting with `/`. An unsigned read from an AI bot is refused only under these; the rest of the site passes to the origin. The discovery addresses answer everywhere: §3.4. Unset or `[]` means the whole site. Matching fails closed: percent-escapes are decoded, repeated slashes merged and case ignored before comparing, and a path that cannot be decoded or holds a dot segment counts as licensed. | `["/articles/","/archive/"]` |
| `FORA_ENFORCE_BINDING` | Optional | `"false"` switches this Worker to the weaker mode, where a delivery address works for whoever holds it. Unset means the proof check runs — §3.6. | `true` |

All values are strings, including the ones that look like lists or booleans:
Cloudflare vars are text, and the Worker parses them. A JSON value that does not
parse, or does not match the expected shape, makes the Worker fail hard rather
than silently disable a check — but the failure lands on the first request, not
on `wrangler deploy`. The deploy reports success and the Worker then answers
`500` to **every** request until you fix the value.

**A name the Worker does not know is ignored, not rejected.** The check is on
values, not on names: a variable outside the table above is simply ignored, so a
misspelled name behaves exactly like an unset one. Nothing at deploy time will
tell you. Manually compare the names you set against the table.

**Settings an earlier release read.** `PROVIDER`, `EXCHANGES_JSON`,
`CATALOG_CONTRIBUTORS_JSON` and `RSL_BODY` once made the discovery document and
the RSL file the Worker answered itself. This release reads none of them: it
passes both addresses through to the origin (§3.4). A deployment that still sets
them keeps running, and they are ignored like any other unknown name, a value
that would once have failed every request included. **Before you deploy this
release over one that used them, put both files on the origin**: the
`fora.json` your Exchange operator handed you at `/.well-known/fora.json`, and
your RSL document at `/rsl.txt` if you published one. Otherwise agents find
nothing at those addresses.

---

## 3. Settings that depend on each other

### 3.1 Origin mode — choose exactly one

"Origin" is the real backend server holding the website's pages. The Worker must
know how to reach it, and there are two ways.

| | `SAME_ZONE_ORIGIN="true"` | `ORIGIN_URL="https://…"` |
|---|---|---|
| **Choose when** | The zone already has an origin configured in Cloudflare — typically a shared load balancer that routes by `Host` (the name of the site being requested). | There is one dedicated backend address for this site alone. |
| **What the Worker does** | Re-fetches the incoming address with the signature parameters removed; Cloudflare routes that subrequest to the zone's origin, keeping the original `Host`. | Rewrites the request to that address, keeping path and query. The backend sees **its own** hostname in `Host`. |
| **Needs an address?** | No | Yes |
| **Works on** | Cloudflare only | Cloudflare, Fastly |
| **Common mistake** | Only works on a zone route, so `workers_dev` must be off — [`DEPLOYMENT.md`](DEPLOYMENT.md) §6.3. | The address must **not** resolve back to the Worker's own route, or requests loop forever. |

`SAME_ZONE_ORIGIN` is **Cloudflare-only**, by design: the Fastly adapter does
not forward the variable at all (so setting it there does nothing), and the AWS
Lambda@Edge adapter throws on its first invocation if it sees it, because
forwarding to the incoming address on CloudFront would make the function fetch
its own distribution. As everywhere else, that failure comes when traffic
arrives, not when the function is published.

**Setting neither is the most common misconfiguration.** On Cloudflare the Worker
does not partly work — it fails on *every* request, with this message:

```
edge misconfigured: set ORIGIN_URL to the origin backend, or SAME_ZONE_ORIGIN="true" to forward to this zone's configured origin
```

This is deliberate. Without the guard, every verified request would get an empty
`200` — a deployment that looks healthy while delivering nothing.

### 3.2 `FORA_VERIFY_KEYS` — pinning, and why rotation still works

By default the Worker fetches the Exchange's public keys from `EXCHANGE_WBA_URL`
and caches them. Setting `FORA_VERIFY_KEYS` to a JSON list of public keys pins
them instead: verification then runs with no network call at all.

Pinning does **not** stop you from rotating keys. When a signed URL names a key
id that is not in the pinned set, the Worker performs one directory fetch and
retries the lookup against what it finds. That single fetch is what makes a
rotation fix itself — pinning is a latency and availability optimisation, not a
fixed set.

The trade-off to be aware of: with keys pinned, an unreachable directory no
longer affects the keys you pinned, but a rotation you have not yet followed
still needs the directory to be reachable at the moment the new key first appears.

### 3.3 The bot lists — what they are, and what replaces them

On an unsigned `GET`/`HEAD` the Worker sorts the caller into human (pass through),
search crawler (pass through, free — the publisher wants indexing) or AI bot
(`403` plus instructions on how to buy). Cloudflare's own verified-bot data is
consulted first where available; the two pattern lists below are the fallback,
and most deployments rely only on these lists.

**Built-in search-crawler allow list** (matched case-insensitively, first):

```
Googlebot        bingbot        DuckDuckBot        Applebot(?!-Extended)        YandexBot
```

The `Applebot(?!-Extended)` pattern deliberately excludes `Applebot-Extended`,
Apple's AI crawler, which is still blocked and must pay.

**Built-in AI-bot deny list** (matched after the allow list):

```
bot\b        crawler        spider        GPTBot        ClaudeBot        anthropic-ai
OAI-SearchBot        CCBot        PerplexityBot        Bytespider        Google-Extended
Meta-ExternalAgent        Claude-User        ChatGPT-User        Perplexity-User        Meta-ExternalFetcher
```

`Claude-User`, `ChatGPT-User`, `Perplexity-User` and `Meta-ExternalFetcher` are
the user agents assistants send for user-initiated fetches (a human asked for
the page, as opposed to autonomous crawling). They are gated like the crawlers:
AI access to paid content is licensed regardless of who initiated the fetch.

A caller sending **no** identity string at all counts as a bot — normal browsers
always send one. Anything matching neither list is a human.

`BOT_UA_ALLOW_JSON` and `BOT_UA_DENY_JSON` **replace** the corresponding built-in
list entirely — they do not extend it. If you set one, include again the
built-in entries you still want. Each is a JSON array of regular-expression patterns, with
hard limits:

| Limit | Value | If violated |
|---|---|---|
| Entries per list | at least 1, at most 64 | Every request fails |
| Characters per entry | at most 256 | Every request fails |
| Pattern validity | must compile as a regular expression | Every request fails |

Note the minimum: an **empty list (`"[]"`) is refused** just like a malformed
one. To make a list match nothing, do not set it to empty — see
[`RUNBOOK.md`](RUNBOOK.md) §4.3 for the safe way to switch the bot gate off.

Failing hard on an invalid pattern is deliberate: a broken pattern list must
never silently disable the bot gate. Note where the failure lands. The lists are
read when the first request arrives, not at deploy time, so `wrangler deploy`
succeeds and the Worker then answers `500` to **every** request until you fix the
value. Check the bot gate after deploying a change to either list. Keep patterns to simple
name fragments like the built-ins — the size caps limit how much text is scanned,
but they cannot save you from a badly written pattern such as `(a+)+`, which can
take a very long time to run.

### 3.4 The discovery addresses — which the Worker answers, and which it passes on

Two addresses the Worker answers itself, and two it passes through to the origin.
None of the four is gated, and none is subject to `LICENSED_PATHS_JSON`, whoever
asks.

| Address | Who answers | What comes back |
|---|---|---|
| `/healthz` | the Worker | Always `200` with the body `ok` |
| `/.well-known/http-message-signatures-directory` | the Worker, from `WBA_KEYS_JSON` | The publisher's public keys, as a Web Bot Auth key directory; **404** when `WBA_KEYS_JSON` is unset |
| `/.well-known/fora.json` | the origin | The publisher's own discovery document — the `fora.json` the Exchange operator hands the publisher, served from the publisher's site |
| `/rsl.txt` | the origin | The publisher's own RSL document, if its site has one; the origin's **404** if not |

**The discovery document and the RSL file are the publisher's files, not the
Worker's.** The Worker forwards both addresses to the origin and returns the
origin's answer unchanged — status, headers and body — to every client, an AI
bot included, with no signed address asked for. The reserved signed-address
parameters are stripped from the forward, as on every pass-through. The
publisher changes who may sell or import its pages by editing its own file;
nothing in the Worker's settings describes it, so nothing here can disagree with
it. If the origin cannot be reached, the Worker answers `502` with the reason
`origin_fetch_failed` — it never makes a document of its own. On a deployment
whose CDN fetches the origin itself (CloudFront with no origin mode), the
function hands the request back to the CDN, as for content.

**`WBA_KEYS_JSON` has a strict shape.** It is a JSON array of 1 to 64 key
objects, and every field below is required. A missing or different value makes
the Worker answer `500` to every request — like the pattern lists in §3.3, the
value is read at the first request, not at deploy time:

| Field | Required value |
|---|---|
| `kty` | exactly `"OKP"` |
| `crv` | exactly `"Ed25519"` |
| `use` | exactly `"sig"` |
| `alg` | exactly `"EdDSA"` |
| `x` | the public key, base64url, exactly 43 characters |
| `not_before` | RFC 3339 timestamp, e.g. `2026-01-01T00:00:00Z` |
| `not_after` | RFC 3339 timestamp — the key stops validating at this moment |

A complete one-key value:

```json
[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA",
  "x":"<43-char base64url public key>",
  "not_before":"2026-01-01T00:00:00Z","not_after":"2027-01-01T00:00:00Z"}]
```

There is deliberately no `kid` field: verifiers name each key by its RFC 7638
thumbprint, which they compute from the key itself.

**A 404 on the key directory is a legitimate configuration, not a fault.** That
directory publishes the *publisher's own* signing keys, and a publisher that
issues none has nothing to publish. It is unrelated to the keys the Worker uses to
check signed URLs — those come from the *Exchange's* directory at
`EXCHANGE_WBA_URL` and are never served here.

Note that `/rsl.txt` and `/healthz` sit at the top level, not under
`/.well-known/`. The Worker matches each address exactly: `/.well-known/fora.json/`
or any other spelling is an ordinary path, gated like the rest of the site.

This table says who answers each address; §3.5 covers the one field of the
discovery document that decides who gets paid, and §8 names where a filled-in
copy of each document lives.

### 3.5 The payee id in the publisher's `fora.json`

The publisher's discovery document lists the Exchanges that sell its pages, in
`exchanges[]`. Each entry takes this shape:

| Field | What it is |
|---|---|
| `domain` | The Exchange's own canonical domain. Must match **exactly** what the Exchange calls itself, or the payee lookup below finds nothing. |
| `endpoint` | Full address of the Exchange. |
| `relationship` | `PROVIDER_RELATIONSHIP_DIRECT`. |
| `ext` | Extension map. Carries `resource_owner_id`. |

**`ext.resource_owner_id` is the publisher's payee id — the account that
receives the payments for the content.** The Exchange reads it out of the
document the publisher's origin serves, matching on the entry whose `domain`
equals the Exchange's own. Nothing computes this value for the publisher, and
there is no default:

> **Without it, every catalog push for this publisher is rejected**, with the
> reason `missing_resource_owner_id`. The publisher's content never enters the
> catalog, so nothing about it can ever be sold — while the Worker itself looks
> perfectly healthy and passes the discovery document through without complaint.

The publisher console writes the whole file for a publisher, payee id included,
and the Exchange checks it: a domain registered on this Exchange whose document
attests another payee is refused with `resource_owner_id_mismatch`. A publisher
writing the file by hand confirms two things with the Exchange operator: the
exact `domain` string the Exchange identifies itself by, and the
`resource_owner_id` it registered the site under. A filled-in copy is in
[`../../deploy/publisher-wellknown/fora.json.example`](../../deploy/publisher-wellknown/fora.json.example).

---

### 3.6 `FORA_ENFORCE_BINDING` — proof that the caller holds the key

A delivery address the Exchange ties to one agent carries that agent's key
fingerprint. With this check on, the Worker requires the caller to prove it holds
the matching private key, so an address that leaks is useless to whoever picked it
up. With it off, the address is a *bearer credential*: whoever holds it reads the
article until it expires, protected only by its short lifetime and HTTPS.

**Unset means on.** The rule is "enforce wherever the Worker can run the check",
so the safe mode needs no configuration. Set it to `"true"` anyway if you want
the choice visible in your own config; only `"true"` and `"false"` are accepted,
and any other value makes every request fail rather than choosing a mode for
you. The deploy itself still succeeds — §2.

Agents that sign up through a registry do not hold their own key — the registry
keeps it and fetches the content for them, so the registry is what proves
possession. Nothing is required of those agents for this check to pass.

**Deploy the registry before this Worker goes live with the check on.** A
registry that cannot yet present the key is refused, and every fetch through it
fails with `403`. If this Worker must be deployed first, set
`FORA_ENFORCE_BINDING = "false"` and remove it once the registry is live. Treat
that period as a temporary, less secure state, not a setting to keep
permanently.

Publishers fronted by CloudFront alone cannot run this check at all — CloudFront
verifies the address itself and has nowhere to run the extra step. Those sites
keep the bearer mode, and this variable does not apply to them.

---

## 4. Non-configurable defaults you must still know

These are fixed in the code. No environment variable changes them; a different
value needs a code change and a redeploy.

| Behaviour | Value | Why it matters to you |
|---|---|---|
| Key-directory cache lifetime | **1 hour** | How long a stale key set can persist, and therefore how long a key-rotation problem takes to clear on its own. There is no variable for it. |
| Key-directory body size cap | 64 KiB | A larger response is rejected before parsing. |
| Keys per directory | at most 64 | A larger key set is rejected. |
| Caller identity string scanned | first **512** characters | The bot patterns are applied only to these characters. A marker after that point is never matched. |
| Reserved query parameters | `exp`, `sig`, `kid`, `agent_id` | Stripped before every forward to the origin. The site must not use these names for its own purposes. |
| `compatibility_date` | `2026-07-01` | The Cloudflare runtime version the code is developed against. Keep it aligned — see [`RUNBOOK.md`](RUNBOOK.md) §4.3. |
| `compatibility_flags` | `["nodejs_compat"]` | Required; the code relies on it. |
| Configuration read | once per isolate, then cached | An "isolate" is the small sandbox Cloudflare runs one copy of the Worker in. A variable change reaches traffic through a new deployment version, not through a live reload. |

---

## 5. The Exchange-side prerequisite

Configuring the Worker correctly is not sufficient on its own. **The publisher's
site must be registered on the Exchange with `signing_scheme = 'ED25519'` on its
`fora.publisher_domains` row.** That setting is what makes the Exchange sign
delivery addresses in the form this Worker verifies. The scheme is a property of
the site, not of the Exchange seat that sells it, so one seat can serve a
Cloudflare-fronted site and a CloudFront-fronted one at the same time. With any
other scheme the delivery address does not carry the signature the Worker checks,
and every paid request is refused.

This is an Exchange-side action, taken by the Exchange operator — see
[`../exchange/RUNBOOK.md`](../exchange/RUNBOOK.md). Confirm it is done before
running the signed-URL verification in [`DEPLOYMENT.md`](DEPLOYMENT.md) §8.

---

## 6. Development defaults you must not copy

The repository's compose files exist to run automated tests on a private Docker
network. Several of their values are deliberately unsuitable for production:

| Value seen in the test stack | Why it is there | Why it must not ship |
|---|---|---|
| `EXCHANGE_URL: "http://exchange:8081"` | Container-to-container names on a private bridge. | Unencrypted, and the hostname resolves nowhere on the internet. |
| `EXCHANGE_WBA_URL: "http://…"` | Same. | Public keys fetched over unencrypted HTTP could be modified in transit. |
| `ORIGIN_URL: "http://publisher:8001"` | Same; one port per demo publisher, each answering with that publisher's own discovery documents. | Same. |
| `"domain":"exchange:8081"` in the test publishers' `fora.json` | Matches the test Exchange's own identity, which is a container name and port. | A real deployment's `domain` is a real hostname — §3.5. |
| `WBA_KEYS_JSON` from `deploy/edge-keys/edge.env` | Generated test keys for the local stack. The file is created by `scripts/gen-e2e-keys.sh`, is not tracked in git, and holds public keys only. | The matching private halves are throwaway test material on a developer machine, not the publisher's real keys. Generate the publisher's own keys, or leave the variable unset. |

The commented-out `[vars]` block at the top of `wrangler.toml` is likewise an
example with `example.com` placeholders, not a starting configuration.

---

## 7. Complete example — staging values

The `[env.staging]` block in `wrangler.toml` is a real, working example. Values
marked *fill in* are specific to your deployment.

| Variable | Staging value |
|---|---|
| `EXCHANGE_URL` | `https://exchange.example` |
| `EXCHANGE_WBA_URL` | `https://exchange.example/.well-known/http-message-signatures-directory` |
| `FORA_ENFORCE_BINDING` | `true` — set explicitly in the block; the same as the unset default (§3.6) |
| `SAME_ZONE_ORIGIN` | `true` |

Worker settings, the same for every environment:

| Setting | Value |
|---|---|
| Worker name | `fora-edge` (staging uses `fora-edge-staging`) |
| Entry point | `src/entries/cloudflare.ts` |
| `compatibility_date` | `2026-07-01` |
| `compatibility_flags` | `["nodejs_compat"]` |
| `workers_dev` | `false` |
| Observability | enabled, `head_sampling_rate = 1` |
| Bindings | none |

Everything not listed stays unset. In particular `FORA_VERIFY_KEYS`,
`WBA_KEYS_JSON`, `BOT_UA_ALLOW_JSON` and `BOT_UA_DENY_JSON` are absent, which
is the intended default in every case. A name that is not in the §2 table is
not read at all, set or unset.

The staging site's discovery document is not in this block either: it is the
publisher's `fora.json`, served from its origin, with the payee id in it (§3.5).

---

## 8. The two discovery documents

§2 lists the settings. This section says where each discovery document comes
from: the publisher's own file, or the Worker's settings.

**A filled-in copy of each document is in
[`../../deploy/publisher-wellknown/`](../../deploy/publisher-wellknown/)**, along
with the operator's procedure for the signing key, the two publisher setups, and
the `curl` checks. The test suite checks both copies: the discovery document
through the Exchange's own parser, and the key directory against the Worker's
routes, so neither can drift away from what is served.

### 8.1 `/.well-known/fora.json`

The publisher's own file on its origin; no setting here produces any field of it.
The Worker passes the address through (§3.4). The console hands the publisher
the file to serve, and the reference copy above shows every field; the one that
decides who gets paid is `exchanges[].ext.resource_owner_id` (§3.5).

### 8.2 `/.well-known/http-message-signatures-directory`

Served with `content-type: application/http-message-signatures-directory+json`,
the media type the Web Bot Auth profile names, and only when `WBA_KEYS_JSON` is
set — otherwise this address answers `404` (§3.4). A reader under the profile
refuses the older `application/jwk-set+json` label.

A reader under the profile also takes a key only from a response signed by it,
for the host it fetched from. With `WBA_SIGNED_DIRECTORY_JSON` set, the Worker
serves that pre-signed body byte for byte with its `content-digest`,
`signature-input` and `signature` headers; without it the directory goes out
unsigned. How to produce the value is in the reference folder.

| Field | Comes from | Notes |
|---|---|---|
| `keys[]` | `WBA_KEYS_JSON` | Copied through unchanged, in the order you wrote them. At most 64 — §4. |
| `revocation_url` | `WBA_REVOCATION_URL` | Present only when that variable is set. Leave it unset — the reference folder explains why, and what to use instead. |

A malformed `WBA_KEYS_JSON` is not caught at deploy time: nothing reads it until a
request arrives, so `wrangler deploy` succeeds and the Worker then answers `500`
to **every** request, article traffic included.

The reference folder covers the rest for an operator: which fields every key
carries and why a `kid` is a defect, how to generate the keypair and read out
`x`, and the procedure for retiring a key.
