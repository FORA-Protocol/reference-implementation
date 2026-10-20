# FORA TigerBeetle — Configuration Reference

This document lists every setting the ledger and the Exchange understand about
TigerBeetle, what each one does, and what happens if you leave it out. It is a
reference, not a procedure — for the step-by-step install see
[`DEPLOYMENT.md`](DEPLOYMENT.md), and for day-to-day operation see
[`RUNBOOK.md`](RUNBOOK.md).

Audience: you, the DevOps engineer deploying the ledger. You do not need to read
the source code. Words that may be new are explained the first time they appear.

TigerBeetle is the **financial ledger** the Exchange settles into: agent
balances, reservations, settled charges, publisher revenue and the platform fee
all live in it. It is a ready-made product from another company — the only part
of this platform your team has probably never run before.

---

## 1. Read this before you plan anything else

TigerBeetle needs three things from the machine and the container runtime that
run it: a Linux kernel new enough for `io_uring`, permission to use the
`io_uring` system calls, and permission to lock memory. This document calls them
the **host capabilities** — "host" meaning the machine and the container runtime,
not the Exchange. Some hosting platforms will not give them. If yours will not,
TigerBeetle cannot run there and no setting will change that. Check this first.

### 1.1 The four mandatory capabilities

| Capability | What it is | Why TigerBeetle needs it |
|---|---|---|
| `io_uring` (Linux kernel **5.6 or newer**) | The Linux interface for disk and network work that runs in the background. | TigerBeetle does all of its disk and network work through it. There is no other way. |
| `kernel.io_uring_disabled` = `0` or `1` | A host-wide switch that can turn `io_uring` off for everything on the machine. | At `2` the kernel refuses `io_uring` outright, and **no container setting can override it** — see below. |
| `seccomp=unconfined` | Turns off the container runtime's default system-call filter. | Docker 25 and newer block the `io_uring` system calls in their default filter. |
| `IPC_LOCK` capability | Permission to lock memory so the kernel never moves it to disk. | TigerBeetle locks its cache in memory rather than risk the ledger's data being moved to disk. |

**The sysctl is the one that catches people out**, because it is not a container
setting and grants nothing when you fix the container. Several hardened and recent
distributions ship it at `2`, which disables `io_uring` for unprivileged processes
across the whole machine. Check it before anything else:

```bash
sysctl kernel.io_uring_disabled
# Expect: kernel.io_uring_disabled = 0
# 1 is also workable. 2 means TigerBeetle cannot run on this host at all until
# the host owner changes it — seccomp=unconfined will not help.
```

TigerBeetle names it in the failure itself, which is the quickest way to tell this
apart from a container-permission problem:

```
error(io): io_uring is not available
error(io): likely cause: the syscall is disabled by sysctl, try 'sysctl -w kernel.io_uring_disabled=0'
error: PermissionDenied
```

**These are required on the ledger container AND on the Exchange container.**
The Exchange embeds the TigerBeetle client, and that client uses `io_uring` too.
Granting them only to the ledger leaves the Exchange failing to start.

Without them you get one of these, and nothing else works:

```
failed to initialize IO: PermissionDenied    ← seccomp=unconfined is missing
error: SystemResources                       ← IPC_LOCK is missing
```

Under Docker Compose that is:

```yaml
security_opt:
  - "seccomp=unconfined"
cap_add:
  - "IPC_LOCK"
```

**A managed platform that refuses `seccomp=unconfined` or `IPC_LOCK` cannot host
either container.** Several do refuse — many container-hosting services, and
most limited hosting plans. Check yours before you plan anything else. The
verification command is [`DEPLOYMENT.md`](DEPLOYMENT.md) §2.

### 1.2 There is no managed TigerBeetle service

Nobody sells TigerBeetle as a hosted service — not AWS, not GCP, not Azure, not
the company that makes it. Unlike PostgreSQL or Redis, there is no "just point it
at the managed endpoint" option.

You have to plan for this yourself:

- It runs on **a machine you manage**, with a disk you supply.
- **You have to write the backup and restore procedure yourself.** TigerBeetle
  has no backup command — see [`RUNBOOK.md`](RUNBOOK.md) §5.
- **Updates, monitoring and capacity planning are your job**, on your own
  schedule.

### 1.3 Image and architecture

