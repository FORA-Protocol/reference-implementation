# FORA Broker — Configuration Reference

This document lists every setting the Broker understands, what each one does, and
what happens if you leave it out. It is a reference, not a procedure — for the
step-by-step install see [`DEPLOYMENT.md`](DEPLOYMENT.md), and for day-to-day
operation see [`RUNBOOK.md`](RUNBOOK.md).

Audience: the DevOps engineer deploying the Broker. You do not need to read the
source code. Words that may be new are explained the first time they appear.

---

## 1. What the Broker needs to run

The Broker is a single Go binary in a container. It needs:

| Dependency | Required? | Why |
|---|---|---|
| PostgreSQL | **Yes** | Holds the list of Exchanges the Broker routes to, plus an audit log. The binary exits if it cannot reach it. |
| Redis | Strongly recommended | Stops the same signed request being replayed. Without it the Broker still runs, but the protection is per-process (see §2). |
| A reachable Exchange | Yes, at runtime | The Broker reads each Exchange's public `/.well-known/fora.json` to find its address. |

It listens on **one port, `:8082`**. There is no admin port and no second listener.

Two things are worth knowing up front because operators often look for them and
they do not exist:

- **There are no command-line flags.** Configuration is 100% environment variables.
- **There is no `/metrics` endpoint** and no Prometheus support. Alerting is done
  on log events — see [`RUNBOOK.md`](RUNBOOK.md) §2.2.

---

## 2. Environment variables

