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
// for the zone's origin and records what reached it. That origin serves the
// publisher's own discovery document and RSL file, as a publisher's site does:
// a package deployed in front of it must leave both reachable, unchanged.
//
// The package runs twice. As generated, it licenses the golden's two paths, so
// both documents sit outside the licensed paths. Licensed as a whole site, it
// carries the same settings without LICENSED_PATHS_JSON, which is the package
// the console writes for a publisher who names no paths
// (TestNoScopeGatesTheWholeSite in src/console/internal/setup/edgepackage_test.go
// pins that). There the documents are inside the licensed paths, and only their
// own route keeps the bot gate off them.
import { copyFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { Miniflare } from 'miniflare';
import { afterAll, beforeAll, beforeEach, describe, expect, it } from 'vitest';
import { unstable_readConfig } from 'wrangler';

import { WORKERS_COMPAT } from '../../scripts/workers-compat.mjs';
import { futureExp, signUrl } from '../helpers/ed25519.js';
import { goldenSigningKey } from '../helpers/golden-key.js';
import {
  DOCUMENT_PATHS,
  documentReads,
  expectOriginAnswer,
  originDocument,
} from '../helpers/publisher-documents.js';
import { buildRelease, goldenPath } from '../helpers/release-build.js';
import { expectNoSignatureParams } from '../helpers/test-setup.js';

const SITE = 'https://www.publisher.example';
const ORIGIN_BODY = 'origin content';
const BOT = { 'user-agent': 'ClaudeBot/1.0' };
const HUMAN = { 'user-agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605.1.15' };

// One running copy of the package, and what reached its origin.
interface RunningPackage {
  mf: Miniflare;
  originRequests: string[];
}

let out: string | undefined;
let config: ReturnType<typeof unstable_readConfig>;
let asGenerated: RunningPackage | undefined;
let wholeSite: RunningPackage | undefined;

function running(pkg: RunningPackage | undefined): RunningPackage {
  if (!pkg) throw new Error('the package is not running');
  return pkg;
}

// startPackage runs the unpacked package in root under bindings, in front of
// an origin that serves the publisher's documents and answers anything else
// with ORIGIN_BODY.
async function startPackage(
  root: string,
  bindings: Record<string, string>,
): Promise<RunningPackage> {
  const originRequests: string[] = [];
  const mf = new Miniflare({
    modules: true,
    // The unpacked package is its own module root, as it is for wrangler.
    modulesRoot: root,
    scriptPath: config.main ?? '',
    compatibilityDate: config.compatibility_date ?? '',
    compatibilityFlags: config.compatibility_flags,
    bindings,
    outboundService: (request) => {
      originRequests.push(request.url);
      const path = new URL(request.url).pathname;
      if (!DOCUMENT_PATHS.includes(path)) return new Response(ORIGIN_BODY);
      const answer = originDocument(path);
      return new Response(answer.body, { status: answer.status, headers: answer.headers });
    },
  });
  await mf.ready;
  return { mf, originRequests };
}

beforeAll(async () => {
  const root = buildRelease();
  out = root;
  copyFileSync(join(root, 'cloudflare-worker.mjs'), join(root, 'fora-worker.mjs'));
  copyFileSync(goldenPath('wrangler.toml'), join(root, 'wrangler.toml'));
  config = unstable_readConfig({ config: join(root, 'wrangler.toml') });
  const vars = config.vars as Record<string, string>;
  const { LICENSED_PATHS_JSON: _scope, ...wholeSiteVars } = vars;
  [asGenerated, wholeSite] = await Promise.all([
    startPackage(root, vars),
    startPackage(root, wholeSiteVars),
  ]);
}, 120_000);

afterAll(async () => {
  await Promise.all([asGenerated?.mf.dispose(), wholeSite?.mf.dispose()]);
  if (out) rmSync(out, { recursive: true, force: true });
});

beforeEach(() => {
  for (const pkg of [asGenerated, wholeSite]) {
    if (pkg) pkg.originRequests.length = 0;
  }
});

describe('the generated Cloudflare package', () => {
  it('runs under the Workers settings the edge suites run under', () => {
    expect(config.compatibility_date).toBe(WORKERS_COMPAT.compatibilityDate);
    expect(config.compatibility_flags).toEqual(WORKERS_COMPAT.compatibilityFlags);
    expect(config.main?.endsWith('fora-worker.mjs')).toBe(true);
  });

  it('passes a human read through to the zone origin', async () => {
    const pkg = running(asGenerated);
    const res = await pkg.mf.dispatchFetch(`${SITE}/article/42`, { headers: HUMAN });
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(ORIGIN_BODY);
    expect(pkg.originRequests).toEqual([`${SITE}/article/42`]);
  });

  it('refuses an unsigned AI bot inside the licensed paths without reaching the origin', async () => {
    const pkg = running(asGenerated);
    const res = await pkg.mf.dispatchFetch(`${SITE}/article/42`, { headers: BOT });
    expect(res.status).toBe(403);
    expect(res.headers.get('x-content-rules')).toBe(`${SITE}/.well-known/fora.json`);
    expect(pkg.originRequests).toEqual([]);
  });

  it('lets an unsigned AI bot read outside the licensed paths', async () => {
    const pkg = running(asGenerated);
    const res = await pkg.mf.dispatchFetch(`${SITE}/about/team`, { headers: BOT });
    expect(res.status).toBe(200);
    expect(pkg.originRequests).toEqual([`${SITE}/about/team`]);
  });

  it('delivers a signed read with the signature params stripped', async () => {
    const pkg = running(asGenerated);
    const { privateKey, kid } = await goldenSigningKey();
    // The golden enforces binding, which applies only to URLs bound to an
    // agent; an unbound delivery URL verifies on its signature alone.
    const url = await signUrl(SITE, privateKey, { exp: futureExp(), kid, path: '/article/42' });
    const res = await pkg.mf.dispatchFetch(url, { headers: BOT });
    expect(res.status).toBe(200);
    expect(pkg.originRequests).toHaveLength(1);
    expectNoSignatureParams(new URL(pkg.originRequests[0] ?? ''));
  });
});

describe.each([
  ['as generated, licensing /article/ and /q&a/', () => asGenerated],
  ['licensed as a whole site', () => wholeSite],
])('the package %s', (_scope, packageOf) => {
  it.each(
    documentReads([
      ['an AI bot', BOT],
      ['a person', HUMAN],
    ]),
  )("leaves the publisher's own %s reachable for %s, unchanged", async (path, _who, headers) => {
    const pkg = running(packageOf());
    await expectOriginAnswer(
      await pkg.mf.dispatchFetch(`${SITE}${path}`, { headers }),
      originDocument(path),
    );
    expect(pkg.originRequests).toEqual([`${SITE}${path}`]);
  });
});

describe('the package licensed as a whole site', () => {
  it('refuses an unsigned AI bot anywhere else on the site without reaching the origin', async () => {
    const pkg = running(wholeSite);
    const res = await pkg.mf.dispatchFetch(`${SITE}/about/team`, { headers: BOT });
    expect(res.status).toBe(403);
    expect(res.headers.get('x-content-rules')).toBe(`${SITE}/.well-known/fora.json`);
    expect(pkg.originRequests).toEqual([]);
  });
});
