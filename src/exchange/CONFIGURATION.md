# FORA Exchange — Configuration Reference

This document lists every setting the Exchange understands, what each one does,
and what happens if you leave it out. It is a reference, not a procedure — for
the step-by-step install see [`DEPLOYMENT.md`](DEPLOYMENT.md), and for day-to-day
operation see [`RUNBOOK.md`](RUNBOOK.md).

Audience: the DevOps engineer deploying the Exchange. You do not need to read the
source code. Words that may be new are explained the first time they appear.

---

## 1. What the Exchange needs to run

The Exchange is a single Go binary in a container. It needs:

| Dependency | Required? | Why |
|---|---|---|
| PostgreSQL — **two separate databases** | **Yes** | The first holds the catalog, the transaction log, the evidence store and the audit log (`EXCHANGE_DSN`). The second holds the account registry (`EXCHANGE_SOR_DSN`). The binary exits if either connection string is unset. Both must exist before the Exchange starts — see §2.1. |
| An Ed25519 private key | **Yes** | Signs offers and signed delivery URLs. The binary exits without one. |
| An RSA private key | Only for AWS CloudFront publishers | Signs AWS CloudFront delivery URLs. An all-Ed25519 deployment boots and runs without one; a CloudFront publisher's requests are refused until it is added — see §2.2. |
| The Broker's address | **Yes** | The Broker publishes the list of withdrawn keys. The Exchange refuses to start without an address for it (`EXCHANGE_BROKER_WELLKNOWN_URL`). The boot never waits on reaching the Broker: while the Broker cannot be fetched, the Exchange runs but rejects signed requests — see §2.3. |
| Redis | Strongly recommended | Stops the same signed request being replayed. Without it the Exchange still runs, but the protection is per-process (see §2.1). |
| TigerBeetle | Only for `tigerbeetle` billing | The money ledger. Not needed for the default `free` adapter. |

It listens on **two ports**: the public Exchange listener (`EXCHANGE_ADDR`,
default `:8081`) and a separate internal admin listener (`ADMIN_ADDR`, default
`:8082`). The container image publishes 8081 only.

Two things are worth knowing up front because operators often look for them and
they do not exist:

- **There are no command-line flags.** Configuration is 100% environment
  variables, and there is no configuration file. The binary does accept one
  subcommand: `exchange healthcheck` probes its own `/healthz` and exits,
  so a container health check can run the service binary itself.
- **There is no `/metrics` endpoint** and no Prometheus support. Alerting is done
  on log events — see [`RUNBOOK.md`](RUNBOOK.md) §2.2.

### How values are read

Two parsing rules apply throughout and are worth stating once:

- **An empty string counts as unset.** `EXCHANGE_DOMAIN=""` gives you the default
  `exchange.fora.local`, not an empty domain. There is no way to set a setting to
  the empty string on purpose.
- **The on/off settings are strict.** A switch is on only for exactly `true`
  (any case) or `1`; **any other value means off**. So a typo like
  `FORA_TRUST_PROXY_HEADERS=flase` silently means *off* — after setting a
  switch, confirm the behaviour changed rather than assuming. This one rule
  covers every on/off setting the Exchange reads, `SKIP_SSRF` and
  `ALLOW_INSECURE` included — see §2.4.

---

## 2. Environment variables

"Required" means the Exchange will not start without it. Two connection strings
carry that mark: `EXCHANGE_DSN` and `EXCHANGE_SOR_DSN`. Everything else has a
default, but several of the defaults are **not** safe for production — those are
called out in §3 and §5.

### 2.1 Core and listeners