"Required" means the Broker will not start without it. Everything else has a
default, but several of the defaults are **not** safe for production — those are
called out in §3 and §5.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `BROKER_DSN` | **Required** | The PostgreSQL connection string. The Broker exits immediately if this is unset. | `postgres://fora:PASSWORD@db.internal:5432/fora?sslmode=require` |
| `BROKER_ADDR` | Optional | The address the Broker listens on. Default `:8082`. The container image publishes port 8082, so if you change this you must change the published port too. | `:8082` |
| `BROKER_LOG_LEVEL` | Optional | The lowest level of log record the Broker writes: `DEBUG`, `INFO`, `WARN` or `ERROR`, in any case. Default `INFO`. Any other value stops the Broker at start-up with a `broker.exit` line naming the variable. Read the section below before raising it. | `INFO` |
| `FORA_TRUST_PROXY_HEADERS` | **Required** behind a TLS-terminating proxy, forbidden otherwise | Makes signature verification use the scheme from `X-Forwarded-Proto`, so requests signed for `https` verify behind a proxy that forwards plain `http`. Only the exact values `true` or `1` enable it — anything else keeps it off. **Never set it on a directly-exposed Broker** — see §4. | `true` |
| `REDIS_URL` | Optional, **set it** | Redis connection string, used to remember which signed requests have already been seen. Leave it unset and each process remembers on its own — see the warning below. Username, password and TLS all travel inside the URL; there is no separate variable for them. | `rediss://:PASSWORD@cache.internal:6379/0` |
| `BROKER_ID` | Optional | A short name for this Broker, used in the documents it publishes. Default `broker-local`. | `broker-01` |
| `BROKER_DOMAIN` | Optional | The public hostname of this Broker. It is written into the documents the Broker publishes, so it must match the name callers actually use. Default `broker.local`. | `broker.example` |
| `BROKER_PUBLIC_ORIGIN` | Optional | The Broker's own https origin. Every request the Broker sends to an Exchange is signed naming this origin as its `Signature-Agent`, and the Exchange fetches the relay key from `<origin>/.well-known/http-message-signatures-directory`, so it must be where this Broker serves its key directory, over https, with the `Host` header preserved by any proxy in front. Default `https://<BROKER_DOMAIN>`. **The Broker refuses to start** when the value is not an https origin: `https://`, a lowercase host, a port only when it is not 443, and nothing after it. | `https://broker.example` |
| `BROKER_RELAY_KEY_FILE` | Optional, **set it** | Path to the **private** key the Broker signs its outbound calls to the Exchange with. Default `deploy/broker/broker-key.json`. Without it those calls go out unsigned and the Exchange answers `401`. | `/relay/broker-key.json` |
| `BROKER_ED25519_SEED` | **Required** (or the file below) | The Broker's own signing key, as 32 raw bytes in base64url. This is the identity the Broker publishes to the world. The Broker will not start without it — see below. | `<43-character base64url string>` |
| `BROKER_ED25519_KEY_FILE` | Alternative to the seed | The same key supplied as a PEM file. Only read when `BROKER_ED25519_SEED` is empty. Note the format problem in §3. | `/keys/cosign.pem` |
| `BROKER_REVOCATION_URL` | Optional, **set it** | The public web address where the Broker publishes its list of withdrawn keys. The Exchange polls this. If unset, the Broker does not advertise it and the Exchange has nothing to poll. | `https://broker.example/.well-known/fora-key-revocations.json` |
| `BROKER_REVOCATION_FILE` | Optional | Path to the file whose contents are served at the address above. If unset or missing, the Broker serves an empty "nothing has ever been withdrawn" answer, dated 1 January 1970, forever. The file is re-read when it changes — no restart needed. | `/revocations/revocations.json` |
| `BROKER_REGISTRY_FILE` | Optional, **set it** | Path to a YAML file listing the Exchanges this Broker routes to. Nothing else fills that list, so with this unset the Broker starts with no Exchanges and every discovery returns nothing. | `/config/exchanges.yaml` |
| `BROKER_QUERY_PROVIDER` | Optional | Which catalog search turns a free-text query into URLs: `none`, `llm` or `elasticsearch`. Default `none`. Any other value stops the Broker at start-up. Each provider has settings of its own — see §7. | `elasticsearch` |
| `EXA_API_KEY` | Optional | The key for the EXA search service. It is used only with `BROKER_QUERY_PROVIDER=none`, and only for requests that carry a free-text query and no URLs: EXA turns the query into candidate publisher domains. With it unset, every such request fails at once with `no domains for query`. Requests that name a URL directly never use it. Even with a key, this path returns no offers yet — see [`RUNBOOK.md`](RUNBOOK.md) §6. | *(unset unless you serve free-text queries)* |
| `FORA_WELLKNOWN_SCHEME` | Optional | Which protocol the Broker uses when fetching other parties' `/.well-known/fora.json` documents. Default `https`. **Must stay `https` in production.** A key directory is always fetched over https, whatever this says. | `https` |
| `FORA_WELLKNOWN_PORT` | Optional | Appends a port when fetching those documents. Only needed inside a local test network. Leave unset. It does not apply to key directories. | *(leave unset)* |
| `SKIP_SSRF` | Optional — **leave unset** | Setting this to `true` removes the guard that stops the Broker being tricked into calling internal addresses. Development only. | *(leave unset)* |
| `ALLOW_INSECURE` | Optional — **leave unset** | Setting this to `true` allows plain unencrypted `http` for those same calls. Development only. It does not admit a plain `http` Elasticsearch URL: that has its own opt-in, `BROKER_ES_ALLOW_HTTP` (§7). | *(leave unset)* |
| `BROKER_ALLOW_EPHEMERAL_KEY` | Optional — **leave unset** | Setting this to `true` lets the Broker start without an identity key, creating an ephemeral (throwaway) one that changes on every restart. Development only. | *(leave unset)* |

### `BROKER_LOG_LEVEL` — what each level hides

The Broker writes structured JSON logs to standard output. ("Structured" means
each log line is machine-readable data, not free text.) `BROKER_LOG_LEVEL` sets
the lowest level it writes, and it is read once, at start-up, so a change needs a
restart. Unset means `INFO`, which is what the Broker wrote before the setting
existed.

- **`DEBUG`** adds routine detail for troubleshooting: an `http request` line for
  every request, and the lines [`RUNBOOK.md`](RUNBOOK.md) §2.2 lists at `DEBUG`.
  Expect far more output.
- **`WARN`** still writes every event the alerts in [`RUNBOOK.md`](RUNBOOK.md) §2.3
  are built on, but it hides every `INFO` line: the start-up lines
  [`DEPLOYMENT.md`](DEPLOYMENT.md) tells you to check for, and the purchase audit
  records of the purchases the Broker sent. The "no Exchange `LIVE`" alert
  reads the registry's `probe_state` column, not a log line. If you build it from
  `broker.registry.probe_state_changed` records instead, note that an Exchange
  leaving `LIVE` is a `WARN` record but its return to `LIVE` is an `INFO` one, so
  at `WARN` the alert would never see the recovery.
