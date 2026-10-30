import { describe, expect, it } from 'vitest';

import { buildDeps, parseEnv } from '../../src/config.js';
// Minimum env the schema accepts. Tests extend this object.
import { EARLIER_RELEASE_SETTINGS } from '../helpers/earlier-release-settings.js';
import { BASE_SCHEMA_ENV as baseEnv } from '../helpers/test-setup.js';

describe('the Exchange hint', () => {
  it('sources exchangeUrl (X-FORA-Exchange hint) from EXCHANGE_URL', () => {
    const deps = buildDeps(parseEnv(baseEnv));
    expect(deps.exchangeUrl).toBe('http://exchange:8081');
  });
});

// A Worker deployed from a package an earlier release built still carries the
// settings that release made the discovery document and the RSL file from.
// This release reads none of them: they parse, are dropped, and break nothing,
// even a value the earlier release would have refused on the first request.
describe('settings an earlier release read', () => {
  it('parse, and the parsed settings carry none of them', () => {
    const env = parseEnv({ ...baseEnv, ...EARLIER_RELEASE_SETTINGS });
    for (const name of Object.keys(EARLIER_RELEASE_SETTINGS)) {
      expect(env, name).not.toHaveProperty(name);
    }
  });

  it('assemble a Worker as if they were absent', () => {
    expect(() => buildDeps(parseEnv({ ...baseEnv, ...EARLIER_RELEASE_SETTINGS }))).not.toThrow();
  });
});

// Where these throws land: buildDeps runs on the first request, not at deploy
// time, so a rejected list makes the Worker answer 500 to every request while
// `wrangler deploy` reports success. What the assertions below pin is that the
// value is REJECTED rather than trimmed or ignored — the timing is a property of
// the entries, which call buildDeps from inside fetch.
describe('bot pattern overrides', () => {
  it('compiles BOT_UA_ALLOW_JSON / BOT_UA_DENY_JSON case-insensitively', () => {
    const deps = buildDeps(
      parseEnv({
        ...baseEnv,
        BOT_UA_ALLOW_JSON: JSON.stringify(['FriendlyIndexer']),
        BOT_UA_DENY_JSON: JSON.stringify(['EvilScraper']),
      }),
    );
    expect(deps.botAllowPatterns?.[0]?.test('friendlyindexer/1.0')).toBe(true);
    expect(deps.botDenyPatterns?.[0]?.test('EVILSCRAPER/2.0')).toBe(true);
  });

  it('rejects more than 64 patterns — a bloated list throws instead of being trimmed', () => {
    const tooMany = JSON.stringify(Array.from({ length: 65 }, (_, i) => `bot-${i}`));
    expect(() => buildDeps(parseEnv({ ...baseEnv, BOT_UA_DENY_JSON: tooMany }))).toThrow();
  });

  it('rejects a pattern source over 256 chars — an oversized source throws instead of being skipped', () => {
    // Length cap only: regex STRUCTURE is not analyzed, so a short
    // backtracking pattern would pass. The comment on parseBotPatterns says
    // why and what operators must avoid.
    const oversized = JSON.stringify(['b'.repeat(300)]);
    expect(() => buildDeps(parseEnv({ ...baseEnv, BOT_UA_DENY_JSON: oversized }))).toThrow();
  });
});

describe('binding enforcement', () => {
  it('is ON when the env says nothing', () => {
    // ADR-013 D6.1: default enforce wherever the edge can run the check. The
    // default is the security posture, so an operator who sets nothing gets
    // the safe one — and the custodial flow satisfies it, because the registry
    // holds the bound key and makes the fetch itself (ADR-023).
    const deps = buildDeps(parseEnv(baseEnv));
    expect(deps.enforceBinding).toBe(true);
  });

  it('is ON when the env asks for it', () => {
    const deps = buildDeps(parseEnv({ ...baseEnv, FORA_ENFORCE_BINDING: 'true' }));
    expect(deps.enforceBinding).toBe(true);
  });

  it('can be opted down to bearer security', () => {
    // The escape hatch exists so a worker can roll AHEAD of a registry that
    // cannot yet present the bound key. With it set, a delivery URL is a plain
    // bearer credential until it expires.
    const deps = buildDeps(parseEnv({ ...baseEnv, FORA_ENFORCE_BINDING: 'false' }));
    expect(deps.enforceBinding).toBe(false);
  });

  it('rejects a value that is neither true nor false', () => {
    // A typo must throw rather than silently pick a posture. parseEnv runs on the
    // request path, so that throw reaches an operator as a 500 on every request,
    // not as a rejected deploy.
    expect(() => parseEnv({ ...baseEnv, FORA_ENFORCE_BINDING: 'yes' })).toThrow();
  });
});