| Name | Required? | What it is | Example |
|---|---|---|---|
| `EXCHANGE_DSN` | **Required** | The PostgreSQL connection string for the catalog database. The Exchange exits immediately if this is unset. | `postgres://fora:PASSWORD@db.internal:5432/fora?sslmode=require` |
| `EXCHANGE_SOR_DSN` | **Required** | The PostgreSQL connection string for the **account registry**, a **second, separate database**. Not a second schema in the catalog database — a different database. The Exchange exits immediately if this is unset. See §2.6. | `postgres://fora:PASSWORD@db.internal:5432/fora_sor?sslmode=require` |
| `FORA_SOR_ADAPTER` | Optional | Which account registry to use. Only `postgres` exists, and it is the default. **Any other value stops the boot** — see §2.6. | *(leave unset)* |
| `EXCHANGE_SOR_CACHE_TTL` | Optional | How long the Exchange reuses an account's on/off status before re-reading it. Default `30s`. A value it cannot parse stops the boot. | `30s` |
| `EXCHANGE_DEFAULT_TENANT` | Optional | The seat whose "activate new agents automatically" policy applies when an agent registers. A tenant row is this Exchange's seat, so the value is this Exchange's own hostname — the same string as `EXCHANGE_DOMAIN`, which is what it falls back to when unset. Leaving it unset is therefore normally right. A publisher's hostname is not a tenant, and **if no tenant row matches this name, every agent registration fails** — the Exchange warns about it at boot (§2.6). | `exchange.example` |
| `EXCHANGE_DEFAULT_AGENT_CREDIT` | Optional | One-time credit granted to each newly registered agent, **per currency**, written as comma-separated `CODE=AMOUNT` pairs. Amounts are in **whole units** — `EUR=100` grants €100.00 per agent, not 100 cents. A currency you bill in but do not list gets no grant; a currency you do **not** bill in stops the boot. This variable is the **only** place the welcome credit is set: the Exchange reads it once at boot and grants from it at every agent's first `Register`, so dropping a currency from the list removes its grant and unset (or empty) disables the grant everywhere, each from the next restart. Nothing is stored in the database and there is no admin RPC; a default tenant created after the Exchange started grants the credit without a restart. Each amount is a plain decimal only — digits with an optional fraction, at most 8 decimal places. **Anything else stops the boot, including a bare number** (the single-currency format this variable used to take): there is no longer a currency to infer. | `EUR=100,USD=100` |
| `EXCHANGE_ADDR` | Optional | Address the public listener binds to. Default `:8081`. The image publishes 8081, so change the published port too if you change this. | `:8081` |
| `EXCHANGE_LOG_LEVEL` | Optional | The lowest level of log record the Exchange writes: `DEBUG`, `INFO`, `WARN` or `ERROR`, in any case. Default `INFO`. **Any other value stops the boot**, with an `exchange.exit` line naming the variable. Read the section below before raising it. | `INFO` |
| `ADMIN_ADDR` | Optional | Address the internal admin listener binds to. Default `:8082`. Bind it to an internal interface — see [`DEPLOYMENT.md`](DEPLOYMENT.md) §7. | `127.0.0.1:8082` |
| `ADMIN_ALLOWED_CIDRS` | Optional, **set it** | Comma-separated IPs and CIDR ranges permitted to call the admin listener. **Empty means deny everything.** An unparseable entry stops the Exchange booting, naming the token. | `10.0.4.0/24,10.0.5.17` |
| `FORA_TRUST_PROXY_HEADERS` | **Required** behind a TLS-terminating proxy, forbidden otherwise | Makes signature verification use the scheme from `X-Forwarded-Proto`, so requests signed for `https` verify behind a proxy that forwards plain `http`. Only the exact values `true` or `1` enable it — anything else keeps it off. **Never set it on a directly-exposed Exchange** — see §4. | `true` |
| `EXCHANGE_DOMAIN` | Optional, **set it** | The public hostname of this Exchange. Written into the documents it publishes and into every offer it signs, and it is the key a publisher's own manifest is matched against, so it must be the name callers actually use. Default `exchange.fora.local`. | `exchange.example` |
| `EXCHANGE_PUBLIC_ORIGIN` | Optional | The origin (scheme + host) advertised as this Exchange's RPC endpoint. Default `https://<EXCHANGE_DOMAIN>`. It must match the endpoint the Broker has registered for you. | `https://exchange.example` |
| `REDIS_URL` | Optional, **set it** | Redis connection string, used to remember which signed requests have already been seen. Username, password and TLS all travel inside the URL; there is no separate variable for them. Leave it unset and each process remembers on its own — see the warning below. | `rediss://:PASSWORD@cache.internal:6379/0` |
| `EXCHANGE_MAX_INTERMEDIARY_HOPS` | Optional | How many signatures a request may carry, every signature counted, the agent's included; a request with more is refused `resource_exhausted`. Published as `max_intermediary_hops`. Default `4`. A non-numeric or out-of-range value silently falls back to `4` (§3). | `4` |
| `EXCHANGE_CATALOG_URI_SCHEME` | Optional | Overrides the scheme written into stored catalog URLs. Default `https`. Leave unset in production. | *(leave unset)* |
| `EXCHANGE_CONSOLE_WELLKNOWN_URL` | Optional | The address of the publisher console's published key directory. Setting it turns the publisher-registration routes on; leaving it unset means this deployment runs no console and those routes answer `404`. The Exchange verifies every registration request against **this** directory and no other: a request is accepted only when its signature names the URL's https origin as its `Signature-Agent`, so a caller signing with a key from some directory of its own is refused. **Boot fails** when it is set to anything but an https URL. | `https://console.example/.well-known/http-message-signatures-directory` |
| `EXCHANGE_TERMS_CURRENCY` | Optional | The currency every arrangement a publisher registers through the console is priced in. The console reads it from the Exchange and previews terms in it, so the console never sets it. The catalog importer (`fora-import`) reads the same variable and prices every imported entry in it on each run, so it must carry the same value on both; a change re-prices every imported page at the next run. Default `EUR`. | `EUR` |
| `EXCHANGE_TERMS_UNIT` | Optional | What a charging arrangement counts. Read by the console and the catalog importer the same way, and it must match on both for the same reason. Default `fetches`. | `fetches` |
| `EXCHANGE_CATALOG_CONTRIBUTOR_HOST` | **Required** with a console | The hostname of the catalog contributor you operate: the party that submits every publisher's catalog on this deployment. A publisher's own document has to authorize it, and a domain check does not pass until it does. It must equal the `kid` of the key the catalog importer (`fora-import`) signs with, or every push the importer makes is refused. **Boot fails if the console is on and this is unset** — see below. | `catalog-contributor.exchange.example` |

#### The publisher console, and why the contributor host is required with it

The console is a separate service of yours. A person signs in there, and the
console calls the Exchange on their behalf over a signed connection. Two
variables describe that arrangement, and they belong together:

- `EXCHANGE_CONSOLE_WELLKNOWN_URL` is how the Exchange knows the request really
  came from your console. Only a request signed by a key that directory
  publishes is allowed to say which person is acting.
- `EXCHANGE_CATALOG_CONTRIBUTOR_HOST` is the party that will submit those
  publishers' catalogs. The document each publisher serves has to name it, and
  the Exchange checks for it before it activates that publisher's site.

Setting the first without the second stops the boot:

```
EXCHANGE_CATALOG_CONTRIBUTOR_HOST is required when EXCHANGE_CONSOLE_WELLKNOWN_URL is set:
the generated publisher manifest names the operator's catalog contributor, and a domain
check cannot pass without it
```

The refusal is deliberate. Without a contributor to name, publishers would
register, their sites would go live, and nothing could ever submit their
content — a failure that would surface days later as a rejected import rather
than at the boot that caused it.

One line tells you which state the Exchange is in:

```
{"level":"INFO","msg":"exchange.console.enabled","console_wellknown_url":"...","catalog_contributor_host":"..."}
{"level":"INFO","msg":"exchange.console.disabled","reason":"EXCHANGE_CONSOLE_WELLKNOWN_URL is not set"}
```

#### `REDIS_URL` — what "unset" actually costs

With `REDIS_URL` unset the Exchange keeps a private, in-process record of which
signed requests it has already seen, and says so at boot:

```
{"level":"INFO","msg":"exchange.httpsig.replay_store_disabled"}
```

With it set you get `exchange.httpsig.replay_store_ready` instead, carrying the
address. Exactly one of the two appears on every boot, so `replay_store` is the
single string to grep for the answer.

- With **one** Exchange instance, this works.
- With **two or more**, it does not: a request already used against instance A is
  still unknown to instance B, so it can be replayed there. **Nothing but that
  one log line tells you** — the requests themselves succeed either way.

If you run more than one instance, `REDIS_URL` is mandatory, and every instance
must point at **the same** Redis. Do not give each instance its own. Redis's own
settings are in [`deploy/storage/redis/CONFIGURATION.md`](../../deploy/storage/redis/CONFIGURATION.md).

#### `EXCHANGE_LOG_LEVEL` — what each level hides

The Exchange writes structured JSON logs to standard output. ("Structured" means
each log line is machine-readable data, not free text.) `EXCHANGE_LOG_LEVEL` sets
the lowest level it writes, and it is read once, at start-up, so a change needs a
restart. Unset means `INFO`, which is what the Exchange wrote before the setting
existed.

- **`DEBUG`** adds routine detail for troubleshooting, such as an `http request`
  line for every request. Expect far more output.
- **`WARN`** still writes every event the alerts in [`RUNBOOK.md`](RUNBOOK.md) §2.3
  are built on, but it hides every `INFO` line. That includes the boot lines
  [`DEPLOYMENT.md`](DEPLOYMENT.md) and this document tell you to read — the
  `replay_store` line above among them — and the `exchange.execute_transaction`
  and `exchange.report_usage` records of the purchases and usage reports that
  succeeded.
- **`ERROR`** also hides those `WARN` alert events, `exchange.httpsig.reject`
  among them. Choose it only if nothing alerts on the Exchange's logs.

Run `INFO` in production. Whatever the setting, messages the Go HTTP server
writes on its own, such as a recovered panic, are logged at `ERROR`, so no level
hides them. One writer is outside the setting: when `REDIS_URL` is set, the Redis
client library writes its own messages, such as a connection it failed to close,
as plain text on standard error. They are not JSON records and no level hides
them.

### 2.2 Signing keys

The Exchange never generates a signing key for itself: a key that changed on
every restart would invalidate every URL and offer signed before it. So a key
that is needed and missing stops the boot rather than being invented.

The **Ed25519 key is always needed** — every publisher's offers are signed with
it — so it is mandatory at boot. The **RSA key is needed only by publishers on
the AWS CloudFront scheme**, and is optional for a deployment that has none. See
below.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `FORA_ED25519_PRIVATE_PEM` | **Required** (or the file below) | The Ed25519 private key used to sign offers and Ed25519 delivery URLs, supplied inline as PEM text. **PKCS#8 only** — the `-----BEGIN PRIVATE KEY-----` form `openssl genpkey` writes. | that header, then the base64 body |
| `FORA_ED25519_PRIVATE_PEM_FILE` | Alternative to the above | Path to the same key as a file. Read only when the inline variable is empty. A path that is set but unreadable is a boot failure. | `/keys/ed25519-private.pem` |
| `FORA_RSA_PRIVATE_PEM` | Required **only** for AWS CloudFront publishers (or the file below) | The RSA private key used to sign AWS CloudFront delivery URLs, supplied inline as PEM text. Accepts both the PKCS#1 `BEGIN RSA PRIVATE KEY` header and the PKCS#8 `BEGIN PRIVATE KEY` one. | either header, then the base64 body |
| `FORA_RSA_PRIVATE_PEM_FILE` | Alternative to the above | Path to the same key as a file. | `/keys/rsa-private.pem` |
| `FORA_DEMO_ED25519_KEY_REF` | Optional | Internal name the Ed25519 key is filed under. Default `exchange-primary`. Changing it has no external effect. | *(leave at default)* |
| `FORA_DEMO_RSA_KEY_REF` | Optional | Internal name the RSA key is filed under. Default `cf-rsa-primary`. | *(leave at default)* |

#### When the RSA key is needed, and what happens when it is missing

Each publisher chooses one of two ways its delivery URLs are signed: **Ed25519**
(verified by the Cloudflare or Fastly worker) or **AWS CloudFront RSA** (verified
natively by CloudFront). A deployment where every publisher is on Ed25519 never
uses an RSA key for anything, and does not need one.

Leaving it out is safe rather than silent. At start-up the Exchange notes that it
has no RSA key and carries on:

```
{"level":"INFO","msg":"no RSA signing key configured; requests for AWS_CLOUDFRONT_RSA sites will be refused","rsa_key_ref":"cf-rsa-primary"}
```

