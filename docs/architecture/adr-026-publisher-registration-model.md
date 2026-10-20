# ADR-026 — Publisher Registration Model and the Publisher Console

**Status:** Proposed (2026-09-17)

**Amended 2026-09-18.** The content path scope and the terms preset move off
`publisher_domains` onto `publisher_domain_listings`. They describe what one publisher
offers one seat, so they belong to the relationship, not to the site. `signing_scheme`
and `cloudfront_key_pair_id` stay on the site. See the
[Amendment (2026-09-18)](#amendment-2026-09-18--per-relationship-scope-and-terms) section.

**Amended 2026-10-05.** Publishers sign in through a Zitadel instance of their own,
not through the provider the identity service uses, so D4's "one deployment can point
both services at one provider" no longer describes the FORA deployment. See the
[Amendment (2026-10-05)](#amendment-2026-10-05--publishers-sign-in-through-an-instance-of-their-own)
section.

**Amended 2026-10-08.** A site is saved before anything proves it is the publisher's,
and is verified separately, once its document proves it and the console has seen its
package answer: D6's order, which the first implementation had reversed. Saving holds
the hostname. See the
[Amendment (2026-10-08)](#amendment-2026-10-08--a-site-is-saved-before-it-is-proven)
section.

**Relates to:** ADR-010 (publisher payout — the resource-owner payee and the commission
registry; amended by this ADR), ADR-017 (the agent-side identity service, which the
console deliberately does not extend; its account key and its "a link is a hint, the
Exchange is the authority" pattern are copied), ADR-021 (the protocol website, where the public onboarding page lives),
ADR-022 (the admin plane, which registration deliberately does not use), ADR-023 (the
guarded fetch posture every publisher-supplied URL is fetched under).

---

## Context

The Exchange has no registration. A publisher is onboarded by an operator running SQL
over SSH: insert a `fora.tenants` row, hand-write the domain's `/.well-known/fora.json`,
exchange key material by email. Self-service onboarding replaces that with a public
onboarding page, a registration flow, a publisher dashboard and, later, an operator
dashboard. Each of those needs the same three answers first: what a "publisher" is in
the data model, how a publisher proves it controls a domain, and who a logged-in person
is to the Exchange. This ADR settles those and the decisions that hang off them.

### What the data model says today

The word *tenant* carries three meanings at once, and the code implements only one:

- `fora.tenants` is keyed by `domain`, `UNIQUE`, and every catalog push derives the
  tenant from the entry's domain by exact match. In the code, **a tenant is a publisher
  domain**. The deployed stack has one tenant and one domain.
- The same row carries `signing_scheme`, `rsa_key_ref`, `cloudfront_key_pair_id` and
  `ed25519_key_ref`. Those depend on the CDN in front of one *site*, so a CloudFront
  site and a Cloudflare site cannot share a tenant. This is what pins tenant to domain.
- The same row also carries `fee_rate_bps`, `reporting_policy`, `allow_broker_relay`
  and `activate_new_agents_by_default`. Those are *seller*
  settings: the commission a seat charges, the policy it imposes on agents. They have
  nothing to do with one site's CDN.
- The roles catalog describes a third shape: one tenant hosting many publishers. The
  `tenant_resource_owner_fee` registry (ADR-010 amendment B) was built for that shape —
  "an aggregator tenant can charge a different commission per owner it represents" — but
  the per-domain tenant lookup can never put two owners under one tenant, so the
  override table is only ever written by tests.

The *publisher* is `resource_owner_id` (ADR-010 amendment A): a string the domain's own
manifest attests, stored per catalog row, with no table, no foreign key and no
uniqueness. It groups domains into one payee. Nothing stops a second domain from
attesting somebody else's id; ADR-010 accepted that because v1 reporting moves no money
and recorded the obligation to close it before a payout path ships.

The product decision that triggered this ADR: **tenant–publisher relationships are
many-to-many.** A publisher is not owned by one tenant; tenant-specific configuration
and permissions are scoped to the relationship; sharing a publisher across tenants must
not share access between them. Under the code's current reading (tenant = domain) that
sentence has no meaning. Under the seller reading it does, and it is the reading the
protocol already encodes: a publisher's manifest lists the Exchanges it authorizes in
`exchanges[]`, each entry carrying its own `resource_owner_id` and a
`PROVIDER_RELATIONSHIP_DIRECT` / `RESELLER` relationship. This ADR adopts that reading.

## The problem

Five questions block self-service onboarding, and one hidden question sits under three of them.

1. Is a tenant a publisher domain, or can it hold many publishers?
2. How does a publisher prove it controls a domain?
3. Where does the publisher console run, and how does a publisher log in?
4. Who holds which keys?
5. Which service owns the registration RPC, what does it carry, and what authenticates it?

The hidden question: **how does a login become a publisher id, and how does a second
domain join an existing publisher without a free-text claim?** If the console's scope is
"whatever `resource_owner_id` a manifest attests", any domain can attest your id and
its rows appear in your dashboard; under the future payout adapter, its revenue would be
yours to withdraw and yours to be taxed on. A registration model that leaves this open
is not a registration model.

## Decision

### D1 — Three records: tenant (a seat), publisher (a payee), publisher domain (a site). Tenant–publisher is many-to-many.

**A tenant is an Exchange seat**: one Exchange identity that sells on this deployment.
It is what a publisher's manifest names in `exchanges[].domain`. A seat owns the seller
settings that already sit on `fora.tenants` — commission default, reporting policy,
broker relay, agent activation and welcome credit — and the URL-signing key material
(D5). `tenants.domain` becomes **the hostname the seat answers on**, the value that today
lives in the single `EXCHANGE_DOMAIN` variable. The first deployment runs exactly one
seat whose domain is that variable's value; the seed changes from "the demo publisher's
domain" to "the Exchange's domain", and nothing else about that deployment changes.

**A publisher is the payee.** It has one durable id, minted by the Exchange (D2), a
display name, and nothing CDN-shaped. It is a global record, like `agents`: the id is
what groups one owner's sites across every seat that sells them, so it cannot be
tenant-scoped.

**A publisher domain is a site.** It belongs to exactly one publisher, deployment-wide,
and carries the site-shaped settings that leave the tenant row: `signing_scheme`, the
CloudFront key-pair id (D5), the content path scope and the terms preset the generated
artifacts are built from.

**The relationship is explicit and carries the per-relationship state.** A domain
*listed* on a seat is a `(tenant, domain)` row with a state — `pending_manifest` until
the domain's manifest proves the listing (D3), then `active`. Per-relationship
configuration and permissions key on `(tenant, publisher)`: the commission override
(`tenant_resource_owner_fee`, unchanged) and the console grants (D4). Listing one
publisher on two seats creates two listings, two grant sets and two fee rows; nothing is
shared through the publisher record. That is the product decision made structural.

Schema, as entities and keys — the migration is the registration task's:

| Table | Key | Holds |
|---|---|---|
| `tenants` (existing) | `tenant_id`; `domain` UNIQUE = the seat's hostname | seller settings, `ed25519_key_ref`, `rsa_key_ref` |
| `publishers` | `publisher_id` (= `resource_owner_id`, D2) | display name, created_at |
| `publisher_domains` | `domain` | `publisher_id` FK, `signing_scheme`, `cloudfront_key_pair_id`, path scope, terms preset |
| `publisher_domain_listings` | `(tenant_id, domain)` | state, `verified_at`, `last_checked_at` |
| `publisher_grants` | `(tenant_id, publisher_id, oidc_issuer, oidc_subject)` | who may act for this publisher on this seat |
| `tenant_resource_owner_fee` (existing) | `(tenant_id, resource_owner_id)` | gains an FK to `publishers` |

`signing_scheme` and `cloudfront_key_pair_id` leave `tenants`. `catalog.resource_owner_id`
gains a foreign key to `publishers` and loses its empty-string default.

**Catalog push under D1.** The entry's domain resolves to its `publisher_domains` row,
which names the publisher; the seat is the one the request arrived at. The manifest
fetched for the push must list this seat with that publisher's id (D2) and the listing
must be active. The rejection vocabulary grows by one reason — the attested id does not
match the registered one — next to the existing `missing_resource_owner_id`.

**What D1 does not do yet.** With one seat per deployment, resolving the seat from the
request `Host` and scoping the discovery trie and `catalog.uri` uniqueness per seat are
not needed: `EXCHANGE_DOMAIN` still names the only seat, and the catalog constraint
`UNIQUE (uri)` still holds. A deployment that runs a second seat needs exactly that
change — seat from `Host`, `UNIQUE (tenant_id, uri)`, a per-seat trie — and this ADR
records it as the designed path so the schema does not have to move again. Until then, a
second seat is a second deployment.

### D2 — The Exchange mints the publisher id; the manifest attests it; the two must match

`publisher_id` **is** `resource_owner_id`. One value, one word in prose ("publisher"),
the protocol's name on the wire and in the columns that already exist. Renaming
`resource_owner_id` in the schema or the SDK buys nothing and costs a protocol change.

The id is **minted by the Exchange at registration** (opaque, `pub_<uuid>`-shaped), shown
to the publisher in the console, and written by the publisher into every domain's
manifest as `exchanges[].ext.resource_owner_id` for this seat — exactly what the
generated manifest (D6) already contains. Nobody types it.

**Attestation is still required, and it is now checked.** ADR-010 amendment A made the
manifest the source of the id and accepted any value because a domain can only donate
its own revenue. That stays true on the wire and for every other Exchange that reads the
manifest. On this deployment the rule tightens: the attested value must equal the id
under which the domain was registered, or the push is rejected. A domain therefore joins
a publisher in two steps that both need the publisher's cooperation — someone holding a
grant for that publisher registers the domain in the console, and someone controlling
the domain serves the manifest naming the id. Neither step alone attaches a domain to a
payee. This is the "authenticate against the same domain root of trust" obligation
ADR-010 D3 placed on the payout adapter, discharged at the registration boundary instead
of deferred to it. ADR-010 carries a dated amendment recording the narrowing.

**Where the id lives outside the Exchange.** In manifests, and in the console's link
table as a navigation hint (D4). Nothing decides from that copy: grants live in the
Exchange, and the console asserts who a person is, never what they own.

### D3 — Proof of domain control is the served manifest; the protocol's HTTP-01 pair stays unimplemented

A listing is proven when `https://<domain>/.well-known/fora.json` parses as a
`WellKnownManifest`, its `exchanges[]` has an entry whose `domain` is this seat's
hostname with `ext.resource_owner_id` equal to the registered publisher id, and its
`catalog_contributors[]` names the contributor directory registered for the domain
(D5). The Exchange fetches it at registration, on every "check again" from the
console, and — because Gate 2 and the owner-id read already fetch it — at every catalog
push. The proof is continuous, not a one-time ceremony: a manifest that stops naming the
seat stops the domain from pushing.

The fetch is the guarded fetch of ADR-023 D6: resolve first, refuse private and
link-local addresses, follow no redirects, cap body and time, send no credentials. The
domain is publisher-supplied and the console is reachable by anyone with a login, so
this is an SSRF surface and the posture is mandatory.

**Why not `RequestDomainVerification` / `ConfirmDomainVerification`.** The protocol
module defines them (ACME HTTP-01 style: serve a token at
`/.well-known/fora-verify/<token>`, then confirm and register a signing key). They prove
control once and bind one key. The manifest proves control *and* carries the
authorization the Exchange needs at every push — which seat may sell, which contributor
may push, which payee is owed — and the publisher must serve it regardless. Implementing
the pair would add a second proof of a subset of what the first already proves, and a
second place for the two to disagree. The RPCs stay unimplemented on this Exchange; the
Exchange answers them `unimplemented`, as it does today.

### D4 — The console is its own service, operator-side; it owns login and sessions; it calls each Exchange as a signed first-party caller acting for the logged-in person

**Where.** The public onboarding page is static, on the protocol website (ADR-021),
calling the operator-side preview API from the browser. Everything after login is the
**publisher console**: a separate service (`src/console/`), operator-side, with its own
database. It is **not** part of the identity service. That service is the agent-side
registry (ADR-017 D2): it hosts identities for agents that have no domain of their own,
and a publisher is defined by having one. The console also grows in a direction the
registry never will: one account connects to **several Exchanges** — a second seat on
this deployment, or another operator's deployment — and that hub belongs to no single
Exchange and to no agents' registry.

**Login and accounts.** The console has its own upstream OIDC client, its own session
store and its own accounts table, keyed on `(oidc_issuer, oidc_subject)` — the same key
the identity service uses for developers, so one deployment can point both services at
one provider. One upstream issuer per console deployment; which provider it is — the
operator's own Zitadel, Google, an enterprise IdP — is a deployment choice, not a
protocol one. The reusable OIDC-client and session packages move out of the identity
service's internal tree into the shared `internal/` tree; they are not copied.

**How the console calls an Exchange.** The browser talks only to the console. The
console calls `PublisherService` (D6) **server-side**, signing every request under
RFC 9421 with its own key, served from a Web Bot Auth directory on the console's domain
— the same Gate 1 that contributors and the Broker already pass. Each Exchange is
configured with that directory (`EXCHANGE_CONSOLE_WELLKNOWN_URL`, the same shape as the
Broker's `EXCHANGE_BROKER_WELLKNOWN_URL`) and classifies the caller `CONSOLE`; no
self-signup can produce that class. Every `PublisherService` request carries an
**`actor`** — the OIDC `(issuer, subject)` and verified email of the person acting — and
the Exchange accepts an actor only from a `CONSOLE` caller. The console asserts *who*;
the Exchange decides *what*: grants live in the Exchange (D1), are created in the same
transaction as the publisher they grant (D6), and are resolved on every request from the
asserted actor. There is no token issuer, no CORS and no second trust path beside the
request signature the Exchange already verifies.

**Connecting to an Exchange.** Signing up in the console creates an account and nothing
else. "Connect to `exchange.example`" runs `RegisterPublisher` on that Exchange, which
mints the publisher id there (D2) and grants it to the actor. The console records the
link — account, Exchange domain, publisher id — as a **hint** for its own navigation,
under the rule the identity service already applies to its Exchange-registration notes:
the Exchange is the authority, the link is a cache that the next call rebuilds. A
publisher connected to two Exchanges has two publisher ids, one per `exchanges[]` entry
in its manifest, which is how that array already works.

### D5 — Publishers hold the contributor keys; the seat holds the URL-signing keys; the CloudFront key-pair id is per domain

**Contributor keys are the publisher's.** The key that signs catalog pushes is generated
by the publisher with the SDK or the ingest tool and **served from a Web Bot Auth
directory the publisher controls**. The registration form takes the directory host and
defaults it to the publisher domain itself; the manifest-building terraform module takes
the same value as its contributor id; the manifest lists it in `catalog_contributors[]`; Gate 1 resolves the
key from that directory on the first push exactly as it does today. We never generate,
receive or store a publisher's private key. A raw public-key upload is **not** accepted
in v1: the push path resolves contributor keys from a directory, and a stored key with no
directory would be a second resolution path with its own rotation story. A publisher that
cannot serve a directory is the case the hosted registry (ADR-017 D2) exists for and is
deferred. One contributor is the operator's rather than the publisher's: the automatic
catalog import signs with an operator-held key that the publisher authorizes the same
way — see section B of the amendment of 2026-09-21 below.

**URL-signing keys are the seat's.** The Exchange signs delivery URLs, so the private key
is ours and stays on `tenants` (`ed25519_key_ref`, `rsa_key_ref` — one key per scheme
per seat, generated by the deployment, never by the publisher). What the publisher
receives is the **public** half, embedded in the generated edge artifact: the Ed25519
public key baked into the Cloudflare or Fastly worker config, the RSA public key to
upload to the CloudFront trusted key group. CloudFront then assigns a **key-pair id**
in the publisher's AWS account; the publisher enters it in the console and it is stored
on `publisher_domains.cloudfront_key_pair_id`, which is why that column moves off the
tenant row while `rsa_key_ref` does not. URL minting reads the scheme and key-pair id
from the domain row and the private key reference from the seat.

### D6 — Registration and publisher reads are one publisher-authenticated service on the Exchange

**Owner.** The Exchange. It owns the tables (D1), the manifest fetch (D3), the key
material (D5) and the audit log; the console owns nothing a registration writes. The admin plane (ADR-022) is not used: it is IP-allowlisted, unsigned and
carries no actor, and registration must be attributable to the publisher who performed it.

**Shape.** A new proto package `fora.publisher.v1` with one `PublisherService`, in the
protocol module next to `fora.admin.v1` — the same plane-separation reasoning as ADR-022
D1: a validated contract in every SDK language, and a package boundary that keeps the
console plane out of the agent hot-path service. It mounts on the **public listener**
(the console may run on another host, or another operator's) under a route prefix the
RFC 9421 verify seam covers, so every call is a signed call. A `CONSOLE`-only
interceptor then refuses any caller of another class and any request without an
`actor` (D4), and every handler resolves the actor's grants before touching a
repository. Ships as a protocol release and a pin bump, as the admin package did.

Write RPCs, and what each carries:

| RPC | Carries | Does |
|---|---|---|
| `RegisterPublisher` | display name | mints `publisher_id`; creates the caller's grant on this seat in the same transaction; audit row names the OIDC subject |
| `RegisterDomain` | `publisher_id`, domain, contributor directory host, path scope, terms preset | detects the CDN (the preview API's detector), derives `signing_scheme`, creates the domain row and a `pending_manifest` listing; returns the generated bundle |
| `CheckDomain` | `publisher_id`, domain | re-fetches the manifest (D3); moves the listing to `active` or reports why not |
| `SetDomainKeyPairID` | `publisher_id`, domain, CloudFront key-pair id | records the id (D5); only for `AWS_CLOUDFRONT_RSA` domains |

Everything else a registration might be expected to carry is derived, not supplied: the
signing scheme comes from detection, the publisher id from minting, the contributor key
from the directory. A detected CDN of "none" refuses `RegisterDomain`
with a reason that maps to the public page's "contact us" path.

The **generated bundle** is built by the Exchange from the same builders the terraform
modules use: the exact `fora.json` to serve, the ingest command line with the sample
feed, and the per-CDN artifact — the generic worker plus binding values for Cloudflare,
the compute package plus config for Fastly, the RSA public key plus the Lambda@Edge build
instructions for CloudFront. Storing path scope and terms preset on the domain row is what
lets the console regenerate the bundle later without asking again.

The publisher-scoped **read RPCs** (totals per period, transaction list and detail,
catalog, settings) join the same service. Every one filters by the caller's
`(tenant, publisher)` grant; a caller with no grant for the requested publisher gets
`not_found`, never another publisher's rows.

### D7 — Vocabulary and the three console levels

Three words, fixed from here on:

- **Operator** — whoever runs the deployment. Owns the admin plane and, later, the
  cross-tenant dashboard.
- **Tenant** — a seat (D1). Today the operator's own single seat. A tenant's own
  dashboard — self-service over its commission default and per-publisher overrides — is
  a **separate product**, never a screen inside the publisher console, and it is the
  post-v1 target ADR-022 D4 already names, **not** part of this work. While the only
  tenant is the operator, that dashboard runs on the admin plane behind its IP
  allowlist, and that is enough: there is no second party to separate and no outside
  reader of the audit trail. Two events end that: the first tenant that is not the
  operator (an allowlist knows a network, not a party), or the first need to say *who*
  changed a rate (admin audit rows carry no actor). Either one triggers the ADR-022
  amendment that gives the plane an operator identity.
- **Publisher** — the payee and the person acting for it in the console. "Resource
  owner" is the same thing, used only where the protocol's field name forces it.

Consequently the "operator dashboard" of self-service onboarding is the **cross-tenant** view, whose
reads carry the explicit cross-tenant marker and whose operator identity is the ADR-022
amendment that task already requires. The publisher dashboard is the per-relationship
view. There is no platform-to-tenant commission in the model; if a deployment ever hosts a
seat it does not own, that is a new decision, not an omission here.

## Consequence map

Each decision, and what it does to the tenants table, the manifest and the registration
flow.

| Decision | `fora.tenants` | The manifest | The registration flow |
|---|---|---|---|
| D1 seats and publishers | loses `signing_scheme`, `cloudfront_key_pair_id`; `domain` is now the seat's hostname; the first deployment's seed row changes accordingly | `exchanges[].domain` names the seat; one publisher lists any number of seats | creates publisher, domain and listing rows, never a tenant |
| D2 minted id | — | `ext.resource_owner_id` must equal the registered id for that domain; mismatch rejects the push | mints the id; the publisher never types it |
| D3 manifest as proof | — | is the proof: seat entry with the id, contributor listed | ends in `pending_manifest`; `CheckDomain` activates |
| D4 console and login | — | — | is its own service; the console asserts the actor, the Exchange resolves grants |
| D5 keys | keeps `ed25519_key_ref`, `rsa_key_ref` | `catalog_contributors[]` names the publisher's directory | takes a directory host and, for CloudFront, a key-pair id back |
| D6 the service | audit rows gain the actor's OIDC subject | is fetched by `RegisterDomain` and `CheckDomain` | is `fora.publisher.v1.PublisherService` on the Exchange's public listener, called by the signed `CONSOLE` caller |

## Consequences

**Positive.**
- The word *tenant* means one thing in code, docs and product — a seat — and the
  many-to-many relationship is a row, not a sentence.
- The self-attested payee namespace is closed at registration, before any payout path
  exists; ADR-010 D3's obligation is met early rather than remembered late.
- The manifest stays the single authorization document; registration adds no second
  proof mechanism, no token endpoint, no stored public keys.
- Grants, publishers and audit rows are written in one database in one transaction; the
  identity service stays agent-side and untouched.
- The console can front more than one Exchange from day one, because it authenticates
  to each the way any signed caller does.
- The seed for the existing deployment is a one-row change; the running Exchange keeps
  `EXCHANGE_DOMAIN` and its single seat.

**Negative / costs.**
- One coupled migration: three new tables, two columns moved, one foreign key added,
  the seed and the e2e fixtures updated together. The push path changes its lookup order
  (domain → publisher, then seat → manifest entry) and gains a rejection reason.
- A protocol release for `fora.publisher.v1`, and a pin bump here, before the
  registration task can build its handlers.
- Multi-seat on one deployment is designed, not built: `Host` resolution, per-seat URI
  uniqueness and a per-seat trie are a later change, and until then "many-to-many" is
  realized across deployments that share a publisher id.
- A new service to build and operate: OIDC client, sessions, a database, a signing key
  with a WBA directory. The OIDC-client and session packages move out of the identity
  service's internal tree first.
- The Exchange trusts one configured console caller to assert the acting person — the
  same trust shape as the Broker relay. A compromised console key can act as any
  publisher on that Exchange until it is revoked through the console's directory; the
  key is therefore custodied like the Broker's, never like a contributor's.

## Rejected alternatives

- **One tenant per publisher domain** (the shape the code implements today). Needs the
  least migration and matches every existing test, but it cannot express the many-to-many
  decision at all, leaves `tenant_resource_owner_fee` unreachable by design, and makes
  the seller settings — commission, agent policy — per site instead of per seat.
- **The HTTP-01 verification pair as the proof.** Proves a subset of what the manifest
  already proves, once; adds a token path and a key-registration path the push flow does
  not use. See D3.
- **Accepting the manifest's `resource_owner_id` as the console's scope.** Lets any
  domain attach itself to any payee by attesting its id. See D2.
- **Registration on the admin plane.** No actor, no per-publisher scope, IP-allowlisted;
  ADR-022 D4 says as much about itself.
- **The console inside the identity service.** Reuses its OIDC client and sessions, but
  that service is the agent-side registry (ADR-017 D2), and the console must connect one
  account to several Exchanges — neither an agent concern nor one Exchange's.
- **The browser calling the Exchange directly with a console-issued token.** Needs CORS
  on the Exchange and a second trust path (a JWT issuer) beside the request signature it
  already verifies, and binds each Exchange to one console's key format. A signed
  first-party caller reuses the Gate the Broker already passes.
- **Grants in the console, the Exchange trusting a publisher id it is handed.** Moves
  authorization out of the Exchange, and puts the publisher mint and its grant in two
  databases with no transaction between them.
- **Raw public-key upload for the contributor key.** A second key-resolution path
  beside the WBA directory, with its own rotation and revocation story. See D5.

## Out of scope

- The tenant-level (seat operator) console and moving the fee setters off the admin
  plane — the ADR-022 amendment the operator-dashboard task owns.
- Multi-seat resolution from `Host`, and the catalog constraints that go with it (D1).
- Payout destinations, KYC and the `PayoutAdapter` (ADR-010 D3).
- Editing terms, pricing or keys from the dashboard; any write RPC beyond D6's table.
- Federating several upstream OIDC issuers.
- Promoting `resource_owner_id` from `ext` to a typed proto field (ADR-010 amendment,
  still open).

## Amendments to earlier ADRs

- **ADR-010 amendment A** ("self-attestation is safe", any attested value accepted) is
  narrowed by D2: the attested id must equal the id the domain was registered under.
  Recorded as a dated amendment on ADR-010.
- **ADR-012 (D7 and D9) and ADR-015** name `tenants.signing_scheme` as what selects Ed25519
  or CloudFront RSA per tenant. D5 moves that column to `publisher_domains`, so in both
  places read it as the site's scheme: the choice follows the CDN the site sits behind,
  not the seat that sells it. Neither ADR's decision changes — only where the value is
  read from — so neither carries its own amendment.
- **The roles catalog** (`roles-and-use-cases.md`) is updated in the same change: the
  tenant/publisher paragraph, the publisher role's authentication, EC-1, and the stale
  "no admin HTTP surface" sentence.

## Amendment (2026-09-18) — per-relationship scope and terms

**Status of this amendment:** Proposed together with ADR-026 (2026-09-18). It takes
effect with the ADR; the schema task writes the columns in the shape recorded here.

**What D1 says today.** `publisher_domains` is keyed by the publisher's domain and is
deployment-wide — one row per site, whoever sells it. D1 puts five things on that row:
`publisher_id`, `signing_scheme`, `cloudfront_key_pair_id`, the content path scope and
the terms preset. `publisher_domain_listings`, keyed by `(tenant_id, domain)`, carries
only the listing state and its timestamps.

**Why that is wrong for two of the five.** A site listed on two seats has one
`publisher_domains` row and two listings, so the path scope and the terms preset are
shared across both seats and cannot differ. The path scope is the one with teeth: it
states which part of the site is offered for licensing, and a publisher that wants to
offer `/articles/` through one seat and `/archive/` through another has no way to say
it. The terms preset is the template the generated feed and manifest start from, so
sharing it is milder — the authoritative terms ride per resource on
`ResourceEntry.terms` and the catalog is already tenant-scoped — but it is a commercial
default, and a publisher selling through two seats under different arrangements wants
two of them. Both contradict the rule this ADR otherwise holds: per-relationship
configuration keys on the relationship.

**The change.** The content path scope and the terms preset move to
`publisher_domain_listings`. Its key is unchanged, `(tenant_id, domain)`, where `domain`
is the publisher's site and the seat arrives as `tenant_id`. `publisher_domains` keeps
`publisher_id`, `signing_scheme` and `cloudfront_key_pair_id`. The D1 schema table and
the D5 sentence that places the key-pair id per domain are read subject to this.

**The rule the split follows**, stated once so a later column lands in the right place
without re-deriving it: **the site row holds physical facts about the site, the listing
row holds the commercial arrangement.** A site sits behind one CDN and has one key-pair
id no matter who sells it, so those are the site's. What is offered, and on what terms,
is agreed between one publisher and one seat, so that is the listing's.

**What does not change.** Registration still creates a publisher, a site and a listing;
the listing still starts `pending_manifest` and is activated by the served manifest (D3).
Catalog push is unaffected — it reads the site row to find the publisher and the listing
to check it is active, and neither the path scope nor the preset gates a push. With one
seat per deployment nothing observable differs today; the split is what keeps the schema
correct when a second seat exists.

## References

- Protocol: `github.com/FORA-Protocol/protocol` at the version pinned in the root
  `go.mod` — `WellKnownManifest`, `AuthorizedExchange` (`ext.resource_owner_id`,
  `ProviderRelationship`), `CatalogContributor`, `RequestDomainVerification` /
  `ConfirmDomainVerification`, `fora.admin.v1`.
- ADR-010 §Amendment A and B; ADR-017 D2, D11; ADR-021; ADR-022 D1, D4; ADR-023 D6.
- Migrations `000002_signing_scheme`, `000012_catalog_uri_unique`,
  `000018_catalog_resource_owner`, `000020_resource_owner_fee_override` under
  `src/exchange/internal/db/migrations/`.
- `internal/forawellknown/publisher.go` — `ResourceOwnerID`, `AuthorizesContributor`.
- `src/identity/internal/db/migrations/000003_developer_account.up.sql` and
  `000005_exchange_registration.up.sql` — the `(oidc_issuer, oidc_subject)` account
  key and the "link is a hint, the Exchange is the authority" rule the console copies.
- `src/exchange/cmd/server/keyresolver.go` — the Broker's configured key directory,
  the shape the console's `EXCHANGE_CONSOLE_WELLKNOWN_URL` follows.

## Amendment (2026-09-21) — the contract, the contributor, the CloudFront mode, and one publisher per account

**Status of this amendment:** accepted with the registration implementation
(2026-09-21). Four changes, each recorded with what it replaces.

### A. The registration contract is implementation-owned, not protocol

**What D6 says.** Registration is a new proto package `fora.publisher.v1` in the
protocol module, shipped as a protocol release and a pin bump before the
handlers can be written.

**What changes.** It is withdrawn. How an Exchange registers publishers is that
implementation's own question: the protocol says what a licensed access looks
like on the wire, not how a publisher comes to have one. Two Exchanges may
answer it differently — one with a console like this, one with a form its
operator fills in, one with nothing at all — and none of that has to be
negotiated between them, because no agent and no other Exchange ever calls it.
Putting it in the protocol would standardise a surface that has exactly one
caller per deployment, and would make every future change to it a protocol
release.

**The shape instead.** Plain JSON over HTTP on the Exchange's public listener,
under `/exchange/v1/console/`, signed under RFC 9421 like every other surface —
the same shape the existing agent self-signup endpoint has. Protocol types are
reused where they already say the thing (the manifest, the license terms); the
request and response envelopes are this repository's.

**What is unchanged.** Everything D6 says about ownership, authorization and
the admin plane. The Exchange still owns the tables, the manifest fetch and the
audit log; the console still asserts an actor and nothing else; the admin plane
is still not used, for the reason D6 gives — it carries no actor, and a
registration has to be attributable to the person who made it.

### B. The catalog contributor is the operator's

**What D5 says.** The key that signs catalog pushes is generated by the
publisher and served from a Web Bot Auth directory the publisher controls. The
registration form takes that directory host.

**What changes.** The operator holds the contributor key and serves its
directory, at a host of its own — separate from the Exchange's own domain, so
the key that pushes catalogs is not the key that signs offers. The publisher
generates nothing and uploads nothing. What they do instead is authorize that
host in their manifest, which the generated manifest already does for them, and
which the domain check requires before it activates a listing.

**Why.** The publisher-held key assumed the publisher runs the import. They do
not: the import is automatic, driven by the operator from the sources the
publisher configured at registration. A key the operator must use but the
publisher must hold has no custody story at all.

**What is unchanged.** The resolution path. Gate 1 still resolves a contributor
key from the contributor's own directory, and Gate 2 still requires the
manifest to name it. Only which party that contributor is has changed, so
nothing in the push path moves.

**The configuration.** The Exchange reads the contributor host from
`EXCHANGE_CATALOG_CONTRIBUTOR_HOST` and refuses to boot with the registration
routes on and no value. The console reads the same host, writes it into every
manifest it generates, and the Exchange checks for it on every domain check.

**Custody and revocation.** The operator's contributor key is distinct from the
Exchange's offer-signing keys and from the seat's URL-signing keys. Its public
half is served at the contributor host's
`/.well-known/http-message-signatures-directory`, and that host is the `kid`
the automatic catalog import signs as. The private key never reaches the
Exchange process: it is delivered to the host in a directory of its own and
mounted only by the importer. Authorization stays the publisher's act: removing
the host from the manifest refuses the importer's next push, and nothing on the
Exchange side can restore it. A publisher that pushes its own catalog with its
own key still can, because Gate 2 accepts any contributor the manifest names.

### C. CloudFront registers on Ed25519, through its edge function

**What D5 says.** A CloudFront site uses the native signed-URL check: the RSA
public key goes into a trusted key group, CloudFront assigns a key-pair id, and
the publisher enters it in the console, which is why the id sits on the site
row.

**What changes.** Registration does not offer that. The Lambda@Edge package
this deployment ships verifies the delivered address itself, exactly as the
Cloudflare worker and the Fastly package do, and the terraform module that
deploys it leaves the distribution's own trusted key groups empty on purpose —
enabling both would refuse addresses the function had already accepted. So a
CloudFront site registers with `signing_scheme = ED25519`, no key-pair id is
collected, and the `SetDomainKeyPairID` operation D6's table lists is not built.

**What is unchanged.** The schema. `AWS_CLOUDFRONT_RSA` remains a value of the
enum and `cloudfront_key_pair_id` remains a column, because sites built the
other way by hand exist and the URL signer still reads both. What changes is
that nothing a publisher does can produce such a site.

**The consequence for the site row.** The signing scheme no longer tells the
three integrations apart — all three are Ed25519 — so which CDN serves a site is
recorded on the site row in its own column. It is a physical fact about the
site, true whichever seat sells it, so it belongs there under the rule the
2026-09-18 amendment states.

### D. One publisher per account per seat

**What D1 and D6 leave open.** Nothing says how many payees one person may hold
on one seat, and `RegisterPublisher` as described mints a new one on each call.

**What changes.** An account holds exactly one publisher on a seat.
`RegisterPublisher` is idempotent by actor: a caller that already holds a grant
gets that publisher back. Additional sites attach to it, which is the
multi-site case and the one publishers actually have.

**Why this rather than an idempotency key.** Registration must survive a double
submit, a lost response and a re-entered form. With one publisher per account,
the grant set *is* the retry record — there is nothing to key and nothing that
can disagree. A caller-supplied key would add a second record beside the
grants, and a publisher minted under a key nobody holds a grant for is
unreachable forever, since the console never asserts ownership.

**What it does not close.** A person genuinely running two businesses with
separate payouts uses two accounts. The schema allows more than one grant per
actor and nothing here removes that, so a later flow can lift the restriction
without a migration.

## Amendment (2026-10-05) — publishers sign in through an instance of their own

### What D4 left open

D4 keys console accounts on `(oidc_issuer, oidc_subject)`, "the same key the identity
service uses for developers, so one deployment can point both services at one
provider". The first deployment that did so showed what one provider means for the
people using it. The provider keeps one browser session, so a person who had signed in
for MCP was signed in to the console as well, without a password, and landed in an
empty publisher panel. Both sides showed the same sign-in pages and emails. And the
provider's usernames are unique within it, so one email address could hold a developer
account or a publisher account, not both.

### Decision

The console signs publishers in through a **second instance of the deployment's
Zitadel**, created through Zitadel's System API, at a hostname of its own
(`login.console.<domain>`). An instance has its own accounts, sessions, sign-in pages,
mail settings and look, inside the same Zitadel process and database: no second
container, database or master key.

- The console trusts that instance's issuer and no other. A session it sealed under
  another issuer counts as no session, so moving a console to a new provider ends the
  sessions sealed before the move.
- The identity service keeps the instance Zitadel creates on its first start. Each
  instance holds exactly its side's OIDC client.
- One email address can hold a developer account and a publisher account, with
  different passwords. Nothing keys on the address, so neither service changes for it.
- Each instance carries its side's name in its page titles and email subjects, and its
  side's colours and mark. Scripts own every one of these settings; nothing is set by
  hand.
- The System API key holds SYSTEM_OWNER: it can create and delete instances and set the
  system-wide features every instance follows. Its private half stays with the
  operators, and the deployment answers 404 on every public hostname for each route a
  system permission opens.

The publishers that only the previous provider's accounts could act for are dropped at
the switch, with their sites and those accounts, so the sites can be registered again
(`deploy/terraform/scripts/drop-issuer-publishers.sh`). A publisher with sales is not
dropped: the transaction log keeps every sale, and what happens to that publisher is
its data owner's decision. The Exchange has no admin call for this, so the drop is
operator SQL, the channel for admin changes without one; it writes one audit row per
dropped publisher and seat in the same transaction, as every write on the publisher
plane does.

### Rejected alternatives

- **A second organization inside the one instance.** Organizations share the
  instance's session, so the silent sign-in across the two sides remains. Sign-ups and
  the look follow an organization only when every sign-in asks for it by scope, the
  identity service's included. And usernames are unique per instance, so one address
  holding two accounts needs a domain policy that adds a username field to the
  publishers' sign-up.
- **A second Zitadel deployment.** It separates the sides as completely, at the cost of
  a second container, database, master key and backup, and a host replacement to give
  production a sixth database. The second instance gives the same separation inside
  what already runs.

## Amendment (2026-10-08) — a site is saved before it is proven

### What changed before this

D6 has `RegisterDomain` create the site and a `pending_manifest` listing, and
`CheckDomain` activate it. The first implementation reversed that order:
registration read the site's document first and wrote nothing unless it proved the
caller controlled the site, then created and activated the listing in one
transaction. Its reason was the hostname. A site belongs to one payee for the whole
deployment, so the first registration of a hostname holds it, and writing on the
strength of a typed-in name let anyone hold a site they had nothing to do with.

The cost of that order fell on publishers. Nothing about a domain could be kept until
its document was published and its package installed, so a publisher who could not
deploy to their CDN that day lost everything they had entered.

### Decision

**Registration saves; verification is separate.** `RegisterDomain` writes the site and
a `PENDING_MANIFEST` listing and reads nothing from the site, as D6 says. The console
calls it when the publisher presses the wizard's last button, whatever the checks said
before, so nothing the publisher entered is lost. A site whose answers include no terms
is saved too -- the "Skip for now" on the console's licensing step saves it that way --
and its terms are null until they are chosen.

**Verification needs the document, the package and the terms.** `CheckDomain` stays
the only way a listing becomes `ACTIVE`, and it now refuses a listing whose terms are
not chosen yet, before it reads anything, as a failed precondition. The table says the
same thing: a CHECK refuses an `ACTIVE` listing whose terms are not a JSON object. The
package cannot be seen from the Exchange -- it runs on the publisher's own CDN -- so the
console checks it: its server asks the site what an agent, a visitor and the
well-known path get, right before it asks the Exchange to verify, and asks nothing
when the package does not answer. That ask is made in one place in the console, which
a structural guard holds.

**A site not verified yet can have its terms set.** A route of its own
(`domains/terms`) sets the terms of a `PENDING_MANIFEST` listing, with the sitemap and
the import's take-over answer chosen beside them on the console's licensing step. It is
how a site saved without terms gets them, and how terms chosen earlier change before
anything is sold under them. It refuses an `ACTIVE` listing as a failed precondition,
and the state is in the write's own predicate, so a listing verified a moment earlier is
never changed. A verified site's terms still cannot be changed.

**A site not verified yet is finished from where it stands.** The console keeps no
record of where the publisher stopped. Its "Finish setup" reopens the wizard on the
saved site's answers and asks the site what it answers now: the first check that does
not pass is the step it reopens on, and the licensing step when both pass. The last
button then sets the terms chosen there and verifies the site.

**"Not verified" is `PENDING_MANIFEST`.** No new listing state. A listing in it has no
catalog entries, because catalog push and the automatic import both refuse a listing
that is not `ACTIVE`, so nothing of the site can be discovered or bought. The console
shows such a site as not verified.

**Saving holds the hostname.** A stranger who saves somebody else's domain can never
verify it, because verifying takes a document served from the domain, so nothing of it
is sold. But the name stays held, and the real owner is told the domain belongs to
another publisher until an operator releases it with the recovery procedure in
`src/exchange/RUNBOOK.md`. There is no automatic release. This is accepted
deliberately: it is the price of letting a publisher save a domain they cannot set up
yet.

**At most ten held without proof.** One payee may hold at most ten sites on a seat
saved and not verified (`consoleapi.MaxUnverifiedDomains`); `RegisterDomain` refuses
another as a failed precondition, and verified sites do not count. The count is taken
under the payee's row lock, in the transaction that writes the listing, so concurrent
saves cannot both take the last place. It limits what one account can hold, not what
many accounts can.

### Rejected alternatives

- **A proof releases an unproven hold.** When another payee's document proves the
  site, the Exchange would drop the unproven hold and register the prover. It needs no
  operator, but it is an ownership change made inside a publisher's request, and since
  a site is deployment-wide while listings are per seat, it means deleting listings on
  other seats from it.
- **Saving a draft that holds nothing.** The unproven answers would be kept per payee
  in a table of their own, and the site and listing written only on proof, as before.
  It keeps the old guarantee, at the cost of a second record of a site beside the one
  registration writes, and "not verified" would not be a state of the listing.
- **The Exchange records the package check.** A listing column the console sets, and
  the Exchange refuses to activate without. The Exchange cannot look at the package
  itself, so the column would record only what the console said, and the console is
  where the check already runs.
- **The browser alone decides the package check.** That was the old last button: the
  wizard enabled it once the package answered, and nothing on the server asked again.
  A request made outside the wizard would skip the check.
- **A failed check at the last button saves nothing.** It keeps a stranger from holding
  a name by pressing the button, but anyone can hold a name by saving it anyway, and it
  throws away the terms the publisher just set.