- **`ERROR`** also hides those `WARN` alert events, `broker.httpsig.reject` among
  them. Choose it only if nothing alerts on the Broker's logs.

Run `INFO` in production. Whatever the setting, messages the Go HTTP server
writes on its own, such as a recovered panic, are logged at `ERROR`, so no level
hides them. One writer is outside the setting: when `REDIS_URL` is set, the Redis
client library writes its own messages, such as a connection it failed to close,
as plain text on standard error. They are not JSON records and no level hides
them.

### `REDIS_URL` — what "unset" actually costs

With `REDIS_URL` unset the Broker still starts, logs one `INFO` line
(`broker.redis.disabled`) and carries on. Each process then keeps its own private
record of which signed requests it has already seen.

- With **one** Broker instance, this works.
- With **two or more**, it does not: a request already used against instance A is
  still unknown to instance B, so it can be replayed there. Nothing warns you.

If you run more than one Broker instance, `REDIS_URL` is mandatory, and every
instance must point at **the same** Redis. Do not give each instance its own.

### The identity key is mandatory

The Broker publishes its own public key in a directory that other parties fetch and
cache. Each published key carries a 90-day validity window, and consumers such as
the Exchange re-fetch the directory on their own schedule — once an hour by
default. A key that changed on every restart would leave everybody
holding one that no longer verifies — so the Broker **refuses to start** when neither
`BROKER_ED25519_SEED` nor `BROKER_ED25519_KEY_FILE` is set, rather than creating one
for you:

```
{"level":"ERROR","msg":"broker.exit","err":"signer setup: no Broker identity key: ..."}
```

There is one way out, and it is for development and CI only: setting
`BROKER_ALLOW_EPHEMERAL_KEY` to `true` or `1` restores the throwaway-key behaviour.
It is parsed strictly — any other value, including a typo like `flase`, leaves the
requirement in place. **Never set it in production**; see §5.

---

## 3. The file settings, and what happens when one is missing

Ten settings point at files. Two of them fail quietly, which makes them the
most common source of "it deployed fine but nothing works".

There is no key file for verification: the Broker learns an agent's public key
by fetching the directory named by the request's signed `Signature-Agent`
header. An agent that does not publish its key gets `401`.

| Setting | If the file is missing | If the file is present but broken |
|---|---|---|
| `BROKER_RELAY_KEY_FILE` | Warning `broker.relay.key_absent`, Broker starts, **outbound calls to the Exchange are unsigned** and get `401`. | The Broker **refuses to start**. |
| `BROKER_ED25519_KEY_FILE` | The Broker **refuses to start** unless `BROKER_ED25519_SEED` is set instead (see §2). | The Broker **refuses to start**. |
| `BROKER_REGISTRY_FILE` | The Broker **refuses to start** — the open error appears in `broker.exit`. The quiet case is leaving the variable **unset**: then no Exchange is registered, every discovery returns nothing, and the only signal is one `broker.registry.no_bootstrap` warning at start-up. | The Broker **refuses to start**. |
| `BROKER_REVOCATION_FILE` | Serves an empty list dated `1970-01-01T00:00:00Z` forever — the Exchange concludes no key has ever been withdrawn. | Returns HTTP `500` on that one route; the rest of the Broker keeps working. The log line `broker.revocation.unavailable` carries the validation error. |
| `BROKER_QUERY_CATALOG_FILE` | With `BROKER_QUERY_PROVIDER=llm`, the Broker **refuses to start**. | The Broker **refuses to start**. |
| `BROKER_ES_API_KEY_FILE`, `BROKER_ES_PASSWORD_FILE` | With `BROKER_QUERY_PROVIDER=elasticsearch`, the Broker **refuses to start**, naming the setting. | A key or password the cluster refuses stops the start too: see the boot probe in §7. |
| `BROKER_ES_CA_FILE` | With `BROKER_QUERY_PROVIDER=elasticsearch`, the Broker **refuses to start**, naming the setting. | The Broker **refuses to start** when the file holds no PEM certificate. |
| `BROKER_ES_TEMPLATE_FILE` | With `BROKER_QUERY_PROVIDER=elasticsearch`, the Broker **refuses to start**, naming the setting. | An empty file, one over 64 KiB, or one using `{{{ }}}`, `{{& }}`, `{{#join}}` or a change of delimiters (`{{= }}`) stops the start. A template the cluster cannot render starts the Broker with `broker.es.misconfigured`: see the boot probe in §7. |
| `BROKER_ES_FILTERS_SCHEMA_FILE` | With `BROKER_QUERY_PROVIDER=elasticsearch`, the Broker **refuses to start**, naming the setting. | The Broker **refuses to start**, naming the rule the schema breaks (§7). |

