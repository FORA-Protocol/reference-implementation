import { generateKeyPairSync } from 'node:crypto';

import { describe, expect, it } from 'vitest';

import { WBA_DIRECTORY_MEDIA_TYPE } from '@fora-protocol/sdk/resolvers/edge';
import { WellKnownManifestVersion } from '@fora-protocol/sdk/wire';
import { buildDeps, parseEnv } from '../../src/config.js';
import { MAX_WBA_KEYS } from '../../src/keys.js';
import {
  WBA_DIRECTORY_CONTENT_TYPE,
  type WbaKey,
  buildPublisherManifest,
  buildPublisherWba,
} from '../../src/types.js';
import { assertValidManifest, assertValidWba } from '../helpers/wellknown-schema.js';

describe('edge publisher overlay manifest conforms to the canonical FORA schema', () => {
  // The smallest manifest this worker serves. One builder rather than a copy per
  // test: the shape is the subject of every assertion below, so a member added
  // to it has to reach all of them at once.
  const minimalManifest = () =>
    buildPublisherManifest('pub.example', [
      { domain: 'x.example', endpoint: 'https://x.example/v1' },
    ]);

  it('buildPublisherManifest with exchanges, profiles, and contributors', () => {
    const m = buildPublisherManifest(
      'pub.example',
      [
        {
          domain: 'x.example',
          endpoint: 'https://x.example/v1',
          supported_profiles: ['fora-news-v1'],
        },
      ],
      [{ domain: 'pub.example', relationship: 'publisher' }],
    );
    assertValidManifest(m);
    expect(m.role).toBe('ROLE_PUBLISHER');
    expect(m.supported_profiles).toEqual(['fora-news-v1']);
  });

  it('buildPublisherManifest minimal (no profiles, no contributors)', () => {
    assertValidManifest(minimalManifest());
  });

  it('stamps the manifest document version the SDK owns', () => {
    // well-known.mjs is plain ESM shared with the raw-Node AWS shim, which cannot
    // import the SDK's TypeScript source, so the literal there mirrors the SDK
    // constant rather than importing it. This is the check that keeps the mirror
    // honest: a bump in the SDK that the literal did not follow fails here.
    expect(minimalManifest().ver).toBe(WellKnownManifestVersion);
  });

  it('the WBA directory media type literal is the SDK media type', () => {
    // The same mirror, for the media type the edge serves its directory under and
    // requires of the Exchange's. The SDK names it in its edge resolvers entry,
    // the one an edge runtime imports. The worker itself does not import that
    // entry, because doing so would bundle every strict schema the readers
    // check, so this test is where the literal and the SDK constant are compared.
    expect(WBA_DIRECTORY_MEDIA_TYPE).toBe('application/http-message-signatures-directory+json');
    expect(WBA_DIRECTORY_CONTENT_TYPE).toBe(WBA_DIRECTORY_MEDIA_TYPE);
  });

  it('the canonical schema does not adjudicate the version at all', () => {
    // This is the property the relaxation buys, and asserting the accepted minor
    // alone would not show it: ver is typed as a plain string, so '1.1' passes
    // against a schema tightened back to a "^1\\." pattern too, and that test
    // would go on passing if the rule moved back into the schema.
    //
    // What must hold is that a WRONG MAJOR also validates here. The version rule
    // belongs to the consumer, applied in code before the schema runs, and a
    // document at an unaccepted major has to reach that gate to be classified as
    // a version refusal. A schema that rejected it first would report the same
    // document as a SHAPE failure, which is a different answer to the caller and
    // sends a settled verdict down the retryable path.
    const m = minimalManifest();
    for (const ver of ['1.1', '1.99', '2.0', '10.0']) {
      assertValidManifest({ ...m, ver });
    }
  });

  it('the canonical schema still requires ver to be present', () => {
    const { ver: _dropped, ...withoutVer } = minimalManifest();
    // Asserted on the reported violation, not merely on the helper's prefix:
    // that prefix is what EVERY schema failure carries, so matching it alone
    // would pass if this document were rejected for some other missing member.
    expect(() => assertValidManifest(withoutVer)).toThrow(/ver/);
  });

  it('overlay manifest carries no identity keys (WBA split)', () => {
    const m = minimalManifest() as unknown as Record<string, unknown>;
    expect(m.public_keys).toBeUndefined();
    expect(m.invalidation_url).toBeUndefined();
  });

  it('buildDeps manifest assembled from env', () => {
    const env = parseEnv({
      EXCHANGE_URL: 'https://exchange.example',
      EXCHANGE_WBA_URL: 'https://exchange.example/.well-known/http-message-signatures-directory',
      PROVIDER: 'pub.example',
      EXCHANGES_JSON: JSON.stringify([
        {
          domain: 'x.example',
          endpoint: 'https://x.example/v1',
          supported_profiles: ['fora-news-v1'],
        },
      ]),
      CATALOG_CONTRIBUTORS_JSON: JSON.stringify([
        { domain: 'pub.example', relationship: 'publisher' },
      ]),
    });
    assertValidManifest(buildDeps(env).manifest);
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
      PROVIDER: 'pub.example',
      EXCHANGES_JSON: JSON.stringify([{ domain: 'x.example', endpoint: 'https://x.example/v1' }]),
      WBA_KEYS_JSON: JSON.stringify([sampleKey]),
    });
    const wba = buildDeps(env).wba;
    expect(wba).toBeDefined();
    assertValidWba(wba);
    expect(wba?.keys).toHaveLength(1);
  });
});
