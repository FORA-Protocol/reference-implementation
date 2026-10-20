# Publisher console — deployment

How to stand the console up, in the order the steps depend on each other.

---

## 1. What this service is, and where it sits

The console is the web application a publisher uses. They sign in, register
their business and their sites, download the files to deploy, and press a
button that confirms the site is theirs.

The browser talks only to the console. The console talks to the Exchange
server-side over a signed connection. Nothing a publisher owns is stored here:
the Exchange decides what a person may do, and this service asks.

```
browser ──► console ──(signed)──► Exchange
                │
                └──► login provider (sign-in, email confirmation, password recovery)
```

---

## 2. What you need before you start

- A PostgreSQL database of its own. One table; a schema inside another
  service's database is not the same thing and will collide on the next
  migration.
- A login provider with a client registered for this console, separate from
  any other service's client. Its redirect address is this console's
  `/auth/callback`, and its post-logout address is this console's
  `/signed-out`, both registered exactly.
- An Exchange that is already running, and its two console settings applied:
  the address of this console's key directory and the contributor hostname.
  Without them the Exchange serves no registration routes and every page here
  reports that it cannot reach it.
- An Ed25519 key pair for this console. The private half stays here; the
  public half is published in this console's own key directory, which is what
  the Exchange fetches.

---

## 3. Build or pull the image

The image is published to the GitHub Container Registry. Set the version once —
this is the only place this document names one, and every command below reuses
it:

```bash
VERSION=1.0.0-rc.5
docker pull ghcr.io/fora-protocol/console:$VERSION
```

There is no `latest` tag, so the version is never optional. In production,
deploy the digest rather than the tag.

---

## 4. Generate the signing key

```bash
openssl genpkey -algorithm ed25519 -out console-signing-key.pem
openssl pkey -in console-signing-key.pem -pubout -out console-signing-key.pub.pem
```

Keep the private half with your other secrets and mount it read-only. The
public half goes into the key directory this console serves, which is what
the Exchange reads. Two settings have to agree about that directory: this
console's `CONSOLE_DIRECTORY_ORIGIN` (by default its `CONSOLE_PUBLIC_ORIGIN`)
and the Exchange's own console setting. They name the same https origin, and a
mismatch refuses every call with a signature error. The directory is served by
the console itself, signed by its key, and must be reachable at that origin
over https with the `Host` header preserved.

---

## 5. Run it

```bash
docker run -d --name console \
  -p 8085:8085 \
  -e CONSOLE_DSN="postgres://fora:PASSWORD@db.internal:5432/console?sslmode=require" \
  -e CONSOLE_SESSION_KEY="$(openssl rand -base64 32)" \
  -e CONSOLE_SIGNING_KEY_FILE=/run/secrets/console-signing-key.pem \
  -e CONSOLE_PUBLIC_ORIGIN="https://console.example" \
  -e CONSOLE_OIDC_ISSUER="https://login.console.example" \
  -e CONSOLE_OIDC_CLIENT_ID="console" \
  -e CONSOLE_OIDC_CLIENT_SECRET_FILE=/run/secrets/console-client-secret \
  -e CONSOLE_OIDC_REDIRECT_URL="https://console.example/auth/callback" \
  -e CONSOLE_EXCHANGE_URL="https://exchange.example" \
  -e CONSOLE_EXCHANGE_DOMAIN="exchange.example" \
  -e CONSOLE_EXCHANGE_WBA_URL="https://exchange.example/.well-known/http-message-signatures-directory" \
  -e CONSOLE_CONTRIBUTOR_HOST="catalog-contributor.exchange.example" \
  -e CONSOLE_SUPPORT_EMAIL="help@exchange.example" \
  -v /run/secrets:/run/secrets:ro \
  ghcr.io/fora-protocol/console:$VERSION
```

The schema is applied at boot. Every setting is in the configuration document
beside this one.

Keep the session key out of the command line in production — a value typed
into a `docker run` is in the shell history and in the process list. Use your
orchestrator's secret mechanism.

---

## 6. The one thing to get right

**The redirect address must match, character for character**, in three places:
registered at the login provider, in `CONSOLE_OIDC_REDIRECT_URL`, and reachable
at `CONSOLE_PUBLIC_ORIGIN` + `/auth/callback`.

A mismatch fails at the provider, after the person has typed their password,
with the provider's own error page. Nothing in this service's logs explains it,
because the request never arrives here.

**The post-logout address follows the same rule.** Signing out sends the
browser to the provider's logout address, naming `CONSOLE_PUBLIC_ORIGIN` +
`/signed-out` as the way back, and the provider sends it there only if that
address is registered for this client. An unregistered one leaves the person
on the provider's error page: their console session is already gone, but they
are not returned to the console.

---

## 7. Verify the deployment

The console answers a health probe as soon as it is serving:

```bash
curl -s https://console.example/healthz
# {"status":"ok"}
```

That says the process is up and nothing more. To know the console can actually
do its job, sign in and register a test site against a test publisher domain
you control:

1. Open the console and sign in. You should land on a page offering to register
   a business.
