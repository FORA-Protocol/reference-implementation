// Build a deployment's Lambda@Edge function in the layout the console hands
// publishers: the release bundle, and an index.mjs beside it that assigns this
// deployment's settings (Lambda@Edge has no environment variables) and
// re-exports the handler. Then smoke-invoke the result, so a config the
// worker's schema rejects fails the build, not the deploy.
//
// The bundle is built by the same code as the release bundle
// (release-targets.mjs), so nothing about the deployment is compiled into it.
// lambda-entry.mjs writes index.mjs and explains why the assignment reaches
// the handler even though the re-export is loaded first. An esbuild define is
// NOT usable to bake settings instead: the entry passes the whole process.env
// object to its parser, and a whole-object define would rewrite every
// process.env read in the bundle, dependencies included.
//
// Usage: node scripts/build-lambda-edge.mjs --config <deploy-config.json>
//        [--out-dir dist/lambda-edge]
//
// The config file is a flat JSON object of env-shaped string values
// (EXCHANGE_URL, EXCHANGE_WBA_URL, PROVIDER, EXCHANGES_JSON, and the optional
// keys the worker's EnvSchema documents). Values must be strings, because they
// stand in for environment variables. Output: <out-dir>/index.mjs and
// <out-dir>/lambda-edge.mjs; both go into the zip, and the handler string is
// "index.handler".

import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';

import { viewerRequestEvent } from './cloudfront-event.mjs';
import { LAMBDA_BUNDLE, LAMBDA_ENTRY, lambdaEntrySource } from './lambda-entry.mjs';
import { buildTarget, edgeDir, lambdaTarget } from './release-targets.mjs';

const { values } = parseArgs({
  options: {
    config: { type: 'string' },
    'out-dir': { type: 'string', default: 'dist/lambda-edge' },
  },
});

if (!values.config) {
  console.error('usage: node scripts/build-lambda-edge.mjs --config <deploy-config.json>');
  process.exit(2);
}

const outDir = resolve(edgeDir, values['out-dir']);

function loadConfig(path) {
  const parsed = JSON.parse(readFileSync(path, 'utf8'));
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    throw new Error(`${path}: config must be a flat JSON object of string values`);
  }
  const nonString = Object.entries(parsed).filter(([, v]) => typeof v !== 'string');
  if (nonString.length > 0) {
    const keys = nonString.map(([k]) => k).join(', ');
    throw new Error(
      `${path}: every value must be a string (they stand in for environment variables); not strings: ${keys}`,
    );
  }
  return parsed;
}

// Invoke the BUILT package the way Lambda will: prove the settings reach the
// env parser (a bad config throws here) and both response shapes work — a
// generated response for the manifest route, a pass-through request for an
// unsigned human read. Importing index.mjs assigns the settings onto this
// process's env; that is confined to this short-lived build process.
async function smokeInvoke(config) {
  const { handler } = await import(pathToFileURL(join(outDir, LAMBDA_ENTRY)).href);
  const origin = `https://${config.PROVIDER}`;

  const manifest = await handler(viewerRequestEvent(`${origin}/.well-known/fora.json`));
  if (manifest.status !== '200') {
    throw new Error(`smoke: fora.json answered status ${manifest.status}, want 200`);
  }
  const domain = JSON.parse(manifest.body ?? '').domain;
  if (domain !== config.PROVIDER) {
    throw new Error(`smoke: fora.json domain is ${domain}, want ${config.PROVIDER}`);
  }

  const passThrough = await handler(
    viewerRequestEvent(`${origin}/smoke-article`, {
      'user-agent': 'Mozilla/5.0 (Macintosh) Safari/605.1.15',
    }),
  );
  if (!('uri' in passThrough)) {
    throw new Error(
      'smoke: an unsigned human read must pass through as a request object so CloudFront fetches the origin',
    );
  }
}

// removeOutputs deletes both files of the package. Nothing from an earlier run
// may survive a build that fails at any step -- a bad config, a bundler error,
// a failed smoke invoke -- or anything pointing at the directory would pick up
// the previous package and deploy it as if it were this one.
function removeOutputs() {
  rmSync(join(outDir, LAMBDA_ENTRY), { force: true });
  rmSync(join(outDir, LAMBDA_BUNDLE), { force: true });
}

removeOutputs();
try {
  const config = loadConfig(values.config);
  mkdirSync(outDir, { recursive: true });
  buildTarget(lambdaTarget, outDir);
  writeFileSync(join(outDir, LAMBDA_ENTRY), lambdaEntrySource(config.PROVIDER ?? '', config));
  await smokeInvoke(config);
} catch (err) {
  removeOutputs();
  throw err;
}
console.log(`built and smoke-invoked ${join(outDir, LAMBDA_ENTRY)} and ${LAMBDA_BUNDLE}`);