The withdrawn-keys file is the one file you edit while the Broker is running — it is
re-read whenever it changes, no restart needed. A ready-to-copy starting file ships as
`deploy/broker/revocations.example.json`; the format and the procedure for adding a key
are in [`RUNBOOK.md`](RUNBOOK.md) §4.2.

### The Exchange list only ever grows

Start-up *adds and updates* rows from `BROKER_REGISTRY_FILE`; it never deletes.
Removing an entry from the file therefore leaves the row in place, and removing an
Exchange for real takes one SQL statement — see [`RUNBOOK.md`](RUNBOOK.md) §4.1.

### The Ed25519 PEM format problem

If you supply the Broker's key via `BROKER_ED25519_KEY_FILE`, note that the
obvious command produces the wrong format:

```bash
openssl genpkey -algorithm ED25519 -out cosign.pem   # DOES NOT WORK
```

OpenSSL writes a 48-byte PKCS#8 wrapper; the Broker expects the 64-byte raw key
(seed followed by public key) as the PEM block's payload and rejects anything
else. Do not write this file by hand. The staging deploy delivers the key this
way on purpose — a root-owned key file, never an env value inside the
world-readable compose file — and its generator,
`deploy/terraform/scripts/gen-keys.sh`, writes
`broker-identity-key.pem` in exactly the shape the loader wants. Outside that
deploy path, `BROKER_ED25519_SEED` remains the simpler choice with no format
to get wrong. The generation command is in [`DEPLOYMENT.md`](DEPLOYMENT.md) §5.

One more detail: the generator labels the block `ED25519 PRIVATE KEY`, and the
staging Terraform checks for that label before deploying — but the Broker's own
loader checks only the payload length, not the label. A file that passes the
Broker can still be refused by Terraform's stricter plan-time check.

---

## 4. If you terminate TLS in front of the Broker — read this

**Behind a proxy that terminates HTTPS, you must set
`FORA_TRUST_PROXY_HEADERS=true` — and set it ONLY there.**

Agents sign their requests using a scheme called HTTP Message Signatures
(RFC 9421). Part of what gets signed is the full request URL — *including whether
it was `http` or `https`*. The Broker reconstructs that URL from the connection it
actually received.

So if a reverse proxy (Caddy, nginx, an AWS load balancer) accepts `https://` from
the agent and forwards plain `http://` to the Broker without this flag, the agent
signed one URL and the Broker checks a different one. **Every signed request fails
verification**, and the logs show a stream of `broker.httpsig.reject` with
`outcome=signature`. The service looks healthy; nothing works.

With `FORA_TRUST_PROXY_HEADERS=true`, the Broker reads the scheme from the
`X-Forwarded-Proto` header the proxy sets, so verification runs against the
`https` URL the agent actually signed. The staging compose stack (Caddy in
front) deploys exactly this shape.

Rules for using it safely:

- **Set it only behind a proxy you control.** On a directly-exposed Broker the
  header comes from the caller, and honoring it lets the caller pick the scheme
  its signature is verified against. That is why the flag is off unless the
  value is exactly `true` or `1` — any other value (including a typo) keeps it
  off.
- **The proxy must replace, not append to, any incoming `X-Forwarded-Proto`.**
  Caddy and AWS ALB do this by default. A proxy that appends would leave the
  first (caller-supplied) value in charge.
- Only the scheme is trusted. `X-Forwarded-Host` is deliberately ignored: the
  proxy preserves the `Host` header, and honoring a forwarded host would let a
  request signed for one endpoint verify at another.

Without the flag, the older options still work: terminate TLS in pass-through
mode, or put no TLS-terminating proxy in front.

---

## 5. Development defaults you must not copy

The compose files in this repository exist to run automated tests on a private
network. Several of their settings are deliberately unsafe and must never be
carried into production:

| Setting | Why it is there | Why it must not ship |
|---|---|---|
| `FORA_WELLKNOWN_SCHEME: "http"` | The services serve plain http on the test networks. | Public documents would be fetched unencrypted and could be tampered with in transit. |
| `SKIP_SSRF: "true"` | Test services live on private addresses the guard blocks. | Removes the protection against the Broker being steered into your internal network. |
| `ALLOW_INSECURE: "true"` | Same reason. | Same consequence. |
| `BROKER_ES_ALLOW_HTTP: "true"` | The test stack's Elasticsearch speaks plain `http`. | The Elasticsearch credential and every query would travel unencrypted. |
| `sslmode=disable` in the DSN | The database is on the same private bridge. | Correct only while the connection stays on one host. On a network you do not control exclusively TLS is required: without it every row travels in the clear, and what the login exposes depends on the cluster's authentication method ([`deploy/storage/postgres/CONFIGURATION.md`](../../deploy/storage/postgres/CONFIGURATION.md) §2.3). |
| `BROKER_ALLOW_EPHEMERAL_KEY: "true"` | Tests are torn down between runs, so nothing caches the Broker's identity. | Every restart changes the identity the Broker publishes, and everyone who cached it is left holding a key that no longer verifies. |

---

## 6. Worked example

Values marked *fill in* are specific to your environment.

```
BROKER_DSN=postgres://fora:<password>@<db-host>:5432/fora?sslmode=require
BROKER_ADDR=:8082
BROKER_LOG_LEVEL=INFO
REDIS_URL=rediss://:<password>@<redis-host>:6379/0
BROKER_ID=broker-01
BROKER_DOMAIN=broker.example
BROKER_RELAY_KEY_FILE=/keys/broker-key.json
BROKER_ED25519_SEED=<fill in — see DEPLOYMENT.md §5>
BROKER_REGISTRY_FILE=/config/exchanges.yaml
BROKER_REVOCATION_URL=https://broker.example/.well-known/fora-key-revocations.json
BROKER_REVOCATION_FILE=/revocations/revocations.json
```

Everything not listed is left at its default. In particular `EXA_API_KEY`,
`SKIP_SSRF`, `ALLOW_INSECURE` and `FORA_WELLKNOWN_PORT` stay unset,
`FORA_WELLKNOWN_SCHEME` stays at its `https` default, and
`BROKER_QUERY_PROVIDER` stays at `none`, so this Broker answers requests that
name URLs and has no catalog search (§7). A Broker on the `elasticsearch` provider
adds the settings in §7, over `https`: `BROKER_ES_ALLOW_HTTP` stays unset there too.

The secret values above (`BROKER_DSN`, `REDIS_URL`, `BROKER_ED25519_SEED`) are read
as plain environment variables. The Broker does not know or care where they came
from, so the secret store and the tooling that supplies them are your choice — the
only requirement is that they are present in the environment when the process
starts.

---

## 7. Catalog search providers

A *catalog search* turns an agent's free-text query into catalog URLs. Each URL
then takes the same route as a URL the agent named: the publisher's `fora.json`
names the Exchange, the Exchange supplies the offers, prices and license terms,
and the Broker verifies their signatures. A search can only propose a URL. It
cannot produce an offer the Exchange does not stand behind.

A deployment runs one provider, chosen with `BROKER_QUERY_PROVIDER`. When it fails,
the Broker never falls back to another one.

| Value | What a free-text query does | `search_filters` |
|---|---|---|
| `none` (default) | Goes to EXA, if configured. This path returns no offers yet ([`RUNBOOK.md`](RUNBOOK.md) §6). | A fora-music-v1 key is refused: `INVALID_ARGUMENT` when its value is malformed, otherwise `UNIMPLEMENTED` on the field `search_filters`. A key the profile does not define is ignored. |
| `llm` | A model extracts music tags from the query, and the tags are matched against a copy of the music feed held in memory. | Applied, by the fora-music-v1 profile. A key the profile does not define is ignored. |
| `elasticsearch` | The index owner's search template runs against their index. No model is called and no catalog file is read. | Applied when the deployment's filter schema declares the key; any other key is refused with `UNIMPLEMENTED`. |

### `llm`

