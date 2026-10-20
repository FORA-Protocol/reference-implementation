# ADR-021 — FORA SDK Documentation: Layered Guides + Generated Reference, Co-located on the Protocol Website

**Status:** Accepted · **amended (2026-09-11)** — the SDK now ships as tagged
releases, so the off-commit consumption this ADR builds on is superseded by
ADR-020's own amendment. §7's versioning decision, §3's "Install & setup" row and
the Rejected-alternatives entry about a versioned release all describe the period
before the `v1.0.3` release. See the
[Addendum (2026-09-11)](#addendum-2026-09-11--the-sdk-ships-releases-so-the-docs-track-a-version).

---

## Overview

ADR-020 moved the reusable protocol mechanics — RFC 9421 sign/verify, RFC 7638 thumbprint, signed-URL/PoP, `ErrorDetail` mapping, the unified `Verifier`/`KeyResolver`, the client/server interceptors — out of the platform and into the layered SDK (`sdk/{go,ts,python}` in `FORA-Protocol/protocol`). The *noun* layer (the wire contract) is documented; the *verb* layer (how you actually call the SDK) is not.

The protocol website (Astro Starlight) today documents **only the protocol**: concepts, RPCs, extension profiles — the *Explanation* quadrant. It has no *Reference* for the proto messages or the SDK API, no *How-to* task guides, and no per-language examples. An OSS integrator who wants to stand up an Exchange, Broker, agent, or edge must re-derive the SDK's usage from source.

This ADR defines the **SDK documentation**: its structure, where it lives, the multi-language example model, the proto↔SDK cross-linking, the auto-generated reference, versioning, and the non-load-bearing incremental delivery model.

## The problem

- **The SDK is undocumented as a library.** The mechanics exist and are tested, but there is no "here is how you verify an offer / sign an acceptance / verify a delivery URL" — in any language.
- **The website is Explanation-only.** No *Reference* (proto or per-language API) and no *How-to* (task guides); no per-language examples at all (`<Tabs syncKey>` is not yet used anywhere).
- **The protocol pages describe the wire but don't point to the SDK.** When a page documents `ExchangeService.DiscoverResources`, nothing says "in the SDK, that's `client.Discover()` / `client.discover()`," so the description and the usable code are disconnected.

## Decision

### 1. Organize by Diátaxis; the SDK adds the two missing quadrants

Follow the [Diátaxis](https://diataxis.fr) framework and keep the four modes distinct:

| Mode | Answers | FORA mapping |
|------|---------|--------------|
| **Explanation** | "How does it work?" | The existing **Protocol** section — language-neutral, kept as-is |
| **Reference** | "What are the exact facts?" | Proto messages/enums/RPCs **+** per-language SDK API — **generated** |
| **How-to** | "How do I do X?" | The **SDK guides**, multi-language — the core of this work |
| **Tutorial** | "Teach me by doing" | One end-to-end "buy an offer" walkthrough per language |

FORA is not missing "docs"; it is missing the **Reference** and **How-to** quadrants for the SDK. Do not blend them (the common failure): guides are curated, reference is generated.

### 2. A new top-level `SDK` section on the same Starlight site

Add one sidebar group, sibling to Protocol (not a separate website — the protocol context belongs right next door):

```
SDK
├─ Overview            what it is; the mental model (convenience → primitives); install per language
├─ Install & setup     Go / TS / Python — the off-commit git-pin snippet each
├─ Guides  (how-to, MULTI-LANGUAGE)
│   ├─ Verify a received offer          (lead slice — the client-side gap ADR-020 §4 closed)
│   ├─ Sign an offer acceptance
│   ├─ Discover → select → execute
│   ├─ Verify a delivery URL (edge OR your own CMS)
│   └─ Build a custom Exchange / Broker (server interceptors)
├─ Concepts            the layering as a mental model (see §4)
└─ Reference
    ├─ Proto (messages, enums, RPCs)    generated from .proto
    ├─ Go API                           pkg.go.dev / gomarkdoc
    ├─ TypeScript API                   TypeDoc (starlight-typedoc)
    └─ Python API                       pdoc
```

### 3. Multi-language examples via Starlight synced tabs

Every code sample uses Starlight's `<Tabs syncKey="lang">`, which persists the reader's language choice across the whole site — the Stripe "pick your language once" behaviour, built in, no custom component:

```mdx
<Tabs syncKey="lang">
  <TabItem label="Go">         ```go     … ``` </TabItem>
  <TabItem label="TypeScript"> ```ts     … ``` </TabItem>
  <TabItem label="Python">     ```python … ``` </TabItem>
</Tabs>
```

### 4. Progressive disclosure — convenience first, primitives beneath; no "L0/L1/L2" in user-facing docs

Each guide leads with the one-call convenience path, then reveals the composable primitives under a "Need more control?" disclosure. The ADR-020 layer names (L0/L1/L2) are the *authors'* mental model, **not** user-facing vocabulary. Example framing:

> **Accept an offer.** The one-call way: `client.execute(offer)` — it verifies the offer, signs your acceptance, and executes.
> *Need more control?* That is three primitives you can call directly — **verify** (`Verifier`), **sign** (`signOfferAcceptance`), **execute** — usable standalone. E.g. verify a delivery URL inside your own CMS instead of at the edge.

A short **Concepts** page explains the layering for readers who want the model.

### 5. Bidirectional proto↔SDK cross-linking, CI-verified

The protocol description stays a description; it is *augmented*, not replaced:

- Each **proto reference** page carries an aside: *"In the SDK → Go `client.Discover()` · Python `client.discover()` · TS `client.discover()`"* linking to the guide + the API item.
- Each **guide** links back: *"Implements `ExchangeService.DiscoverResources` (proto reference →)."*

`starlight-links-validator` is **already installed**, so every cross-link is build-gated — broken links fail CI. That is what makes the augment-and-cross-link approach maintainable as the surface grows.

### 6. Generated reference (harvest existing doc comments)

The proto comments already flow into the generated Go/TS/Python, so the reference is mostly *harvesting*, not authoring:

- **Proto** → `protoc-gen-doc` (markdown) or Buf schema docs, embedded as MDX.
- **Go** → link to `pkg.go.dev` initially (free, zero setup); `gomarkdoc` into the site later.
- **TypeScript** → `starlight-typedoc` (generates Starlight sidebar pages from TypeDoc).
- **Python** → `pdoc`.

### 7. Versioning is a single pin; delivery is non-load-bearing and incremental

- **Docs-only, under `website/` in the proto repo.** No code touched — it cannot break the protocol or the SDK.
- **CI is just the existing `astro build` + `starlight-links-validator`** (plus the API-generation step). Non-blocking by construction.
- **Incrementally releasable**: land the SDK skeleton + one guide, then add guides/languages one small PR at a time; each is shippable on its own.
- **Versioning is one banner** — "SDK docs for protocol `<version>` / SDK `<commit>`." Since the SDK is consumed off-commit (ADR-020 §6), the docs track that commit; when it semver-stabilizes, the docs version with it. Nothing else to coordinate.

### 8. First slice (proves the whole pattern in one PR)

Add the `SDK` sidebar group + **Overview** + **one** guide — **"Verify a received offer"** — fully multi-language via `<Tabs syncKey="lang">`, plus **one** bidirectional cross-link (`DiscoverResources` proto page ↔ that guide), with the Go/TS/Python reference stubbed out to pkg.go.dev / TypeDoc / pdoc. That single PR establishes the section, the synced-tab pattern, the progressive-disclosure template, and the cross-link contract; everything after is fill-in-the-blanks at its own pace.

## Consequences

- **OSS integrators get usable, cross-linked, multi-language docs** — not a spec to re-derive. The reference cannot drift from the contract because it is generated from the same source the SDK is.
- **Non-load-bearing**: the effort cannot break the protocol or the SDK, and lands incrementally — it can pause and resume at any granularity.
- **Standing obligation**: guides must be kept current as the SDK evolves. Mitigated by (a) generated reference carrying the exact API, and (b) link validation catching drift in the hand-written guides.
- **The website grows a build step** (API generation) — modest, and it reuses doc comments that already exist.

## Rejected alternatives

- **A separate documentation website.** Fragments the reader's context; the protocol explanation belongs one click from the SDK guide. A sibling sidebar section on the same Starlight site is strictly better.
- **Hand-written API reference.** Drifts from the code the moment the code changes — the dominant docs-rot failure. Generate it from doc comments.
- **Single-language examples.** Forces every non-matching reader to translate; multi-language synced tabs are nearly free in Starlight and are the industry norm (Stripe, Temporal, gRPC).
- **Hosting the docs in the app repo.** The SDK and the website both live in the proto repo; the docs belong with what they document. (This ADR *record* lives with the ADR series in the app repo, per the ADR-020 precedent; the *implementation* is a proto-repo PR.)
- **Coupling docs to a blocking/versioned release.** Unnecessary: an off-commit pin plus a version banner is sufficient while the contract is in flux.

## Non-goals

- Not rewriting the protocol (Explanation) docs — they stay; they are augmented with SDK asides.
- Not documenting a high-tier "agent SDK" (`fetch`) — that follows if/when ADR-020 §2's high tier is built.
- Not a semver release or registry publication of the docs — those follow SDK stabilization.
- Not exposing the L0/L1/L2 layer names as user vocabulary (§4).

## References

- **ADR-020** — FORA SDK: Layered Protocol Libraries (the SDK this documents; §2 tiers, §4 verification, §6 off-commit consumption).
- **Diátaxis** — https://diataxis.fr — the four-quadrant documentation framework this ADR organizes around.
- **Exemplars** — Stripe (persistent multi-language selector, guides + generated reference, progressive disclosure); Temporal (neutral core + per-language SDK guide split); gRPC.io / Protocol Buffers (parallel per-language guides over an IDL); Rust (the Book + docs.rs — narrative cross-linked into generated reference).
- **Starlight capabilities** — `<Tabs syncKey>` (synced code tabs), `starlight-typedoc` (TS API pages), `starlight-links-validator` (already installed — CI-gated cross-links).
- **First slice** — SDK sidebar group + "Verify a received offer" multi-language guide + one bidirectional cross-link (§8).

---

## Addendum (2026-09-11) — the SDK ships releases, so the docs track a version

**Context.** This ADR was written while the SDK was consumed off-commit, and it
says so in four places: §3's sidebar sketch describes the Install & setup page as
"the off-commit git-pin snippet each"; §7 decides that the version banner tracks a
commit; the Rejected-alternatives entry calls a versioned release unnecessary
"while the contract is in flux"; and the References entry cites ADR-020 §6 for
off-commit consumption.

ADR-020's own amendment records what changed: the `v1.0.3` release tags the
protocol repository and publishes `@fora-protocol/sdk` on npm and
`fora-protocol-sdk` and `fora-protocol` on PyPI. A consumer installs a release.

**What this changes.** §7's versioning decision is the one that was load-bearing.
It reads "Since the SDK is consumed off-commit (ADR-020 §6), the docs track that
commit; when it semver-stabilizes, the docs version with it." The premise is gone
and the conditional has fired, so the decision is now the second half of its own
sentence: the docs track a version.

The website implements that. A build-time plugin resolves the SDK version from the
protocol repository's root `package.json` — the same file the release procedure
advances — and substitutes it into the install instructions, so the site follows a
release rather than restating one. It also fails the build when a version written
by hand next to an SDK package name has gone stale, which is what makes "one
source" hold rather than merely be intended. The check is scoped to the SDK
package names on purpose: the site also cites CoMP, RSL, C2PA and the protocol
version, and a rule over every `X.Y.Z` would flag those too.

§3's sidebar sketch describes the Install & setup page as carrying an off-commit
git-pin snippet per language. That page now carries a release install per
language, in the forms ADR-020's addendum lists; the sketch describes the page as
it was planned, not as it stands. The Rejected-alternatives entry stands as a record of
the sequencing decision — coupling the docs to a release was premature when this
ADR was written, and the release has since happened — the same way ADR-020 treats
its own entry on publication.

**What it does not change.** Everything else in this ADR is independent of how a
consumer names the dependency: the Diátaxis quadrant split, the sidebar shape, the
synced multi-language tabs, the generated reference, the cross-link contract, and
the incremental-slice plan. Those were never arguments about pinning.
