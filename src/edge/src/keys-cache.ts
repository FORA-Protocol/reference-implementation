// The Exchange's verify-key cache. It resolves a delivery URL's keyid — the RFC
// 7638 thumbprint of an Ed25519 key, since a Web Bot Auth key carries no kid —
// to the raw 32-byte public key, from optional pre-provisioned static keys or
// from the Exchange's Web Bot Auth directory
// (/.well-known/http-message-signatures-directory). It caches the directory's
// keys with a TTL, de-duplicates concurrent loads into one fetch, and fetches
// once more when a keyid misses the static set (rotation self-heal).
//
// The directory is read by the SDK's edge-entry reader
// (createWBAOfferDirectoryFetch from @fora-protocol/sdk/resolvers/edge), the
// same read the SDK's key resolvers make. It applies the Web Bot Auth profile:
// the directory answers 200 itself (a redirect is refused, never followed), it
// is served as application/http-message-signatures-directory+json, and the file
// it returns lists only the keys that signed the response (tag
// "http-message-signatures-directory", covering "@authority";req and
// content-digest), so a listed key that did not sign is never handed out while
// the keys that did still resolve. A member this edge does not know is a newer
// minor version of the directory, not a reason to stop verifying, so the read
// is lenient about unknown members. The response signatures are checked through
// `verifyEd25519`, which a runtime without WebCrypto Ed25519 (Fastly Compute)
// supplies. The SDK reader bounds the read itself — a body over 1 MiB is
// refused, the whole read ends at 30 seconds, and a key directory is never
// fetched through a redirect — and needs no AbortController, which Fastly
// Compute lacks. This module keeps a tighter cap of its own on the body.
//
// Keys are kept as raw bytes on every runtime, so the same Ed25519 primitive
// checks the directory, the signed URL and the delivery proof.

import { decodeBase64Url } from '@fora-protocol/sdk/base64url';
import type { Ed25519Verify } from '@fora-protocol/sdk/pop';
import {
  type FetchInit,
  type FetchLike,
  WBA_DIRECTORY_MEDIA_TYPE,
  WBA_DIRECTORY_PATH,
  createWBAOfferDirectoryFetch,
} from '@fora-protocol/sdk/resolvers/edge';
import { thumbprint } from '@fora-protocol/sdk/thumbprint';

import { logRecord } from './log.js';

export interface KeyCache {
  resolve(keyid: string | undefined): Promise<Uint8Array<ArrayBuffer> | undefined>;
  refresh(): Promise<void>;
}

// The one member of a JWK the cache reads: `x`, the base64url encoding of the
// raw Ed25519 public key.
export interface PublicJwk {
  x: string;
}

export interface KeyCacheDeps {
  // Absolute URL of the Exchange's WBA directory. Its path must be the Web Bot
  // Auth directory path, because the reader builds the URL from the authority.
  wbaUrl: string;
  // Optional pre-provisioned verify keys (D5). When supplied and they cover the
  // requested keyid, the directory is NOT fetched. A keyid that misses the
  // static set triggers a one-shot directory fetch (rotation self-heal).
  staticKeys?: PublicJwk[];
  fetcher?: typeof fetch;
  now?: () => number;
  ttlMs?: number;
  // The Ed25519 primitive the directory response signatures are verified with.
  // Omitted, the SDK's WebCrypto default applies.
  verifyEd25519?: Ed25519Verify;
}

interface Imported {
  keys: Map<string, Uint8Array<ArrayBuffer>>;
  defaultKeyId?: string;
}

// Upper bound on a fetched WBA directory, mirroring the Go loader's 64 KiB cap
// (internal/forawellknown/fetch.go `maxDocBytes`). The SDK reader refuses a body
// over 1 MiB itself; this cap is the tighter of the two, so a directory body over
// 64 KiB, far more than a handful of keys, is refused here before it is decoded.
const MAX_WBA_DIRECTORY_BYTES = 64 * 1024;

// readBoundedBytes reads a fetch Response body while refusing to buffer more
// than maxBytes. It short-circuits on a Content-Length already over the cap,
// then enforces the same cap on the bytes actually read — Content-Length may be
// absent or dishonest — by draining the body stream chunk by chunk.
async function readBoundedBytes(res: Response, maxBytes: number): Promise<Uint8Array<ArrayBuffer>> {
  const declared = res.headers.get('content-length');
  if (declared !== null && Number(declared) > maxBytes) {
    throw new Error(`wba directory too large: content-length ${declared} over ${maxBytes} bytes`);
  }
  const body = res.body;
  if (!body) {
    // No readable stream (e.g. a stubbed Response): buffer then check the bytes.
    const bytes = new Uint8Array(await res.arrayBuffer());
    if (bytes.byteLength > maxBytes) {
      throw new Error(`wba directory too large: over ${maxBytes} bytes`);
    }
    return bytes;
  }
  const reader = body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    if (!value) continue;
    total += value.byteLength;
    if (total > maxBytes) {
      await reader.cancel();
      throw new Error(`wba directory too large: over ${maxBytes} bytes`);
    }
    chunks.push(value);
  }
  const out = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return out;
}

// directoryAuthority returns the scheme and authority the reader builds the
// directory URL from. A wbaUrl whose path is not the Web Bot Auth directory
// path, or that carries a query or fragment, is refused: the reader would fetch
// another address than the one configured.
function directoryAuthority(wbaUrl: string): { scheme: string; authority: string } {
  const url = new URL(wbaUrl);
  if (url.pathname !== WBA_DIRECTORY_PATH || url.search !== '' || url.hash !== '') {
    throw new Error(`EXCHANGE_WBA_URL must be an origin followed by ${WBA_DIRECTORY_PATH}`);
  }
  return { scheme: url.protocol.slice(0, -1), authority: url.host };
}