If a CloudFront publisher is then configured, the request that would need the key
is refused with the `failed_precondition` code. The calling agent sees only a
short message ("delivery URL signing is not provisioned for this publisher") —
which variables supply the key is operator knowledge, so the full refusal lands
in the Exchange's own log, where you will read it:

```
no RSA signing key: this tenant signs delivery URLs with the AWS_CLOUDFRONT_RSA
scheme, which needs one — set FORA_RSA_PRIVATE_PEM or FORA_RSA_PRIVATE_PEM_FILE
```

Two consequences worth knowing:

- A key you **do** supply is read at start-up, so one that is corrupt or in the
  wrong format stops the boot immediately instead of failing on the first
  CloudFront delivery. Supplying a key means it gets checked.
- Nothing else changes for Ed25519 publishers. Add the RSA key the moment you
  onboard a CloudFront publisher; the generation command is in
  [`DEPLOYMENT.md`](DEPLOYMENT.md) §5. Treat it as a real secret.

The Ed25519 key, being needed by every publisher, does stop the boot when it is
missing:

```
{"level":"ERROR","msg":"exchange.exit","err":"no Ed25519 signing key: set FORA_ED25519_PRIVATE_PEM or FORA_ED25519_PRIVATE_PEM_FILE"}
```

### 2.3 HTTP-signature verification and key resolution

Callers sign their requests using the Web Bot Auth profile of HTTP Message
Signatures (RFC 9421). Each signature names its signer's key directory, an
https origin, in its own signed `Signature-Agent` member. These settings
control which keys the Exchange will accept.

There is no key file. The Exchange learns every verification key over the
network, from the directory each signature names, and verifies each signature
on its own:

1. **A signature naming the Broker's origin** (the origin of
   `EXCHANGE_BROKER_WELLKNOWN_URL`) is resolved in the Broker's directory. It
   carries the Broker's relay key and the withdrawn-key list, and its verdict
   on a withdrawn or expired key is final.
2. **Any other signature** is resolved in the directory its own member names,
   under that directory's own withdrawn-key list. The Broker's directory and
   list never decide it. A signer that does not publish its key gets `401`.
3. **A catalog push** is the one exception to rule 2: its key is resolved by
   the push's `caller_id`, the contributor hostname. The Exchange learns that
   key from the directory at `https://<caller_id>` on the first push and reads
   that directory again when a later push fails to verify. The ingest tools
   always sign as `https://<caller_id>`, so that is also the origin the push's
   own member names.

Every key directory is fetched over https from the named origin, never through
a redirect, and must answer under the media type
`application/http-message-signatures-directory+json` with a response signed by
the keys it lists; a key that did not sign the response is not used. A
`Signature-Agent` member that is not an https origin is refused with `401`
before anything is fetched. A directory body over 1 MiB is refused.

The Exchange reads a directory the way the protocol SDK does, whether it is
verifying a request or registering a signer: a key needs no `use` or `alg`
member, `kty` and `crv` match in any letter case, a directory may list any
number of keys within the 1 MiB, and a key whose `not_before` or `not_after`
is missing or does not parse is never active.

When the Exchange registers a signer from its directory (on an agent's first
signed request, on a contributor's first catalog push, or on
`POST /exchange/v1/agents/register`), the read takes at most 5 seconds, and a
failure is answered in one of two ways:

- **The signer's to repair:** no directory (`404`), a redirect, another media
  type, a response its keys did not sign, or no key valid now. The request is
  refused `401` (`400` on the register route), because the next read returns
  the same answer.
- **Transient:** the host cannot be reached, does not answer in time, answers
  another status, or serves a body that is not a key directory at all (not
  JSON, JSON of another shape, or over 1 MiB). The request is answered `503`
  (`502` on the register route), and the signer may retry.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `EXCHANGE_BROKER_WELLKNOWN_URL` | **Required** | The address of the Broker's published document. Its https origin is the Broker's identity: a signature whose `Signature-Agent` is exactly that origin is the Broker's, its key is resolved in the Broker's key directory under the Broker's withdrawn-key list, and a purchase it signs is recognised as relayed. Every other signature is resolved in the directory it names itself, and the Broker's list never decides it. Boot fails without it, and when it is not an https URL — see below. | `https://broker.example/.well-known/fora.json` |
| `EXCHANGE_REVOCATION_POLL_INTERVAL` | Optional | How often to re-read the Broker's withdrawn-key list. Unset leaves the built-in interval in place. A malformed value silently falls back (§3). | `5m` |
| `EXCHANGE_DIRECTORY_TTL` | Optional | How long a fetched key directory is cached. Unset leaves the built-in TTL in place. A malformed value silently falls back (§3). | `10m` |

#### `EXCHANGE_BROKER_WELLKNOWN_URL` is mandatory, by design

Without an address for the Broker, the Exchange has nothing to check the
withdrawn-key list against: a key that had been withdrawn would keep working for
as long as the signer's own directory still published it. Rather than warn and
boot anyway, the Exchange refuses to start:

```
EXCHANGE_BROKER_WELLKNOWN_URL is required: refusing to boot without a revocation
authority ahead of the per-agent well-known path (a revoked key would otherwise
keep verifying)
```

The same principle applies at runtime: if the Broker's document cannot be
fetched, signed requests are **rejected**, not let through. The Exchange logs
`exchange.httpsig.broker_wellknown_unavailable` and refuses the request rather
than risk accepting a withdrawn key. See [`RUNBOOK.md`](RUNBOOK.md) §2.3.