| Fact | Value |
|---|---|
| Image | `ghcr.io/tigerbeetle/tigerbeetle:0.17.8` — pinned, never `latest` |
| Built here? | **No.** There is no TigerBeetle Dockerfile in this repository; it runs from the upstream image unmodified. |
| Exchange architecture | **amd64 only.** The TigerBeetle Go client links a native library through CGO, and CGO cannot build for a different processor type than the one it runs on, so the Exchange image is built for the host architecture only. There is no ARM build. |

---

## 2. The ledger's own settings

TigerBeetle is configured entirely on its command line — there are no
environment variables and no configuration file. Two commands matter: `format`
creates the data file once, `start` runs the replica.

| Setting | Command | What it is | Value used here |
|---|---|---|---|
| `--cluster` | `format` | The cluster's numeric id, written into the data file and fixed forever. | `0` — must equal the Exchange's `EXCHANGE_BILLING_TB_CLUSTER_ID` |
| `--replica` | `format` | This replica's index within the cluster, counting from zero. | `0` |
| `--replica-count` | `format` | How many replicas the cluster has. Written into the data file and fixed forever; changing it means formatting a new file. | `1` (single replica — no spare copy, see §5) |
| *data file path* | `format` and `start` | The single file holding the entire ledger. Named `<cluster>_<replica>.tigerbeetle` by convention. | `/data/0_0.tigerbeetle` |
| `--addresses` | `start` | The address the replica listens on. | `0.0.0.0:3000` inside the container |
| `--cache-grid` | `start` | How much memory the replica uses to cache the data file. Defaults to `1GiB`. TigerBeetle's own guidance is to set it as large as the machine allows: **total RAM minus 3 GiB for TigerBeetle itself minus 1 GiB for the system** — so 12 GiB on a 16 GiB machine. | `256MiB` in development; size it for your machine, see §6 |
| `--development` | both | Turns off the Direct I/O requirement and allows smaller caches. | **Development only — never in production**, see §6 |

Two facts that are not settings, but they limit where you can run it:

- **The client port has no authentication and no TLS.** This is what TigerBeetle's
  own manual says, not a gap in this integration: it states plainly that it "does
  not support authentication" and that you should "never allow untrusted users or
  services to interact with it directly". The Exchange therefore sends no password
  and no key when it connects — it supplies only a cluster id and an address.
  Anything that can reach port 3000 can move money, so where you put it on the
  network **is** the only protection it has: keep the ledger on a private network
  reachable only from the Exchange, and do not publish its port to the host.
- **`format` refuses to run against an already-formatted file.** That is why the
  start-up command tests for the file first and formats only when it is absent
  (see [`DEPLOYMENT.md`](DEPLOYMENT.md) §4). A separate service that only ran
  `format` once would fail on every restart, once the volume held data.

---

## 3. The Exchange-side settings

These are read by the **Exchange**, not by the ledger. Every other Exchange
setting is documented in
[`src/exchange/CONFIGURATION.md`](../../../src/exchange/CONFIGURATION.md); only
the ledger-related ones are listed here.

| Name | Required? | What it is | Default |
|---|---|---|---|
| `FORA_BILLING_ADAPTER` | **Set it to `tigerbeetle`** | Chooses which billing backend the Exchange uses. Leave it out and the Exchange bills nothing — the default `free` backend approves everything and records nothing. | `free` |
| `EXCHANGE_BILLING_LEDGER` | **Required** | The ISO 4217 **numeric** currency codes the deployment bills in, comma-separated — one ledger each. Only `978` (EUR) and `840` (USD) are accepted, and the **order decides the advertised `base_currency`** — see §4. | *(none)* |
| `EXCHANGE_BILLING_TB_ADDRESS` | **Required** | The ledger's address as **`IP:port`**. **Hostnames are rejected by the client** — `tigerbeetle:3000` does not work, `10.0.1.7:3000` does. | *(none)* |
| `EXCHANGE_BILLING_TB_CLUSTER_ID` | Optional | The cluster id. Must match the `--cluster` the data file was formatted with. | `0` |
| `EXCHANGE_BILLING_HOLD_GRACE` | Optional | How much longer a reservation stays alive than the signed download URL, so a settlement can still land right at the end of the URL's validity. | `1m` |
| `EXCHANGE_BILLING_TB_OP_TIMEOUT` | Optional | How long a single call to the ledger may take before it fails with an error the Exchange can retry. Without it a call to an unreachable cluster would wait forever. | `5s` |

