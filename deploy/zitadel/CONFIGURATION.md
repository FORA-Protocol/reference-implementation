# FORA Sign-in Provider — Configuration Reference

This document tells you, the DevOps engineer running the sign-in provider for the FORA
platform, what the Identity Service needs from an OIDC provider and how to satisfy it
with Zitadel. You do not need to read the source code. Words that may be new are
explained the first time they appear.

For the step-by-step install see [`DEPLOYMENT.md`](DEPLOYMENT.md); for day-to-day
operation see [`RUNBOOK.md`](RUNBOOK.md).

---

## 1. What the platform needs from a sign-in provider

Developers who want to use FORA sign in somewhere before the Identity Service will
create an agent for them. The Identity Service does not hold their passwords — it hands
sign-in to a provider you run, and trusts the answer. A deployment that runs the
publisher console signs publishers in the same way, through a second instance of the
same Zitadel ([§3](#two-instances-one-for-developers-one-for-publishers)).

"OIDC" is OpenID Connect, the standard way one service hands sign-in over to another.

The requirements are ordinary, and any provider that follows the standard meets them:

| Requirement | Detail |
|---|---|
| OIDC discovery | The provider publishes `/.well-known/openid-configuration`. The Identity Service reads it **at start-up** and exits if it cannot. |
| Authorization-code flow | The standard browser sign-in flow. |
| A **confidential** client | One that authenticates with a client secret. A public browser client will not work. |
| An exact redirect address | `IDENTITY_AUTH_ISSUER` + `/callback`, matching character for character. |
| The scopes `openid profile email` | The default. The service needs the developer's identity and email address. |

**Zitadel is the provider FORA is tested against**, and the rest of this document is
Zitadel-specific. If you already run an OIDC provider, use it instead —
only §1 and §5 apply to you, and the four values in §5 are all the Identity Service
ever learns about it.

---

## 2. What the platform does not need

Worth stating, because operators reasonably look for these:

- **No user management on the FORA side.** Accounts, passwords, multi-factor settings
  and social sign-in are entirely the provider's business. The Identity Service reads
  who signed in and nothing else.
- **No dynamic client registration at the provider.** The Identity Service's own client
  is created once, by you. (It does offer dynamic registration to *agent software*
  connecting to it — that is a separate mechanism, on the Identity Service, described
  in [`src/identity/RUNBOOK.md`](../../src/identity/RUNBOOK.md) §4.2.)
- **No role or group mapping.** Anyone who can sign in can create an agent. Whether
  that agent may spend is decided on the Exchange —
  [`src/exchange/RUNBOOK.md`](../../src/exchange/RUNBOOK.md) §4.2.

---

## 3. Zitadel settings

| Setting | Value | Why |
|---|---|---|
| Image | `ghcr.io/zitadel/zitadel:v3.4.9` | Pinned. v4.x has a known crash at start-up, during its own migrations, that is not yet fixed. Do not move to v4 without testing it against a copy of your data first. |
| `ZITADEL_EXTERNALDOMAIN` | The public hostname developers reach | **Written into Zitadel's storage at first start** — see below. |
| `ZITADEL_EXTERNALPORT` | `443` | With `EXTERNALSECURE` on. |
| `ZITADEL_EXTERNALSECURE` | `"true"` | Makes the published issuer `https`. |
| `ZITADEL_TLS_ENABLED` | `"true"`, or terminate TLS in front | The traffic is sign-in credentials. |
| `--masterkey` | 32 characters, from your secret manager | Encrypts everything Zitadel stores. **Lose it and the data is unreadable.** |
| `ZITADEL_DATABASE_POSTGRES_*` | Its own PostgreSQL database | Separate from the FORA databases. |

### The external domain is fixed at first start

Zitadel writes its own address into its storage the first time it starts, and it is
what appears as the `issuer` in everything it publishes. **Changing it later is not a
configuration edit** — the stored value stays, the published issuer stops matching, and
the Identity Service refuses the sign-in responses it gets back.

The only way to change it is to start a fresh instance with empty storage, which loses
every account. Decide the hostname before the first start.

The same is true from the other side: `IDENTITY_AUTH_ISSUER` on the Identity Service
determines the redirect address this provider must have on file, so changing that means
updating the client here ([`RUNBOOK.md`](RUNBOOK.md) §3).

### Two instances: one for developers, one for publishers

A deployment that runs the publisher console runs one Zitadel with two *instances*.
An instance is a separate set of accounts, sign-in pages, sessions and settings inside
the same Zitadel, behind a hostname of its own. Zitadel decides which instance a request
is for from the hostname the browser asked for.

| Instance | Hostname | Who signs in there | Its OIDC client |
|---|---|---|---|
| developers | `ZITADEL_EXTERNALDOMAIN` — `login.<domain>` in production | developers, for the Identity Service (MCP) | `fora-identity` |
| publishers | the console's login hostname — `login.console.<domain>` in production | publishers, for the console | `fora-console` |

Why two. With one instance, a person who had signed in for MCP was signed in to the
console as well, without typing a password, and landed in an empty publisher panel.
Both sides also showed the same pages, and one email address could hold only one
account. With two instances, each side has its own accounts and its own browser
session, and one address can hold a developer account and a publisher account, with
different passwords.

The developers' instance is the one Zitadel creates on its first start. The publishers'
instance is created later, through Zitadel's System API (next section), by
`deploy/zitadel/bootstrap/instance.sh`. It needs no container, database or master key of
its own: both instances live in the one Zitadel and its one database. The console
trusts only the publishers' instance as its issuer, and ends any session it sealed
under another one.

### The System API, which creates the publishers' instance

Zitadel's System API manages instances. It can create one, and it can also delete one.
FORA uses it for two things: creating the publishers' instance, and recovering that
instance's bootstrap token if the token is lost or may have been copied
([`RUNBOOK.md`](RUNBOOK.md) §4.6). `deploy/zitadel/bootstrap/publishers.sh` runs the
instance step and then the provisioning; every stack runs the publishers' side through
it.

The System API accepts requests signed by a *System API user*: a name and an RSA public
key that Zitadel is given at start-up. FORA's user is `fora-instance-admin`.

| Setting | Value |
|---|---|
| `ZITADEL_SYSTEMAPIUSERS` | `{"fora-instance-admin":{"KeyData":"<the public key's PEM, base64>"}}`. The compose bundle renders it from the Terraform input `zitadel_system_api_public_key`. The e2e and laptop stacks give a `Path` to a key file instead. |
| The private key | RSA-3072. `deploy/terraform/scripts/gen-keys.sh` writes it once, for the stack that runs the publisher console, as `keys/zitadel-system-api-private.pem`, and keeps it. It never leaves the operators' machines: Terraform does not read it, and neither the bundle nor the VM ever holds it. |
| A System API token | An RS256 JWT whose `iss` and `sub` are `fora-instance-admin` and whose `aud` is Zitadel's external URL (`https://login.<domain>`, with no port). It is dated a minute back against clock differences and is valid for five minutes. `deploy/zitadel/bootstrap/system-token.sh` makes one; `bootstrap-identity.sh` makes one only when the publishers' instance has to be created or recovered. |

**What the key can do, and where.** FORA's System API user holds Zitadel's
`SYSTEM_OWNER` role. That role creates and deletes instances, and it also sets the
*system-wide features* of Zitadel's FeatureService, which every instance follows. One of
them sends every sign-in, developers' and publishers' alike, to a login page at an
address of the setter's choosing. Zitadel accepts a System API token on any of its APIs,
not only on the System API.

So on a deployment with the publisher console, Caddy answers 404, on every Zitadel
hostname, for every route that a system permission opens in the pinned Zitadel version,
v3.4.9:

- the System API: `/system/*` and `/zitadel.system.v1.SystemService/*`;
- the system-wide features: `/v2/features/system` and `/v2beta/features/system`, with
  any path below them, and the `SetSystemFeatures`, `ResetSystemFeatures` and
  `GetSystemFeatures` methods of `zitadel.feature.v2.FeatureService` and
  `zitadel.feature.v2beta.FeatureService`.

Only the VM's own network reaches them. `zitadel-instances-test.py` sends each of those
routes, with a System API token, through Caddy in front of a real Zitadel and checks
that Caddy itself refuses it. A Zitadel upgrade can add a route that a system
permission opens; [`RUNBOOK.md`](RUNBOOK.md) §4.3 lists them again on every upgrade.

A deployment without the console configures no System API user, so Zitadel refuses
every System API token there. While a recovery window is open (RUNBOOK §4.6), the key
also acts as IAM_OWNER of the publishers' instance, and that instance's Admin and
Management APIs answer on its public hostname: keep the window short. Someone who holds
the private key and can reach the VM's network can create and delete instances, so
keep the key the way you keep the master key.

### Look and language

Each instance's sign-in pages and emails show which side they belong to. These
settings belong to the provisioning scripts: `deploy/zitadel/bootstrap/branding.sh`
applies them on every run and changes only what differs, so a change made by hand in
the Zitadel console is undone by the next run.

| | Developers' instance | Publishers' instance |
|---|---|---|
| Title of the sign-in page and of the two sign-up pages | FORA Developers | FORA Publishers |
| Four account emails — sign-up, address verification, password reset, password changed | FORA developer wording for the subject, title, inbox preview, greeting, body, button and footer | FORA publisher wording for the subject, title, inbox preview, greeting, body, button and footer |
| Colours | Light background `#fafafa`, dark text `#12121a`, primary buttons `#4f6df5`, errors `#cd3d56` | [FORA website](https://fora-protocol.org/): sign-in background `#0a0a0f`, email background `#12121a` (the console's panels), text `#e8e8f0`, primary buttons `#4f6df5`, errors `#ff8a80` |
| Theme mode | Always light | Always dark, so native input borders and disabled controls remain visible |
| Login header | Small `fora protocol` label at the top left; centered symbol removed | Small `fora protocol` wordmark at the top left |
| Logo and icon | compact dark `fora protocol` wordmark as logo; FORA mark as icon | compact `fora protocol` wordmark as logo; white FORA mark as icon |
| Font | Instrument Sans | Instrument Sans |
| Languages | English only | English only |

English only is not cosmetic: without it, Zitadel picks a language from the browser,
and with none given it falls back to the first language it knows, Bulgarian, for the
sign-up email. The marks and the font are in `deploy/zitadel/bootstrap/branding/`,
with their sources and licences.

The publisher welcome email has the subject `Welcome to FORA Publishers` and a
`Set up your account` button. It explains that the account is for the FORA publisher
console, keeps the sign-in username and verification code, and tells an unexpected
recipient they can ignore it. Verification and password emails use the same FORA
wording; the password-change notification tells the recipient to reset their password
immediately if they did not make the change. Zitadel supplies the action links and
renders the user and code placeholders. These texts are applied through the instance
Admin API on fresh provisioning and reconciled on subsequent bootstrap runs.
Developer emails use the same instructions and action labels, with `FORA Developers`
subjects and footer and developer-account wording throughout. Their background stays light.

Email styling uses the light label-policy colors, even with dark sign-in enabled.
The wordmark renders at 180 × 40 pixels, replacing the large square symbol, and
the body uses the same Instrument Sans font as the console. Mail clients that block
web fonts use Zitadel's system-font fallback. Zitadel v3.4.9 does not expose an API
to replace the email HTML template, so its centered layout, spacing and footer
divider remain; the website's gradients belong to the browser sign-in CSS.

The publisher login also uses `deploy/zitadel/publisher-login.css`: the website's
gradient, blue glow, secondary text (`#a5a5b8`), link accent (`#9aaeff`) and wordmark.
The developer login uses `deploy/zitadel/developer-login.css` for the same page
spacing and a top-left `fora protocol` label, keeping its light theme. Both hide
the centered logo on sign-in and registration pages. Caddy serves each stylesheet
only on its respective hostname, at Zitadel's existing stylesheet URL. Each imports
the original stylesheet with `?fora-original=1`; authentication HTML, scripts and
endpoints pass through unchanged. The icon remains the FORA mark;
emails use the wordmark. The deployed Caddyfile embeds the CSS, so no extra asset mount is needed.

Locally, `make zitadel-up` starts the same kind of CSS route in `zitadel-login` on
the existing port 58080. With the publisher console running, open
`http://localhost:8085/login` to sign in or register for publisher onboarding.
`http://127.0.0.1:58080/ui/console/` previews the publisher login styling but
returns to Zitadel's identity settings (`localhost` on port 58080 selects the
developer instance). CSS edits are bind-mounted;
hard-refresh the browser after editing. For palette changes in `branding.sh`, run
`docker compose --profile zitadel run --rm --no-deps zitadel-publishers-init`
for publishers, or `docker compose --profile zitadel run --rm --no-deps zitadel-init`
for developers.

### Outbound mail, and why it is also a first-start setting

Self-registration sends a confirmation code, and Zitadel does not treat the account
as usable until the code is entered. Without a mail provider that code is never
delivered, so anyone who registers is stuck.

**The publisher console makes a mail provider effectively required.** It offers
sign-in with an email address and password, so a publisher registers themselves,
confirms their address, and resets a forgotten password — all three by mail. The
console refuses its publisher pages to an address the provider has not confirmed,
and so does the Exchange, so a deployment with no mail provider has publishers
who can sign in and do nothing. If every account arrives through an external
provider such as Google, which vouches for the address, no mail provider is
needed.

A provider can be configured in two ways, and they behave differently:

| Setting | Value | Why |
|---|---|---|
| `ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_HOST` | `host:port` | Dialled verbatim. There is no default port — a bare hostname fails at send time. |
| `ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_USER` | Relay username | For Amazon SES, the IAM access key id of the sending user. |
| `ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_SMTP_PASSWORD` | Relay password | For SES, the region-derived SMTP password — **not** the raw secret access key. |
| `ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_TLS` | `"true"` | STARTTLS. Off, the password crosses the network in the clear. |
| `ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_FROM` | Sender address | Must be one the relay is allowed to send as. |
| `ZITADEL_DEFAULTINSTANCE_SMTPCONFIGURATION_FROMNAME` | Display name | Shown next to the address. |

**These are read only when Zitadel creates an instance**: the developers' one on the
first start, in the same `FirstInstance` step that creates the admin user, and the
publishers' one when the System API creates it. Setting or changing them on an
instance that already exists does nothing at all: each instance's provider is stored in
Zitadel's database, and the environment is no longer consulted. This is the same trap as the
external domain above, with one difference — a wrong mail setting is recoverable
without losing data, because the stored provider can be edited afterwards.

Editing the stored provider is the second way: Zitadel console → Default settings →
Notifications → SMTP provider, or the Admin API's email-provider endpoints, once in each
instance. That is how a running deployment changes relays or rotates a password. An edit made there
does not travel back into the environment, so a later rebuild from empty storage
starts from whatever the environment says.

The address in `FROM` matters beyond Zitadel. A relay that pins the sender — SES does,
when the credential's policy names the address — rejects any send whose `FROM` differs,
and the failure appears as unsent mail rather than as a configuration error.

### The OIDC clients

There are two, one per service, each in its side's instance: `fora-identity` in the
developers' instance and `fora-console` in the publishers'. They are separate because the
two authenticate different people for different things — agent developers and
publishers — and a secret rotated for one would be an outage for the other.

Both are ordinary confidential web applications, differing only in the redirect
addresses:

| Property | Value |
|---|---|
| Application type | Web |
| Authentication method | Client secret (Basic) |
| Grant types | Authorization code (plus refresh token) |
| Response type | Code |
| Redirect address | the identity service: `IDENTITY_AUTH_ISSUER` + `/callback`. The publisher console: `CONSOLE_PUBLIC_ORIGIN` + `/auth/callback`. Exactly, in both cases |
| Post-logout address | the publisher console only: `CONSOLE_PUBLIC_ORIGIN` + `/signed-out`, exactly. Signing out of the console ends the Zitadel session and Zitadel sends the browser back here; an address that is not registered stops the sign-out on Zitadel's own error page |
| User Info inside ID Token | **On**. Both services read the person's email address, whether Zitadel has confirmed it (`email_verified`), and their name from the ID token only; neither calls Zitadel's userinfo endpoint. With this setting off, the ID token carries none of the three. The console then treats every address as unconfirmed and refuses its publisher pages, and the identity service records developers without an email address |
| Development mode | **Off** |

Development mode exists so Zitadel will accept a plain `http` address on your own
machine as the redirect. Leaving it on in a deployment removes a check on the redirect
address, so turn it off. The provisioning script,
`deploy/zitadel/bootstrap/bootstrap.sh`, sets it for each client from the client's
addresses: off when every redirect and post-logout address is `https`, and on when one
of them is not, as on a laptop or in the e2e stack.

**Zitadel never shows a client secret twice.** If you lose it, you cannot read it back
— you generate a new one, which is the rotation procedure in
[`RUNBOOK.md`](RUNBOOK.md) §4.

**What the provisioning script does to a client that already exists.** It does not
create a second one. It updates the existing client: the redirect and post-logout
addresses and Development mode get the values of this run. The same update sets every
other setting of the client back to the script's value or to Zitadel's default, so a
change someone made by hand is lost. For example, a redirect address added in the
Zitadel console is removed, unless the script is given it in `EXTRA_REDIRECT_URIS` or
`EXTRA_CONSOLE_REDIRECT_URIS`. A run keeps the client's secret when the stored files
name this client and Zitadel's token endpoint accepts them, and writes the files only
when something changed. It mints a new secret only when the stored one is missing or
refused, or when asked to (`ROTATE_CLIENT_SECRET=1`; `ROTATE=developers`, `publishers`
or `all` in `bootstrap-identity.sh`). `deploy/terraform/scripts/bootstrap-identity.sh`
recreates a service only when its client's files changed.

**Where the services read their client.** In the compose bundle
(`deploy/terraform/modules/compose-stack`), the identity service reads its client id and
secret from a Docker volume it shares with Zitadel. That volume also holds Zitadel's
admin token and machine key, so the console does not mount it. The console reads its
client from a host directory of its own, `/opt/fora/console-oidc`, owned by root with the
console's group (65532), with mode 0750 on the directory and 0640 on the two files.
On a stack that has a `console_login_url` output,
`deploy/terraform/scripts/bootstrap-identity.sh` creates that directory and writes the
console's files into it, from the publishers' instance.

### Password recovery must be visible

The sign-in policy hides the "forgot password" link by default, and this platform
turns it on. A publisher who cannot reset a password has no way back into the
console, and the provider is the only party that can reset one — neither the
console nor the Exchange stores a password. Each instance has its own sign-in policy,
and the provisioning script sets both the same way, so the link appears on the identity
service's sign-in page too, which is correct for the same reason.

---

## 4. Development defaults you must not copy

The compose files and `init-steps.yaml` in this directory set up a throwaway Zitadel
for automated tests:

| Setting | Why it is there | Why it must not ship |
|---|---|---|
| `--masterkey MasterkeyNeedsToHave32Characters` | A fixed key so tests are reproducible. | It is published in this repository. Anyone could decrypt everything Zitadel stores. |
| `--tlsMode disabled`, `ZITADEL_EXTERNALSECURE: "false"` | No certificate is issued for Zitadel on the test networks. | Sign-in credentials in the clear. |
| The passwords in `init-steps.yaml` | Setting up a throwaway instance. | Published in this repository. The file says so itself. |
| `ZITADEL_EXTERNALDOMAIN: zitadel` | A container name on a private network. | Not reachable, and fixed permanently once started. |
| `devMode: true` on the OIDC client | Lets a laptop use an `http://localhost` redirect. | Relaxes redirect-address checking. |
| A test user with a known password | Tests need someone to sign in as. | An account with a published password. |
| `ZITADEL_ADMIN_PASSWORD: Publisher-Admin-1!` for the publishers' instance | Its admin needs a password in a throwaway stack. | Published in this repository. |
| A System API key made by `local-system-key.sh`, with `Path` in `ZITADEL_SYSTEMAPIUSERS` | The stack creates its own publishers' instance. | A key made and kept by a test stack, in a Docker volume. |

`init-steps.yaml` carries its own warning: *"The passwords here are local-development
defaults published in a tracked file. Nothing outside a laptop stack may use this
file."*

---

## 5. What the Identity Service is told

Four values, and this is the entire interface between the two systems.

| Value | Identity Service variable | Where it comes from |
|---|---|---|
| The provider's address | `IDENTITY_OIDC_ISSUER` | The `issuer` in its discovery document — copy it exactly, do not assume it |
| The client id | `IDENTITY_OIDC_CLIENT_ID` or `..._FILE` | §3, at creation |
| The client secret | `IDENTITY_OIDC_CLIENT_SECRET` or `..._FILE` | §3, at creation — shown once |
| This service's own public address | `IDENTITY_AUTH_ISSUER` | Your choice; it decides the redirect address |

The `_FILE` variants let the two secrets arrive as mounted files rather than
environment variables. A file that is set but unreadable stops the Identity Service
starting — it is not treated as "not configured".

---

## 6. Worked example

Values marked *fill in* are specific to your environment.

Zitadel:

```
ZITADEL_EXTERNALDOMAIN=login.example
ZITADEL_EXTERNALPORT=443
ZITADEL_EXTERNALSECURE=true
ZITADEL_TLS_ENABLED=true
ZITADEL_DATABASE_POSTGRES_HOST=<fill in>
ZITADEL_DATABASE_POSTGRES_DATABASE=zitadel
--masterkey <fill in — 32 characters, from your secret manager>
```

The Identity Service:

```
IDENTITY_OIDC_ISSUER=https://login.example
IDENTITY_OIDC_CLIENT_ID_FILE=/secrets/oidc_client_id
IDENTITY_OIDC_CLIENT_SECRET_FILE=/secrets/oidc_client_secret
IDENTITY_AUTH_ISSUER=https://id.example
```

which gives the client this redirect address, and no other:

```
https://id.example/callback
```
