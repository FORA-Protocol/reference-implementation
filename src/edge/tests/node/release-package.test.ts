// The release package, and the one property everything else depends on: a
// bundle built with no settings still runs once the console's index.mjs is put
// beside it.
//
// That is what lets the console hand every publisher the same reviewed bytes
// and add only their own values. If it stopped being true -- an entry that read
// its configuration at module load rather than on first use, a bundler flag
// that froze process.env -- the console would go on assembling packages that
// fail at the edge, where nobody is watching.
//
// The index.mjs under test is not written here. It is the golden
// edgerelease/testdata/lambda-index.mjs.golden, which a Go test holds byte for byte to
// what the console writes into a publisher's download, and which
// lambda-entry.test.ts holds to what a deployment's own build writes. So this
// suite runs the exact pair of files a publisher downloads.

import { createHash } from 'node:crypto';
import { copyFileSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { pathToFileURL } from 'node:url';
import type { CloudFrontRequest } from 'hono/lambda-edge';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import type { handler as awsHandler } from '../../src/entries/aws-lambda.js';
import { makeViewerRequestEvent } from '../helpers/cloudfront.js';
import { futureExp, signUrl } from '../helpers/ed25519.js';
import { goldenSigningKey } from '../helpers/golden-key.js';
import { buildRelease, goldenPath } from '../helpers/release-build.js';
import { expectNoSignatureParams } from '../helpers/test-setup.js';

// The release build, made in beforeAll.
let out: string | undefined;

function built(name: string): string {
  if (!out) throw new Error('the release was not built');
  return join(out, name);
}

// What the golden's settings name. Asserting on them is how a test notices the
// golden and this suite no longer describe the same publisher.
const PROVIDER = 'www.publisher.example';
const ORIGIN = `https://${PROVIDER}`;

type Handler = typeof awsHandler;
type Result = Awaited<ReturnType<Handler>>;

let handler: Handler;
// Taken before index.mjs assigns the golden's settings, so afterAll can put
// the environment back. Undefined when beforeAll failed before that point.
let envBefore: NodeJS.ProcessEnv | undefined;

function isRequest(result: Result): result is CloudFrontRequest {
  return 'uri' in result;
}

describe('the release package behind the console index.mjs', () => {
  beforeAll(async () => {
    out = buildRelease();
    copyFileSync(goldenPath('lambda-index.mjs.golden'), join(out, 'index.mjs'));
    // Importing index.mjs assigns the golden's settings to process.env; the
    // snapshot lets afterAll put the environment back.
    envBefore = { ...process.env };
    const entry = (await import(pathToFileURL(built('index.mjs')).href)) as {
      handler?: unknown;
    };
    expect(typeof entry.handler).toBe('function');
    handler = entry.handler as Handler;
  });

  afterAll(() => {
    if (out) rmSync(out, { recursive: true, force: true });
    const before = envBefore;
    if (!before) return;
    // Both halves: a key index.mjs added goes, and a key it overwrote gets its
    // old value back.
    for (const key of Object.keys(process.env)) {
      if (!(key in before)) delete process.env[key];
    }
    Object.assign(process.env, before);
  });

  it('builds a Lambda bundle with nothing about a publisher in it', () => {
    const bundle = readFileSync(built('lambda-edge.mjs'), 'utf8');
    expect(bundle).not.toContain(PROVIDER);
    expect(bundle).not.toContain('exchange.example');
  });

  it('serves the publisher manifest from the settings in index.mjs', async () => {
    const answer = await handler(makeViewerRequestEvent(`${ORIGIN}/.well-known/fora.json`));
    expect(isRequest(answer)).toBe(false);
    if (isRequest(answer)) return;
    expect(answer.status).toBe('200');
    // The document it serves is the publisher's, which is only true if the
    // assignment in index.mjs reached the parser the entry runs on first use.
    expect(JSON.parse(answer.body ?? '').domain).toBe(PROVIDER);
  });

  it('hands a signed read back to CloudFront with the signature params stripped', async () => {
    const { privateKey, kid } = await goldenSigningKey();
    const url = await signUrl(ORIGIN, privateKey, {
      exp: futureExp(),
      kid,
      path: '/article/42',
      query: { page: '2' },
    });
    const answer = await handler(makeViewerRequestEvent(url));
    expect(isRequest(answer)).toBe(true);
    if (!isRequest(answer)) return;
    expect(answer.uri).toBe('/article/42');
    expectNoSignatureParams(new URL(`${ORIGIN}${answer.uri}?${answer.querystring}`));
    expect(new URLSearchParams(answer.querystring).get('page')).toBe('2');
  });

  it('hands an unsigned human read back to CloudFront', async () => {
    const answer = await handler(
      makeViewerRequestEvent(`${ORIGIN}/article/42`, {
        'user-agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605.1.15',
      }),
    );
    expect(isRequest(answer)).toBe(true);
  });

  it('refuses an unsigned AI bot with a generated 403 pointing at the manifest', async () => {
    const answer = await handler(
      makeViewerRequestEvent(`${ORIGIN}/article/42`, { 'user-agent': 'ClaudeBot/1.0' }),
    );
    expect(isRequest(answer)).toBe(false);
    if (isRequest(answer)) return;
    expect(answer.status).toBe('403');
    expect(answer.headers?.['x-content-rules']?.[0]?.value).toBe(`${ORIGIN}/.well-known/fora.json`);
  });

  it('hands an unsigned AI bot outside the licensed paths back to CloudFront', async () => {
    // The golden licenses /article/ and /q&a/ only. The rest of the site is
    // not for sale, so CloudFront fetches it from the origin as it would for
    // anyone.
    const answer = await handler(
      makeViewerRequestEvent(`${ORIGIN}/about/team`, { 'user-agent': 'ClaudeBot/1.0' }),
    );
    expect(isRequest(answer)).toBe(true);
  });

  it('writes a checksum that matches the bundle beside index.mjs', () => {
    const sums = readFileSync(built('SHA256SUMS'), 'utf8');
    expect(sums).toMatch(/^[0-9a-f]{64} {2}cloudflare-worker\.mjs$/m);
    const bundle = readFileSync(built('lambda-edge.mjs'));
    const digest = createHash('sha256').update(bundle).digest('hex');
    expect(sums).toContain(`${digest}  lambda-edge.mjs\n`);
  });
});
