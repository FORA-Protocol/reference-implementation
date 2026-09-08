# ADR-025 — The Manifest Version Is a Separate Namespace from the Protocol Version

**Status:** Accepted (2026-09-03)
**Relates to:** ADR-020 (FORA SDK layered libraries) — the SDK resolvers that read the
manifest are where this decision is enforced.

---

## Context

Every FORA message carries a `ver` string at field 1. The `/.well-known/fora.json`
document is one of those messages, `WellKnownManifest` in the pinned protocol module
`github.com/FORA-Protocol/protocol`, so it carries `ver` too. Both read `"1.0"` today,
and until this decision nothing said whether they were the same number or two numbers
that happened to agree.

The two fields are governed by opposite rules. The RPC envelope `ver` is advisory: a
receiver need not check it, may refuse an unrecognised major version, and must never
refuse a minor one. The manifest `ver` is stated as a hard gate: it MUST equal `"1.0"`
and consumers reject anything else. The protocol source explains the difference by
calling the manifest `ver` "the schema version of the manifest document, a namespace
separate from the RPC envelope". That sentence was written to reconcile the two rules
after the envelope rule was made advisory. It records the separation without recording
a reason for it, and it is not applied anywhere: the Go, Python and TypeScript SDK
resolvers decode the manifest into the endpoint and the key set and discard `ver`
unread, so a manifest served with `"ver": "9.9"`, or with no `ver` at all, is accepted.
The one place the field is checked is this implementation's embedded JSON Schema,
`internal/forawellknown/schema/fora-well-known.json`, which requires the exact string
`"1.0"` and so refuses a `"1.1"` manifest that the protocol text would accept.

Two facts about the manifest make it unlike an RPC message and are the reason the
question needs its own answer.

- **The manifest has no versioned route.** An RPC is addressed to
  `/fora.v1.ExchangeService/Query`; the package name in the path carries the major
  version, so a 2.0 service is reachable at a different path from a 1.0 service and a
  mismatched request fails before any message is parsed. The manifest is a static
  document at one fixed path, `/.well-known/fora.json`, for every version. The only
  place a reader can learn which layout it is holding is inside the document.
- **The manifest is read by parties that never speak an RPC.** A Broker reads it to
  find an Exchange endpoint, an agent reads it to find a Broker, a publisher reads it
  to find where to push a catalog. But so does an edge worker checking a publisher's
  authorised exchanges, a monitoring probe, a directory crawler, or a publisher's own
  configuration tooling. Those readers hold a manifest parser and nothing else. If the
  manifest version moved every time an RPC message changed, each of them would be
  told to upgrade for changes that cannot affect them.

The manifest is also read before any signature is checked, and a misread manifest is
not a harmless failure: the `endpoint` it names is where a signed request is sent, and
the keys it references are what an offer signature is verified against. A reader that
processes a layout it does not understand can be redirected, or verify against the
wrong key. That is why the manifest rule is a gate rather than advisory, and why the
gate must fail closed.

## Decision

### D1 — The manifest version and the protocol version are two namespaces

`WellKnownManifest.ver` is the version of the manifest document layout. It is not the
protocol version stamped on RPC messages, and it is not derived from it. The two
values are independent constants: the protocol version is the SDK's `ProtocolVersion`;
the manifest version is a separate constant that this implementation holds as
`forawellknown.Version` and that the SDK must export under its own name in all three
languages, pinned to the shared wire-constants vectors beside `ProtocolVersion`.

Both read `"1.0"` today. That is a coincidence of history, not a rule, and no code may
stamp one from the other.

### D2 — A manifest change moves both numbers; a protocol change moves only its own

The manifest is part of the protocol. Any change to the manifest layout is therefore
a protocol change and bumps the protocol version by the ordinary rule for that change,
minor if additive and major otherwise. The same change bumps the manifest version by
the same rule.

The reverse does not hold. A protocol change that leaves the manifest layout untouched
— a new field on `ResourceQuery`, a new denial reason, a new service — bumps the
protocol version and leaves the manifest version where it is. The manifest version
therefore moves no faster than the protocol version, and usually slower.

The consequence for a reader that only parses manifests is the property this decision
exists to give it: it upgrades when the manifest changes, and at no other time.

### D3 — Consumers accept a recognised major version and refuse everything else

A consumer of `/.well-known/fora.json` reads `ver` before it acts on any other member
of the document, splits it as `MAJOR.MINOR`, and compares the major against the majors
it implements. A recognised major is accepted whatever the minor, because a minor
revision of the manifest is additive by definition and a reader ignores members it
does not know. An unrecognised major is refused, and the refusal is reported as a
distinct failure naming the version found, so an operator can tell a version mismatch
from a network failure or a malformed document.

