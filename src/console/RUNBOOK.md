# Publisher console — runbook

What to watch, what the failures look like from the outside, and what to do
about each one.

---

## 1. Overview

The console is stateless apart from one table of accounts. It holds a session
cookie key and a signing key, and it calls the Exchange for everything a
publisher asks about.

That shape decides what can go wrong. There is no queue to drain, no catalogue
to rebuild and no money in flight. The failures are: the database is gone, the
login provider is gone, the Exchange is gone, or the two services disagree
about a key or a hostname.

---

## 2. Monitoring

### 2.1 Health

```bash
curl -s https://console.example/healthz
# {"status":"ok"}
```

It reports that the process is serving and checks nothing else, deliberately. A
console whose Exchange is unreachable is still one a person can sign in to and
read; a probe that failed on the Exchange's outage would restart a healthy
process and fix nothing.

### 2.2 Logs

Every line is JSON and carries a `request_id` that the Exchange logs under too,
so one publisher action is one thread across both services.

| Message | Level | What it means |
|---|---|---|
| `console.auth_failed` | ERROR | A sign-in could not be completed. The cause is on the line; the person saw a sentence, not the cause. |
| `console.exchange_call_failed` | ERROR | A call to the Exchange did not succeed. Check the status on the line before anything else. |
| `console.form_refused` | WARN | A form was refused. Ordinary when a publisher mistypes a domain; a burst of them is not. |
| `console.bundle_failed` | ERROR | A publisher's files could not be produced. Their registration is unaffected. |
| `console.render_failed` | ERROR | A page failed part-way through writing. The person saw a truncated page. |
| `console.read_not_found` | INFO | A dashboard page named a transaction or resource the signed-in person's publisher does not hold, and they saw the not-found page. The line carries who asked, as `oidc_issuer` and `oidc_subject`. Ordinary after a mistyped or shared address; a burst with one subject is someone trying ids. |

### 2.3 Alerts

Worth waking someone for:

- `console.exchange_call_failed` at a sustained rate. No publisher can do
  anything while it lasts.
- Any `console.auth_failed` rate above a trickle. A provider outage, or a
  secret that was rotated on one side only.

Not worth waking someone for: `console.form_refused` or `console.read_not_found`.
Those are publishers mistyping domains and addresses.

---

## 3. Troubleshooting

### 3.1 Symptom → cause → fix

| Symptom | Cause | Fix |
|---|---|---|
| Every page says the exchange could not be reached | The Exchange is down, or `CONSOLE_EXCHANGE_URL` is wrong | Check the Exchange's own health, then the setting |
| Every registration is refused with a signature error | The Exchange is reading a different key directory than this console publishes | `CONSOLE_DIRECTORY_ORIGIN` (default `CONSOLE_PUBLIC_ORIGIN`) here and the console setting on the Exchange must name the same https origin |
| Publishers are signed out constantly | `CONSOLE_SESSION_KEY` differs between instances, or is regenerated on deploy | One key, from your secret store, the same on every instance |
| Sign-in fails at the provider, before reaching us | The redirect address does not match what is registered | Compare all three spellings; they must be identical |
| A publisher's check never passes | Their file does not match | The page names the reason. Each one is one edit to the file they downloaded |
| A publisher is told they already have 10 domains that are not verified | They hold the most domains one publisher may hold saved and not verified | They finish setting one up, or the operator releases one they do not want (the Exchange's runbook has the procedure) |
| A domain stays "Not verified" | Its file is not served, its package does not answer yet, or the Exchange failed while verifying it | The domain page says what verifying needs. Once it is in place, the publisher presses **Finish setup** on the domain: the wizard reopens at the first step that does not pass yet, and its last button verifies the domain |
| Everyone is stuck on "confirm your email" | The provider is not reporting the address as verified | Check the provider's own settings; the console reads what it is told |
| The sign-in page says "FORA Developers", or a developer's account signs in here | `CONSOLE_OIDC_ISSUER` names the developers' Zitadel instance | Point it at the publishers' instance and restart. Publishers' accounts live there, not in the developers' instance |
| The console exits at boot: its client id or secret file is missing | The publishers' instance has not been provisioned on this machine | Run `deploy/terraform/scripts/bootstrap-identity.sh` on a stack with the `console_login_url` output; it creates the instance and writes the two files |
| Every publisher was signed out at once, and signing in works | The console now trusts another issuer, and ends every session sealed under the old one | Expected once after the issuer changes. If it keeps happening, two consoles are running with different `CONSOLE_OIDC_ISSUER` values |

### 3.2 Diagnostics

Which state the console is in:

```bash
# Is it serving at all?
curl -s -o /dev/null -w '%{http_code}\n' https://console.example/healthz

# Does the Exchange accept this console's signature? Sign in and open any page;
# a 502 with "could not reach the exchange" is a network problem, a page that
# renders with no sites when a publisher has some is an identity problem.
```

The accounts table answers who has signed in. `oidc_issuer` says through which
login provider: after a deployment moves its publishers to their own Zitadel instance,
rows with the old issuer are accounts nobody can sign in to again
(`deploy/terraform/scripts/drop-issuer-publishers.sh` removes them with the
publishers that only they could act for).

```sql
SELECT oidc_issuer, oidc_subject, email, email_verified, last_login_at
  FROM console.accounts ORDER BY last_login_at DESC LIMIT 20;
```

It answers nothing about what those people registered. That is the Exchange's,
by design, and its own runbook has the queries.

---

## 4. Routine operations

### 4.1 Rotating the session key

Signs everyone out once, and nothing else. Set the new value and restart.

### 4.2 Rotating the signing key

The Exchange resolves this console's key from the directory the console
publishes, so the order matters:

1. Publish both keys in the directory, old and new.
2. Restart the console with the new private key.
3. Once no signature from the old one can still be in flight — the console
   signs for two minutes at a time — remove the old key from the directory.

Doing step 3 first refuses every in-flight call.

### 4.3 Removing an account

Deleting the row here signs that person out and removes nothing else. Their
grants live in the Exchange, so they can sign in again and be exactly where
they were. To actually remove someone's access, remove their grant at the
Exchange.

### 4.4 A publisher who cannot finish

The common ones, in the order they occur:

- **Their domain stays not verified.** It is saved, and nothing on it is sold
  until it is verified. A publisher who pressed **Skip for now** on the file,
  package or licensing step ends up here too: the last step saves the domain
  either way, and skipping the licensing step saves it with no terms.
  **Finish setup**, on the domain list and the domain's page, reopens the
  wizard at the first step that does not pass, or on the licensing step when
  both checks pass. The file step names what is wrong with the file: almost
  always it is published at the wrong path, or edited. The package step names
  what the site answered.
- **They have no domain at all.** Skip for now on the domain step ends
  registration that way, with nothing saved about a domain. It is an ordinary
  state: My domains asks for their first one.
- **Their site is behind a network we do not support.** There is nothing to
  configure; this is a conversation.
- **They saved a hostname they do not control.** They cannot verify it, ever,
  and it holds the name. Releasing it is an operator action — the Exchange's
  runbook has the procedure.
