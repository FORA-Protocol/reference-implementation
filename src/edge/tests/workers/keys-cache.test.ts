import { beforeAll, describe, expect, it, vi } from 'vitest';

import type { Ed25519Verify } from '@fora-protocol/sdk/pop';

import { nobleEd25519 } from '../../src/ed25519-noble.js';
import { webCryptoEd25519 } from '../../src/ed25519.js';
import { createKeyCache } from '../../src/keys-cache.js';
import { type SignedDirectory, signedDirectory } from '../helpers/directory.js';
import { generateKeypair, keyThumbprint, rawPublicKey } from '../helpers/ed25519.js';

// Exercises the cache mechanics — TTL, single-flight, the default-key fallback,
// the body cap and the injected Ed25519 primitive — over a real signed
// directory. The Web Bot Auth directory rules and the pre-provisioned keys are
// covered by keys.test.ts. A key is resolved by its RFC 7638 thumbprint and
// handed out as its raw 32-byte public key.

const WBA_URL = 'https://exchange.test/.well-known/http-message-signatures-directory';

let DIR: SignedDirectory;
let KEY_ID: string;
let KEY_RAW: Uint8Array;

beforeAll(async () => {
  const kp = await generateKeypair('exchange-primary');
  KEY_ID = await keyThumbprint(kp);
  KEY_RAW = rawPublicKey(kp);
  DIR = await signedDirectory([kp], 'exchange.test');
});

function wbaFetcher(): { fetcher: typeof fetch; calls: () => number } {
  const fn = vi.fn(async () => new Response(DIR.body, { status: 200, headers: DIR.headers }));
  return { fetcher: fn as unknown as typeof fetch, calls: () => fn.mock.calls.length };
}

function rawBytesCache(
  fetcher: typeof fetch,
  opts?: { now?: () => number; ttlMs?: number; verifyEd25519?: Ed25519Verify },
) {
  return createKeyCache({ wbaUrl: WBA_URL, fetcher, ...opts });
}

describe('keys-cache — cache mechanics', () => {
  it('resolves a keyid to the raw public key', async () => {
    const { fetcher } = wbaFetcher();
    const bytes = await rawBytesCache(fetcher).resolve(KEY_ID);
    expect(bytes).toEqual(KEY_RAW);
  });

  it('falls back to the first key when keyid is undefined', async () => {
    const { fetcher } = wbaFetcher();
    // Assert the fallback returns the FIRST key's bytes, not merely "something":
    // a bug returning an empty/other buffer would still be defined.
    expect(await rawBytesCache(fetcher).resolve(undefined)).toEqual(KEY_RAW);
  });

  it('returns undefined for an unknown keyid', async () => {
    const { fetcher } = wbaFetcher();
    expect(await rawBytesCache(fetcher).resolve('nope')).toBeUndefined();
  });

  it('caches within the TTL — a second resolve does not refetch', async () => {
    const { fetcher, calls } = wbaFetcher();
    const cache = rawBytesCache(fetcher);
    await cache.resolve(KEY_ID);
    await cache.resolve(KEY_ID);
    expect(calls()).toBe(1);
  });

  it('refetches after the TTL expires', async () => {
    let clock = Date.now();
    const { fetcher, calls } = wbaFetcher();
    const cache = rawBytesCache(fetcher, { now: () => clock, ttlMs: 50 });

    await cache.resolve(KEY_ID);
    expect(calls()).toBe(1);

    clock += 51; // advance past ttlMs
    await cache.resolve(KEY_ID);
    expect(calls()).toBe(2);
  });

  it('single-flights concurrent cold-cache resolves into one fetch', async () => {
    const { fetcher, calls } = wbaFetcher();
    const cache = rawBytesCache(fetcher);
    await Promise.all([cache.resolve(KEY_ID), cache.resolve(KEY_ID), cache.resolve(KEY_ID)]);
    expect(calls()).toBe(1);
  });
});