| Name | Required? | What it is | Example |
|---|---|---|---|
| `BROKER_QUERY_CATALOG_FILE` | **Required** | Path to the JSONL catalog feed the Exchange ingests. It must hold `music.*` records. | `/catalog/feed.jsonl` |
| `BROKER_QUERY_MODEL` | **Required** | The Bedrock model that extracts the tags. | `amazon.nova-lite-v1:0` |
| `BROKER_QUERY_URI_SCHEME` | Optional | The scheme of the catalog URIs. Default `https`. It must match the Exchange's `EXCHANGE_CATALOG_URI_SCHEME`. | `https` |

The model is reached through the standard AWS configuration chain, so the
deployment also supplies a region and credentials (`AWS_REGION`,
`AWS_SHARED_CREDENTIALS_FILE`). A Broker that cannot find a region refuses to
start.

### `elasticsearch`

Tested with Elasticsearch 7.15.0.

The Broker uses only the search-template and render-template APIs
(`_search/template` and `_render/template`), which every Elasticsearch since 6.x
and OpenSearch serve. It talks to them over plain HTTP, with no client library, so
no product check or client version stands between it and the cluster.

The index belongs to someone else: they populate it and keep it current, and the
Broker only reads it. Indexing, synchronisation and hosting are outside the Broker.
The query belongs to them too. The Broker runs their **search template**: they
decide which fields are searched, how hits rank, how each filter applies and where
a hit carries its URL. [`SEARCH-TEMPLATE.md`](SEARCH-TEMPLATE.md) is the guide for
whoever writes it, and `deploy/broker/search-template.example.mustache` with
`deploy/broker/search-filters.example.json` is a working pair to start from.

