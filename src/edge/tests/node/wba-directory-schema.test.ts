import { generateKeyPairSync } from 'node:crypto';

import { describe, expect, it } from 'vitest';

import { WBA_DIRECTORY_MEDIA_TYPE } from '@fora-protocol/sdk/resolvers/edge';
import { buildDeps, parseEnv } from '../../src/config.js';
import { MAX_WBA_KEYS } from '../../src/keys.js';
import { WBA_DIRECTORY_CONTENT_TYPE, type WbaKey, buildPublisherWba } from '../../src/types.js';
import { assertValidWba } from '../helpers/wellknown-schema.js';

describe('the WBA directory media type', () => {
  it('the literal is the SDK media type', () => {
    // well-known.mjs is plain ESM shared with the raw-Node AWS shim, which cannot
    // import the SDK's TypeScript source, so the literal there mirrors the SDK
    // constant rather than importing it. The edge serves its directory under it
    // and requires it of the Exchange's. The SDK names it in its edge resolvers
    // entry, the one an edge runtime imports. The worker itself does not import
    // that entry, because doing so would bundle every strict schema the readers
    // check, so this test is where the literal and the SDK constant are compared.
    expect(WBA_DIRECTORY_MEDIA_TYPE).toBe('application/http-message-signatures-directory+json');
    expect(WBA_DIRECTORY_CONTENT_TYPE).toBe(WBA_DIRECTORY_MEDIA_TYPE);
  });
});

describe('edge publisher WBA directory conforms to the canonical FORA schema', () => {
  // A REAL Ed25519 public key — Node exports OKP/Ed25519 directly as a JWK whose
  // `x` is the RFC 8037 base64url-unpadded 32-byte key (43 chars). Keeps the
  // fixture honest: after the WBA split the key is named by its thumbprint of
  // this very `x`. (Cryptographic verification is the e2e test's job.)
  const { publicKey } = generateKeyPairSync('ed25519');
  const jwk = publicKey.export({ format: 'jwk' }) as { x: string };
  const sampleKey: WbaKey = {
    kty: 'OKP',
    crv: 'Ed25519',
    use: 'sig',
    alg: 'EdDSA',
    x: jwk.x,
    not_before: '2020-01-01T00:00:00Z',
    not_after: '2100-01-01T00:00:00Z',
  };

  it('buildPublisherWba serves keys[] with no kid', () => {
    const wba = buildPublisherWba([sampleKey]);
    assertValidWba(wba);
    expect(wba.keys).toHaveLength(1);
    expect((wba.keys[0] as unknown as Record<string, unknown>).kid).toBeUndefined();
  });

  it('buildPublisherWba carries an optional directory-level revocation_url', () => {
    const wba = buildPublisherWba(
      [sampleKey],
      'https://pub.example/.well-known/fora-key-revocations.json',
    );
    assertValidWba(wba);
    expect(wba.revocation_url).toBe('https://pub.example/.well-known/fora-key-revocations.json');
  });

  it('rejects a keys[] over the maxItems bound', () => {
    // 65 keys exceeds the canonical schema's maxItems:64. Each key is valid; the
    // only fault is the array size, so an over-large directory is rejected.
    const tooMany = { keys: Array.from({ length: MAX_WBA_KEYS + 1 }, () => sampleKey) };
    expect(() => assertValidWba(tooMany)).toThrow(/schema validation failed/);
  });

  it('buildDeps wba carries keys from WBA_KEYS_JSON', () => {
    const env = parseEnv({
      EXCHANGE_URL: 'https://exchange.example',
      EXCHANGE_WBA_URL: 'https://exchange.example/.well-known/http-message-signatures-directory',
      WBA_KEYS_JSON: JSON.stringify([sampleKey]),
    });
    const wba = buildDeps(env).wba;
    expect(wba).toBeDefined();
    assertValidWba(wba);
    expect(wba?.keys).toHaveLength(1);
  });
});
