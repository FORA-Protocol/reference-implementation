// The edge packages a release is made of, and the one way each is built.
//
// build-release.mjs builds every target here for the console to hand out.
// build-lambda-edge.mjs builds the Lambda@Edge target for a deployment's own
// function. Both call buildTarget, so the bundle a deployment uploads and the
// bundle a publisher downloads come from the same entry, the same platform
// preset and the same bundler settings.

import { spawnSync } from 'node:child_process';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const edgeDir = dirname(dirname(fileURLToPath(import.meta.url)));

// The two runtimes a publisher can set up themselves. Fastly is absent for the
// reason it is absent everywhere else: there is no package to install.
export const cloudflareTarget = {
  name: 'cloudflare-worker.mjs',
  entry: 'src/entries/cloudflare.ts',
  platform: 'workerd',
};
export const lambdaTarget = {
  name: 'lambda-edge.mjs',
  entry: 'src/entries/aws-lambda.ts',
  platform: 'node',
};
export const releaseTargets = [cloudflareTarget, lambdaTarget];

// buildTarget bundles one target into <outDir>/<target.name> and returns the
// path it wrote.
export function buildTarget(target, outDir) {
  const out = join(outDir, target.name);
  const result = spawnSync(
    process.execPath,
    [
      join(edgeDir, 'scripts/build-worker.mjs'),
      '--entry',
      join(edgeDir, target.entry),
      '--out',
      out,
      '--platform',
      target.platform,
    ],
    { cwd: edgeDir, stdio: 'inherit' },
  );
  if (result.status !== 0) {
    throw new Error(`bundling ${target.name} failed`);
  }
  return out;
}