// A ReadableStream body keeps a manually-set Content-Length from being recomputed
// by the runtime, letting these tests drive the header short-circuit and the
// streamed-body cap independently.
function streamResponse(body: Uint8Array, contentLength?: string): Response {
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(body);
      controller.close();
    },
  });
  const headers = new Headers(DIR.headers);
  if (contentLength !== undefined) headers.set('content-length', contentLength);
  return new Response(stream, { status: 200, headers });
}

// The cache caps a fetched WBA directory at 64 KiB (mirroring the Go loader's
// maxDocBytes) so a compromised/hostile origin cannot OOM the worker by streaming
// an unbounded body into the JSON parse.
describe('keys-cache — WBA directory response-size bound', () => {
  const CAP = 64 * 1024;

  it('rejects on an over-cap Content-Length before reading the body', async () => {
    const small = new TextEncoder().encode(DIR.body);
    const fetcher = vi.fn(async () =>
      streamResponse(small, String(CAP + 1)),
    ) as unknown as typeof fetch;
    await expect(rawBytesCache(fetcher).resolve(KEY_ID)).rejects.toThrow(/too large/i);
  });

  it('rejects a body that streams past the cap even when Content-Length under-reports', async () => {
    const oversize = new TextEncoder().encode(`{"keys":[{"x":"${'A'.repeat(CAP + 8)}"}]}`);
    const fetcher = vi.fn(async () => streamResponse(oversize, '16')) as unknown as typeof fetch;
    await expect(rawBytesCache(fetcher).resolve(KEY_ID)).rejects.toThrow(/too large/i);
  });

  it('accepts and parses a normal-sized directory streamed under the cap', async () => {
    const body = new TextEncoder().encode(DIR.body);
    const fetcher = vi.fn(async () => streamResponse(body)) as unknown as typeof fetch;
    expect(await rawBytesCache(fetcher).resolve(KEY_ID)).toEqual(KEY_RAW);
  });
});

// The Fastly entries check the directory response signatures through an
// injected primitive, because Fastly's SubtleCrypto has no Ed25519. The cache
// must hand that primitive to the SDK's reader rather than fall back to
// WebCrypto.
describe('keys-cache — injected Ed25519 primitive', () => {
  it('verifies the directory response through the injected primitive', async () => {
    const { fetcher } = wbaFetcher();
    const verify = vi.fn(webCryptoEd25519);
    const bytes = await rawBytesCache(fetcher, { verifyEd25519: verify }).resolve(KEY_ID);
    expect(bytes).toEqual(KEY_RAW);
    expect(verify).toHaveBeenCalled();
  });

  it('hands out no key the injected primitive does not verify', async () => {
    const { fetcher } = wbaFetcher();
    const refuse: Ed25519Verify = async () => false;
    expect(await rawBytesCache(fetcher, { verifyEd25519: refuse }).resolve(KEY_ID)).toBeUndefined();
  });

  // A response may carry a signature that does not verify; the reader ignores
  // it and keeps the keys the others verify. One that is not even 64 bytes
  // must be ignored the same way, through the primitive the Fastly entry
  // injects, and must not cost the edge its whole key directory.
  it('ignores an extra response signature that is not 64 bytes', async () => {
    const input = DIR.headers['signature-input'] as string;
    const headers = {
      ...DIR.headers,
      'signature-input': `${input}, extra=${input.slice(input.indexOf('=') + 1)}`,
      signature: `${DIR.headers.signature}, extra=:${btoa('\0'.repeat(10))}:`,
    };
    const fetcher = vi.fn(
      async () => new Response(DIR.body, { status: 200, headers }),
    ) as unknown as typeof fetch;
    const verify = vi.fn(nobleEd25519);
    expect(await rawBytesCache(fetcher, { verifyEd25519: verify }).resolve(KEY_ID)).toEqual(
      KEY_RAW,
    );
    expect(verify.mock.calls.some(([, signature]) => signature.length === 10)).toBe(true);
  });
});
