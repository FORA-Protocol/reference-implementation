# FORA Demo Catalog Manifest

This manifest describes the **demo catalog**. Every resource below is produced
from the three JSONL feeds in this directory (`philosophy.jsonl`, `music.jsonl`,
`sfx.jsonl`) via the real ingestion path (`ParseJSONL` → `mapRecord`, which
canonicalizes each term through the SDK's `helpers.NormalizeLicenseTerm` →
`CatalogService.PushResources`, where the Exchange runs the SDK's
`helpers.ValidateLicenseTerm`). All 49 records / 69 terms parse, map, and
validate with zero warnings and zero hard rejects against the registered FORA
vocab pinned in `go.mod`. `internal/catalogfeed/demo_feeds_test.go`
also checks that every record's path names a committed file under
`deploy/content/demo/`, so a feed entry the origin would answer 404 for fails
locally rather than at the next deployment.

The Docker e2e suite owns its own feeds under
`tests/e2e/harness/fixtures/catalog/`, so the two sets are free to diverge: a
demo prices everything in one currency and tells a tidy story, while a test
suite wants the awkward combinations. That is the demo's choice, not a platform
limit: an Exchange bills in as many currencies as it is configured with.
The Exchange music integration test additionally reads the three-term MP3 example
from this demo feed and checks signed public-RPC discovery for the same agent in
both personal and broadcast contexts.

Content files live under `deploy/content/demo/<publisher>/...` at paths that
match the `path` column below (the Phase-2 origin container serves that tree).

## Publishers

| Publisher | Domain | Content base | Records |
|-----------|--------|--------------|---------|
| Stoa Press | `demo.fora-protocol.org` | `deploy/content/demo/stoa-press/articles/{philosophers/{,html,pdf,md},publishers/md}/` | 27 |
| Harmonia Records | `music.demo.fora-protocol.org` | `deploy/content/demo/harmonia-records/{lyrics,tracks}/` | 16 |
| FoleyWorks | `sfx.demo.fora-protocol.org` | `deploy/content/demo/foleyworks/sfx/` | 6 |

The three domains above are how the feeds are written. A deployed stack has
one publisher tenant and one domain: `deploy/terraform/scripts/seed-staging.sh`
rewrites every host, the `music.` and `sfx.` prefixes included, to the
stack's publisher domain and pushes the three feeds as one submission.
The license URIs are namespaced per brand. The playable music tracks use
`/licensing/harmonia/music-v0.1.txt`, which the origin serves and the demo edge
leaves outside its licensed paths, so an agent can read the terms before it
buys. The legacy examples use other `/licensing/harmonia/...` and
`/licensing/foley/...` URIs, which the origin does not serve.

## Retrieval canary

`/articles/philosophers/thales-of-miletus.txt` embeds the retrieval-proof
canary the e2e/demo checks. The retrieved body MUST contain, verbatim:

- token `FORA-DEMO-CANARY-8FK3J2-0418`
- Greek phrase `καῦμα διὰ ῥάμπης`
- marker `entry XI.47-bis`

Thales is deliberately a FLAT-priced (`9.99 EUR`) term with an attribution +
backlink obligation, so the canary path also exercises a priced fetch with an
obligation.

## Resource → terms (philosophy.jsonl · Stoa Press · `demo.fora-protocol.org`)

