# FORA Vault — Deployment Instructions

This document tells you, the DevOps engineer running the key store for the FORA
platform, how to prepare HashiCorp Vault and point the Identity Service at it. You do
not need to read the source code. Words that may be new are explained the first time
they appear. Follow the steps in order.

Every setting mentioned here is described in full in
[`CONFIGURATION.md`](CONFIGURATION.md). Once Vault is running, day-to-day operation is
in [`RUNBOOK.md`](RUNBOOK.md).

---

## 1. What Vault holds, and why it is the sensitive one

The Identity Service creates a private key for every agent that signs up, and stores it
in Vault. It never gives that key out — when an agent asks it to buy something, the
service signs the request on the agent's behalf.

That makes Vault the one store where a read is as damaging as a write. **Anyone who can
read it can act as any agent in it**: buy content, spend the agent's money, and file
usage reports in its name. Set it up with that in mind, and treat its backups the same
way.

Nothing else in the platform uses Vault. The Exchange and the Broker do not touch it.

---

## 2. What you need before you start

| What | Where it goes | How to check you have it |
|---|---|---|
| A running Vault, unsealed | `VAULT_ADDR` | `vault status` → `Sealed  false` |
| A token that can create engines and policies (an operator token, used for this setup only) | your shell | `vault token lookup` succeeds |
| The Vault CLI, or the API | — | `vault version` |

If you do not have a Vault yet, a managed Vault service works well here and is the
quickest way. Whatever you use, read [`CONFIGURATION.md`](CONFIGURATION.md) §4 before
you set it up — auto-unseal, TLS, storage you can snapshot and an audit device are all
much easier to put in place now than later.

**Do not use a `-dev` Vault.** It keeps nothing across a restart, so every agent key in
it is lost when the process stops.

---

## 3. Step 1 — enable the secrets engine

Give the platform its own KV version 2 engine rather than sharing the default one.
"KV version 2" is Vault's ordinary key/value engine with version history; version 1
will not work.

```bash
vault secrets enable -path=fora-agents -version=2 kv
# Expect: Success! Enabled the kv secrets engine at: fora-agents/
```

Confirm it is there and is version 2:

```bash
vault secrets list
# Expect a row for fora-agents/ with Type "kv", e.g.
# Path             Type    Accessor       Description
# ----             ----    --------       -----------
# fora-agents/     kv      kv_8f7715da    n/a
```

Note the path you chose — it becomes `IDENTITY_KV_MOUNT`.

---

## 4. Step 2 — write the policy

The Identity Service needs four operations on one prefix and nothing else. The rules are
in [`identity-policy.hcl`](identity-policy.hcl), the same file the e2e stack and the
tests use, and the one a deployment's own bootstrap should render. Render it with your mount name in place of `fora-agents`. If you set
`IDENTITY_KV_PREFIX` to something other than `agents`, change `agents` in both paths of
the rendered file too.

```bash
sed 's/${KV_MOUNT}/fora-agents/g' deploy/storage/vault/identity-policy.hcl > fora-identity.hcl

vault policy write fora-identity fora-identity.hcl
# Expect: Success! Uploaded policy: fora-identity
```

> **`delete` on the metadata path is not optional.** It is what erases every version of
> a secret. Without it a revoked key is recorded as revoked but its private key stays
> sitting in Vault's version history — readable by anyone who can read the store, which
> is the exact leak a revocation exists to close.
> [`CONFIGURATION.md`](CONFIGURATION.md) §3 has the full failure table.

---

## 5. Step 3 — create the service's token

```bash
vault token create -policy=fora-identity -period=768h -field=token
# Expect: a token beginning "hvs."
```

`-period=768h` makes it renewable rather than simply expiring after 32 days; renewal
is §3 of [`RUNBOOK.md`](RUNBOOK.md). Store the token in your secret manager and supply
it to the Identity Service as `VAULT_TOKEN`.

**Do not use your operator token, and do not use a root token.** The setup token above
was needed only for §3 and §4; the service gets this narrow one.

---

## 6. Step 4 — verify the token can do exactly what is needed

Run these **as the new token**, not as your operator token. They prove the policy is
right before the Identity Service depends on it.

```bash
export VAULT_TOKEN=<the token from §5>
TP=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA   # a placeholder key name

vault kv put fora-agents/agents/probe.example.org/$TP seed=x
# Expect: a table ending with
# version            1

vault kv list fora-agents/agents
# Expect:
# Keys
# ----
# probe.example.org/

vault kv list fora-agents/agents/probe.example.org
# Expect: the placeholder name above

vault kv metadata delete fora-agents/agents/probe.example.org/$TP
# Expect: Success! Data deleted (if it existed) at:
#         fora-agents/metadata/agents/probe.example.org/AAAA...
```

If the last one answers `permission denied`, the policy is missing `delete` on the
metadata path — go back to §4. That failure will not show up again until the first time
someone tries to revoke a compromised key, which is the worst possible moment to find
it.

Clean up the probe entry if anything is left:

```bash
vault kv list fora-agents/agents
# Expect: "No value found at fora-agents/metadata/agents" on a fresh deployment
```

---

## 7. Step 5 — point the Identity Service at it

There are two ways to connect the Identity Service. `VAULT_ADDR` is set in both;
`VAULT_TOKEN` is set in exactly one of them. Pick one, set its variables on the service
and restart it.

