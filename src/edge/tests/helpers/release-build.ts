// Builds the release packages into a fresh temporary directory, the way the
// console's image does (scripts/build-release.mjs), for the suites that run a
// golden from edgerelease/testdata against a real release bundle.
import { execFileSync } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const edgeDir = fileURLToPath(new URL('../..', import.meta.url).href);

// goldenPath names one of the goldens edgerelease's Go tests pin.
export function goldenPath(name: string): string {
  return join(edgeDir, 'edgerelease/testdata', name);
}

// buildRelease returns the directory it built into. The caller removes it.
export function buildRelease(): string {
  const out = mkdtempSync(join(tmpdir(), 'fora-release-'));
  execFileSync(process.execPath, [join(edgeDir, 'scripts/build-release.mjs'), '--out', out], {
    cwd: edgeDir,
    stdio: 'pipe',
  });
  return out;
}
