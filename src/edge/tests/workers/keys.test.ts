import { describe, expect, it, vi } from 'vitest';

import { createKeyCache } from '../../src/keys-cache.js';
import type { Jwk } from '../../src/keys.js';
import { type SignedDirectory, signedDirectory } from '../helpers/directory.js';
import {
  type TestKeypair,
  generateKeypair,
  keyThumbprint,
  rawPublicKey,
} from '../helpers/ed25519.js';

const WBA_URL = 'https://exchange.test/.well-known/http-message-signatures-directory';
const AUTHORITY = 'exchange.test';

// Builds a fetcher that answers every call with `response` and counts how many
// times it was invoked (to assert the no-fetch / one-shot-fetch contract).
function fetcherOf(response: () => Response): { fetcher: typeof fetch; calls: () => number } {
  const fn = vi.fn(async () => response());
  return { fetcher: fn as unknown as typeof fetch, calls: () => fn.mock.calls.length };
}

function serve(dir: SignedDirectory, status = 200): Response {
  return new Response(dir.body, { status, headers: dir.headers });
}

// A fetcher serving a directory that lists `listed`, signed as the Web Bot Auth
// profile requires (by every listed key, for the authority the edge fetches).
// Keys carry NO kid — they are resolved by RFC 7638 thumbprint.
async function wbaFetcher(
  listed: TestKeypair[],
): Promise<{ fetcher: typeof fetch; calls: () => number }> {
  const dir = await signedDirectory(listed, AUTHORITY);
  return fetcherOf(() => serve(dir));
}

function toJwk(publicJwk: JsonWebKey): Jwk {
  return { kty: 'OKP', crv: 'Ed25519', x: publicJwk.x ?? '' };
}

// assertResolvesToKey checks the cache resolved the SPECIFIC expected keypair,
// not merely "some key" — a keyid-matching regression that returned a different
// directory key would pass toBeDefined() but fail here. Proven by a
// sign(signer.private)/verify(resolved) round-trip, mirroring the Go
// TestForaWBA_KeyVerifiesOfferSignature.
async function assertResolvesToKey(
  resolved: Uint8Array<ArrayBuffer> | undefined,
  signer: TestKeypair,
): Promise<void> {
  if (!resolved) throw new Error('expected the cache to resolve a key');
  const msg = new TextEncoder().encode('fora-edge-key-identity-round-trip');
  const sig = await crypto.subtle.sign('Ed25519', signer.privateKey, msg);
  const key = await crypto.subtle.importKey('raw', resolved, { name: 'Ed25519' }, false, [
    'verify',
  ]);
  expect(await crypto.subtle.verify('Ed25519', key, sig, msg)).toBe(true);
}

describe('createKeyCache — fetched WBA directory path', () => {
  it('resolves a keyid (thumbprint) from keys[] of the fetched WBA directory', async () => {
    const kp = await generateKeypair('exchange-primary');
    const tp = await keyThumbprint(kp);
    const { fetcher } = await wbaFetcher([kp]);
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    // Assert IDENTITY, not just presence: a regression that returned a different
    // directory key would still be 32 bytes but fail the round-trip.
    await assertResolvesToKey(await cache.resolve(tp), kp);
  });

  it('refuses a configured directory URL whose path is not the Web Bot Auth path', () => {
    const { fetcher } = fetcherOf(() => new Response('unreachable', { status: 500 }));
    expect(() => createKeyCache({ wbaUrl: 'https://exchange.test/keys.json', fetcher })).toThrow(
      /must be an origin followed by/,
    );
  });
});