| Domain | Path | Content-Type | #Terms | Terms summary |
|--------|------|--------------|--------|---------------|
| demo.fora-protocol.org | /articles/philosophers/socrates.txt | text/plain | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; user[academic]; geo[EU] |
| demo.fora-protocol.org | /articles/philosophers/plato.txt | text/plain | 1 | FREE; fn[ai-input+ai-index+search]; PROHIBIT[ai-train]; user[academic]; geo[EU,EEA] |
| demo.fora-protocol.org | /articles/philosophers/aristotle.txt | text/plain | 1 | FLAT 7.50 EUR; fn[ai-input+ai-index]; PROHIBIT[ai-train]; geo[EU]; obl:attribution(on_use, backlink) |
| demo.fora-protocol.org | /articles/philosophers/epicurus.txt | text/plain | 1 | PER_UNIT 0.0001/characters EUR; fn[ai-input]; PROHIBIT[ai-train]; user[individual]; geo[US,GB] |
| demo.fora-protocol.org | /articles/philosophers/heraclitus.txt | text/plain | 1 | FREE; fn[ai-input+search+research]; PROHIBIT[ai-train]; geo[EU] |
| demo.fora-protocol.org | /articles/philosophers/parmenides.txt | text/plain | 1 | reference_only / subscription (scopes: subscription:premium); pricing free EUR; license uri required |
| demo.fora-protocol.org | /articles/philosophers/democritus.txt | text/plain | 1 | FLAT 6.00 EUR; fn[ai-input+ai-index]; geo[GB]; obl:attribution(on_use) + obl:notice(on_distribution) |
| demo.fora-protocol.org | /articles/philosophers/pythagoras.txt | text/plain | 1 | PER_UNIT 0.00002/tokens EUR; fn[ai-input+ai-index]; user[commercial_entity]; geo[GB,EU]; quota 2000000 tokens/daily; obl:contribution(on_use, royalty) |
| demo.fora-protocol.org | /articles/philosophers/zeno-of-citium.txt | text/plain | 1 | FREE; fn[ai-input+research+search]; PROHIBIT[ai-train]; user[academic]; geo[EU] |
| demo.fora-protocol.org | /articles/philosophers/thales-of-miletus.txt | text/plain | 1 | **CANARY** · FLAT 9.99 EUR; fn[ai-input+ai-index+search]; PROHIBIT[ai-train]; geo[EU]; obl:attribution(on_use, backlink) |
| demo.fora-protocol.org | /articles/philosophers/html/anaximander.html | text/html | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; geo[EU] |
| demo.fora-protocol.org | /articles/philosophers/html/anaxagoras.html | text/html | 1 | FLAT 5.50 EUR; fn[ai-input+ai-index]; PROHIBIT[ai-train]; geo[EU]; obl:attribution(on_use, backlink) |
| demo.fora-protocol.org | /articles/philosophers/html/empedocles.html | text/html | 1 | PER_UNIT 0.02/accesses EUR; fn[ai-input+ai-index]; user[commercial_entity]; geo[EU,GB]; quota 5000 accesses/daily |
| demo.fora-protocol.org | /articles/philosophers/html/diogenes-of-sinope.html | text/html | 1 | FREE; fn[ai-input+ai-index+search]; PROHIBIT[ai-train]; geo[EU,EEA] |
| demo.fora-protocol.org | /articles/philosophers/pdf/xenophanes.pdf | application/pdf | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; geo[EU] |
| demo.fora-protocol.org | /articles/philosophers/pdf/chrysippus.pdf | application/pdf | 1 | FLAT 8.00 EUR; fn[ai-input+ai-index]; geo[EU]; obl:attribution(on_use, backlink) |
| demo.fora-protocol.org | /articles/philosophers/md/anaximenes.md | text/markdown | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; geo[EU] |
| demo.fora-protocol.org | /articles/philosophers/md/zeno-of-elea.md | text/markdown | 1 | FLAT 4.50 EUR; fn[ai-input+ai-index]; PROHIBIT[ai-train]; geo[EU]; obl:attribution(on_use, backlink) |
| demo.fora-protocol.org | /articles/philosophers/md/protagoras.md | text/markdown | 1 | PER_UNIT 0.03/accesses EUR; fn[ai-input+ai-index]; user[commercial_entity]; geo[EU,GB]; quota 5000 accesses/daily |
| demo.fora-protocol.org | /articles/philosophers/md/pyrrho.md | text/markdown | 1 | FREE; fn[ai-input+ai-index+search]; PROHIBIT[ai-train]; user[academic]; geo[EU,EEA] |
| demo.fora-protocol.org | /articles/philosophers/md/epictetus.md | text/markdown | 1 | PER_UNIT 0.00003/tokens EUR; fn[ai-input+ai-index]; user[commercial_entity]; geo[EU]; quota 1000000 tokens/daily; obl:contribution(on_use, royalty) |
| demo.fora-protocol.org | /articles/philosophers/md/plotinus.md | text/markdown | 1 | FLAT 6.50 EUR; fn[ai-input+ai-index+research]; geo[EU,GB]; obl:notice(on_distribution) |
| demo.fora-protocol.org | /articles/philosophers/md/leucippus.md | text/markdown | 1 | FREE; fn[ai-input+search+research]; PROHIBIT[ai-train]; geo[EU,GB] |
| demo.fora-protocol.org | /articles/philosophers/md/marcus-aurelius.md | text/markdown | 1 | FLAT 3.00 EUR; fn[ai-input+ai-index+search]; geo[EU,US,GB]; obl:attribution(on_use, backlink) |
| demo.fora-protocol.org | /articles/publishers/md/what-publishers-get-paid-ai-crawlers-revenue.md | text/markdown | 1 | FLAT 0.05 EUR; fn[ai-input+ai-index+search]; PROHIBIT[ai-train] |
| demo.fora-protocol.org | /articles/publishers/md/publishers-pay-per-crawl-ai-licensing.md | text/markdown | 1 | FLAT 0.03 EUR; fn[ai-input+ai-index+search]; PROHIBIT[ai-train] |
| demo.fora-protocol.org | /articles/publishers/md/publisher-ai-content-licensing-terms.md | text/markdown | 1 | FLAT 0.02 EUR; fn[ai-input+ai-index+search]; PROHIBIT[ai-train] |

