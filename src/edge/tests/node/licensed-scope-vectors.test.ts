import { readFileSync } from 'node:fs';
import { URL, fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';
import { z } from 'zod';

import { inLicensedScope } from '../../src/licensed-scope.js';

// The licensed-scope check is pure logic whose branches are the point: every
// case is a spelling of a path that either reaches the bot gate or skips it,
// and a wrong "outside" answer hands licensed content to an AI bot for free.
// The cases live in a repo-root file that the catalog importer's copy of the
// rule (internal/licensedscope) is pinned to as well, so the two cannot drift
// apart. The file is read through node:fs, which is why this runs in the node
// project rather than the Workers pool. The route-level behaviour runs on each
// runtime in tests/licensed-scope/, tests/node/fastly-entry.test.ts,
// tests/node/release-package.test.ts and tests/node/cloudflare-package.test.ts.
const vectorsPath = fileURLToPath(
  new URL('../../../../testdata/licensed-scope-vectors.json', import.meta.url),
);

const VERDICTS = ['inside', 'outside', 'undecidable'] as const;

// Strict on both levels, as the Go reader is: an unknown key is a typo in a
// case, and a typo that silently drops a field would leave the case testing
// nothing.
const vectorFile = z
  .object({
    comment: z.array(z.string()),
    vectors: z.array(
      z
        .object({
          name: z.string().min(1),
          scope: z.array(z.string()).nullable(),
          path: z.string(),
          verdict: z.enum(VERDICTS),
        })
        .strict(),
    ),
  })
  .strict();

const { vectors } = vectorFile.parse(JSON.parse(readFileSync(vectorsPath, 'utf8')));

describe('inLicensedScope — shared licensed-scope vectors', () => {
  it('has vectors to check', () => {
    expect(vectors.length).toBeGreaterThan(0);
  });

  it('names every vector once', () => {
    const names = vectors.map((v) => v.name);
    expect(new Set(names).size).toBe(names.length);
  });

  it('covers every verdict', () => {
    expect(new Set(vectors.map((v) => v.verdict))).toEqual(new Set(VERDICTS));
  });

  // The edge sends a bot to negotiate for an undecidable path as it does for an
  // inside one, because it fails closed: only "outside" lets the read through.
  for (const v of vectors) {
    it(v.name, () => {
      expect(inLicensedScope(v.path, v.scope ?? undefined)).toBe(v.verdict !== 'outside');
    });
  }
});
