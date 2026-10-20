// Programmatic Fastly Compute harness. The real target is `fastly compute serve`,
// but that CLI is not assumed to be present in CI; this test stubs the Fastly
// service-worker API (`fastly.getEnv`, `addEventListener('fetch', ...)`) so that
// the fastly entry module can be imported and exercised end-to-end.
//
// TODO(edge/fastly): once the fastly CLI is provisioned in CI, replace this
// with `fastly compute serve` + HTTP client. Until then, the Docker-based
// real-runtime harness at tests/e2e/fastly-edge/ (repo root) carries the
// real-runtime coverage; this file is a fast entry-module check.

import { beforeAll, describe, expect, it, vi } from 'vitest';

import { WBA_PATH } from '../../src/types.js';
import {
  type TestKeypair,
  boundUrl,
  futureExp,
  generateKeypair,
  keyThumbprint,
  popHeaders,
  signUrl,
  tamperSignature,
} from '../helpers/ed25519.js';
import { fetchMock } from '../helpers/fetch-mock.js';
import { PUB_ORIGIN, setupWbaDirectoryMock } from '../helpers/test-setup.js';

interface FetchEvent {
  request: Request;
  respondWith(p: Promise<Response>): void;
}

type FetchListener = (event: FetchEvent) => void;

const ORIGIN_URL = 'https://origin.pub.test';
const ORIGIN_BODY = 'origin content';

const env: Record<string, string> = {
  EXCHANGE_URL: 'https://exchange.test',
  EXCHANGE_WBA_URL: 'https://exchange.test/.well-known/http-message-signatures-directory',
  PROVIDER: 'fastly-edge.test',
  EXCHANGES_JSON: JSON.stringify([
    { domain: 'exchange.test', endpoint: 'https://exchange.test', supported_profiles: [] },
  ]),
  RSL_BODY: '# test rsl',
  ACME_TOKENS_JSON: '{"demo":"response-body"}',
  // A Fastly deployment must name its origin — the entry refuses to serve
  // without one (see the misconfiguration test at the bottom).
  ORIGIN_URL,
  // The bot gate applies under /article/ only; the scope tests below read
  // this through the entry's hand-forwarded env.
  LICENSED_PATHS_JSON: JSON.stringify(['/article/']),
};

let keypair: TestKeypair;
let exchangeKid: string;
let listener: FetchListener | undefined;

async function invoke(request: Request): Promise<Response> {
  if (!listener) throw new Error('fetch listener not registered');
  let captured: Promise<Response> | undefined;
  listener({
    request,
    respondWith: (p) => {
      captured = p;
    },
  });
  if (!captured) throw new Error('respondWith not called');
  return captured;
}

beforeAll(async () => {
  keypair = await generateKeypair('k1');
  exchangeKid = await keyThumbprint(keypair);
  (globalThis as unknown as { fastly: { getEnv(n: string): string } }).fastly = {
    getEnv: (n) => env[n] ?? '',
  };
  (globalThis as unknown as { addEventListener: unknown }).addEventListener = (
    type: string,
    handler: FetchListener,
  ): void => {
    if (type === 'fetch') listener = handler;
  };

  // Same shared fetch mock every sibling e2e suite uses — including its
  // stronger failure mode: with net connect disabled, an unmatched fetch
  // THROWS (surfacing as the app's 502 path) instead of answering a soft 500.
  // Both intercepts persist: they are the file's whole outbound surface.
  fetchMock.activate();
  fetchMock.disableNetConnect();
  await setupWbaDirectoryMock(keypair);
  fetchMock
    .get(ORIGIN_URL)
    .intercept({ path: /.*/, method: 'GET' })
    .reply(200, ORIGIN_BODY)
    .persist();

  await import('../../src/entries/fastly.js');
});

