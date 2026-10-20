# FORA Sign-in Provider — Deployment Instructions

This document tells you, the DevOps engineer running the sign-in provider for the FORA
platform, how to set up Zitadel and register the Identity Service against it. You do
not need to read the source code. Words that may be new are explained the first time
they appear. Follow the steps in order.

Every setting mentioned here is described in full in
[`CONFIGURATION.md`](CONFIGURATION.md). Once it is running, day-to-day operation is in
[`RUNBOOK.md`](RUNBOOK.md).

---

## 1. What this is for, and whether you need it

Developers sign in here before the Identity Service will create an agent for them. The
Identity Service never holds their passwords — it hands sign-in to the provider and
trusts the answer.

```
Developer ──► Sign-in provider ──► Identity Service ──► an agent, and a key
```

**If you already run an OIDC provider, use it and skip to §6.** The Identity Service
needs nothing unusual — discovery, the authorization-code flow, a confidential client,
and one exact redirect address. The requirements are in
[`CONFIGURATION.md`](CONFIGURATION.md) §1, and §6 below is the part that applies to any
provider.

The rest of this document sets up Zitadel, the provider FORA is tested against.

---

## 2. What you need before you start

| What | Where it goes | How to check you have it |
|---|---|---|
| A PostgreSQL database for Zitadel, separate from the FORA ones | `ZITADEL_DATABASE_POSTGRES_*` | `psql` connects as the configured user |
| A public hostname, with TLS | `ZITADEL_EXTERNALDOMAIN` | `curl -sI https://login.example.com` returns anything but a DNS error |
| A 32-character master key, from your secret manager | `--masterkey` | It is 32 characters, and it is stored somewhere you will still have in a year |
| The Identity Service's public address, decided | its `IDENTITY_AUTH_ISSUER` | You have chosen it; §5 needs it |
| A container runtime | — | `docker version` |

**Two values must be right the first time**, because neither can be changed afterwards
without losing everything:

- **The hostname.** Zitadel writes its own address into its storage at first start, and
  publishes it as its issuer. Changing it later means a fresh instance with empty
  storage — every account gone. [`CONFIGURATION.md`](CONFIGURATION.md) §3.
- **The master key.** It encrypts everything Zitadel stores. Lose it and the data is
  unreadable; there is no recovery. Put it in your secret manager before you start, not
  after.

---

## 3. Step 1 — start Zitadel

Pin the version. `v3.4.9` is what is tested; v4.x has a known crash at start-up, during
its own migrations, that is not yet fixed.

```yaml
services:
  zitadel:
    image: ghcr.io/zitadel/zitadel:v3.4.9
    restart: unless-stopped
    command:
      - start-from-init
      - --masterkey
      - "${ZITADEL_MASTERKEY}"
    environment:
      ZITADEL_EXTERNALDOMAIN: "login.example"
      ZITADEL_EXTERNALPORT: "443"
      ZITADEL_EXTERNALSECURE: "true"
      ZITADEL_TLS_ENABLED: "true"
      ZITADEL_DATABASE_POSTGRES_HOST: "db.internal"
      ZITADEL_DATABASE_POSTGRES_PORT: "5432"
      ZITADEL_DATABASE_POSTGRES_DATABASE: "zitadel"
      ZITADEL_DATABASE_POSTGRES_USER_USERNAME: "zitadel"
      ZITADEL_DATABASE_POSTGRES_USER_PASSWORD: "${ZITADEL_DB_PASSWORD}"
      ZITADEL_DATABASE_POSTGRES_USER_SSL_MODE: "require"
      ZITADEL_DATABASE_POSTGRES_ADMIN_USERNAME: "zitadel"
      ZITADEL_DATABASE_POSTGRES_ADMIN_PASSWORD: "${ZITADEL_DB_PASSWORD}"
      ZITADEL_DATABASE_POSTGRES_ADMIN_SSL_MODE: "require"
```

`start-from-init` creates the schema on first start and starts normally afterwards.

Confirm it is answering, and that the address it publishes is the one you intended:

```bash
curl -s https://login.example/.well-known/openid-configuration | head -c 200
# Expect: JSON beginning
# {"issuer":"https://login.example","authorization_endpoint":"https://login.example/oauth/v2/authorize",...
```

**If `issuer` is not the hostname you meant, stop now.** It is written into storage,
and the only fix is a fresh instance with empty storage. Fixing it before anyone signs
up costs nothing; fixing it afterwards costs every account.

---

## 4. Step 2 — create the organisation and project

Sign in to the Zitadel console as the administrator it created at first start, and make:

- an **organisation** for your deployment, and
- a **project** inside it to hold the Identity Service's client.

Names do not matter to FORA — nothing reads them.

While you are there, set the sign-in policy you want: password rules, multi-factor,
and any social or corporate sign-in providers. **All of that is entirely yours.** The
Identity Service reads who signed in and nothing else, so anything you configure here
works without any change on the FORA side.