### 2.4 Public documents and outbound fetches

| Name | Required? | What it is | Example |
|---|---|---|---|
| `FORA_WELLKNOWN_SCHEME` | Optional | Which protocol the Exchange uses when fetching other parties' `/.well-known/fora.json` documents. Default `https`. **Must stay `https` in production.** A key directory is always fetched over https from the host's own address, with no redirect followed and a signed response required, whatever this says. | `https` |
| `FORA_WELLKNOWN_PORT` | Optional | Appends a port when fetching those documents. Only needed inside a local test network. Leave unset. It does not apply to key directories. | *(leave unset)* |
| `SKIP_SSRF` | Optional — **leave unset** | Setting this removes the guard that stops the Exchange being tricked into calling internal addresses. Development only. | *(leave unset)* |
| `ALLOW_INSECURE` | Optional — **leave unset** | Setting this allows plain unencrypted `http` for those same calls. Development only. | *(leave unset)* |
| `EXCHANGE_REGISTRATION_SCHEMA` | Optional | A JSON Schema describing the registration details this Exchange expects. Published in its `/.well-known/fora.json`. A schema the Exchange cannot use stops the boot. See below. | *(leave unset)* |
| `EXCHANGE_TERMS_URI` | Optional | The address of your terms of service document. Published in the same place. See below. | *(leave unset)* |
| `EXCHANGE_TERMS_DIGEST` | Optional | The digest pinning which revision of that document. Requires `EXCHANGE_TERMS_URI`. See below. | *(leave unset)* |

`SKIP_SSRF` and `ALLOW_INSECURE` follow the same strict rule as every other
on/off setting (§1): they are on only for exactly `true` (any case) or `1`.
Every other value — including `yes`, `on`, and a typo like `ture` — leaves the
guard in place. For these two switches the rule matters most: each one removes
a protection, so a typo must leave the protection on.

#### What agents must send to open an account, and which terms they accept

Three optional settings are published in the Exchange's own
`/.well-known/fora.json`. All three are unset by default, and unset means the
Exchange behaves exactly as it does without them: it accepts whatever
registration details an agent sends, and it names no terms document.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `EXCHANGE_REGISTRATION_SCHEMA` | Optional | A JSON Schema describing the registration details this Exchange expects, written inline. Published as `account_registration.data_schema`. Agents read it and check their details against it before they send, so setting it is how you tell them what you need. Unset means no schema is published and agents send whatever they hold. A schema the Exchange cannot use **stops the boot** — see below. | `{"type":"object","required":["company_name"],"properties":{"company_name":{"type":"string","minLength":1}}}` |
| `EXCHANGE_TERMS_URI` | Optional | The address of your terms of service document. Published as `terms_uri`. | `https://exchange.example/terms/2026-04` |
| `EXCHANGE_TERMS_DIGEST` | Optional | The digest of the document served at `EXCHANGE_TERMS_URI`, written as the method, a colon, then lowercase hex: `sha256:` and 64 characters, `sha384:` and 96, or `sha512:` and 128. Published as `terms_digest`. Use SHA-256 unless you have a reason not to. Setting it **without** `EXCHANGE_TERMS_URI` stops the boot, and so does a value in any other shape. | `sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08` |

Produce the digest from the file you actually serve:

```
printf 'sha256:%s\n' "$(sha256sum terms-2026-04.html | cut -d' ' -f1)"
```

**Why the digest and not just the URL.** A URL says where your terms are, not
which version an operator agreed to. Its content changes, so after your first
revision every earlier registration points at a document that no longer says
what was agreed. The digest names one exact revision. A registering agent reads
it from your manifest and echoes it back in its request, where the agent's own
signature covers it — so the request states which document its operator
accepted. That only holds while the document is still retrievable, so **keep
every terms revision you have published online**; a digest of a deleted
document identifies nothing.

**Publishing the digest turns the check on.** A registration must then name this
exact digest. One that names a different digest is refused, and so is one that
names none at all — the caller would be claiming acceptance of terms it never
identified. Both refusals carry the same reason, `TERMS_DIGEST_STALE`, because
both have the same remedy: fetch the terms document this Exchange publishes, hash
it, and register again. The accepted digest is then stored with the account, so
you can answer later which revision an account agreed to.

The check runs on FIRST registration only. An account that already exists gets
its stored `billing_ref` back unchanged, and nothing is accepted a second time —
publishing a new terms revision does not break the replay that repeat Register
calls rely on.

**Leave `EXCHANGE_TERMS_DIGEST` unset and none of that happens.** No digest is
published, so a value an agent sends is ignored and none is recorded.

**Plan on changing the digest being coordinated rather than free.** Agents echo
the value they last read from your manifest, so every agent holding the old value
is refused until it re-reads your manifest. Publish the new terms document first
and change the variable second, so the value you advertise always names a
document that is actually being served.

**Publishing the schema is what turns enforcement on.** A payload that does not
match it is refused, and the refusal names every member that failed and why, so
an agent can fix them all in one go rather than one per round trip. Leave the
variable unset and a payload reaches the system of record uninspected, exactly as
before the field existed.

The bounds on registration data apply either way, and they come from the protocol
rather than from this Exchange: at most 64 top-level members, at most 32 levels of
nesting, and at most 16 KB once the payload is written in its canonical (RFC 8785)
form. A payload over a bound is refused as a malformed request, which is a
different answer from "does not match your schema" — an agent that reads the
distinction knows whether to shrink the payload or to correct it.