describe('Fastly Compute entry (stubbed runtime)', () => {
  it('healthz returns ok', async () => {
    const res = await invoke(new Request(`${PUB_ORIGIN}/healthz`));
    expect(res.status).toBe(200);
    expect(await res.text()).toBe('ok');
  });

  it('serves fora.json', async () => {
    const res = await invoke(new Request(`${PUB_ORIGIN}/.well-known/fora.json`));
    expect(res.status).toBe(200);
    const body = (await res.json()) as { role: string; domain: string };
    expect(body.role).toBe('ROLE_PUBLISHER');
    expect(body.domain).toBe('fastly-edge.test');
  });

  it('answers 404 on the key directory when the publisher issues no keys', async () => {
    // This deployment's env omits WBA_KEYS_JSON, which is the setup where a named
    // contributor pushes catalog entries on the publisher's behalf and signs with
    // its own key from its own directory. The publisher then has no keys of its
    // own to publish, so 404 is the correct answer and the operator docs tell
    // people not to treat it as an incident. That advice is only safe while a
    // test drives it.
    const res = await invoke(new Request(`${PUB_ORIGIN}${WBA_PATH}`));
    expect(res.status).toBe(404);
  });

  it('blocks bot UA', async () => {
    const res = await invoke(
      new Request(`${PUB_ORIGIN}/article/42`, { headers: { 'user-agent': 'GPTBot/1.0' } }),
    );
    expect(res.status).toBe(403);
    expect(res.headers.get('x-content-rules')).toBe(
      'https://pub.example.com/.well-known/fora.json',
    );
  });

  it('lets a bot UA read outside the licensed paths', async () => {
    const res = await invoke(
      new Request(`${PUB_ORIGIN}/about/team`, { headers: { 'user-agent': 'GPTBot/1.0' } }),
    );
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(ORIGIN_BODY);
  });

  it('passes through valid signed URL and serves the origin body', async () => {
    const signed = await signUrl(PUB_ORIGIN, keypair.privateKey, {
      exp: futureExp(),
      kid: exchangeKid,
    });
    const res = await invoke(new Request(signed));
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(ORIGIN_BODY);
  });

  it('rejects tampered signature with 403', async () => {
    const signed = await signUrl(PUB_ORIGIN, keypair.privateKey, {
      exp: futureExp(),
      kid: exchangeKid,
    });
    const res = await invoke(new Request(tamperSignature(signed)));
    expect(res.status).toBe(403);
    // The 403 body's reason must be a canonical VerifyFailure member (not a
    // stray or build-mangled string): a tampered sig maps to signature_mismatch.
    // Guards the wire-visible reason the Fastly runtime actually emits, which a
    // source-level grep alone cannot.
    const body = (await res.json()) as { reason?: string };
    expect([
      'missing_sig',
      'missing_exp',
      'expired',
      'bad_sig_encoding',
      'signature_mismatch',
    ]).toContain(body.reason);
    expect(body.reason).toBe('signature_mismatch');
  });

  it('refuses a signed URL whose signature is not 64 bytes with 403, not a 503', async () => {
    const signed = await signUrl(PUB_ORIGIN, keypair.privateKey, {
      exp: futureExp(),
      kid: exchangeKid,
    });
    const res = await invoke(new Request(tamperSignature(signed, 10)));
    expect(res.status).toBe(403);
    expect(((await res.json()) as { reason?: string }).reason).toBe('signature_mismatch');
  });

  it('refuses a delivery proof whose signature is not 64 bytes with 403, not a 500', async () => {
    const agentKp = await generateKeypair('agent-short-proof');
    const { url, agentId } = await boundUrl(PUB_ORIGIN, keypair.privateKey, agentKp, {
      exp: futureExp(),
      kid: exchangeKid,
    });
    const headers = await popHeaders(url, agentKp, agentId);
    headers.Signature = `sig1=:${btoa('\0'.repeat(10))}:`;
    const res = await invoke(new Request(url, { headers }));
    expect(res.status).toBe(403);
    expect(((await res.json()) as { reason?: string }).reason).toBe('pop_sig_invalid');
  });
});

// Fastly's SubtleCrypto has no Ed25519. This suite refuses it the same way and
// drives a freshly imported entry, so nothing is cached: the key directory's
// response signatures, the signed URL and the delivery proof must each be
// checked through the primitive the entry injects. A check that fell back to
// WebCrypto answers 503 (the directory or the URL) or a bare 500 (the proof).
describe('Fastly entry without WebCrypto Ed25519', () => {
  it('reads the key directory and serves a bound delivery through the injected primitive', async () => {
    const agentKp = await generateKeypair('agent');
    const { url, agentId } = await boundUrl(PUB_ORIGIN, keypair.privateKey, agentKp, {
      exp: futureExp(),
      kid: exchangeKid,
    });
    const headers = await popHeaders(url, agentKp, agentId);

    const subtle = crypto.subtle;
    const asked: string[] = [];
    const isEd25519 = (algorithm: unknown): boolean => {
      const name =
        typeof algorithm === 'string' ? algorithm : (algorithm as { name?: string }).name;
      return name?.toLowerCase() === 'ed25519';
    };
    const refuse = (method: 'importKey' | 'verify', algorithmAt: number) => {
      const real = subtle[method].bind(subtle) as (...args: unknown[]) => Promise<unknown>;
      return vi.spyOn(subtle, method).mockImplementation(((...args: unknown[]) => {
        if (!isEd25519(args[algorithmAt])) return real(...args);
        asked.push(method);
        return Promise.reject(new DOMException('Ed25519 is not supported', 'NotSupportedError'));
      }) as never);
    };
    const spies = [refuse('importKey', 2), refuse('verify', 0)];
    try {
      vi.resetModules();
      await import('../../src/entries/fastly.js');
      const res = await invoke(new Request(url, { headers }));
      expect(res.status).toBe(200);
      expect(await res.text()).toBe(ORIGIN_BODY);
      expect(asked).toEqual([]);
    } finally {
      for (const spy of spies) spy.mockRestore();
    }
  });
});

// Kept last in the file: it re-imports the entry module with a broken env, so
// it must not run before the suites above have exercised the healthy one.
describe('origin misconfiguration', () => {
  it('refuses to serve when ORIGIN_URL is missing (no silent empty 200s)', async () => {
    // The harness getEnv maps '' to "unset", mirroring fastlyEnv's own rule.
    const saved = env.ORIGIN_URL as string;
    env.ORIGIN_URL = '';
    vi.resetModules();
    await import('../../src/entries/fastly.js');
    await expect(invoke(new Request(`${PUB_ORIGIN}/healthz`))).rejects.toThrowError(/ORIGIN_URL/);
    env.ORIGIN_URL = saved;
  });
});