---

## 5. Step 3 — create the Identity Service's client

Inside the project, create an application:

| Property | Value |
|---|---|
| Application type | Web |
| Authentication method | Client secret (Basic) |
| Grant types | Authorization code (plus refresh token) |
| Response type | Code |
| Redirect address | `https://id.example/callback` |
| User Info inside ID Token | **On** |
| Development mode | **Off** |

The redirect address is the Identity Service's `IDENTITY_AUTH_ISSUER` with `/callback`
appended, and it must match **character for character**. A trailing slash, `http`
instead of `https`, or a different hostname all produce the same failure: sign-in stops
here with a redirect-address error, and nothing appears in the Identity Service's logs
at all, because the request never reaches it.

User Info inside ID Token puts the developer's email address and name into the ID token,
which is the only place the Identity Service reads them from
([`CONFIGURATION.md`](CONFIGURATION.md) §3).

Zitadel shows you a client id and a client secret at this point.

> **Copy the secret now.** Zitadel never shows it again. Losing it is not fatal — you
> generate a new one — but that means changing the credential, not looking it up
> ([`RUNBOOK.md`](RUNBOOK.md) §4).

Write both into files the Identity Service can read, if you are using the file form:

```bash
umask 077
printf '%s' '<client id>'     > /secrets/oidc_client_id
printf '%s' '<client secret>' > /secrets/oidc_client_secret
```

`printf` rather than `echo` avoids a trailing newline. The Identity Service trims
whitespace either way, but the habit is worth keeping for secrets generally.

---

## 6. Step 4 — point the Identity Service at it

Set these four on the Identity Service and restart it:

```
IDENTITY_OIDC_ISSUER=https://login.example
IDENTITY_OIDC_CLIENT_ID_FILE=/secrets/oidc_client_id
IDENTITY_OIDC_CLIENT_SECRET_FILE=/secrets/oidc_client_secret
IDENTITY_AUTH_ISSUER=https://id.example
```

Use the `issuer` value **exactly as the discovery document reported it** in §3 — not
what you typed into the configuration. A provider that publishes itself with a trailing
slash, or on a different hostname than you reached it by, must be named the way it
names itself.

Unlike Vault, this connection is checked immediately: the Identity Service contacts the
provider while starting and exits if it cannot.

```bash
docker compose logs identity | tail -2
# Expect:
#   {"level":"INFO","msg":"migrations applied","version":4,...}
#   {"level":"INFO","msg":"identity listening","addr":":8083"}
```

A failure names the cause:

```
{"level":"ERROR","msg":"identity.exit","err":"sign-up config: oidc upstream: oidcup: discover \"https://login.example\": ..."}
```

That means the address is wrong, or the provider is not reachable from inside the
Identity Service's container — check both, not just from your workstation.

```
{"level":"ERROR","msg":"identity.exit","err":"sign-up config: developer sign-up requires IDENTITY_AUTH_ISSUER and IDENTITY_OIDC_ISSUER/CLIENT_ID/CLIENT_SECRET"}
```

That means one of the four values is empty — including a `_FILE` that points at a file
holding nothing.

---

## 7. Step 5 — verify the round trip

Discovery answering is not proof that sign-in works. The redirect address, the client
secret and the scopes are only tested by a real sign-in, so do one.

1. Have a developer — or you, with a test account — go through the Identity Service's
   sign-up flow.
2. They should be sent to `https://login.example`, sign in, and be returned to
   `https://id.example/callback`.
3. Confirm the account was created:
   ```bash
   psql "$IDENTITY_DSN" -c \
     "SELECT subdomain, client_name, created_at FROM identity.agent_card ORDER BY created_at DESC LIMIT 1"
   # Expect: one row, created just now, with a subdomain inside your identity zone
   ```
4. Confirm the agent's identity is published:
   ```bash
   curl -s -o /dev/null -w '%{http_code}\n' \
     "https://<the subdomain from step 3>/.well-known/http-message-signatures-directory"
   # Expect: 200
   ```

If step 2 stops at the provider with a redirect error, the address on the client does
not match — §5, and [`RUNBOOK.md`](RUNBOOK.md) §3.

---

## 8. Step 6 — back up the database and the master key

Zitadel's PostgreSQL database holds every developer account. Its master key decrypts
that database.

**Neither is any use without the other.** Back up the database on your normal schedule,
and keep the master key in your secret manager — not in the same backup. A restore
needs both.

---

## 9. Step 7 — the publishers' instance, for the publisher console

Skip this step unless you run the publisher console. The console signs publishers in
through a second instance of the same Zitadel, with its own hostname, accounts and
sign-in pages ([`CONFIGURATION.md`](CONFIGURATION.md) §3). It is created through
Zitadel's System API and needs no container, database or master key of its own.