### If you choose the ledger and it cannot connect, the Exchange will not start

This is on purpose, and you can rely on it. With `FORA_BILLING_ADAPTER=tigerbeetle`
set, a ledger the Exchange cannot configure or reach **stops the Exchange from
starting**. It never quietly falls back to the `free` backend, because a
misconfiguration that silently disabled billing would give content away without
recording a charge.

The exact messages, each of which stops boot:

```
billing: EXCHANGE_BILLING_LEDGER is required for FORA_BILLING_ADAPTER=tigerbeetle
billing: EXCHANGE_BILLING_TB_ADDRESS is required for FORA_BILLING_ADAPTER=tigerbeetle
billing: unsupported EXCHANGE_BILLING_LEDGER entry 826 (supported: 840=USD, 978=EUR)
billing: EXCHANGE_BILLING_LEDGER lists 978 (EUR) more than once
billing: invalid EXCHANGE_BILLING_LEDGER entry "EUR": ...
billing: invalid EXCHANGE_BILLING_TB_CLUSTER_ID: ...
billing: invalid EXCHANGE_BILLING_HOLD_GRACE: ...
billing: invalid EXCHANGE_BILLING_TB_OP_TIMEOUT: ...
billing: connect TigerBeetle at 10.0.1.7:3000: ...
billing: TigerBeetle health check: ...
```

Only an **unrecognised** adapter name behaves differently: it logs a warning and
falls back to `free`. So a typo such as `FORA_BILLING_ADAPTER=tigerbeatle` gives
you a running Exchange that bills nothing. Check the boot line
([`RUNBOOK.md`](RUNBOOK.md) §2.2) rather than assuming.

---

## 4. Currencies are deployment-wide, not per-tenant

**A deployment bills in one or more currencies, and every tenant on it shares
that set.** `EXCHANGE_BILLING_LEDGER` names them by ISO 4217 numeric code and is
read once at boot.

| Value | Bills in |
|---|---|
| `978` | EUR |
| `840` | USD |
| `978,840` | EUR and USD |
| anything else | **the Exchange stops at start-up with an error** |

### One ledger per currency, and nothing crosses between them

Each currency gets its own TigerBeetle ledger, numbered with that currency's
code. An agent therefore holds **one balance per currency**, not one balance:

- A EUR-priced article is paid out of the agent's EUR balance. A USD-priced
  article is paid out of its USD balance.
- **Nothing is converted.** An agent with 10 EUR and no USD cannot buy a
  USD-priced article, however much EUR it holds. The charge is denied for
  insufficient balance.
- Balances are never added together. An operator report shows one row per
  (agent, currency) and no total, because a total across currencies would be a
  number that means nothing.
- Commission and owner revenue follow the same rule: commission collected in
  EUR sits in the EUR platform account, commission collected in USD in the USD
  one.

**A catalog term priced in a currency the deployment does not bill in is
refused** with the reason `currency mismatch`, and the agent does not get the
content. That is the same rule as before, applied to a set instead of a single
currency: a EUR-and-USD deployment sells EUR- and USD-priced resources and
refuses a GBP-priced one.

### The ORDER of the list sets `base_currency`

The Exchange's well-known document advertises a single `base_currency`, because
the protocol field holds one value. **That value is the FIRST code in
`EXCHANGE_BILLING_LEDGER`**: `978,840` advertises `EUR`, and `840,978`
advertises `USD`.

Two rules follow, and they matter because nothing in the running system reads
`base_currency` back — a wrong value is invisible from the inside:

- **Add a new currency at the END of the list.** Appending leaves the advertised
  value untouched.
- **Reordering the list is a deliberate decision**, never tidying. Agents
  comparing offers across exchanges read our prices through `base_currency`.

The boot line prints both, so the running value can be checked without reading
the environment:

```
billing adapter: tigerbeetle currencies=EUR,USD base_currency=EUR address=10.0.1.7:3000 hold_timeout=6m0s
```

### Adding a currency to a running deployment