| Name | Required? | What it is | Example |
|---|---|---|---|
| `BROKER_ES_URL` | **Required** | The cluster's base URL, over `https`. It carries no credentials, query or fragment. Plain `http` needs `BROKER_ES_ALLOW_HTTP`. | `https://search.example:9243` |
| `BROKER_ES_ALLOW_HTTP` | Optional — **leave unset** | `true` or `1` admits a plain `http` `BROKER_ES_URL`, and relaxes nothing else. Anything else, a typo included, leaves it off. The Broker logs `broker.es.plain_http` at start-up while it is in use. The stack-wide `ALLOW_INSECURE` does not admit it. | *(leave unset)* |
| `BROKER_ES_INDEX` | **Required** | One index or alias. A pattern or a list is refused, because it would widen what the Broker reads. | `catalog-current` |
| `BROKER_ES_TEMPLATE_ID` | One of these two **required** | The id of a search template stored in the cluster (`PUT _scripts/<id>`, the index owner's call; the Broker never stores one). | `fora-catalog` |
| `BROKER_ES_TEMPLATE_FILE` | The other | Path to the template's Mustache text, sent with every search. At most 64 KiB. A template using `{{{ }}}`, `{{& }}` or `{{#join}}` is refused, because those insert a value unescaped, and so is one that changes the delimiters with `{{= }}`, under which they can be written another way. Setting both template settings is refused. | `/keys/search-template.mustache` |
| `BROKER_ES_HIT_URL_PATH` | **Required** | Where a hit carries its URL, as dotted names: `_source.url` for a stored field, `fields.url` for a projected or computed one. An array at any step stands for its first element. | `_source.url` |
| `BROKER_ES_RESULT_LIMIT` | **Required** | The most URLs one search returns, from 1 to 256. It is also the template's `limit` parameter. There is no default, because how many resources one search may return is your decision, and Elasticsearch's own default of 10 must not make it for you. Hits past the limit are cut, lowest ranked first; there is no paging. | `50` |
| `BROKER_ES_TIMEOUT` | Optional | How long one call to the cluster may take, as a Go duration. Default `3s`. It is the deadline on the Broker's own request, so a search never holds a request longer than this. | `1500ms` |
| `BROKER_ES_AUTH` | **Required** | How the Broker authenticates: `none`, `api_key` or `basic`. There is no default. `none` sends no credentials and logs `broker.es.no_auth` at start-up. A credential setting of another mode is refused. | `api_key` |
| `BROKER_ES_API_KEY` | With `api_key` | The API key: base64 of `id:api_key` from the key's creation answer (later versions also return it ready-made as `encoded`). | *(from the file below)* |
| `BROKER_ES_API_KEY_FILE` | Alternative to the above | Path to a file holding the same value. Only read when `BROKER_ES_API_KEY` is empty. | `/keys/es-api-key` |
| `BROKER_ES_USERNAME` | With `basic` | The user name. | `broker-reader` |
| `BROKER_ES_PASSWORD` | With `basic` | The password. | *(from the file below)* |
| `BROKER_ES_PASSWORD_FILE` | Alternative to the above | Path to a file holding the password. Only read when `BROKER_ES_PASSWORD` is empty. | `/keys/es-password` |
| `BROKER_ES_CA_FILE` | Optional | Path to a PEM file of the CA that signed the cluster's certificate, for a private CA. The CA file replaces the system roots for this connection: it adds to nothing. Unset means the system roots. Certificate verification cannot be switched off. | `/keys/es-ca.pem` |
| `BROKER_ES_FILTERS_SCHEMA` | Optional | The deployment's filter vocabulary: a JSON Schema naming the `search_filters` keys the template applies and the format of each value. Unset means the search applies no filter, and every `search_filters` key is refused. The rules it must follow are below. | *(from the file below)* |
| `BROKER_ES_FILTERS_SCHEMA_FILE` | Alternative to the above | Path to a file holding the same document. Only read when `BROKER_ES_FILTERS_SCHEMA` is empty. Give the identity service the same file (`IDENTITY_MCP_SEARCH_FILTERS_SCHEMA_FILE`), so `fora_discover` tells agents the same vocabulary. | `/keys/search-filters.json` |

Every setting that is missing or invalid is named in one error, so a deployment is
fixed in one pass. The credentials are never logged.

#### What the index owner provides

- **A search template** that renders the request body for `{query, filters,
  limit}`, stored in the cluster or kept in the file `BROKER_ES_TEMPLATE_FILE`
  names. The cluster's `script.allowed_types` must allow it: `stored` for a stored
  template, `inline` for a file.
- **A URL in every hit**, at the path `BROKER_ES_HIT_URL_PATH` names: a stored field
  read from `_source`, or one the template projects or computes (`fields`,
  `script_fields`). Project only the URL: the Exchange supplies everything else.
- **Each document's URL equal to the Exchange's stored URI, byte for byte.** That
  URI is the Exchange's `EXCHANGE_CATALOG_URI_SCHEME`, then `://`, the domain and
  the path, as the publisher's feed gave them. The Exchange looks a URL up by its
  bytes, with nothing decoded or case-folded, so `http` for `https` or an
  upper-case host is a URL the Exchange does not hold, and the document can never
  produce an offer.
- **An index that holds only what this Broker's agents may discover.** There is no
  per-agent or per-seat filtering. If the index holds more, give the Broker a
  filtered alias.
- **A read-only key.** It needs the `read` privilege on the index or alias and
  nothing else: rendering and searching are the only calls the Broker makes. On a
  7.x cluster without TLS on its HTTP interface, API keys are off until
  `xpack.security.authc.api_key.enabled` is set to `true`.

For example, the index owner creates the Broker's key like this:

```
POST /_security/api_key
{"name": "broker", "role_descriptors": {"broker_read": {"indices": [
  {"names": ["catalog"], "privileges": ["read"]}]}}}
```

#### What the Broker sends

Every search is `POST /<BROKER_ES_INDEX>/_search/template` with the template and
three parameters, and the request id as `X-Opaque-Id`:

```json
{
  "id": "fora-catalog",
  "params": {
    "query": "central bank",
    "filters": {"news": {"language": "en"}},
    "limit": 50
  }
}
```

- `query` is the agent's text, and it is **left out** when the request has none, so
  a template tells a filter-only request by its absence. The template engine treats
  any string as present, the empty one included.
- `filters` holds the filters the schema accepted, nested by dot: `news.language`
  arrives as `filters.news.language`. It is left out when there is none.
- `limit` is `BROKER_ES_RESULT_LIMIT`.
- With `BROKER_ES_TEMPLATE_FILE`, the body carries `"source": "<the file>"` instead
  of `"id"`.

#### The filter schema

The schema is the contract between the agents and the template, so the Broker
holds it to rules that keep it one flat list of keys:

- `type` is `"object"`, `properties` declares at least one key, and
  `additionalProperties` is `false`, so a key outside the schema is refused rather
  than ignored.
- Each key is dot-separated parts of letters, digits, `_` and `-`, and no key is
  another's prefix (`news` beside `news.language` would make `filters.news` both a
  value and an object).
