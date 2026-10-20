// LICENSED_PATHS_JSON is how the licensed scope reaches the app: a list of path
// prefixes, and anything else is refused rather than read as the whole site.
// The rule that decides whether a path is inside the scope is pure logic, and
// its cases live in testdata/licensed-scope-vectors.json, which the catalog
// importer's copy of the rule reads too; tests/node/licensed-scope-vectors.test.ts
// runs them against inLicensedScope. The route-level behaviour runs on each
// runtime in tests/licensed-scope/, tests/node/fastly-entry.test.ts,
// tests/node/release-package.test.ts and tests/node/cloudflare-package.test.ts.
import { describe, expect, it } from 'vitest';

import { buildDeps, parseEnv } from '../../src/config.js';
import { BASE_SCHEMA_ENV as baseEnv } from '../helpers/test-setup.js';

const SCOPE = ['/paid/', '/q&a/'];

describe('LICENSED_PATHS_JSON', () => {
  it('carries the scope into the app', () => {
    const deps = buildDeps(parseEnv({ ...baseEnv, LICENSED_PATHS_JSON: JSON.stringify(SCOPE) }));
    expect(deps.licensedPaths).toEqual(SCOPE);
  });

  it('reads an empty list as the whole site', () => {
    const deps = buildDeps(parseEnv({ ...baseEnv, LICENSED_PATHS_JSON: '[]' }));
    expect(deps.licensedPaths).toBeUndefined();
  });

  it.each([
    ['not JSON', '/paid/'],
    ['not a list', '"/paid/"'],
    ['a relative path', '["paid/"]'],
    ['a non-string', '[1]'],
  ])('refuses %s rather than gating the whole site', (_, raw) => {
    expect(() => buildDeps(parseEnv({ ...baseEnv, LICENSED_PATHS_JSON: raw }))).toThrow();
  });
});