This is a repeatable, **non-destructive** procedure: it adds a currency and
changes nothing about the currencies already in use. The currency list is
`EXCHANGE_BILLING_LEDGER` (the `billing_ledger` input of
`deploy/terraform/modules/compose-stack`), and the welcome credit is
`EXCHANGE_DEFAULT_AGENT_CREDIT` (its `default_agent_credit` input).

#### First: is it config-only, or does it need a release?

| Currency | What it takes |
|---|---|
| EUR (`978`) or USD (`840`) | **Config only.** Both are already in every table listed below. Change the currency list, apply it, restart the Exchange. |
| Any other ISO 4217 currency | **Four source edits and a release**, then the config-only procedure. The list is below; none of it is optional. |

The reason for the split: the Exchange validates `EXCHANGE_BILLING_LEDGER`
against a table compiled into the binary, and refuses to start on a code it does
not find there. That is deliberate — a ledger the Exchange cannot name is a
ledger it cannot derive account ids for.

#### A currency the Exchange has never run: the four edits

The supported set is enumerated in four places. Three of them cannot read the
first, because Terraform and shell cannot import Go, so all four are edited
together:

| # | Where | What it is | What happens if you skip it |
|---|---|---|---|
| 1 | `src/exchange/internal/billing/tigerbeetle/currency.go` | `ledgerCurrencies`, the numeric-to-alpha table the whole binary reads | The Exchange refuses to start on the new code |
| 2 | `deploy/terraform/modules/compose-stack/variables.tf` | The `contains([...])` validation on `billing_ledger` | `terraform plan` refuses the new code, so the config change never applies |
| 3 | `deploy/terraform/scripts/fund-staging-agent.sh` | The `CURRENCY)` case that maps the alpha code to its ledger | Funding refuses the currency, so no agent can be given a balance in it |
| 4 | `deploy/terraform/scripts/agent-balances.sh` | The case that maps the ledger number back to an alpha code | The balance report stops on "unsupported ledger" and reports nothing at all |

Each failure surfaces a long way from its cause, which is why a guard test
(`deploy/terraform/scripts/tests/currency-table-consistency-test.py`, run by
`make test-terraform`) fails when the four stop agreeing and names the ones that
were missed. Run it after the edits and before the release.

Editing only the Go table looks like it works — the binary starts, the boot line
reads correctly — and then `terraform plan` refuses the variable change that
would actually put the currency into service.

With the four edits in, build and deploy an Exchange that carries them, as any
other release. After that release is running, the config-only procedure below
applies unchanged.

#### Config-only procedure

1. **Append the new code to the currency list.** Append, do not prepend: the
   FIRST entry is what the Exchange advertises as its `base_currency`, and
   moving another code to the front changes what every other party is told our
   prices are denominated in. Through compose-stack, `terraform plan` refuses an
   unsupported code, a duplicate and an empty list; set directly, each stops the
   Exchange's boot.
2. **Decide the welcome credit for the new currency.** Either add a
   `CODE=AMOUNT` pair to the welcome credit, or leave it out — an agent then
   gets no automatic grant in the new currency and is funded by the operator
   script instead. A currency in the welcome credit that is NOT in the currency
   list stops the Exchange from starting, which is why the two are changed
   together.
3. **Apply and restart the Exchange.** The currency set is read once at boot.
4. **Check the boot line.** It prints the resolved list and the advertised
   currency:

   ```
   billing adapter: tigerbeetle currencies=EUR,USD base_currency=EUR ...
   ```

   If `base_currency` is not what it was before this change, the list was
   reordered rather than appended — revert to the previous order before going
   further.
5. **Verify the existing currencies still work.** Run one paid transaction in
   each currency that already worked, end to end, and confirm the charge landed
   on that currency's balance. This is the check that matters most: the new
   currency having no traffic yet is harmless, an existing currency breaking is
   not.
6. **Verify the new currency.** Publish or pick one catalog term priced in it,
   fund one agent in that currency with the operator funding script
   (`CURRENCY=<code>`), and run one paid transaction. Confirm the charge debited
   the new currency's balance and left the others unchanged.
7. **Fund the agents that need it.** A currency added after an agent registered
   leaves that agent with a zero balance in it: the welcome credit is granted at
   registration and is not re-granted later. Existing agents are funded through
   the operator funding sweep, once per currency — there is no backfill, by
   design, because a backfill would hand money to every account that ever
   registered.

