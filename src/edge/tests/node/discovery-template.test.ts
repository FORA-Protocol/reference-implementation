import { readFileSync } from 'node:fs';
import { URL, fileURLToPath } from 'node:url';

import { afterEach, describe, expect, it, vi } from 'vitest';

import { createApp } from '../../src/app.js';
import { buildDeps, originModeConfigured, parseEnv } from '../../src/config.js';
import worker from '../../src/entries/cloudflare.js';
import { readFastlyEnv } from '../../src/entries/fastly.js';
import { WBA_PATH, WELL_KNOWN_PATH } from '../../src/types.js';
import { assertValidManifest, assertValidWba } from '../helpers/wellknown-schema.js';

// The files under deploy/publisher-wellknown/ are the reference copies an
// operator compares their own deployment against, and they are two different
// kinds of file. fora.json.example is the discovery document the publisher
// serves from its own origin; the Worker forwards that address there and
// answers with whatever the origin sends. The key directory is the one the
// Worker serves itself, built from WBA_KEYS_JSON in template-env.json, the
// checked-in variable values that folder hands operators.
//
// So this suite drives template-env.json through the production path: parseEnv,
// then buildDeps, then a request to each address through the Worker's own
// routes. The key directory's response body must be deep-equal to the template
// on disk. Schema validation alone would not do it: a template can satisfy the
// schema perfectly and still describe a document this Worker never emits. The
// discovery document must come back as the origin's bytes, unchanged: the
// Worker adds nothing to it and takes nothing away.
//
// EXCHANGE_URL and EXCHANGE_WBA_URL reach neither document. EXCHANGE_URL becomes
// deps.exchangeUrl and EXCHANGE_WBA_URL goes to createKeyCache (src/config.ts);
// both are in the fixture only because EnvSchema requires them. ORIGIN_URL is
// where the discovery document is read from, and the last describe block pins
// that the fixture names an origin mode every runtime can use.
//
// The payee placeholder's SHAPE and the absent revocation_url are pinned on the
// Go side, which reads the same two files through the CONSUMER path
// (internal/forawellknown/template_test.go). Asserting either here as well would
// be one property checked twice in two languages under two rules that can
// disagree, which is how the payee guard here and the one there came to accept
// different templates.
//
// What stays below is what nothing else covers: ext's key set, and the absent
// kid. The canonical schema constrains neither — ext is a protobuf Struct, and
// the schema permits extra properties — and ResourceOwnerID reads one key and
// ignores the rest, so an extra key would reach the served document with nothing
// rejecting it.

function templatePath(name: string): string {
  return fileURLToPath(new URL(`../../../../deploy/publisher-wellknown/${name}`, import.meta.url));
}

function readTemplate(name: string): unknown {
  return JSON.parse(readFileSync(templatePath(name), 'utf8'));
}

// Typed once here because two of the assertions below index it by name, which
// does not compile against readTemplate's `unknown`. The cast is not taken on
// trust: EnvSchema declares every field a string, so parseEnv one line down
// throws if the fixture is not a flat map of strings.
const templateEnv = readTemplate('template-env.json') as Record<string, string>;

// The env the Cloudflare entry hands its fetch handler: Partial<EdgeEnv>.
type WorkerEnv = Parameters<typeof worker.fetch>[1];

// The discovery document as the publisher's origin serves it: the template's
// own bytes, with the origin's media type.
const manifestBytes = readFileSync(templatePath('fora.json.example'), 'utf8');

