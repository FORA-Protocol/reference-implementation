# Publisher console — configuration

Every setting the console reads, what happens when you leave it out, and the
few that stop the boot.

---

## 1. What the service needs to run

| Dependency | Required | Why |
|---|---|---|
| PostgreSQL | **Yes** | One table, in a logical database of its own: who has signed in. Nothing a publisher owns is stored here. |
| A login provider | **Yes** | The console does not hold passwords. Sign-in, email confirmation and password recovery all belong to the provider. In the FORA deployment that is Zitadel's publishers' instance, separate from the instance MCP developers sign in to, so a publisher's account, session and sign-in pages are never a developer's ([`deploy/zitadel/CONFIGURATION.md`](../../deploy/zitadel/CONFIGURATION.md) §3). |
| The Exchange | **Yes** | Every registration is written there. The console is unusable without it, and says so rather than pretending otherwise. |
| An Ed25519 signing key | **Yes** | Every call to the Exchange is signed with it, and the Exchange verifies against the key this console publishes. |

---

## 2. Environment variables

| Variable | Required | What it does | Example |
|---|---|---|---|
| `CONSOLE_DSN` | **Required** | The PostgreSQL connection string. Its own database, not a schema inside another service's. The console exits immediately if this is unset. | `postgres://fora:PASSWORD@db.internal:5432/console?sslmode=require` |
| `CONSOLE_SESSION_KEY` | **Required** | 32 bytes, base64. Seals the sign-in cookies. **Boot fails if unset** — see §3. | `openssl rand -base64 32` |
| `CONSOLE_SIGNING_KEY_FILE` | **Required** | Path to the console's Ed25519 private key, in PEM. **Boot fails if unset.** | `/run/secrets/console-signing-key.pem` |
| `CONSOLE_PUBLIC_ORIGIN` | **Required** | The console's own origin, scheme included. **Boot fails if unset.** Four things read it: the cookies are marked secure when it is `https`; unless `CONSOLE_DIRECTORY_ORIGIN` is set, it is what the console names as its own directory in every signature it sends; its `/signed-out` is the address the login provider sends a person back to after signing out, which must be registered there as a post-logout address; and it is the origin the console trusts for form posts and wizard calls, so a proxy that rewrites the `Host` header does not get the console's own pages refused as coming from another site. | `https://console.example` |
| `CONSOLE_DIRECTORY_ORIGIN` | Optional | The https origin the console names as its `Signature-Agent` in every request it signs to the Exchange. The Exchange fetches the console's key from `<origin>/.well-known/http-message-signatures-directory`, so it must reach this console over https with the `Host` header preserved. Default `CONSOLE_PUBLIC_ORIGIN`. Set it only when the address people open the console at is not that https origin. **Boot fails** when the value it resolves to is not an https origin: `https://`, a lowercase host, a port only when it is not 443, and nothing after it. | `https://console.example` |
| `CONSOLE_OIDC_ISSUER` | **Required** | The login provider's issuer URL: the publishers' instance, never the instance developers sign in to. It is the only issuer the console trusts. A session sealed under any other issuer counts as no session, so changing this value signs every publisher out once. | `https://login.console.example` |
| `CONSOLE_OIDC_CLIENT_ID` / `_FILE` | **Required** | The console's own client at the provider. Separate from any other service's. | `console` |
| `CONSOLE_OIDC_CLIENT_SECRET` / `_FILE` | **Required** | The client's secret. | *(from a secret store)* |
| `CONSOLE_OIDC_REDIRECT_URL` | **Required** | Where the provider sends a person back. Must be registered at the provider, exactly. | `https://console.example/auth/callback` |
| `CONSOLE_OIDC_SCOPES` | Optional | Space-separated. Default `openid profile email`. | `openid profile email` |
| `CONSOLE_EXCHANGE_URL` | **Required** | The Exchange's origin. | `https://exchange.example` |
| `CONSOLE_EXCHANGE_DOMAIN` | Optional, **set it** | The Exchange's published hostname, which every generated file names. It must be the same string the Exchange itself publishes, or a publisher's file will not match. Default `exchange.example`. | `exchange.example` |
| `CONSOLE_EXCHANGE_WBA_URL` | Optional, **set it** | Where a deployed edge package fetches the Exchange's public verification keys. It goes into the generated settings; leaving it out gives publishers a package that cannot verify anything. | `https://exchange.example/.well-known/http-message-signatures-directory` |
| `CONSOLE_CONTRIBUTOR_HOST` | Optional, **set it** | The hostname of the catalogue contributor you operate. It goes into every generated file, and the Exchange refuses to confirm a site whose file does not name it, so an unset value means no publisher can finish. | `catalog-contributor.exchange.example` |
| `CONSOLE_EDGE_RELEASE_REF` | Optional | The version of the edge packages the generated instructions tell a publisher to build from. The image sets it to the release tag it was built from (the `EDGE_RELEASE_REF` build argument), so leave it unset when you run a released image. An image built without that argument leaves it empty, and the generated settings then name no version. | `v1.0.0` |
| `CONSOLE_SUPPORTED_PROFILES` | Optional | Space-separated extension profiles, written into the generated file. Default `fora-news-v1`. | `fora-news-v1` |
| `CONSOLE_DEFAULT_RATE` | Optional | What a charging arrangement costs per access. Default `0.01`. Deliberately not zero — see §4. | `0.01` |
| `CONSOLE_EDGE_PACKAGE_DIR` | Optional | Where the edge packages this release built are kept. The image sets it; a deployment that leaves it empty hands publishers the settings file and the written procedure, and no package to install. | `/edge-packages` |
| `CONSOLE_SUPPORT_EMAIL` | Optional, **set it** | The address shown to a publisher whose network the console cannot set up for them. That screen is a dead end by design, so an empty value leaves them a sentence and nobody to send it to. | `help@exchange.example` |
| `CONSOLE_ADDR` | Optional | Address the console binds to. Default `:8085`. | `:8085` |
| `CONSOLE_LOG_LEVEL` | Optional | `DEBUG`, `INFO`, `WARN` or `ERROR`. Default `INFO`. | `INFO` |
| `CONSOLE_FETCH_TIMEOUT` | Optional | How long one read of a publisher's own page may take. Default `10s`. | `10s` |
| `CONSOLE_FETCH_SCHEME` | Optional | The scheme used to reach a publisher's site. Default `https`. Leave it alone in production. | *(leave unset)* |
| `CONSOLE_EXCHANGE_TIMEOUT` | Optional | How long one call to the Exchange may take. Default `15s`. | `15s` |
| `CONSOLE_DNS_TIMEOUT` | Optional | How long one name lookup may take while guessing which network serves a site. Default `3s`. | `3s` |