**A schema the Exchange cannot use stops the boot**, naming what is wrong. It
is not dropped with a warning: an Exchange that silently published nothing
would leave every agent unable to learn what you require while you believed the
requirement was published, and the first evidence would be a registration
arriving without the details you asked for. The rules a published schema must
satisfy come from the protocol, because the agent checking its details against
your schema applies exactly the same ones:

- **JSON Schema draft 2020-12 only.** A `$schema` naming any other draft is refused.
- **Self-contained.** Every `$ref` must point inside the same document, and must
  start with `#`. A reference to another host is refused: an Exchange's manifest
  is read by strangers, and a reference that leaves the document turns every
  reader into a fetcher aimed at an address the schema's author chose.
- **Bounded.** At most 16 KB, at most 32 levels of nesting, and a limit on how
  much work checking one registration against it may cost. A reference chain
  may not loop back on itself. The 16 KB is measured on the schema as it is
  published, not as you wrote it, so indentation and line breaks in the value
  you set do not count against it.
- **Portable patterns.** A `pattern` may only use the regular-expression
  features all three protocol SDKs express identically, so your schema means the
  same thing to every agent that reads it.

A schema describing a business — a company name, a billing email, a tax id —
sits far inside all of these.

**A value that is only whitespace counts as unset.** All three settings are read
with surrounding whitespace removed, so a template that rendered to a blank line
leaves the setting unconfigured rather than publishing the blank. The boot log
line `registration settings resolved` reports what each one resolved to, which
is where to look when a value you set does not appear in the served document.

**`{}` is a published schema, not an absent one.** An empty JSON object is a
valid schema that accepts every payload, so setting the variable to `{}` — or
to a template that rendered empty — publishes the block and tells agents this
Exchange has requirements, while requiring nothing. To publish no schema, leave
the variable unset or empty.

### 2.5 Billing

`FORA_BILLING_ADAPTER` chooses the money backend. The chosen backend's own
settings all live under the `EXCHANGE_BILLING_` prefix.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `FORA_BILLING_ADAPTER` | Optional | One of `free`, `inmemory`, `tigerbeetle`. Default `free` — every charge is approved and nothing is recorded. An **unrecognised value logs a warning and falls back to `free`**, so a typo here means you are not billing at all. | `tigerbeetle` |
| `EXCHANGE_BILLING_SEED` | `inmemory` only | JSON object of starting balances, `{"agent-id": {"value": "100.00", "currency": "USD"}}`. **`USD` is the only accepted currency** — the `inmemory` tier denominates every balance in it, and a balance in another currency can neither receive the Register welcome credit nor be spent safely. Entries that are malformed, or in any other currency, are logged and skipped; that agent simply starts with no balance. | *(leave unset)* |
| `EXCHANGE_BILLING_LEDGER` | **Required** for `tigerbeetle` | The ISO 4217 **numeric** currency codes this deployment bills in, comma-separated. Each code is also its ledger id. Only `978` (EUR) and `840` (USD) are supported; an unknown code, a repeated code, and a non-numeric entry are each a boot failure. **The order decides the advertised `base_currency`** — see below. | `978,840` |
| `EXCHANGE_BILLING_TB_ADDRESS` | **Required** for `tigerbeetle` | TigerBeetle address as `IP:port`. **Hostnames are rejected** — supply an IP. | `10.0.6.20:3000` |
| `EXCHANGE_BILLING_TB_CLUSTER_ID` | Optional | TigerBeetle cluster id. Default `0`. A non-numeric value is a boot failure. | `0` |
| `EXCHANGE_BILLING_HOLD_GRACE` | Optional | How long a reservation outlives the delivery URL. Default `1m`, giving a 6-minute hold against the 5-minute URL. A malformed value is a boot failure. | `1m` |
| `EXCHANGE_BILLING_TB_OP_TIMEOUT` | Optional | Deadline for a single ledger call. Unset leaves the built-in 5s. A malformed value is a boot failure. | `5s` |

### The order of `EXCHANGE_BILLING_LEDGER` decides `base_currency`

The Exchange keeps one ledger per currency. An agent holds one balance per
currency, a charge is taken from the balance in the charge's own currency, and
nothing is ever converted between them.

The well-known document, however, advertises a single `base_currency`, because
the protocol field holds one value. **That value is the FIRST code in
`EXCHANGE_BILLING_LEDGER`.** So:

| Value | Bills in | Advertises |
|---|---|---|
| `978` | EUR | `EUR` |
| `978,840` | EUR and USD | `EUR` |
| `840,978` | USD and EUR | `USD` |

The publisher console's revenue totals use the same value: a totals request that
names no currency is read in it.

Two consequences for operators:

- **Add a new currency at the END of the list.** Appending leaves the advertised
  value untouched. Putting the new code first silently changes what every other
  party is told this Exchange prices in.
- **Reordering the list is a deliberate change**, never a cleanup. `base_currency`
  is a claim that the prices in our offers are denominated in that currency, and
  agents comparing offers across exchanges read our numbers through it.

The resolved list and the advertised value are both printed at boot, so the
running configuration can be checked without reading the environment:

```
billing adapter: tigerbeetle currencies=EUR,USD base_currency=EUR address=… hold_timeout=6m0s
```

**A selected TigerBeetle that cannot be reached fails the boot.** It never
quietly falls back to `free`. The Exchange constructs the client, runs a health
check, and exits on failure with `billing: TigerBeetle health check: …`. That is
the intended behaviour: a billing service that quietly stops charging is worse
than one that will not start.