The Stoa Press feed is the one publisher with four media types. The ten
`.txt` essays are served as `text/plain`, the four under `html/` as
`text/html` and the two under `pdf/` as `application/pdf` (nginx's standard
MIME table, keyed on the extension). The eight under `philosophers/md/` are served as
`text/markdown`: that table has no entry for `.md`, so
`deploy/publisher/nginx.conf` sets the type for Markdown files under
`/articles/` explicitly. The landing page lists the Markdown, HTML and PDF groups; the
`.txt` essays are served and in the feed but not listed there. The three
under `publishers/md/` are the publisher-perspective articles (what a
publisher earns from AI crawlers, pay per crawl, licensing terms): flat
0.02–0.05 EUR, `ai-input`, `ai-index` and `search` permitted, `ai-train`
prohibited, no geo or user-type restriction and no obligation, so the prose
in each article matches the offer an agent is shown. The landing page lists
them in their own section above the philosophy essays. The PDFs are generated from the plain-text sources under
`pdf-src/` in this directory; see the README there.

## Resource → terms (music.jsonl · Harmonia Records · `music.demo.fora-protocol.org`)

| Domain | Path | Content-Type | #Terms | Terms summary |
|--------|------|--------------|--------|---------------|
| music.demo.fora-protocol.org | /lyrics/midnight-harbour.txt | text/plain | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; user[academic]; geo[EU,GB] |
| music.demo.fora-protocol.org | /lyrics/paper-satellites.txt | text/plain | 1 | FLAT 3.50 EUR; fn[ai-input+ai-index]; PROHIBIT[ai-train]; geo[US]; obl:attribution(on_use, backlink) |
| music.demo.fora-protocol.org | /lyrics/ferrograph-blues.txt | text/plain | 1 | reference_only / subscription (scopes: subscription:catalog); pricing free EUR |
| music.demo.fora-protocol.org | /tracks/aurora-drift.json | application/json | 1 | PER_UNIT 0.002/streams EUR; fn[ai-input+sync+stream]; PROHIBIT[ai-train]; user[commercial_entity]; geo[EU,US,GB]; quota 50000 accesses/monthly; obl:contribution(on_use, royalty) |
| music.demo.fora-protocol.org | /tracks/copper-mile.json | application/json | 1 | FLAT 4.00 EUR; fn[ai-input+ai-index+sync]; user[non_profit]; geo[EU]; obl:notice(on_distribution) |
| music.demo.fora-protocol.org | /tracks/static-garden.json | application/json | 1 | PER_UNIT 0.01/minutes EUR; fn[ai-input+stream+sync]; PROHIBIT[ai-train]; user[commercial_entity]; geo[GB]; quota 1000 accesses/daily |
| music.demo.fora-protocol.org | /tracks/mesmerizing-galaxy-loop.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/lord-of-the-rangs.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/adeste-fideles-shorter.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/strength-of-the-titans.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/ancient-rite.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/evening.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/almost-bliss.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/americana.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/hiding-your-reality.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |
| music.demo.fora-protocol.org | /tracks/brain-dance.mp3 | audio/mpeg | 3 | reference_only; FLAT 29 / 450 / 1200 EUR; Online Creator / Broadcast / Advertising; metering NONE; one production each; worldwide |

### Playable music tracks

The ten MP3 tracks are by Kevin MacLeod (incompetech.com), not Harmonia, and
are licensed under CC BY 4.0. Each file is a modified copy of the incompetech
download: trimmed to the first 60 seconds and re-encoded at 96 kbps to keep the
files small. Each track's source, ISRC, checksum and modification statement are
in the `<track>-provenance.txt` file beside its MP3 under
`deploy/content/demo/harmonia-records/tracks/`.