#### What this procedure does NOT do

- **It does not convert anything.** An agent's existing balance stays in its own
  currency. Adding USD does not give a EUR-funded agent any USD.
- **It does not re-grant welcome credits.** See step 7.
- **It does not change existing account ids.** The currency is part of each
  account id, so adding a currency creates NEW accounts and leaves every
  existing one exactly as it was. This is what makes the procedure
  non-destructive.

#### Rollback, and its limits

Removing a currency from the currency list and restarting stops the Exchange
billing in it: terms priced in that currency are refused with `currency
mismatch`, and balance reports no longer include it. That much is clean and
reversible.

What rollback does **not** undo:

- **Money already in that currency stays on its ledger.** The accounts and
  transfers are not deleted; they become unreachable through the Exchange until
  the currency is configured again. Nothing is lost, and nothing is spendable.
- **Welcome credits already granted are not reclaimed.** They were posted
  transfers.
- **Charges already settled in that currency are not reversed.** A refund needs
  the currency configured, so reverse anything that needs reversing BEFORE
  removing the currency.

So the safe order for removing a currency is: stop selling in it (remove the
priced terms from the catalog), settle or reverse what is outstanding, and only
then remove it from the currency list.

---

## 5. Facts you cannot configure, but must know

| Fact | Detail |
|---|---|
| **The reservation timeout is derived, not set** | A reservation lives for the signed-URL lifetime (5 minutes) **plus** `EXCHANGE_BILLING_HOLD_GRACE` — 6 minutes by default. There is no separate setting for it. If a settlement never arrives, the reservation expires by itself at that point and the money returns to the agent. |
| **The amount scale is fixed at 8 decimal places** | The ledger stores integers only, at 10⁻⁸ of the currency unit. `€1.00` is stored as `100000000`. **Divide by 100,000,000 when reading balances** — not by 100. A price with more than 8 decimal places is rejected as an invalid request. The scale cannot be changed once the ledger is formatted. |
| **Account ids are unsalted in production** | An account id is `sha256(prefix + currency + ":" + business id)`, truncated to its low 16 bytes and read little-endian. In production nothing extra is added to that input (that is what "unsalted" means), so the id can be calculated from the business id alone. For an **owner** or the **platform fee** that means anyone holding the public id can calculate the account id and look the account up. An **agent** is different: its business id is the agent's `billing_ref`, not the agent's name, so the id cannot be calculated from anything public — you must read the `billing_ref` out of PostgreSQL first ([`RUNBOOK.md`](RUNBOOK.md) §4.1). The calculator is in [`RUNBOOK.md`](RUNBOOK.md) §3.2. |
| **Account ids are unique per cluster, not per ledger** | Two ledgers in one cluster share the id space. That is why the currency is part of every account id: without it, one agent's EUR and USD accounts would calculate to the same id and the second ledger could never create its account. The same applies to transfer ids, which is why the welcome-credit slot carries the currency too. |
| **A single replica has no spare copy** | `--replica-count=1` means the ledger is one process and one file. There is nothing to fail over to. TigerBeetle supports clusters with several replicas; this deployment does not use one. |

### The three account classes

Every account the Exchange touches is one of three kinds. There are no others.

| Class | Business id hashed into the account id | Holds |
|---|---|---|
| Agent | `agent:{billing_ref}` | The agent's prepaid balance. Limited: an agent can never spend more than it has been given. |
| Resource-owner revenue | `owner:revenue:{resource_owner_id}` | What the publisher has earned. |
| Platform fee | `platform:fee` | The platform's commission. One account for the whole deployment. |

`billing_ref` is the agent's account number: a random UUID the Exchange creates
when the agent registers and stores on the agent's row in PostgreSQL (ADR-021).
It is **not** the agent's name, so the agent's account id cannot be calculated
without reading it from the database first ([`RUNBOOK.md`](RUNBOOK.md) §4.1). The
agent account is created at registration; the other two are created on first use.

Note the prefix for the one who gets paid is **`owner:`**, not `publisher:` — an
owner may cover several domains, so the one who gets paid is not the same as the
tenant.

---

## 6. Development defaults you must not copy

The compose file in this repository runs a ledger for automated tests on a
laptop. Several of its settings are wrong for a machine holding real money.