The ledger's own configuration — cluster sizing, data file, replicas — is in
[`deploy/storage/tigerbeetle/CONFIGURATION.md`](../../deploy/storage/tigerbeetle/CONFIGURATION.md)
rather than repeated here.

### 2.6 The account system of record

The **system of record**, or **SoR**, is the Exchange's register of agent
accounts. An agent that wants to buy paid content calls the `Register` RPC once.
The Exchange writes a row here, gives the agent a `billing_ref` — the identifier
its money is keyed on — and stores that same `billing_ref` on the agent's row in
the catalog database. An agent with no `billing_ref` cannot buy anything.

`FORA_SOR_ADAPTER` chooses the backend, exactly as `FORA_BILLING_ADAPTER` does
for money. The chosen backend's own settings all live under the
`EXCHANGE_SOR_` prefix. Both are listed in §2.1; the three points below are what
make this section worth reading.

**The SoR needs its own database, and you must create it yourself.** The
Exchange opens a *second* connection to `EXCHANGE_SOR_DSN` and applies its own
migrations there at boot, creating the schema `sor` and the table
`sor.agent_accounts`. It never issues `CREATE DATABASE`, so the database has to
exist before the container starts. Pointing `EXCHANGE_SOR_DSN` at the same
database as `EXCHANGE_DSN` is not the intended arrangement — keep them separate.

**A typo in `FORA_SOR_ADAPTER` stops the boot; a typo in `FORA_BILLING_ADAPTER`
does not.** This is deliberate and it is the one contrast to remember:

| Selector | Unrecognised value | Consequence |
|---|---|---|
| `FORA_BILLING_ADAPTER` | Warns, falls back to `free` | The service runs and approves every charge. Survivable, but you are not billing. |
| `FORA_SOR_ADAPTER` | **Exits** with `sor: unknown FORA_SOR_ADAPTER "…"` | The service does not start. Not survivable — and that is the point: there is no safe empty account register to fall back to. |

**A missing default tenant is warned about, not fatal.** At boot the Exchange
looks for the tenant named by `EXCHANGE_DEFAULT_TENANT` (or `EXCHANGE_DOMAIN` if
that is unset). That row is this Exchange's seat, and it is keyed on the
Exchange's own hostname. If there is no such row it logs a warning and carries
on, because the seat may be created after the process starts:

```
{"level":"WARN","msg":"default tenant not found yet — Register will fail until it is seeded","default_tenant_domain":"exchange.example"}
```

Take the warning seriously. Until that tenant row exists, **every** agent
registration is refused, so no agent can buy anything.

---

### 2.7 Request-size limits (fixed, not configurable)

Two limits are compiled in rather than read from the environment, so there is
nothing to set — but they decide what a caller may send, and a refusal that
names neither is hard to place. Both are **1 MiB**, one number applied to both
quantities on the two mounts an agent or a publisher reaches: the
ExchangeService RPCs and the catalog push.

The admin listener is not one of them and carries neither bound. Its protection
is reachability — a separate port, bound to an internal interface, behind an
IP allowlist that denies by default — and an operator who can reach it can
already call every setter. Closing that gap is tracked separately; it is
recorded here rather than left for a reader to infer from a table that does not
mention the mount.

| Bounded quantity | Refusal a caller sees | Why it is bounded |
|---|---|---|
| The raw HTTP body, buffered before the caller is known | HTTP 413 with a `resource_exhausted` Connect body, logged as `outcome=body_too_large` | An RFC 9421 signature is checked over the exact bytes, so the body must be read before it can be verified. Without the bound an unauthenticated caller could spend the Exchange's memory. |
| The decompressed Connect message the handler decodes | `resource_exhausted` | A compressed request inflates at a ratio the caller picks, and the protocol's own list bounds do not limit the work of checking one. |

Consequences worth knowing. A catalog push carries at most 256 entries by
protocol rule, and this size limit applies on top of that — a feed of unusually
large entries can produce a submission that satisfies the count bound and
crosses this one, which the ingest RUNBOOK's §3.1 covers. The `Register` RPC
adds a tighter, semantic bound on `registration_data` itself; this is the outer
wall, that is the inner one.

---

## 3. Settings that take a default silently when malformed

Most bad values stop the boot and name themselves. Three do not — they fall back
to a default with **no log line**, so the Exchange runs with settings you did not
choose and nothing tells you.

| Setting | On a malformed value | Why it matters |
|---|---|---|
| `EXCHANGE_MAX_INTERMEDIARY_HOPS` | Falls back to `4`. Non-numeric, zero, negative, or above 1048576 all qualify. | The value is published as `max_intermediary_hops` and enforced as the number of signatures a request may carry. |
| `EXCHANGE_REVOCATION_POLL_INTERVAL` | Falls back to the built-in interval. A negative duration also qualifies. | You may believe you are polling the withdrawn-key list every minute when you are not. |
| `EXCHANGE_DIRECTORY_TTL` | Falls back to the built-in TTL. | A rotated key may be picked up later than you planned. |

After changing any of these, confirm the value took effect rather than assuming:
the poll interval is echoed in the `exchange.httpsig.wellknown_enabled` boot line,
and the hop count appears in the published `/.well-known/fora.json`.

---

## 4. If you terminate TLS in front of the Exchange — read this

**Behind a proxy that terminates HTTPS, you must set
`FORA_TRUST_PROXY_HEADERS=true` — and set it ONLY there.**

