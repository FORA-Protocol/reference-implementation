// Produce WBA_SIGNED_DIRECTORY_JSON: this publisher's Web Bot Auth key directory
// as a signed response the edge serves byte for byte.
//
// The Web Bot Auth profile requires a key directory response to carry a
// signature by every key it lists (tag "http-message-signatures-directory",
// covering "@authority";req and content-digest), and the edge holds no private
// key. So whoever holds the key signs the directory here, offline, with the
// SDK's signDirectoryResponse, and hands the edge the result as configuration.
// "@authority";req is the host the directory is fetched from, so the value is
// made for one host: re-run this for each host the edge answers under, after
// any change to the keys, and before the signature window closes.
//
// Usage:
//   node scripts/sign-wba-directory.mjs --authority <host[:port]> \
//     --keys '<WBA_KEYS_JSON>' --signing-key <file> [--signing-key <file> ...] \
//     [--revocation-url <url>] [--valid-days <n>]
//
//   --authority      the host the directory is served under, as a client names
//                    it: lowercase, no port when it is the scheme's default
//   --keys           the WBA_KEYS_JSON value the edge is configured with
//   --signing-key    a JSON file holding one listed key's private half: a JWK
//                    {kty, crv, d, x}, or {private_key, public_key} (raw seed and
//                    public key, base64url). One per listed key.
//   --revocation-url the WBA_REVOCATION_URL value, when the edge sets one
//   --valid-days     how long the response signatures stay valid (default 30)
//
// Prints the WBA_SIGNED_DIRECTORY_JSON value on stdout. The private key is read
// from the file and never printed.
//
// The SDK ships as TypeScript source, which Node does not load from
// node_modules, so the two SDK functions this needs are bundled with esbuild
// (already a dependency of this package) when the script runs.

import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { parseArgs } from 'node:util';
import { build } from 'esbuild';

import { buildPublisherWba } from '../src/well-known.mjs';

const EDGE_DIR = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const DEFAULT_VALID_DAYS = 30;
const DAY_SEC = 24 * 60 * 60;

// The SDK's signDirectoryResponse and thumbprint, loaded from the pinned package.
async function loadSdk() {
  const out = await build({
    stdin: {
      contents:
        "export { signDirectoryResponse } from '@fora-protocol/sdk/identity';\n" +
        "export { thumbprint } from '@fora-protocol/sdk/thumbprint';\n",
      resolveDir: EDGE_DIR,
      loader: 'js',
    },
    bundle: true,
    write: false,
    format: 'esm',
    platform: 'node',
    target: 'node22',
    logLevel: 'error',
  });
  const code = out.outputFiles[0].text;
  return import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`);
}

function base64urlToBytes(value) {
  return new Uint8Array(Buffer.from(value, 'base64url'));
}

// readSigningKey accepts a private JWK or the {private_key, public_key} fixture
// shape, and returns the public key's `x` and a non-extractable signing key.
async function readSigningKey(path) {
  const spec = JSON.parse(readFileSync(path, 'utf8'));
  const d = spec.d ?? spec.private_key;
  const x = spec.x ?? spec.public_key;
  if (typeof d !== 'string' || typeof x !== 'string') {
    throw new Error(`${path}: expected a private Ed25519 JWK {d, x} or {private_key, public_key}`);
  }
  const privKey = await crypto.subtle.importKey(
    'jwk',
    { kty: 'OKP', crv: 'Ed25519', d, x },
    { name: 'Ed25519' },
    false,
    ['sign'],
  );
  return { x, privKey };
}

/**
 * signWbaDirectory returns the WBA_SIGNED_DIRECTORY_JSON value, as an object, for
 * `keys` (the WBA_KEYS_JSON array) served under `authority`, signed by
 * `signers` ({x, privKey}, one per listed key). `now` is unix seconds.
 */
export async function signWbaDirectory({
  authority,
  keys,
  revocationUrl,
  signers,
  validDays,
  now,
}) {
  const sdk = await loadSdk();
  const listed = new Set(keys.map((k) => k.x));
  for (const s of signers) {
    if (!listed.has(s.x)) throw new Error(`signing key ${s.x} is not one of the listed keys`);
  }
  if (signers.length !== listed.size) {
    throw new Error(
      `the directory lists ${listed.size} key(s) and ${signers.length} signed it; every listed key signs`,
    );
  }
  const body = JSON.stringify(buildPublisherWba(keys, revocationUrl));
  const created = now - 60;
  const sig = await sdk.signDirectoryResponse(
    authority,
    new Uint8Array(new TextEncoder().encode(body)),
    await Promise.all(
      signers.map(async (s) => ({
        privKey: s.privKey,
        keyid: await sdk.thumbprint(base64urlToBytes(s.x)),
      })),
    ),
    created,
    created + validDays * DAY_SEC,
  );
  return {
    body,
    content_digest: sig.contentDigest,
    signature_input: sig.signatureInput,
    signature: sig.signature,
  };
}

async function main() {
  const { values } = parseArgs({
    options: {
      authority: { type: 'string' },
      keys: { type: 'string' },
      'signing-key': { type: 'string', multiple: true },
      'revocation-url': { type: 'string' },
      'valid-days': { type: 'string' },
    },
  });
  if (!values.authority || !values.keys || !values['signing-key']?.length) {
    console.error(
      'usage: node scripts/sign-wba-directory.mjs --authority <host[:port]> --keys <WBA_KEYS_JSON> --signing-key <file> [...]',
    );
    process.exit(2);
  }
  const validDays = Number(values['valid-days'] ?? DEFAULT_VALID_DAYS);
  if (!Number.isInteger(validDays) || validDays <= 0) {
    console.error(`--valid-days must be a positive whole number, not ${values['valid-days']}`);
    process.exit(2);
  }
  const value = await signWbaDirectory({
    authority: values.authority,
    keys: JSON.parse(values.keys),
    revocationUrl: values['revocation-url'],
    signers: await Promise.all(values['signing-key'].map(readSigningKey)),
    validDays,
    now: Math.floor(Date.now() / 1000),
  });
  process.stdout.write(`${JSON.stringify(value)}\n`);
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  await main();
}