describe('FORA_VERIFY_KEYS', () => {
  it('rejects malformed JSON', () => {
    expect(() => buildDeps(parseEnv({ ...baseEnv, FORA_VERIFY_KEYS: 'not-json' }))).toThrow();
  });

  it('rejects an empty array', () => {
    expect(() => buildDeps(parseEnv({ ...baseEnv, FORA_VERIFY_KEYS: '[]' }))).toThrow();
  });

  it('rejects a non-Ed25519 JWK', () => {
    const raw = JSON.stringify([{ kty: 'RSA', crv: 'Ed25519', kid: 'k', x: 'AA' }]);
    expect(() => buildDeps(parseEnv({ ...baseEnv, FORA_VERIFY_KEYS: raw }))).toThrow();
  });
});

describe('WBA_KEYS_JSON canonical JWK validation', () => {
  // A canonical OKP/Ed25519 WBA signing key: const kty/crv/use/alg, a 43-char
  // base64url x, RFC3339 validity bounds, and NO kid (named by thumbprint). Each
  // negative case mutates exactly one field away from canonical.
  const validKey = {
    kty: 'OKP',
    crv: 'Ed25519',
    use: 'sig',
    alg: 'EdDSA',
    x: 'A'.repeat(43),
    not_before: '2020-01-01T00:00:00Z',
    not_after: '2100-01-01T00:00:00Z',
  };

  it('accepts a canonical WBA key and serves it in the directory', () => {
    const raw = JSON.stringify([validKey]);
    const deps = buildDeps(parseEnv({ ...baseEnv, WBA_KEYS_JSON: raw }));
    expect(deps.wba?.keys).toHaveLength(1);
    expect(deps.wba?.keys?.[0]?.use).toBe('sig');
  });

  it('serves no WBA directory when WBA_KEYS_JSON is unset', () => {
    const deps = buildDeps(parseEnv(baseEnv));
    expect(deps.wba).toBeUndefined();
  });

  it('advertises WBA_REVOCATION_URL as the directory revocation_url', () => {
    const raw = JSON.stringify([validKey]);
    const deps = buildDeps(
      parseEnv({
        ...baseEnv,
        WBA_KEYS_JSON: raw,
        WBA_REVOCATION_URL: 'https://edge.e2e.local/.well-known/fora-key-revocations.json',
      }),
    );
    expect(deps.wba?.revocation_url).toBe(
      'https://edge.e2e.local/.well-known/fora-key-revocations.json',
    );
  });

  it('rejects use !== "sig" (canonical const)', () => {
    const raw = JSON.stringify([{ ...validKey, use: 'enc' }]);
    expect(() => buildDeps(parseEnv({ ...baseEnv, WBA_KEYS_JSON: raw }))).toThrow();
  });

  it('rejects a non-RFC3339 not_before', () => {
    const raw = JSON.stringify([{ ...validKey, not_before: 'soon' }]);
    expect(() => buildDeps(parseEnv({ ...baseEnv, WBA_KEYS_JSON: raw }))).toThrow();
  });

  it('rejects a non-RFC3339 not_after', () => {
    const raw = JSON.stringify([{ ...validKey, not_after: '2100-01-01' }]);
    expect(() => buildDeps(parseEnv({ ...baseEnv, WBA_KEYS_JSON: raw }))).toThrow();
  });
});