### `--development` — the one that matters

`--development` appears on both `format` and `start` in the development compose
file. **Drop it from both in production.**

What it turns off is **Direct I/O** (`O_DIRECT`). Direct I/O skips the operating
system's page cache, so when TigerBeetle reports a transaction as committed, the
bytes are genuinely on the device. Turn it off and writes land in the kernel's
page cache first — so **a host crash can lose transactions the ledger already
reported as committed.** That is a bad trade for the ledger that publisher
payments are calculated from.

The reason it exists is simple: Docker overlay filesystems, Docker bind mounts
and macOS Docker volumes frequently do not support `O_DIRECT` at all, so
TigerBeetle would refuse to start on a developer's machine. Correct on a laptop;
wrong on a machine with a real disk.

Running without it therefore requires:

- the flag **dropped from both `format` and `start`**;
- the data volume on a filesystem that **supports `O_DIRECT`** — a real block
  device with ext4 or xfs, **not** an overlay filesystem and not a bind mount;
- `seccomp=unconfined` and `IPC_LOCK` **kept** — they are independent of Direct
  I/O and are required either way (§1.1).

### The rest

| Development setting | Why it is there | Why it must not ship |
|---|---|---|
| `--cache-grid=256MiB` | A memory limit for the build machines. | Chosen to fit a build machine, not to handle your traffic. Size it for the machine you are deploying on: total RAM − 3 GiB − 1 GiB. Note the replica allocates well beyond the cache figure itself — a 512 MiB grid cache reports about **2.5 GiB allocated** at start-up, so leave headroom or the process is OOM-killed with `exited with code 137` and no log line. |
| `--cluster=0` | The obvious value for a temporary cluster. | Works, but a distinctive cluster id makes it impossible to point an Exchange at the wrong ledger by accident. |
| `--replica-count=1` | One container is enough to test against. | No spare copy: one process, one file, nothing to fail over to. |
| Data volume on a Docker named volume | Portable across developer machines. | Overlay and bind storage frequently cannot do `O_DIRECT` at all — the exact reason `--development` exists. |
| Port published to the host | Convenient for connecting TigerBeetle's own interactive command line (the `repl`) from the same machine. | The client port has no authentication (§2). Do not expose it beyond the Exchange. |

---

## 7. Worked example

Values marked *fill in* are specific to your environment.

The ledger's command line, with `--development` gone:

```bash
tigerbeetle format --cluster=1 --replica=0 --replica-count=1 /data/0_0.tigerbeetle
# Expect:
#   info(io): creating "0_0.tigerbeetle"...
#   info(io): allocating 1.06298828125GiB...
#   info(main): 0: formatted: cluster=1 replica_count=1

tigerbeetle start --addresses=0.0.0.0:3000 --cache-grid=12GiB /data/0_0.tigerbeetle
# Expect:
#   info(main): 0: Allocated 2574MiB during replica init
#   info(main): 0: Grid cache: 512MiB, LSM-tree manifests: 128MiB
#   info(main): 0: cluster=1: listening on 0.0.0.0:3000
#
# The two figures above are from a 512MiB grid cache; yours scale with the
# --cache-grid you set. 12GiB suits a 16 GiB machine — see §2.
```

The Exchange's environment:

```
FORA_BILLING_ADAPTER=tigerbeetle
EXCHANGE_BILLING_LEDGER=978,840
EXCHANGE_BILLING_TB_ADDRESS=<fill in — the ledger's IP>:3000
EXCHANGE_BILLING_TB_CLUSTER_ID=1
EXCHANGE_BILLING_HOLD_GRACE=1m
EXCHANGE_BILLING_TB_OP_TIMEOUT=5s
```

`978,840` makes this a **EUR and USD** deployment: catalog terms may be priced in
either, a term in any other currency is denied, and the two balances never mix
(§4). `978` comes first, so this Exchange advertises `EUR` as its
`base_currency`. `EXCHANGE_BILLING_TB_CLUSTER_ID` matches the `--cluster` the
data file was formatted with — a mismatch fails the Exchange's boot health
check.

Both containers — ledger **and** Exchange — carry `seccomp=unconfined` and
`IPC_LOCK` (§1.1), and the data volume is a real block device with a filesystem
that supports `O_DIRECT` (§6).