The three terms are advisory summaries of the three alternative sections in
`/licensing/harmonia/music-v0.1.txt`, which contains the complete illustrative
texts. The selected term's notice and flat price identify its section; the
sections are not cumulative. Every term carries the same combined-document
digest. This uses the feed's existing record-level license field without a
schema extension. The document and MP3 hashes are checked against their bytes;
any change to either artifact requires updating its digest in the feed.
The document names no single track, so every playable track references it.

`vendor:productions` is the supported namespaced quota metric, with limit 1
and window total. It is a simulated contractual allowance, not an Exchange
counter or a FORA usage-report requirement. No entitlement scopes hide these
alternatives; the agent evaluates customer and intended use itself. Broadcast
and Advertising are purpose-specific; paid advertising is not permitted by
the personal or broadcast offer. All prices and restrictions are fictional
and do not supersede the source audio's CC BY permissions.

The `music.<field>` keys of `ext` are what the Broker's catalog search matches a
free-text request against: `genre`, `mood`, `instruments`, `suitable_for`
(the `use` facet), `energy` (low, medium or high), `vocals`, `bpm` (which
also gives a slow, medium or fast tempo) and `duration_seconds`. A track
without a field fails any exclusion or range on it, so every playable track
carries all of them. Genre, mood, BPM and instruments follow the incompetech
listing of each track. Energy and `suitable_for` are editorial choices.
`vocals` was checked by listening; none of the tracks has singing, including
Ancient Rite, whose listing names a choir.

The license document is outside the demo edge's licensed paths, so an agent
can read it before purchase even when its User-Agent is classified as an AI
bot. Local tests check the document's bytes and that the edge lets such a
read through; they do not prove the live deployment or audio delivery.

## Resource → terms (sfx.jsonl · FoleyWorks · `sfx.demo.fora-protocol.org`)

| Domain | Path | Content-Type | #Terms | Terms summary |
|--------|------|--------------|--------|---------------|
| sfx.demo.fora-protocol.org | /sfx/rain-on-tin-roof.json | application/json | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; user[individual]; geo[EU,US,GB]; obl:attribution(on_use, backlink) |
| sfx.demo.fora-protocol.org | /sfx/wooden-door-creak.json | application/json | 1 | PER_UNIT 0.25/accesses EUR; fn[ai-input+sync+reproduce]; user[commercial_entity]; geo[EU,US,GB]; quota 500 accesses/daily; obl:contribution(on_distribution, royalty) |
| sfx.demo.fora-protocol.org | /sfx/sci-fi-door-whoosh.json | application/json | 1 | FLAT 12.00 EUR; fn[ai-input+sync+modify+reproduce]; PROHIBIT[ai-train]; geo[EU,US,GB]; obl:attribution(on_use) |
| sfx.demo.fora-protocol.org | /sfx/footsteps-gravel.json | application/json | 1 | reference_only / subscription (scopes: subscription:library); pricing free EUR |
| sfx.demo.fora-protocol.org | /sfx/thunder-rumble.json | application/json | 1 | FREE; fn[ai-input+search]; PROHIBIT[ai-train]; user[academic]; geo[EU] |
| sfx.demo.fora-protocol.org | /sfx/typewriter-keystroke.json | application/json | 1 | PER_UNIT 0.10/accesses EUR; fn[ai-input+sync+reproduce+modify]; user[commercial_entity]; geo[US]; quota 250 accesses/hourly |

## Variety matrix (coverage proof)