An absent `ver` is refused. The field is required by the wire shape, and a document
with no version is one whose layout the reader cannot classify; the gate fails closed
rather than guessing. This is a change in behaviour for the SDK resolvers, which
accept such a manifest today.

Exact matching is not permitted. A validator that requires the literal `"1.0"` refuses
a `"1.1"` manifest, which is the minor-revision case D2 promises will keep working. The
embedded schema in this implementation drops the constant and types `ver` as a plain
required string, keeping the schema to wire shape; the major-version check runs in
code, through the same SDK rule the resolvers apply, so there is one implementation of
the rule rather than a schema pattern and a code path that could drift apart.

### D4 — The rule is enforced in the SDK endpoint resolvers, identically in all three languages

The SDK's well-known endpoint resolver — the face that fetches `/.well-known/fora.json`
to learn where an Exchange's service is — is where the check runs, before the
endpoint is read. Go, Python and TypeScript apply the same rule, refuse the same
inputs, and are covered by the same conformance vectors: a manifest with a recognised
major and a higher minor is accepted; a manifest with an unrecognised major is refused;
a manifest with no `ver` is refused. A refusal is a verdict: it is never cached, and the
client tier classifies it as not-sent, so a caller does not retry it.

The well-known key resolver is deliberately outside this rule. It reads a JWKS-shaped
document at an operator-chosen URL, not a manifest, and that document carries no
manifest version; a gate there would refuse every plain key set. Identity keys in this
implementation are served from the Web Bot Auth directory, which has its own document
and its own validator.

### D5 — The protocol text states this decision and the reason

The comment on `WellKnownManifest.ver` in the protocol module, and the manifest
reference pages on the protocol site, state that the manifest version is a separate
namespace, why it is separate, and the recognised-major rule. The "Protocol version"
section at the head of the proto file keeps its statement that the two are not coupled
and adds the direction of coupling from D2.

## Consequences

**Positive.**

- A manifest-only reader has a version to check, a rule to check it by, and a promise
  that it will not be asked to upgrade for changes that do not reach it.
- The gate the protocol text has always described is now applied. A manifest served at
  the wrong major, or with no version, is refused before its endpoint or keys are used.
- The exact-match validator in this implementation stops refusing the minor revisions
  that the protocol text allows.
- The three SDK ports agree, and the agreement is pinned by shared vectors rather than
  by reading the code.

**Negative.**

- A manifest in the wild that omits `ver` is accepted today and will begin failing.
  The field has been required by the wire shape since the unified manifest was
  introduced, so a conforming producer already stamps it, but a hand-written manifest
  might not.
- There are now two version constants to carry and two bump procedures to follow. A
  manifest layout change must bump both, and a reviewer has to check that it did.
- The SDK gains a new exported constant, a new failure kind, and conformance cases in
  three languages. This is the enforcement work the earlier text described and never
  did.

## Non-goals

- **The RPC envelope `ver` and its advisory rule.** Unchanged. Whether a per-message
  version is still worth carrying when the route already names the major is a separate
  question and is not decided here.
- **`WellKnownManifest.protocol_versions_supported`.** The list of RPC versions an
  Exchange accepts is a different thing from either version this decision names, and
  its semantics are unchanged. Nothing in this implementation publishes or reads it
  today; that gap is not closed here.
- **A bump procedure for either version.** Who decides a bump, how it is tagged, and
  what the changelog records are not settled by this decision.

## Rejected alternatives

**One version.** Make `WellKnownManifest.ver` the protocol version, stamped from
`ProtocolVersion`, under the same advisory rule as every other message. This is the
simplest model to explain, and the history supports it: the two fields were commented
identically until the separation sentence was added. It was rejected because it ties
manifest readers to the RPC release train. Every consumer that parses only the
manifest would see the number move on every protocol change, and would have no way to
tell from the number whether the manifest it holds is the one it understands.

**Soften the text.** Leave the resolvers as they are, change the MUST-equal sentence to
describe what consumers actually do, and record that enforcing the document version
belongs to the producer and its schema validator. This keeps behaviour unchanged and
makes the text true. It was rejected because it leaves the manifest with no gate on
the consumer side at all: a reader that fetches a document at a fixed, unversioned path,
before any signature is checked, and then trusts its `endpoint` and keys, must be able
to refuse a layout it does not recognise. Moving that check to the producer protects
nobody, because the producer is the party the reader does not yet trust.

**Exact-match enforcement.** Keep the literal `"1.0"` constant and require it
everywhere. It is what this implementation's schema does today. It was rejected because
it refuses additive minor revisions, so every minor change to the manifest would break
every existing reader, which is the outcome a minor version exists to avoid.
