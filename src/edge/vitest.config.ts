import { cloudflareTest } from '@cloudflare/vitest-pool-workers';
import { defineConfig } from 'vitest/config';

import { signWbaDirectory } from './scripts/sign-wba-directory.mjs';
import { WORKERS_COMPAT } from './scripts/workers-compat.mjs';

// One config, six projects (vitest `test.projects`) — each project's
// membership is its test DIRECTORY, so there are no hand-mirrored
// include/exclude lists to keep in sync:
//
//   tests/workers/            real Workers runtime, binding enforcement ON,
//                             origin configured (mocked per test via the shared
//                             fetch-mock helper)
//   tests/article-path/       same runtime, the shared-URL four-outcome
//                             acceptance suite against a mocked origin backend
//   tests/wellknown-keyless/  same runtime, the publisher that issues no signing
//                             keys: the discovery surface with WBA_KEYS_JSON
//                             deliberately unset
//   tests/licensed-scope/     same runtime, a publisher that licenses only
//                             /article/: the bot gate applies there alone
//   tests/wellknown-signed/   same runtime, a publisher whose key directory is
//                             served pre-signed (WBA_SIGNED_DIRECTORY_JSON)
//   tests/node/               plain Node: fs-reading parity/schema guards, the
//                             console-spy log suites, and the programmatic AWS
//                             Lambda / Fastly harnesses (no CLI dependencies)
//
// Run one project with `vitest run --project <name>`.

// WORKERS_COMPAT is the runtime every generated wrangler.toml carries; see
// scripts/workers-compat.mjs.

// Bindings shared by every Workers-runtime posture. Origin-mode note: the
// Cloudflare entry refuses to run without ORIGIN_URL or SAME_ZONE_ORIGIN;
// suites mock the origin host via the shared fetch-mock helper
// (setupOriginMock or per-test intercepts).
const BASE_BINDINGS = {
  EXCHANGE_URL: 'https://exchange.test',
  EXCHANGE_WBA_URL: 'https://exchange.test/.well-known/http-message-signatures-directory',
  RSL_BODY: '# test rsl',
  ACME_TOKENS_JSON: '{"demo":"response-body"}',
  PROVIDER: 'pub.test',
  EXCHANGES_JSON:
    '[{"domain":"exchange.test","endpoint":"https://exchange.test","supported_profiles":["fora-news-v1"]}]',
  ORIGIN_URL: 'https://origin.pub.test',
};

// The publisher edge's own WBA directory (a self-signed signing key, no kid),
// served at /.well-known/http-message-signatures-directory.
const WBA_KEYS_JSON =
  '[{"kty":"OKP","crv":"Ed25519","use":"sig","alg":"EdDSA","x":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","not_before":"2020-01-01T00:00:00Z","not_after":"2100-01-01T00:00:00Z"}]';

// A Workers-runtime project: the name doubles as the test directory, the
// bindings define the posture. cloudflareTest is the pool-workers Vite
// plugin (the vitest-4 replacement for defineWorkersProject).
function workersProject(name: string, bindings: Record<string, string>) {
  return {
    plugins: [
      cloudflareTest({
        wrangler: { configPath: './wrangler.toml' },
        miniflare: { ...WORKERS_COMPAT, bindings },
      }),
    ],
    test: {
      name,
      include: [`tests/${name}/**/*.test.ts`],
    },
  };
}

// The pre-signed directory the wellknown-signed project serves, made the way a
// deployment makes it: a publisher key, and the production signing tool run
// with its private half for the host the suites fetch from (PUB_ORIGIN's).
// The private key lives only in this process.
async function signedDirectoryBindings(): Promise<Record<string, string>> {
  const pair = (await crypto.subtle.generateKey({ name: 'Ed25519' }, false, [
    'sign',
    'verify',
  ])) as CryptoKeyPair;
  const x = Buffer.from(await crypto.subtle.exportKey('raw', pair.publicKey)).toString('base64url');
  const keys = [
    {
      kty: 'OKP' as const,
      crv: 'Ed25519' as const,
      use: 'sig' as const,
      alg: 'EdDSA' as const,
      x,
      not_before: '2020-01-01T00:00:00Z',
      not_after: '2100-01-01T00:00:00Z',
    },
  ];
  const signed = await signWbaDirectory({
    authority: 'pub.example.com',
    keys,
    signers: [{ x, privKey: pair.privateKey }],
    validDays: 1,
    now: Math.floor(Date.now() / 1000),
  });
  return { WBA_KEYS_JSON: JSON.stringify(keys), WBA_SIGNED_DIRECTORY_JSON: JSON.stringify(signed) };
}

export default defineConfig(async () => ({
  test: {
    projects: [
      // No project sets FORA_ENFORCE_BINDING, deliberately: leaving it unset is
      // what makes these suites run against the PRODUCTION default (on), so a
      // change to that default surfaces here rather than only in config.test.ts.
      workersProject('workers', { ...BASE_BINDINGS, WBA_KEYS_JSON }),
      workersProject('article-path', BASE_BINDINGS),
      // BASE_BINDINGS WITHOUT WBA_KEYS_JSON is the configuration under test here,
      // not an omission: a publisher that issues no signing keys must answer 404
      // at the key directory, and four operator documents tell people that answer
      // is correct. Adding WBA_KEYS_JSON to BASE_BINDINGS would turn that 404
      // into a 200 and break this project.
      workersProject('wellknown-keyless', BASE_BINDINGS),
      // A publisher that licenses part of its site: the bot gate applies under
      // /article/ only, and the rest passes to the origin.
      workersProject('licensed-scope', {
        ...BASE_BINDINGS,
        LICENSED_PATHS_JSON: JSON.stringify(['/article/']),
      }),
      // A publisher whose directory is served pre-signed, the posture the Web
      // Bot Auth profile requires of a directory a reader takes keys from.
      workersProject('wellknown-signed', {
        ...BASE_BINDINGS,
        ...(await signedDirectoryBindings()),
      }),
      {
        test: {
          name: 'node',
          environment: 'node',
          include: ['tests/node/**/*.test.ts'],
        },
      },
    ],
  },
}));