function originServingTheTemplate(seen: string[]): typeof fetch {
  return (async (input: RequestInfo | URL) => {
    seen.push((input as Request).url);
    return new Response(manifestBytes, { headers: { 'content-type': 'application/json' } });
  }) as unknown as typeof fetch;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('deploy/publisher-wellknown templates: the key directory the Worker serves, the discovery document it passes through', () => {
  const manifestTemplate = readTemplate('fora.json.example');
  const wbaTemplate = readTemplate('http-message-signatures-directory.example');

  it('fora.json.example satisfies the canonical manifest schema', () => {
    assertValidManifest(manifestTemplate);
  });

  it('http-message-signatures-directory.example satisfies the canonical WBA schema', () => {
    assertValidWba(wbaTemplate);
  });

  it("the Worker answers the discovery address with the origin's fora.json.example, unchanged", async () => {
    const seen: string[] = [];
    const app = createApp({
      ...buildDeps(parseEnv(templateEnv)),
      fetcher: originServingTheTemplate(seen),
    });
    const res = await app.request(WELL_KNOWN_PATH, { headers: { 'user-agent': 'GPTBot/1.0' } });
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(manifestBytes);
    expect(seen).toEqual([`${templateEnv.ORIGIN_URL}${WELL_KNOWN_PATH}`]);
  });

  it('the key-directory route serves the directory template under the Web Bot Auth media type', async () => {
    const app = createApp(buildDeps(parseEnv(templateEnv)));
    const res = await app.request(WBA_PATH);
    expect(res.status).toBe(200);
    // Written out rather than compared against WBA_DIRECTORY_CONTENT_TYPE. The
    // route sets the header from that same constant, so comparing the two would
    // hold whatever the constant said and the assertion could never fail. This
    // string is the wire contract an off-the-shelf Web Bot Auth verifier reads
    // the directory by, and the README tells operators to curl for it.
    expect(res.headers.get('content-type')).toBe(
      'application/http-message-signatures-directory+json',
    );
    expect(await res.json()).toEqual(wbaTemplate);
  });

  it('the template attests one payee and nothing else in ext', () => {
    // The Go suite owns the placeholder's shape — it must be a value no operator
    // could mistake for a real id. This owns the key SET, which nothing else
    // checks: a second key in ext is carried to every reader of the document and
    // then silently ignored by every consumer.
    const entry = (manifestTemplate as { exchanges: { ext?: Record<string, unknown> }[] })
      .exchanges[0];
    expect(Object.keys(entry?.ext ?? {})).toEqual(['resource_owner_id']);
  });

  it('the template directory key carries no kid', () => {
    // Web Bot Auth keys are named by their RFC 7638 thumbprint. A kid here is a
    // defect, and the schema allows extra properties, so it is asserted directly.
    const key = (wbaTemplate as { keys: Record<string, unknown>[] }).keys[0];
    expect(key?.kid).toBeUndefined();
  });
});

// The block above enters at createApp(buildDeps(parseEnv(...))). The guard that
// decides whether a deployment can serve at all sits one layer higher, in the
// runtime entries, so nothing above can see a fixture that no deployment would
// start on. The README tells operators this file holds every setting the Worker
// requires; these two assertions are what make that sentence checkable.
//
// What they prove: the Cloudflare entry's construction guard accepts these values
// and the discovery address reaches the origin they name. What they do NOT
// prove: anything about article traffic, and they are not real-runtime coverage.
// This suite reads its fixtures off disk, which is what puts it in the plain-node
// project (vitest.config.ts) rather than a Workers one. The discovery addresses
// do have real-runtime coverage, through SELF in tests/workers/ and
// tests/wellknown-keyless/.
describe('the documented environment is one the Worker starts on', () => {
  it('the Cloudflare entry accepts the fixture and reaches the origin for the discovery document', async () => {
    const seen: string[] = [];
    vi.stubGlobal('fetch', originServingTheTemplate(seen));
    // The host only has to be a valid absolute URL; the forward goes to ORIGIN_URL.
    const res = await worker.fetch(
      new Request(`https://www.publisher.example${WELL_KNOWN_PATH}`),
      templateEnv as unknown as WorkerEnv,
      {} as ExecutionContext,
    );
    expect(res.status).toBe(200);
    expect(seen).toEqual([`${templateEnv.ORIGIN_URL}${WELL_KNOWN_PATH}`]);
  });

  it('the fixture names an origin mode the Fastly entry can also use', () => {
    // originModeConfigured on its own would not be enough: SAME_ZONE_ORIGIN
    // satisfies it and still leaves Fastly with no origin, because readFastlyEnv
    // deliberately never forwards that key. Reading the fixture THROUGH
    // readFastlyEnv is what makes the assertion hold for both runtimes.
    expect(originModeConfigured(readFastlyEnv((n) => templateEnv[n]))).toBe(true);
  });
});