Agents and Brokers sign their requests using HTTP Message Signatures (RFC 9421).
Part of what gets signed is the full request URL — *including whether it was
`http` or `https`*. The Exchange reconstructs that URL from the connection it
actually received.

So if a reverse proxy (Caddy, nginx, an AWS load balancer) accepts `https://` from
the caller and forwards plain `http://` to the Exchange without this flag, the
caller signed one URL and the Exchange checks a different one. **Every signed
request fails verification**, and the logs show a stream of
`exchange.httpsig.reject` with `outcome=signature`. The service looks healthy;
nothing works.

With `FORA_TRUST_PROXY_HEADERS=true`, the Exchange reads the scheme from the
`X-Forwarded-Proto` header the proxy sets, so verification runs against the
`https` URL the caller actually signed. The staging compose stack (Caddy in
front) deploys exactly this shape.

Rules for using it safely:

- **Set it only behind a proxy you control.** On a directly-exposed Exchange
  the header comes from the caller, and honoring it lets the caller pick the
  scheme its signature is verified against. That is why the flag is off unless
  the value is exactly `true` or `1` — any other value (including a typo) keeps
  it off.
- **The proxy must replace, not append to, any incoming `X-Forwarded-Proto`.**
  Caddy and AWS ALB do this by default. A proxy that appends would leave the
  first (caller-supplied) value in charge.
- Only the scheme is trusted. `X-Forwarded-Host` is deliberately ignored: the
  proxy preserves the `Host` header, and honoring a forwarded host would let a
  request signed for one endpoint verify at another.

Without the flag, the older options still work: terminate TLS in pass-through
mode, or put no TLS-terminating proxy in front. The admin listener has a
separate, unrelated limit — see [`DEPLOYMENT.md`](DEPLOYMENT.md) §7.

---

## 5. Development defaults you must not copy

The compose files in this repository exist to run automated tests on a private
network. Several of their settings are deliberately unsafe and must never be
carried into production:

| Setting | Why it is there | Why it must not ship |
|---|---|---|
| `FORA_WELLKNOWN_SCHEME: "http"` | The services serve plain http on the test networks. | Public documents would be fetched unencrypted and could be tampered with in transit. |
| `SKIP_SSRF: "true"` | Test services live on private addresses the guard blocks. | Removes the protection against the Exchange being steered into your internal network. |
| `ALLOW_INSECURE: "true"` | Same reason. | Same consequence. |
| `EXCHANGE_CATALOG_URI_SCHEME: "http"` | The test sites serve plain http. | Every catalog URL would be stored as plaintext `http`. |
| `sslmode=disable` in the DSN | The database is on the same private bridge. | Correct only while the connection stays on one host. On a network you do not control exclusively TLS is required: without it every row travels in the clear, and what the login exposes depends on the cluster's authentication method ([`deploy/storage/postgres/CONFIGURATION.md`](../../deploy/storage/postgres/CONFIGURATION.md) §2.3). |
| `FORA_BILLING_ADAPTER: "inmemory"` | The test suite needs a deny path without a real ledger. | Balances live in process memory and vanish on restart. Nothing is ever settled. |

---

## 6. Worked example

Values marked *fill in* are specific to your environment.

```
EXCHANGE_DSN=postgres://fora:<password>@<db-host>:5432/fora?sslmode=require
EXCHANGE_SOR_DSN=postgres://fora:<password>@<db-host>:5432/fora_sor?sslmode=require
EXCHANGE_ADDR=:8081
EXCHANGE_LOG_LEVEL=INFO
ADMIN_ADDR=127.0.0.1:8082
ADMIN_ALLOWED_CIDRS=10.0.4.0/24
EXCHANGE_DOMAIN=exchange.example
EXCHANGE_PUBLIC_ORIGIN=https://exchange.example
# The seat, which is this Exchange's own hostname — never a publisher's. It may
# be omitted entirely, because EXCHANGE_DOMAIN above is the fallback.
EXCHANGE_DEFAULT_TENANT=exchange.example
REDIS_URL=rediss://:<password>@<redis-host>:6379/0
FORA_ED25519_PRIVATE_PEM_FILE=/keys/ed25519-private.pem
FORA_RSA_PRIVATE_PEM_FILE=/keys/rsa-private.pem
EXCHANGE_BROKER_WELLKNOWN_URL=https://broker.example/.well-known/fora.json
EXCHANGE_REVOCATION_POLL_INTERVAL=5m
FORA_BILLING_ADAPTER=tigerbeetle
EXCHANGE_BILLING_LEDGER=978
EXCHANGE_BILLING_TB_ADDRESS=<tigerbeetle-ip>:3000
```

Everything not listed is left at its default. In particular `SKIP_SSRF`,
`ALLOW_INSECURE`, `FORA_WELLKNOWN_PORT` and `EXCHANGE_CATALOG_URI_SCHEME` stay
unset, `FORA_WELLKNOWN_SCHEME` stays at its `https` default, and
`FORA_SOR_ADAPTER` stays at its `postgres` default.

Note the **two** databases: `fora` and `fora_sor`. Both must already exist —
[`DEPLOYMENT.md`](DEPLOYMENT.md) §4 creates them.

The secret values above (`EXCHANGE_DSN`, `EXCHANGE_SOR_DSN`, `REDIS_URL`, and the
two signing keys) are read as plain environment variables. The Exchange does not
know or care where they came from, so the secret store and the tooling that
supplies them are your choice — the only requirement is that they are present in
the environment when the process starts.