| Axis | Value | Resources that cover it |
|------|-------|-------------------------|
| Pricing | FREE | socrates, heraclitus, plato, zeno-of-citium, anaximander, diogenes, xenophanes, anaximenes, pyrrho, leucippus, midnight-harbour, rain-on-tin-roof, thunder-rumble |
| Pricing | FLAT | aristotle, democritus, thales (canary), anaxagoras, chrysippus, zeno-of-elea, plotinus, marcus-aurelius, the three publisher-perspective articles, paper-satellites, copper-mile, sci-fi-door-whoosh |
| Pricing | PER_UNIT | epicurus, pythagoras, empedocles, protagoras, epictetus, aurora-drift, static-garden, wooden-door-creak, typewriter-keystroke |
| Pricing | reference_only / subscription | parmenides (subscription:premium), ferrograph-blues (subscription:catalog), footsteps-gravel (subscription:library) |
| Currency | EUR | every term; the demo catalog is EUR-only |
| Restriction: geo | EU-only | socrates, aristotle, heraclitus, zeno-of-citium, anaximander, anaxagoras, xenophanes, chrysippus, anaximenes, zeno-of-elea, epictetus, copper-mile, thunder-rumble |
| Restriction: geo | US-only / US-incl | epicurus(US,GB), marcus-aurelius(EU,US,GB), paper-satellites(US), typewriter-keystroke(US) |
| Restriction: geo | EEA | plato, diogenes, pyrrho (geo[EU,EEA]) |
| Restriction: user_type | academic | socrates, plato, zeno-of-citium, pyrrho, midnight-harbour, thunder-rumble |
| Restriction: user_type | commercial_entity | pythagoras, empedocles, protagoras, epictetus, aurora-drift, static-garden, wooden-door-creak, typewriter-keystroke |
| Restriction: user_type | individual | epicurus, rain-on-tin-roof |
| Restriction: user_type | non_profit | copper-mile |
| Function | ai-input | every enumerated term |
| Function | ai-index | plato, aristotle, democritus, pythagoras, anaxagoras, empedocles, diogenes, chrysippus, zeno-of-elea, protagoras, pyrrho, epictetus, plotinus, marcus-aurelius, the three publisher-perspective articles, copper-mile, paper-satellites |
| Function | search | socrates, plato, heraclitus, zeno-of-citium, thales, anaximander, diogenes, xenophanes, anaximenes, pyrrho, leucippus, marcus-aurelius, the three publisher-perspective articles, midnight-harbour, rain-on-tin-roof, thunder-rumble |
| Function | research | heraclitus, zeno-of-citium, plotinus, leucippus |
| Function | ai-train PROHIBITED | socrates, plato, aristotle, epicurus, heraclitus, zeno-of-citium, thales, anaximander, anaxagoras, diogenes, xenophanes, anaximenes, zeno-of-elea, pyrrho, leucippus, the three publisher-perspective articles, midnight-harbour, paper-satellites, aurora-drift, static-garden, rain-on-tin-roof, sci-fi-door-whoosh, thunder-rumble |
| Function | synthesis/media (sync, stream) | sync: aurora-drift, copper-mile, static-garden, wooden-door-creak, sci-fi-door-whoosh, typewriter-keystroke; stream: aurora-drift, static-garden |
| Obligation | attribution (with backlink detail) | aristotle, thales, anaxagoras, chrysippus, zeno-of-elea, marcus-aurelius, paper-satellites, rain-on-tin-roof, sci-fi-door-whoosh, democritus |
| Obligation | reporting / royalty (contribution + notice) | contribution: pythagoras, epictetus, aurora-drift, wooden-door-creak; notice: democritus, copper-mile, plotinus |
| Quota | daily access cap on PER_UNIT | empedocles, protagoras, static-garden, wooden-door-creak (also pythagoras and epictetus tokens/daily) |
| Quota | other windows | monthly: aurora-drift; hourly: typewriter-keystroke |
| Terms per resource | one or three | The ten playable music tracks have three alternatives each. The other 39 records retain one term. The Exchange signs one offer per eligible term. |
| Media type | text/plain, text/html, application/pdf, text/markdown, application/json, audio/mpeg | Stoa Press `.txt` (10), `html/` (4), `pdf/` (2), `philosophers/md/` (8), `publishers/md/` (3); legacy music and sfx are text/plain and application/json; the playable music tracks are audio/mpeg |

## Registered-vocab note

The playable music tracks use the supported namespaced quota metric
`vendor:productions`; all other `functions`, `user_types`, `geos`, pricing,
quota and obligation tokens are registered in the pinned protocol module.
A clean run through the SDK's
`helpers.ValidateLicenseTerm`, the ingest-tier check the Exchange runs on every
pushed term, produced zero lint warnings and zero hard rejects across all 69
terms (`internal/catalogfeed/demo_feeds_test.go` asserts this).

Subscription pricing is modeled as `reference_only` semantics with a
`subscription:*` scope and `free` pricing (the proto's closed pricing model set
is FREE/FLAT/PER_UNIT; "subscription" is a scope, not a pricing model).