// The Web Bot Auth profile's rules for reading a key directory: 200 with no
// redirect, its own media type, and only keys that signed the response are
// handed out. Each case breaks one rule and asserts the key is not handed out.
describe('createKeyCache — the Web Bot Auth directory rules', () => {
  it('refuses a redirect instead of following it', async () => {
    const kp = await generateKeypair('exchange-primary');
    const { fetcher } = fetcherOf(
      () => new Response(null, { status: 302, headers: { location: WBA_URL } }),
    );
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    await expect(cache.resolve(await keyThumbprint(kp))).rejects.toThrow(/refused: status 302/);
  });

  it('asks for the directory without following redirects', async () => {
    const kp = await generateKeypair('exchange-primary');
    const dir = await signedDirectory([kp], AUTHORITY);
    const seen: RequestInit[] = [];
    const fetcher = (async (_url: string, init: RequestInit) => {
      seen.push(init);
      return serve(dir);
    }) as unknown as typeof fetch;

    await createKeyCache({ wbaUrl: WBA_URL, fetcher }).resolve(await keyThumbprint(kp));
    expect(seen[0]?.redirect).toBe('manual');
  });

  it('refuses a directory served as a JWK Set', async () => {
    const kp = await generateKeypair('exchange-primary');
    const dir = await signedDirectory([kp], AUTHORITY);
    const { fetcher } = fetcherOf(() =>
      serve({ ...dir, headers: { ...dir.headers, 'content-type': 'application/jwk-set+json' } }),
    );
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    await expect(cache.resolve(await keyThumbprint(kp))).rejects.toThrow(
      /media type, body or response signatures/,
    );
  });

  it('refuses an unsigned directory response', async () => {
    const kp = await generateKeypair('exchange-primary');
    const dir = await signedDirectory([kp], AUTHORITY);
    const { fetcher } = fetcherOf(() =>
      serve({ ...dir, headers: { 'content-type': dir.headers['content-type'] as string } }),
    );
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    await expect(cache.resolve(await keyThumbprint(kp))).rejects.toThrow(
      /media type, body or response signatures/,
    );
  });

  it('refuses a body that does not match its Content-Digest', async () => {
    const kp = await generateKeypair('exchange-primary');
    const dir = await signedDirectory([kp], AUTHORITY);
    const { fetcher } = fetcherOf(() => serve({ ...dir, body: `${dir.body} ` }));
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    await expect(cache.resolve(await keyThumbprint(kp))).rejects.toThrow(
      /media type, body or response signatures/,
    );
  });

  it('never hands out a listed key that did not sign the response', async () => {
    const signer = await generateKeypair('signs');
    const silent = await generateKeypair('listed-only');
    const dir = await signedDirectory([signer, silent], AUTHORITY, { signers: [signer] });
    const { fetcher } = fetcherOf(() => serve(dir));
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    await assertResolvesToKey(await cache.resolve(await keyThumbprint(signer)), signer);
    expect(await cache.resolve(await keyThumbprint(silent))).toBeUndefined();
  });

  it('never hands out a key whose signature was made for another host', async () => {
    const kp = await generateKeypair('exchange-primary');
    const dir = await signedDirectory([kp], AUTHORITY, { signedFor: 'other.example' });
    const { fetcher } = fetcherOf(() => serve(dir));
    const cache = createKeyCache({ wbaUrl: WBA_URL, fetcher });

    expect(await cache.resolve(await keyThumbprint(kp))).toBeUndefined();
  });
});

describe('createKeyCache — FORA_VERIFY_KEYS pre-provisioning (D5)', () => {
  it('resolves a pre-provisioned keyid WITHOUT fetching the directory', async () => {
    const kp = await generateKeypair('exchange-primary');
    const tp = await keyThumbprint(kp);
    const { fetcher, calls } = fetcherOf(() => new Response('unreachable', { status: 500 }));
    const cache = createKeyCache({
      wbaUrl: WBA_URL,
      staticKeys: [toJwk(kp.publicJwk)],
      fetcher,
    });

    expect(await cache.resolve(tp)).toEqual(rawPublicKey(kp));
    expect(calls()).toBe(0);
  });

  it('one-shot fetches the directory when a keyid misses the static set (rotation self-heal)', async () => {
    const provisioned = await generateKeypair('old');
    const provisionedTp = await keyThumbprint(provisioned);
    const rotated = await generateKeypair('new');
    const rotatedTp = await keyThumbprint(rotated);
    const { fetcher, calls } = await wbaFetcher([rotated]);
    const cache = createKeyCache({
      wbaUrl: WBA_URL,
      staticKeys: [toJwk(provisioned.publicJwk)],
      fetcher,
    });

    // Provisioned thumbprint: no fetch, and it resolves to the PROVISIONED key.
    await assertResolvesToKey(await cache.resolve(provisionedTp), provisioned);
    expect(calls()).toBe(0);

    // Unknown (rotated) thumbprint: exactly one fetch, resolving to the ROTATED key.
    await assertResolvesToKey(await cache.resolve(rotatedTp), rotated);
    expect(calls()).toBe(1);
  });
});
