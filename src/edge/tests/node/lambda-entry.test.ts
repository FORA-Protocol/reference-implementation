// The Lambda@Edge index.mjs has two renderers over one template
// (edgerelease/lambda-entry.mjs.tmpl): edgerelease.LambdaEntry in Go, which the
// console uses for a publisher's download, and lambdaEntrySource here, which a
// deployment's own build uses. The Go side is held to
// edgerelease/testdata/lambda-index.mjs.golden by a Go test; this suite holds the edge
// side to the same file. So the two cannot write different entries without one
// of the two suites failing.
//
// The golden's settings include an "&". Go's JSON encoder escapes it unless
// told not to and JSON.stringify never does, so this is the value that proves
// the two agree on escaping.
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import { lambdaEntrySource } from '../../scripts/lambda-entry.mjs';
import { goldenPath } from '../helpers/release-build.js';

const golden = readFileSync(goldenPath('lambda-index.mjs.golden'), 'utf8');

// The settings the golden assigns, read back out of it. The JSON sits between
// the assignment's opening and its closing parenthesis.
function goldenSettings(): Record<string, string> {
  const match = /Object\.assign\(process\.env, (\{[\s\S]*?\n\})\);/.exec(golden);
  if (!match?.[1]) throw new Error('the golden assigns no settings');
  return JSON.parse(match[1]) as Record<string, string>;
}

describe('lambdaEntrySource', () => {
  it('writes the golden entry from the golden settings', () => {
    const settings = goldenSettings();
    expect(settings.LICENSED_PATHS_JSON).toContain('&');
    expect(lambdaEntrySource('www.publisher.example', settings)).toBe(golden);
  });

  it('writes the settings in sorted order whatever order they arrive in', () => {
    const settings = goldenSettings();
    const reversed = Object.fromEntries(Object.entries(settings).reverse());
    expect(lambdaEntrySource('www.publisher.example', reversed)).toBe(golden);
  });

  it('writes a value that looks like a placeholder as it is', () => {
    const entry = lambdaEntrySource('a.example', { PROVIDER: '{{BUNDLE}}' });
    expect(entry).toContain('"PROVIDER": "{{BUNDLE}}"');
  });

  it('refuses a domain that could end the comment it is written in', () => {
    expect(() => lambdaEntrySource('a.example\nglobalThis.stolen = 1;', {})).toThrow();
  });
});
