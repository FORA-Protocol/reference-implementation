// Fastly Compute entry for the FORA edge verifier.
//
// Fastly's js-compute SubtleCrypto does NOT support Ed25519 natively, so every
// Ed25519 check the shared edge app makes — the Exchange key directory's
// response signatures, the signed URL and the delivery proof — runs through the
// @noble/ed25519 primitive the production Fastly entry uses too
// (src/edge/src/ed25519-noble.ts). It is injected once, as
// AppDeps.verifyEd25519 and into the key cache.
//
// The rest of the stack (origin proxy, fora.json, bot-redirect, ACME, etc.)
// is the runtime-agnostic Hono app from @fora/edge.

/// <reference types="@fastly/js-compute" />

import { ConfigStore } from 'fastly:config-store';
import { createApp } from '@fora/edge/src/app.js';
import { nobleEd25519 } from '@fora/edge/src/ed25519-noble.js';
import { createKeyCache } from '@fora/edge/src/keys-cache.js';
import {
  type App,
  type AppDeps,
  buildPublisherManifest,
  buildPublisherWba,
  signedDirectoryFrom,
} from '@fora/edge/src/types.js';

let store: ConfigStore | undefined;
const getStore = () => {
  if (!store) store = new ConfigStore('fora_edge');
  return store;
};

const readEnv = (name: string, fallback = '') => getStore().get(name) || fallback;
const requireEnv = (name: string) => {
  const v = getStore().get(name);
  if (!v) throw new Error(`missing env ${name}`);
  return v;
};

// Fastly's fetch needs an explicit backend; the WBA directory always lives on
// the exchange. The key cache calls this with its own init.
const exchangeFetch: typeof fetch = (input, init) =>
  fetch(input, { ...init, backend: 'exchange' } as RequestInit & { backend: string });

// The production key cache: the SDK's directory reader, keys kept as raw bytes,
// and the directory's response signatures checked through @noble/ed25519.
function resolveKeyFrom(wbaUrl: string): AppDeps['resolveKey'] {
  const keys = createKeyCache({ wbaUrl, fetcher: exchangeFetch, verifyEd25519: nobleEd25519 });
  return (kid) => keys.resolve(kid);
}

// ---------------------------------------------------------------------------
// App wiring — lazy, so Wizer's build-time module init doesn't read env.
// ---------------------------------------------------------------------------

interface RawExchange {
  domain: string;
  endpoint: string;
  supported_profiles?: string[];
}

let cachedApp: App | undefined;

function getApp(): App {
  if (cachedApp) return cachedApp;

  const exchangeUrl = requireEnv('EXCHANGE_URL');
  const wbaUrl = requireEnv('EXCHANGE_WBA_URL');
  const originUrl = requireEnv('ORIGIN_URL');
  const provider = requireEnv('PROVIDER');
  const rawExchanges: RawExchange[] = JSON.parse(requireEnv('EXCHANGES_JSON'));
  // Authorized third-party catalog pushers (optional). Consumed by the
  // Exchange's contributor-authorization gate.
  const rawContributors = readEnv('CATALOG_CONTRIBUTORS_JSON');
  const catalogContributors = rawContributors ? JSON.parse(rawContributors) : undefined;

  // This publisher's self-publish signing key(s), served in the WBA directory's
  // keys[] (no kid) so the Exchange learns them via the well-known fetch (Gate-1
  // self-signup) instead of a DB pre-seed. Optional — omitted when no
  // key is provisioned (no WBA directory is then served).
  const rawWbaKeys = readEnv('WBA_KEYS_JSON');
  const wbaKeys = rawWbaKeys ? JSON.parse(rawWbaKeys) : undefined;
  const wbaRevocationUrl = readEnv('WBA_REVOCATION_URL') || undefined;

  // Shared with production (src/edge/src/types.ts) so the shim cannot drift.
  const manifest = buildPublisherManifest(provider, rawExchanges, catalogContributors);
  const wba = wbaKeys ? buildPublisherWba(wbaKeys, wbaRevocationUrl) : undefined;
  // The same directory pre-signed offline for this host, served verbatim in its
  // place when set (the edge holds no private key to sign it with).
  const rawSigned = readEnv('WBA_SIGNED_DIRECTORY_JSON');
  const wbaSigned = rawSigned ? signedDirectoryFrom(rawSigned) : undefined;

  const fastlyFetcher: typeof fetch = (input, init) => {
    const url = typeof input === 'string' ? input : input.url;
    const backend = url.startsWith(exchangeUrl) ? 'exchange' : 'publisher';
    return fetch(input, { ...init, backend } as RequestInit & { backend: string });
  };

  const deps: AppDeps = {
    manifest,
    exchangeUrl,
    resolveKey: resolveKeyFrom(wbaUrl),
    // The signed-URL check and the delivery-proof check run through this. Left
    // unset, the app falls back to WebCrypto, whose Ed25519 import throws
    // NotSupportedError on this runtime: every signed URL, bound or not, would
    // answer 503 verify_unavailable, because the URL is checked before any
    // proof. That fault stays a 503 and never reads as a genuine refusal: the
    // edge's primitives refuse only a signature that is not 64 bytes, and let
    // every other failure through.
    verifyEd25519: nobleEd25519,
    // Same default as production config.ts: enforce unless explicitly opted down.
    enforceBinding: readEnv('FORA_ENFORCE_BINDING') !== 'false',
    originUrl,
    fetcher: fastlyFetcher,
    rslBody: readEnv('RSL_BODY'),
    ...(wba !== undefined ? { wba } : {}),
    ...(wbaSigned !== undefined ? { wbaSigned } : {}),
  };

  cachedApp = createApp(deps);
  return cachedApp;
}

addEventListener('fetch', (event) => {
  event.respondWith(getApp().fetch(event.request));
});