2. Register one, add a site, and download the files. The file named
   `fora.json` should name your Exchange and your contributor hostname.
3. Publish that file at `/.well-known/fora.json` on the test site, install the
   package, and press the last button. The site should be listed as Verified.

If the site is listed as not verified, press **Finish setup** on it: the wizard
reopens at the first step that does not pass. The file step names what does not
match, each reason one edit to that file, and the package step names what the
site answered.

---

## 8. Where this runs

Behind TLS, on a hostname a person will type. The console sets secure cookies
only when `CONSOLE_PUBLIC_ORIGIN` starts with `https`, so an http deployment is
a local bring-up and not a production one.

It holds a session cookie and a signing key, so treat the host the way you
treat the Exchange's: no shared tenancy with anything untrusted, and secrets
mounted rather than baked in.

---

## 9. Running it with the compose bundle

The Terraform Compose bundle in this repository (`deploy/terraform/modules/compose-stack`)
can run the console as an optional service next to the Exchange, the Broker and the
identity service. Seven inputs turn it on, and they are set together: `console_hostname`,
`console_image`, `console_signing_key_pem`, `console_contributor_host`,
`console_support_email`, and the two that give publishers a Zitadel instance of their
own: `console_login_hostname`, where that instance answers, and
`zitadel_system_api_public_key`, the public half of the key that creates it
([`deploy/zitadel/CONFIGURATION.md`](../../deploy/zitadel/CONFIGURATION.md) §3). With them, the bundle's `exchange_terms_currency` and
`exchange_terms_unit` are required too, because the console prices every publisher's
terms in them. `console_default_rate` is optional; without it the console uses its own
default.

The plan fails before anything is deployed when:

- one of the required inputs is missing;
- the key is not a PKCS#8 Ed25519 key, the kind §4 generates;
- the bundle also runs the catalog importer, and `console_contributor_host` is not the
  `kid` of the importer's key;
- an input has the wrong shape, such as a hostname with a scheme;
- `console_login_hostname` is the Zitadel hostname developers use, or the console's own.

From those inputs the module renders:

- the console service, with every setting §5 shows. Most of them come from the bundle
  itself: the origin is `https://<console_hostname>`, the login provider is the
  publishers' instance of the bundle's Zitadel, `https://<console_login_hostname>`, and
  the Exchange is the bundle's Exchange at its public hostname. The session
  key is generated once and kept in the Terraform state. The module sets no edge
  release, because a released image carries its own;
- a Caddy site for `console_hostname` that passes every path to the console;
- `console_login_hostname` as a second address of the Zitadel site, which also answers
  404 on both hostnames for every route a system permission opens: Zitadel's System
  API and the system-wide features;
- the System API user `fora-instance-admin` in Zitadel's environment, with the public
  key;
- a generated password for the publishers' instance's admin, `zadmin`, exposed as the
  `console_login_admin_password` output and never written into the bundle;
- the Exchange's two console settings: the address of the console's key directory, and
  the contributor hostname, from the same `console_contributor_host` the console gets;
- the console's connection to a database of its own, `console` (see below);
- the signing key, which the module's cloud-init writes to
  `/opt/fora/keys/console/console-signing-key.pem`. Only the console mounts it, read-only.

`console_hostname` needs a public DNS record that points at the machine. The Exchange
fetches the console's key directory from that name, and it refuses a private address.
`console_login_hostname` needs one too: publishers' browsers are sent there to sign in.

**The login client.** The console reads its client id and secret from
`/opt/fora/console-oidc` on the machine, a directory of its own. It does not use the
volume the identity service reads its client from, because that volume also holds the
developers' instance's admin token. `deploy/terraform/scripts/bootstrap-identity.sh`
writes the two files on a stack that has the `console_url` and `console_login_url`
outputs; `console_url` must be exactly the console's origin, `https://<console_hostname>`.
On its first run there, the script creates the publishers' instance through Zitadel's
System API, signing the request with the System API private key, which stays on the
operator's machine. It then registers the client in that instance with the console's
redirect and post-logout addresses, and with Development mode off, because both
addresses are `https`. It makes root the owner of the directory, with the console's
group (65532), and sets mode 0750 on the directory and 0640 on the two files. Until both
files exist, the console exits at boot and Docker starts it again.

Every run brings the client to the current addresses and keeps its secret while the
stored one still works; it recreates the console only when the files changed. After a
change of `console_hostname`, a plain run updates the client's addresses. To replace the
secret on purpose, run it with `ROTATE=publishers`: it mints a new one and recreates the
console on it. `FORCE=1` is retired, and the script refuses it.

**The database.** On self-hosted PostgreSQL, the module's init SQL creates the `console`
database. PostgreSQL runs that SQL only when it first creates its data directory, so on
a server that already holds data, create the database by hand; until it exists, the
console exits at boot. On external PostgreSQL, the module generates a password for a
`console` user and adds both to its `database_config_json` output, which lists every
database for provisioning.

---

## 10. Where to go next

- The configuration document beside this one lists every setting.
- The runbook beside it covers what to watch and what the failures look like.
- The Exchange's own configuration document covers the two settings it needs
  before any of this works.