---

## 3. `CONSOLE_SESSION_KEY` — why the boot fails without it

The console could generate a key at startup, and it deliberately does not.

A session here lasts a working day, because setting a site up spans a DNS
change and a CDN deployment. A key generated per process means every restart
signs every publisher out, and, if you run more than one instance, a person is
signed out each time they reach a different one. Both are invisible until a
publisher reports that the console keeps forgetting them.

Generate one and keep it with your other secrets:

```
openssl rand -base64 32
```

Rotating it signs everyone out once. That is the whole cost — nothing else
reads it.

---

## 4. The default rate is not zero

`CONSOLE_DEFAULT_RATE` is what the "charge for each access" arrangement costs.
Left unset it is `0.01`, not `0`.

A zero rate is a different arrangement: it is the free one, which publishers
choose explicitly. A charging arrangement that silently became free would give
content away while the console reported it was selling it — the one wrong
answer that looks like it worked.

---

## 5. What is stored here, and what is not

The console has one table: who has signed in, keyed on the identity the login
provider assigns. It holds the address and name the provider last reported,
and whether the provider says it checked the address.

Everything else — which publisher a person acts for, which sites they
registered, which of those are confirmed — belongs to the Exchange and is read
back from it on every page that needs it. There is nothing here to keep in
step, and a person who signs in from a second device sees the same state.

---

## 6. Worked example

```
CONSOLE_DSN=postgres://fora:PASSWORD@db.internal:5432/console?sslmode=require
CONSOLE_SESSION_KEY=<openssl rand -base64 32>
CONSOLE_SIGNING_KEY_FILE=/run/secrets/console-signing-key.pem
CONSOLE_PUBLIC_ORIGIN=https://console.example

CONSOLE_OIDC_ISSUER=https://login.console.example
CONSOLE_OIDC_CLIENT_ID_FILE=/run/secrets/console-client-id
CONSOLE_OIDC_CLIENT_SECRET_FILE=/run/secrets/console-client-secret
CONSOLE_OIDC_REDIRECT_URL=https://console.example/auth/callback

CONSOLE_EXCHANGE_URL=https://exchange.example
CONSOLE_EXCHANGE_DOMAIN=exchange.example
CONSOLE_EXCHANGE_WBA_URL=https://exchange.example/.well-known/http-message-signatures-directory
CONSOLE_CONTRIBUTOR_HOST=catalog-contributor.exchange.example
CONSOLE_SUPPORT_EMAIL=help@exchange.example
```

The Exchange needs two settings of its own for any of this to work — the
address of this console's key directory, and the same contributor hostname.
Both are in the Exchange's own configuration document.