- Nothing at the top level constrains the whole object (`required`, `oneOf`,
  `allOf`, `minProperties` and the rest are refused): a request carrying only a
  query must pass.
- No reference keyword anywhere (`$ref`, `$defs`, `$id`, `$anchor`, `$dynamicRef`,
  `$dynamicAnchor`). The identity service embeds the schema in its `fora_discover`
  tool, where a reference would resolve against the tool instead.
- No `default` anywhere. A filter applies only when the agent sends it: the
  identity service's MCP library would fill a default into every call, while the
  Broker applies none to a request it receives directly.
- `$schema` is absent or exactly `https://json-schema.org/draft/2020-12/schema`.
- The protocol's rules for a published registration schema apply as well: at most
  16 KiB, a bounded depth and evaluation cost, and `pattern` limited to an alphabet
  every SDK language reads alike. `format` is not checked: state a value's shape
  with `pattern` or `enum`.

A schema that breaks a rule stops the start, naming `BROKER_ES_FILTERS_SCHEMA` and
the rule.

#### How a search behaves

With `BROKER_QUERY_PROVIDER=elasticsearch`, a query returns one group per URL the search template matched, in the order the template ranks them, and every group is stamped `DISCOVERY_METHOD_SEARCH`.
Two hits with one URL give one group. The example template breaks equal scores on the URL.

A document whose URL the Exchange does not hold gets no offer: beside hits that have offers, its group comes back empty with the reason `NOT_IN_CATALOG`, and when no hit has an offer, the whole answer is `NOT_IN_CATALOG` with no groups.

A query no document matches answers `NOT_IN_CATALOG` with no groups.

The search applies the `search_filters` keys the deployment's filter schema declares: each accepted filter reaches the template as `filters.<key>`, and a well-formed value no document has answers `NOT_IN_CATALOG`.

A `search_filters` key the schema does not declare is refused with `UNIMPLEMENTED` on the field `search_filters.<key>`, and with no schema every key is.

A value that breaks its key's rule in the schema is refused with `INVALID_ARGUMENT` on the field `search_filters.<key>`.

Neither refusal quotes the value, and no search runs. Results that ignored a filter
would answer a different question, so a filter is never dropped.

A request that names URLs never reaches the index, so it works while the cluster is down.

A failed search answers `TEMPORARILY_UNAVAILABLE` with no groups, and no Exchange is contacted.
That covers a connection failure, a timeout, a refused request, a template the
cluster cannot render, a missing index, an unreadable answer, a timed-out or
partial result, a redirect, an answer over 1 MiB, and a hit with no usable URL at
`BROKER_ES_HIT_URL_PATH`: a result that may be incomplete is not an answer. The
search is never retried, because the agent can retry. The cause is on the
`broker.resolve.query_search_failed` log line ([`RUNBOOK.md`](RUNBOOK.md) §2.2).

#### The boot probe

At start-up the Broker renders the template once without searching
(`_render/template`), then runs it once with a limit of one, both with a fixed
query text. Both need only the `read` privilege.

| Outcome | What the Broker does |
|---|---|
| Both calls succeed | Starts, and logs `broker.es.ready`. |
| The cluster refuses the credentials (`401` or `403`) | **Refuses to start**, naming the credential settings. These are your own settings, and waiting does not fix them. |
| The stored template is missing, the template does not render as a JSON object, or the index is missing (`404` or `400`) | Starts, and logs `broker.es.misconfigured`. |
| The probe's search returns a hit with no URL at `BROKER_ES_HIT_URL_PATH` | Starts, and logs `broker.es.misconfigured` naming the path. |
| No answer, a timeout or a server error | Starts, and logs `broker.es.unreachable`. |

The middle rows can be the index owner's side, so they do not stop the Broker. An
outage of the index must not take requests that name URLs, or purchases, down with
it. Queries answer `TEMPORARILY_UNAVAILABLE` until it heals, and nothing needs a
restart. The probe's query rarely matches anything, so a broken hit path can also
first show on a real query.

A node that has just restarted can answer `401` for a few seconds while it recovers,
so a Broker started in that window refuses to start. Run it under a restart policy,
as the deployment bundle does, and the next attempt succeeds.