// rawKey decodes a JWK's raw Ed25519 public key, refusing an `x` that is not
// valid base64url, so a malformed key surfaces a clear error instead of being
// thumbprinted over garbage.
export function rawKey(jwk: PublicJwk): Uint8Array<ArrayBuffer> {
  const raw = decodeBase64Url(jwk.x);
  if (!raw) throw new Error('keys: JWK `x` is not valid base64url');
  return raw;
}

async function importJwks(jwks: readonly PublicJwk[]): Promise<Imported> {
  const keys = new Map<string, Uint8Array<ArrayBuffer>>();
  let defaultKeyId: string | undefined;
  for (const jwk of jwks) {
    const raw = rawKey(jwk);
    const id = await thumbprint(raw);
    keys.set(id, raw);
    if (defaultKeyId === undefined) defaultKeyId = id;
  }
  return defaultKeyId !== undefined ? { keys, defaultKeyId } : { keys };
}

// createDirectoryRead returns the one read of the Exchange's directory: the
// SDK's reader over a transport that asks for the directory's media type,
// keeps the runtime's fetch, and caps the body. The SDK reader answers
// undefined for every refusal, so the transport records what it saw itself —
// a status other than 200 or a failed fetch — for the load_failed record to
// name. When the transport saw nothing wrong, the reader refused the media
// type, the body or the response signatures, or the read ran past the reader's
// own 30-second deadline.
function createDirectoryRead(deps: KeyCacheDeps): () => Promise<PublicJwk[]> {
  const { scheme, authority } = directoryAuthority(deps.wbaUrl);
  const fetcher = deps.fetcher ?? fetch;
  return async () => {
    let seen: string | undefined;
    const transport: FetchLike = async (url: string, init?: FetchInit) => {
      try {
        const res = await fetcher(url, {
          ...init,
          headers: { accept: WBA_DIRECTORY_MEDIA_TYPE },
        });
        if (res.status !== 200) {
          seen = `status ${res.status}`;
          return res;
        }
        const body = await readBoundedBytes(res, MAX_WBA_DIRECTORY_BYTES);
        return new Response(body, { status: res.status, headers: res.headers });
      } catch (err) {
        seen = err instanceof Error ? err.message : String(err);
        throw err;
      }
    };
    const read = createWBAOfferDirectoryFetch({
      fetch: transport,
      scheme,
      ...(deps.verifyEd25519 !== undefined ? { verifyEd25519: deps.verifyEd25519 } : {}),
    });
    const file = await read(authority);
    if (file === undefined) {
      throw new Error(
        `wba directory refused: ${seen ?? "its media type, body or response signatures did not pass the Web Bot Auth checks, or no complete answer arrived within the reader's deadline"}`,
      );
    }
    return file.keys ?? [];
  };
}

export function createKeyCache(deps: KeyCacheDeps): KeyCache {
  const ttl = deps.ttlMs ?? 60 * 60 * 1000;
  const now = deps.now ?? Date.now;
  const readDirectory = createDirectoryRead(deps);

  // Pre-provisioned keys are imported lazily once, then never refetched.
  let staticReady: Promise<Imported> | undefined;
  function ensureStatic() {
    if (!deps.staticKeys || deps.staticKeys.length === 0) return undefined;
    if (!staticReady) staticReady = importJwks(deps.staticKeys);
    return staticReady;
  }

  let cache: Map<string, Uint8Array<ArrayBuffer>> | undefined;
  let fetchedAt = 0;
  let defaultKeyId: string | undefined;
  let inflight: Promise<void> | undefined;

  async function load(): Promise<void> {
    try {
      const imported = await importJwks(await readDirectory());
      cache = imported.keys;
      defaultKeyId = imported.defaultKeyId;
      fetchedAt = now();
    } catch (err) {
      // A key directory that is down OR malformed means every signed-URL
      // verification starts failing — the operational record is what lets an
      // operator tell "the exchange's directory is broken" apart from "agents
      // send bad URLs". The event says load (not fetch) because this catch
      // spans the whole step: the read and the key import. Deliberately NO
      // request_id: the cache is a context-free singleton and single-flight
      // de-duplicates concurrent loads, so one fetch serves many requests —
      // naming any single request would be misleading. The directory URL is the
      // correlating fact instead.
      logRecord('error', 'edge.keys.load_failed', {
        message: err instanceof Error ? err.message : String(err),
        wba_url: deps.wbaUrl,
      });
      throw err;
    }
  }

  async function ensureFresh(): Promise<void> {
    if (cache && now() - fetchedAt < ttl) return;
    if (!inflight) {
      inflight = load().finally(() => {
        inflight = undefined;
      });
    }
    await inflight;
  }

  // Resolve from the fetched directory cache. Used both when no static keys are
  // configured and as the one-shot fallback when a keyid misses the static set.
  async function resolveFetched(
    keyid: string | undefined,
  ): Promise<Uint8Array<ArrayBuffer> | undefined> {
    await ensureFresh();
    const lookup = keyid ?? defaultKeyId;
    if (!lookup) return undefined;
    return cache?.get(lookup);
  }

  return {
    async resolve(keyid) {
      const staticSet = ensureStatic();
      if (staticSet) {
        const { keys, defaultKeyId: staticDefault } = await staticSet;
        const lookup = keyid ?? staticDefault;
        if (lookup) {
          const hit = keys.get(lookup);
          if (hit) return hit;
        }
        // Unknown keyid (rotation) — one-shot directory fetch to self-heal.
        return resolveFetched(keyid);
      }
      return resolveFetched(keyid);
    },
    async refresh() {
      cache = undefined;
      await ensureFresh();
    },
  };
}