**A. The service holds the token itself.**

```
VAULT_ADDR=https://vault.internal:8200
VAULT_TOKEN=<the token from §5>
IDENTITY_KV_MOUNT=fora-agents
```

**B. A Vault Proxy holds the token.** The Proxy logs in to Vault with a method of its
own and adds its token to every request it forwards. The service only needs the
Proxy's listener, which can be a Unix socket. The compose bundle's server mode runs this
way; what the host provides for it is in
[Server mode in the compose bundle](#server-mode-in-the-compose-bundle-what-the-host-provides)
below.

```
VAULT_ADDR=unix:///vault-proxy/proxy.sock
IDENTITY_KV_MOUNT=fora-agents
```

No `VAULT_TOKEN` in B: with a token set, the client would send that token instead of
letting the Proxy add its own. In B, §5 creates no token at all, and §4's policy is
attached to the Proxy's login role instead.

### Server mode in the compose bundle: what the host provides

`deploy/terraform/modules/compose-stack` runs shape B when its `vault_mode` input is
`"server"`: a Vault server and a Vault Proxy, both on the host network, with the Identity
Service on the Proxy's socket. The module renders the two containers and mounts three
host directories into them read-only. It does not write those directories: the stack that
turns server mode on writes them before the bundle starts.

| Host directory | Mounted at | Mounted by | What it must hold |
|---|---|---|---|
| `/opt/fora/vault/server` | `/vault/config` | the Vault server | The server configuration; the image loads every file in the directory. A `listener "tcp"` on `127.0.0.1:8200` with the certificate and key from `/vault/tls`, `storage "raft"` with `path = "/vault/file"` (the bundle's `vaultdata` volume), `api_addr` and `cluster_addr` on the loopback, and the `seal` stanza of the deployment's auto-unseal, if it has one. |
| `/opt/fora/vault/proxy` | `/vault/proxy` | the Vault Proxy | `proxy.hcl`, the file the Proxy is started with: a `vault` stanza for `https://127.0.0.1:8200` with `ca_cert = "/vault/tls/vault.crt"`, an `auto_auth` method of the deployment's choice, `api_proxy { use_auto_auth_token = true }`, and a `listener "unix"` at `/vault-proxy/proxy.sock` with `tls_disable = true`, `socket_mode = "0660"`, `socket_user = "100"` and `socket_group = "65532"`. It has a directory of its own because the server loads the whole of its directory and refuses a proxy stanza. |
| `/opt/fora/vault/tls` | `/vault/tls` | both | `vault.crt` and `vault.key` for the server's listener. The Proxy and the server container's healthcheck verify the server against `vault.crt`. |

Host networking makes `127.0.0.1` in these files the host's loopback, so only the Proxy and
an operator on the host reach the server. The Proxy runs as uid 100 with gid 65532, and the
Identity Service as uid and gid 65532; that is why the socket is group-owned by 65532 and
opened `0660`. Vault Proxy 1.20 applies a Unix socket's permissions only when all three
`socket_*` fields are set. The bundle gives the Identity Service
`VAULT_ADDR=unix:///vault-proxy/proxy.sock` and no `VAULT_TOKEN`, as B above says, and it
mounts a `vaultlogs` volume at `/vault/logs` for a file audit device to write to.

On first start the server is uninitialised and sealed, and the bundle's healthcheck counts
that as healthy, because the container answers. Initialising it, and enabling the auth
method and role the Proxy logs in with, the KV v2 mount `vault_kv_mount` names (§3) and the
`fora-identity` policy (§4), is the deployment's own bootstrap, done once.

**There will be no confirmation in the logs.** The Identity Service does not contact
Vault at start-up, so a wrong address or a bad token produces a service that boots
cleanly and reports healthy. The check that actually proves it is Check C in
[`src/identity/DEPLOYMENT.md`](../../../src/identity/DEPLOYMENT.md) §10 — run it now.

---

## 8. Step 6 — set up backups before the first agent exists

There is no second copy of these keys anywhere. If Vault's storage is lost, every agent
loses its identity permanently and must sign up again.

Snapshot however your Vault is deployed — `vault operator raft snapshot save` for
Integrated Storage, your provider's mechanism for a managed Vault, the storage layer's
own backup otherwise. The procedure and the restore drill are in
[`RUNBOOK.md`](RUNBOOK.md) §5.

> **A snapshot of this store is as sensitive as the store itself.** It contains every
> agent's private key in usable form. Encrypt it, restrict who can read it, and keep it
> out of general-purpose backup buckets.

Back it up **on the same schedule as the Identity Service's PostgreSQL database**.
Restoring keys without the revocation list quietly brings back keys you revoked;
restoring the database without the keys leaves records pointing at key material that no
longer exists.

---

## 9. Where to go next

| Task | Where |
|---|---|
| Vault is sealed and nothing works | [`RUNBOOK.md`](RUNBOOK.md) §3 |
| Renewing or rotating the service's token | [`RUNBOOK.md`](RUNBOOK.md) §4 |
| Taking and testing a snapshot | [`RUNBOOK.md`](RUNBOOK.md) §5 |
| What to alert on | [`RUNBOOK.md`](RUNBOOK.md) §2.3 |
| The settings from the service's side | [`src/identity/CONFIGURATION.md`](../../../src/identity/CONFIGURATION.md) §4 |
