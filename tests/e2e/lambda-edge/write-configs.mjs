// Write the per-deployment config files the Lambda@Edge build writes into
// each package's index.mjs. Lambda@Edge has no environment variables, so every value a normal
// edge deployment reads from the environment is fixed at build time by
// scripts/build-lambda-edge.mjs; this script is the E2E stack's stand-in for
// whatever a real deployment pipeline would render.
//
// Two configs, because the WBA directory route has two postures and one
// container can serve only one handler:
//
//   with-wba.json — the publisher publishes a signing key, so the directory is
//                   served, pre-signed for the publisher's host
//                   (WBA_SIGNED_DIRECTORY_JSON) by the production signing tool.
//   no-wba.json   — the publisher publishes no key, so the route answers 404.
//
// Everything else is identical, so a difference between the two containers can
// only come from that one setting.
//
// Usage: node write-configs.mjs --out-dir <dir> --edge-dir <src/edge with node_modules>

import { generateKeyPairSync } from 'node:crypto';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';

const { values } = parseArgs({
  options: { 'out-dir': { type: 'string' }, 'edge-dir': { type: 'string' } },
});
if (!values['out-dir'] || !values['edge-dir']) {
  console.error('usage: node write-configs.mjs --out-dir <dir> --edge-dir <dir>');
  process.exit(2);
}
// The edge package's directory signing tool, which bundles the pinned SDK's
// signDirectoryResponse out of that package's node_modules.
const { signWbaDirectory } = await import(
  pathToFileURL(join(resolve(values['edge-dir']), 'scripts/sign-wba-directory.mjs')).href
);

// The publisher this deployment fronts. It is NOT a compose network alias:
// the Lambda runtime emulator speaks only the Lambda invoke protocol, so no
// plain HTTP traffic is ever routed to this service by hostname. The name
// still has to be a real publisher identity because it is what fora.json
// advertises as `domain` and what the delivery URLs are signed over.
const PROVIDER = 'lambda.demo.fora-protocol.org';

// exchange-a. The delivery-URL verify key is resolved by fetching this
// directory over the compose network — the production key-resolution path,
// with no pre-provisioned FORA_VERIFY_KEYS shortcut.
const EXCHANGE_URL = 'http://exchange:8081';
const EXCHANGE_WBA_URL = `${EXCHANGE_URL}/.well-known/http-message-signatures-directory`;

// No ORIGIN_URL and no SAME_ZONE_ORIGIN: this is the CloudFront-native
// posture, where CloudFront owns the origin fetch and the function answers an
// authorized read by handing the request back to the CDN. That is the answer
// shape the E2E asserts on — a returned request object rather than a
// generated response.
const shared = {
  EXCHANGE_URL,
  EXCHANGE_WBA_URL,
  PROVIDER,
  EXCHANGES_JSON: JSON.stringify([
    {
      domain: 'exchange:8081',
      endpoint: EXCHANGE_URL,
      supported_profiles: ['fora-news-v1', 'fora-comp-v1', 'fora-music-v1'],
      ext: { resource_owner_id: 'demo-resource-owner' },
    },
  ]),
  // Stated explicitly rather than left to the default, like the other edges in
  // the stack: proof-of-possession enforcement is what two of the E2E cases
  // are about, and a test whose subject is implied by a default proves less
  // than one whose subject is written down.
  FORA_ENFORCE_BINDING: 'true',
  RSL_BODY: '# e2e rsl lambda',
};

// A fresh publisher signing key per image build. The public half is published
// in the directory, and the private half signs the directory response here, at
// build time, and is then discarded: the deployed function holds no private key,
// as a real deployment's does not. Generating it here keeps key material out of
// git without adding a provisioning step. The validity window mirrors the shape
// the canonical WBA schema requires (RFC 3339, half-open); the edge ignores these
// bounds on the verify path, where the signed URL's own `exp` governs expiry.
async function wbaSettings() {
  const { privateKey } = generateKeyPairSync('ed25519');
  const jwk = privateKey.export({ format: 'jwk' });
  const now = new Date();
  const notAfter = new Date(now.getTime() + 365 * 24 * 60 * 60 * 1000);
  const keys = [
    {
      kty: 'OKP',
      crv: 'Ed25519',
      use: 'sig',
      alg: 'EdDSA',
      x: jwk.x,
      not_before: now.toISOString(),
      not_after: notAfter.toISOString(),
    },
  ];
  const privKey = await crypto.subtle.importKey('jwk', jwk, { name: 'Ed25519' }, false, ['sign']);
  const signed = await signWbaDirectory({
    authority: PROVIDER,
    keys,
    signers: [{ x: jwk.x, privKey }],
    validDays: 365,
    now: Math.floor(now.getTime() / 1000),
  });
  return { WBA_KEYS_JSON: JSON.stringify(keys), WBA_SIGNED_DIRECTORY_JSON: JSON.stringify(signed) };
}

const outDir = values['out-dir'];
mkdirSync(outDir, { recursive: true });
writeFileSync(
  join(outDir, 'with-wba.json'),
  `${JSON.stringify({ ...shared, ...(await wbaSettings()) }, null, 2)}\n`,
);
writeFileSync(join(outDir, 'no-wba.json'), `${JSON.stringify(shared, null, 2)}\n`);
console.log(`wrote with-wba.json and no-wba.json to ${outDir}`);