On a deployment from the compose bundle, Terraform and
`deploy/terraform/scripts/bootstrap-identity.sh` do all of this: the bundle renders the
System API user from the `zitadel_system_api_public_key` input, and on a stack that has
the `console_login_url` output the script creates and provisions the instance. By hand,
the steps are:

1. **Make the System API key pair**, RSA-3072, outside any checkout of this repository,
   and keep the private half with your other operator secrets. `system-key.sh` makes
   it the way every stack does: the private key under `umask 077`, written whole or
   not at all:
   ```bash
   sh deploy/zitadel/bootstrap/system-key.sh /secure/system-api-private.pem /secure/system-api-public.pem
   ```
   Outside the checkout, because `bootstrap-identity.sh` uploads the tracked files of
   `deploy/zitadel/bootstrap` to a VM; a key kept anywhere in a checkout is one
   mistake away from a copy.
2. **Give Zitadel the public half** and restart it:
   ```
   ZITADEL_SYSTEMAPIUSERS={"fora-instance-admin":{"KeyData":"<base64 of system-api-public.pem>"}}
   ```
3. **Keep the routes a system permission opens off the internet.** The user holds
   SYSTEM_OWNER, which creates and deletes instances and sets the system-wide features
   every instance follows ([`CONFIGURATION.md`](CONFIGURATION.md) §3 lists the routes).
   Whatever terminates TLS in front of Zitadel must answer 404 for each of them, on
   every hostname. With Caddy, inside the site that serves Zitadel:
   ```
   @system {
   	path /system/* /zitadel.system.v1.SystemService/*
   	path /v2/features/system /v2/features/system/* /v2beta/features/system /v2beta/features/system/*
   	path /zitadel.feature.v2.FeatureService/SetSystemFeatures /zitadel.feature.v2.FeatureService/ResetSystemFeatures /zitadel.feature.v2.FeatureService/GetSystemFeatures
   	path /zitadel.feature.v2beta.FeatureService/SetSystemFeatures /zitadel.feature.v2beta.FeatureService/ResetSystemFeatures /zitadel.feature.v2beta.FeatureService/GetSystemFeatures
   }
   respond @system 404
   ```
4. **Point the publishers' hostname at the same Zitadel**, with DNS and TLS for it, the
   way `ZITADEL_EXTERNALDOMAIN` is set up.
5. **Create and provision the instance** from a machine on Zitadel's own network, with
   the token and the secrets in a directory only you can read. `publishers.sh` runs the
   instance step and then the provisioning:
   ```bash
   (umask 077 && sh deploy/zitadel/bootstrap/system-token.sh /secure/system-api-private.pem \
     https://login.example > /secure/system.token)
   ZITADEL_BASE_URL=http://zitadel:8080 ZITADEL_INSTANCE_HOST=login.console.example \
     ZITADEL_PAT_FILE=/secure/publishers.pat ZITADEL_SYSTEM_TOKEN_FILE=/secure/system.token \
     ZITADEL_ISSUER=https://login.console.example CONSOLE_PUBLIC_ORIGIN=https://console.example \
     CONSOLE_OUT_DIR=/secure/console-oidc ZITADEL_ADMIN_PASSWORD_FILE=/secure/admin-password \
     IMPORT_DEMO_USERS=0 sh deploy/zitadel/bootstrap/publishers.sh
   rm /secure/system.token
   ```
   The instance step writes the instance's own bootstrap token, which Zitadel shows
   only once, to `ZITADEL_PAT_FILE`; keep that file as you keep the master key. The
   provisioning writes the console's client id and secret to `CONSOLE_OUT_DIR`.
6. **Point the console at it**: `CONSOLE_OIDC_ISSUER=https://login.console.example`, and
   the client files from step 5 ([`src/console/CONFIGURATION.md`](../../src/console/CONFIGURATION.md)).
7. **Check it**: the discovery document at `https://login.console.example` names that
   issuer; from outside, a POST to `https://login.example/system/v1/instances/_search`
   and a PUT to `https://login.example/v2/features/system` both answer 404; and a
   publisher can sign up at the console and confirm the address from the email, whose
   subject starts with "FORA Publishers:".

---

## 10. Where to go next

| Task | Where |
|---|---|
| Sign-in is failing with a redirect error | [`RUNBOOK.md`](RUNBOOK.md) §3 |
| Rotating the client secret | [`RUNBOOK.md`](RUNBOOK.md) §4 |
| The publishers' instance lost its token | [`RUNBOOK.md`](RUNBOOK.md) §4.6 |
| Upgrading Zitadel | [`RUNBOOK.md`](RUNBOOK.md) §4.3 |
| What to alert on | [`RUNBOOK.md`](RUNBOOK.md) §2.3 |
| The settings from the service's side | [`src/identity/CONFIGURATION.md`](../../src/identity/CONFIGURATION.md) §2 |
