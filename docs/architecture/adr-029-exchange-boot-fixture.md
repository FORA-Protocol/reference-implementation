# ADR-029 — Exchange Boot Fixture: the Rows a Deployment Starts With

**Status:** Proposed (2026-10-22)

**Relates to:** ADR-026 (the seat, publisher and site records), ADR-022 (the admin transport plane), ADR-028 D6 (state with no admin RPC is set through an e2e-only control service).

---

## Context

An Exchange sells as a seat (`fora.tenants`), and sells a publisher's site (`fora.publishers`, `fora.publisher_domains`) only through a listing on that seat (`fora.publisher_domain_listings`) that is `ACTIVE` (ADR-026).

- **Nothing that serves a request creates a seat.** The admin plane sets a seat's fee rate and reporting policy, but only on a seat that already exists.
- **A publisher's site and its listing come from the publisher console.** The publisher signs in, registers, saves the site, and the console's manifest check activates the listing. A deployment that runs no console has no surface for them. A site on the `AWS_CLOUDFRONT_RSA` scheme cannot be registered through the console at all.

So a deployment has been built from SQL an operator runs by hand, and the e2e harness wrote the same rows through a database driver of its own. Neither is reviewable, neither is audited, and the harness's copy made "the e2e suite never touches the database" (ADR-028 H10) untrue.

## Decision

### D1 — An optional file, read at start-up

`EXCHANGE_BOOT_FIXTURE_FILE` names a YAML file that declares seats, publishers, their sites and the sites' listings. Unset, nothing changes: a deployment that creates its rows another way boots as before.

- **The file is read and checked before the Exchange connects to anything.** A file that fails stops the boot for the cost of a parse, not of a connection and a migration run on every restart.
- **It is applied in the tenant boot step**, after the welcome-credit settings are parsed and before the missing-default-seat warning, so the warning reports what the fixture left.

### D2 — The format is strict and nests what belongs together

- `version: 1`, exactly one YAML document, no key the format does not name. An empty file, or one that declares nothing, is refused.
- A seat carries its tenant id, its domain (the value `EXCHANGE_DOMAIN` carries, a host with an optional port), its key references and both of its policies, which are required.
- A publisher nests its sites and a site its listings, so a site cannot exist without its publisher or a listing without its site. Every publisher has at least one site and every site at least one listing, on a seat the same file declares.
- A site's domain must already be in the form registration stores. Its scheme is `ED25519` or `AWS_CLOUDFRONT_RSA`, and only an RSA site carries a CloudFront key-pair id, which it must. An RSA site may be listed only on a seat that names an RSA key reference, the key its delivery URLs are signed with; D4 says when that key is resolved.
- Each listing names a terms file inside the fixture's directory. The terms decode strictly into the shape the console stores and must build into license terms in the deployment's denomination: the same checks console registration applies.
- **Not in the file:** the fee rate and the reporting policy, which the admin RPCs set (ADR-022), and accounts, grants and catalog rows, which have product surfaces of their own.

### D3 — Insert if absent; never update, never delete

At every boot the Exchange creates each declared row that is not stored yet, and leaves every stored row exactly as it is.

- A stored row whose settings differ from the file is **kept**, and the boot log names each differing setting. An operator or a product surface may have changed the row on purpose since; the file is how a deployment starts, not a second writer that undoes later changes.
- A stored `PENDING_MANIFEST` listing is never activated by the file.
- Restarts are therefore no-ops, and several replicas booting at once are safe: each insert is `ON CONFLICT DO NOTHING`, followed by a read-back in the same transaction.

### D4 — Identity conflicts stop the boot

The file may not contradict who owns what. The boot stops, and nothing is written, when:

- a seat's tenant id is stored with another domain;
- a seat's domain is held by another seat;
- a site is registered to a different publisher than the file declares. Without this, the file's listing would sell someone else's site.

A seat must also name an Ed25519 key reference the Exchange holds. RSA key references are not resolved at boot: the Exchange resolves an RSA key only when it first signs a CloudFront URL, so an Ed25519-only deployment neither pays for nor inherits the failure of an RSA key it does not use.

### D5 — One transaction, audited

The whole file applies in one transaction: all of its new rows, or none. Each created seat and each created listing writes a `fora.audit_log` row, with source `boot-fixture`, naming the file and its sha256. A seat's row records its domain, its key references and both policies. A listing's row also records whether the same boot created its publisher and its site, which have no tenant of their own to be audited under; the signing scheme and CDN of the site as stored, which the listing sells under; and the listing's path scope and the terms it sells under, which the file's sha256 does not cover.

### D6 — The trust model

The file is as trusted as the operator's SQL it replaces: whoever can write it can create seats and listings.

- A listing the file declares is created `ACTIVE`, with `verified_at` left empty, because no manifest check ran. This is the one exception to ADR-026 D1's rule that a listing stays `pending_manifest` until the site's manifest proves it, and it is the exception the operator's SQL already made.
- Catalog push still checks every push against the site's manifest, which the Exchange fetches through its manifest cache. The manifest must still name this Exchange with the declared publisher id and list the pushing contributor, so the file does not let anyone push for a site that has not authorized them.
- Mount the file read-only. The Exchange reads the file and its terms files through an `os.Root` on the file's directory, so a terms file cannot name a file outside it, by path or through a link. The fixture is capped at 1 MiB and each terms file at 64 KiB.

## Consequences

### Positive

- A deployment's seats, publishers, sites and listings are declared in reviewable configuration, validated by the rules registration applies, and created with an audit trail.
- The e2e stack builds each Exchange's rows from a fixture file, so the harness no longer needs a database driver (ADR-028 H10).

### Negative

- The file creates `ACTIVE` listings without a manifest check, so it must be protected like any other operator credential.
- Changing a stored setting through the file is not possible by design. An operator changes it through its own surface, or by SQL where none exists yet.

## Rejected alternatives

- **Admin RPCs that create seats, sites and listings.** The protocol's admin plane carries setters on existing seats and no creation surface. Adding one is a protocol change, and it would put creation of `ACTIVE` listings on a network surface rather than in deployment configuration.
- **Upsert or reconcile on every boot.** Overwriting stored rows would undo every change made through a product surface since the last boot, and a typo in the file would move a domain to another publisher.
- **Seeding rows from database migrations.** Migrations are the same for every deployment; seats and sites are not.
- **An e2e-only seeding control.** It would serve the test stack and leave deployments on hand-written SQL, and ADR-028 D6 keeps that service for state that only tests need to change.

## References

- ADR-026 — Publisher registration model (seat, publisher, site, listing).
- ADR-022 — Exchange admin transport plane.
- ADR-028 — E2E obligation harness (D6, H10).
- `src/exchange/CONFIGURATION.md` — `EXCHANGE_BOOT_FIXTURE_FILE`, its format and its log lines.
