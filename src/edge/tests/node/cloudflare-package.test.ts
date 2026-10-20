// The Cloudflare package a publisher downloads, run the way wrangler would run
// it: the release Worker as fora-worker.mjs, beside the golden wrangler.toml
// the console writes (edgerelease/testdata/wrangler.toml, pinned byte for byte
// by a Go test). The project file is read by wrangler's own config reader and
// the Worker runs in Miniflare under the date, flags and variables that file
// names. So a package the console hands out that the runtime cannot start, or
// that gates the wrong paths, fails here.
//
// The Worker sits on the zone route with SAME_ZONE_ORIGIN, so a pass-through
// is a subrequest to the incoming URL. Miniflare's outbound service stands in
// for the zone's origin and records what reached it.
import { copyFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { Miniflare } from 'miniflare';
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { unstable_readConfig } from 'wrangler';

import { WORKERS_COMPAT } from '../../scripts/workers-compat.mjs';
import { futureExp, signUrl } from '../helpers/ed25519.js';
import { goldenSigningKey } from '../helpers/golden-key.js';
import { buildRelease, goldenPath } from '../helpers/release-build.js';
import { expectNoSignatureParams } from '../helpers/test-setup.js';

const SITE = 'https://www.publisher.example';
const ORIGIN_BODY = 'origin content';
const BOT = { 'user-agent': 'ClaudeBot/1.0' };
const HUMAN = { 'user-agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605.1.15' };

let out: string | undefined;
let mf: Miniflare | undefined;
let config: ReturnType<typeof unstable_readConfig>;
// What reached the origin, reset per test.
let originRequests: string[] = [];

function worker(): Miniflare {
  if (!mf) throw new Error('the package is not running');
  return mf;
}

beforeAll(async () => {
  out = buildRelease();
  copyFileSync(join(out, 'cloudflare-worker.mjs'), join(out, 'fora-worker.mjs'));
  copyFileSync(goldenPath('wrangler.toml'), join(out, 'wrangler.toml'));
  config = unstable_readConfig({ config: join(out, 'wrangler.toml') });
  mf = new Miniflare({
    modules: true,
    // The unpacked package is its own module root, as it is for wrangler.
    modulesRoot: out,
    scriptPath: config.main ?? '',
    compatibilityDate: config.compatibility_date ?? '',
    compatibilityFlags: config.compatibility_flags,
    bindings: config.vars as Record<string, string>,
    outboundService: (request) => {
      originRequests.push(request.url);
      return new Response(ORIGIN_BODY);
    },
  });
  await mf.ready;
}, 120_000);

afterAll(async () => {
  await mf?.dispose();
  if (out) rmSync(out, { recursive: true, force: true });
});

beforeEach(() => {
  originRequests = [];
});

describe('the generated Cloudflare package', () => {
  it('runs under the Workers settings the edge suites run under', () => {
    expect(config.compatibility_date).toBe(WORKERS_COMPAT.compatibilityDate);
    expect(config.compatibility_flags).toEqual(WORKERS_COMPAT.compatibilityFlags);
    expect(config.main?.endsWith('fora-worker.mjs')).toBe(true);
  });

  it('serves the publisher discovery document', async () => {
    const res = await worker().dispatchFetch(`${SITE}/.well-known/fora.json`, { headers: BOT });
    expect(res.status).toBe(200);
    expect(((await res.json()) as { domain: string }).domain).toBe('www.publisher.example');
  });

  it('passes a human read through to the zone origin', async () => {
    const res = await worker().dispatchFetch(`${SITE}/article/42`, { headers: HUMAN });
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(ORIGIN_BODY);
    expect(originRequests).toEqual([`${SITE}/article/42`]);
  });

  it('refuses an unsigned AI bot inside the licensed paths without reaching the origin', async () => {
    const res = await worker().dispatchFetch(`${SITE}/article/42`, { headers: BOT });
    expect(res.status).toBe(403);
    expect(res.headers.get('x-content-rules')).toBe(`${SITE}/.well-known/fora.json`);
    expect(originRequests).toEqual([]);
  });

  it('lets an unsigned AI bot read outside the licensed paths', async () => {
    const res = await worker().dispatchFetch(`${SITE}/about/team`, { headers: BOT });
    expect(res.status).toBe(200);
    expect(originRequests).toEqual([`${SITE}/about/team`]);
  });

  it('delivers a signed read with the signature params stripped', async () => {
    const { privateKey, kid } = await goldenSigningKey();
    // The golden enforces binding, which applies only to URLs bound to an
    // agent; an unbound delivery URL verifies on its signature alone.
    const url = await signUrl(SITE, privateKey, { exp: futureExp(), kid, path: '/article/42' });
    const res = await worker().dispatchFetch(url, { headers: BOT });
    expect(res.status).toBe(200);
    expect(originRequests).toHaveLength(1);
    expectNoSignatureParams(new URL(originRequests[0] ?? ''));
  });
});
