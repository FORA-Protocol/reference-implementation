// Build the edge packages a publisher installs, once per release.
//
// Nothing about a publisher is in them. The Cloudflare worker reads its
// settings from the bindings the platform hands it. The Lambda@Edge bundle
// reads process.env, and the console puts an index.mjs beside it that assigns
// that publisher's settings and re-exports the handler. So one build serves
// every publisher, and what they install has a version and a checksum rather
// than being a one-off nobody can identify afterwards.
//
// Usage: node scripts/build-release.mjs [--out dist/release]
import { createHash } from 'node:crypto';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { parseArgs } from 'node:util';

import { buildTarget, edgeDir, releaseTargets } from './release-targets.mjs';

const { values } = parseArgs({
  options: { out: { type: 'string', default: 'dist/release' } },
});
// resolve, not join: an absolute --out is a path, not a suffix.
const outDir = resolve(edgeDir, values.out);
mkdirSync(outDir, { recursive: true });

const checksums = [];
for (const target of releaseTargets) {
  const body = readFileSync(buildTarget(target, outDir));
  checksums.push(`${createHash('sha256').update(body).digest('hex')}  ${target.name}`);
}

// What a publisher can check their download against, and what support can ask
// for when someone reports that the thing they installed misbehaves.
writeFileSync(resolve(outDir, 'SHA256SUMS'), `${checksums.join('\n')}\n`);
console.log(`\n${checksums.join('\n')}`);
console.log(`\nwrote ${releaseTargets.length} packages and SHA256SUMS to ${outDir}`);
