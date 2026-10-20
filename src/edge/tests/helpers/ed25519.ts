import { decodeBase64Url, encodeBase64Url } from '@fora-protocol/sdk/base64url';
import {
  buildSignatureBase,
  requestComponentValue,
  signatureInputInner,
} from '@fora-protocol/sdk/core/sign-request';
import { newNonce } from '@fora-protocol/sdk/core/signing-transport';
import { thumbprint } from '@fora-protocol/sdk/thumbprint';
import { canonicalMessage } from '@fora-protocol/sdk/verify';

export interface TestKeypair {
  publicKey: CryptoKey;
  privateKey: CryptoKey;
  publicJwk: JsonWebKey & { kid: string };
}

// keyThumbprint returns the RFC 7638 thumbprint of a test keypair's public key —
// the RFC 9421 keyid a request carries after the WBA split, and the value a
// signed delivery URL's `kid` param now holds.
export function keyThumbprint(kp: TestKeypair): Promise<string> {
  return thumbprint(rawPublicKey(kp));
}

interface SubtleLike {
  generateKey(
    algorithm: { name: string },
    extractable: boolean,
    usages: string[],
  ): Promise<CryptoKeyPair>;
  exportKey(format: 'jwk', key: CryptoKey): Promise<JsonWebKey>;
  sign(algorithm: string, key: CryptoKey, data: Uint8Array): Promise<ArrayBuffer>;
}

// rawPublicKey extracts the raw 32-byte Ed25519 public key from a test keypair's
// JWK (the `x` member is its base64url encoding).
export function rawPublicKey(kp: TestKeypair): Uint8Array<ArrayBuffer> {
  const x = decodeBase64Url(kp.publicJwk.x ?? '');
  if (!x) throw new Error('keypair JWK missing x');
  return x;
}

// The exp-window convention every suite shares: a signed URL under test is
// either comfortably inside its validity window or comfortably past it.
// Living next to popHeaders so a change to the window is a single edit.
export function futureExp(): number {
  return Math.floor(Date.now() / 1000) + 300;
}

export function pastExp(secondsAgo = 60): number {
  return Math.floor(Date.now() / 1000) - secondsAgo;
}

// The https origin of the agent key directory every test proof names in its
// Signature-Agent member. The edge verifies offline with the presented key and
// never fetches this directory, so any https origin serves.
export const AGENT_DIRECTORY = 'https://agent.example';

// The Web Bot Auth limit on a signature's lifetime, in seconds.
const MAX_PROOF_LIFETIME_SEC = 300;

export interface GetSignOptions {
  // The keyid the proof names. Defaults to the signer's own thumbprint; a suite
  // that drives keyid_mismatch passes another value.
  keyid?: string;
  created: number;
  expires: number;
  // HTTP method the proof covers (RFC 9421 @method). Defaults to GET — the
  // read case every suite drives; the signed-write forwarding test signs POST.
  method?: string;
}

// signGetHeaders produces the delivery proof of possession for a fetch of fullUrl
// (GET unless opts.method says otherwise), signed by kp under the Web Bot Auth
// profile: covered ("@method" "@target-uri" "signature-agent";key="sig1"), a
// 64-byte nonce and tag="web-bot-auth". The signature base and the
// Signature-Input value come from the SDK's own builders — the SAME ones the
// verifier rebuilds with — so the test signer cannot drift from the verifier's
// byte contract. The SDK's signInbound signs only a GET and always names the
// signer's own thumbprint, so this helper composes the builders directly: it
// also signs other methods and a mismatched keyid.
export async function signGetHeaders(
  fullUrl: string,
  kp: TestKeypair,
  opts: GetSignOptions,
): Promise<Record<string, string>> {
  const member = `sig1="${AGENT_DIRECTORY}"`;
  const covered = [
    { name: '@method', params: [] },
    { name: '@target-uri', params: [] },
    { name: 'signature-agent', params: [{ key: 'key', value: 'sig1' }] },
  ];
  const inner = signatureInputInner({
    covered,
    keyid: opts.keyid ?? (await keyThumbprint(kp)),
    alg: 'ed25519',
    created: opts.created,
    expires: opts.expires,
    nonce: newNonce(),
    tag: 'web-bot-auth',
  });
  const base = buildSignatureBase(
    covered,
    requestComponentValue({
      method: opts.method ?? 'GET',
      url: fullUrl,
      header: (name) => (name === 'signature-agent' ? member : undefined),
    }),
    inner,
  );
  const sig = new Uint8Array(
    await subtle().sign('Ed25519', kp.privateKey, new TextEncoder().encode(base)),
  );
  let bin = '';
  for (let i = 0; i < sig.length; i += 1) bin += String.fromCharCode(sig[i] as number);
  return {
    'X-FORA-Agent-Key': encodeBase64Url(rawPublicKey(kp)),
    'Signature-Agent': member,
    'Signature-Input': `sig1=${inner}`,
    Signature: `sig1=:${btoa(bin)}:`,
  };
}

// popHeaders wraps signGetHeaders with the proof window every suite uses:
// created slightly in the past (tolerates clock skew), lasting the profile's
// five-minute maximum. The skew convention lives here once, so a change to it is
// a single edit.
export function popHeaders(
  fullUrl: string,
  kp: TestKeypair,
  keyid?: string,
  method = 'GET',
): Promise<Record<string, string>> {
  const now = Math.floor(Date.now() / 1000);
  return signGetHeaders(fullUrl, kp, {
    ...(keyid !== undefined ? { keyid } : {}),
    created: now - 5,
    expires: now - 5 + MAX_PROOF_LIFETIME_SEC,
    method,
  });
}

// boundUrl signs a delivery URL bound to an agent: agent_id is the agent
// key's thumbprint, the URL itself is signed by the exchange key. Returns the
// agentId too, since proof-of-possession tests need it as the keyid.
export async function boundUrl(
  origin: string,
  exchangePrivateKey: CryptoKey,
  agentKp: TestKeypair,
  opts: { exp: number; kid: string; path?: string },
): Promise<{ url: string; agentId: string }> {
  const agentId = await keyThumbprint(agentKp);
  const url = await signUrl(origin, exchangePrivateKey, { ...opts, agentId });
  return { url, agentId };
}

// tamperSignature replaces the sig param with `bytes` zero bytes. The default,
// 64, is a valid length and encoding with the wrong bytes: the verifier resolves
// the key and fails on the signature check itself — the signature_mismatch path,
// never a parse error. Any other length is a signature no Ed25519 key can make,
// which must take that same path.
export function tamperSignature(url: string, bytes = 64): string {
  const tampered = new URL(url);
  tampered.searchParams.set('sig', encodeBase64Url(new Uint8Array(bytes)));
  return tampered.toString();
}

function subtle(): SubtleLike {
  return crypto.subtle as unknown as SubtleLike;
}

export async function generateKeypair(kid = 'test-key-1'): Promise<TestKeypair> {
  const pair = await subtle().generateKey({ name: 'Ed25519' }, true, ['sign', 'verify']);
  const pubJwk = await subtle().exportKey('jwk', pair.publicKey);
  return {
    publicKey: pair.publicKey,
    privateKey: pair.privateKey,
    publicJwk: { ...pubJwk, kid },
  };
}

export interface SignOptions {
  exp: number;
  kid?: string;
  agentId?: string;
  path?: string;
  query?: Record<string, string>;
}

export async function signUrl(
  origin: string,
  privateKey: CryptoKey,
  opts: SignOptions,
): Promise<string> {
  const url = new URL(opts.path ?? '/some/resource', origin);
  url.searchParams.set('exp', String(opts.exp));
  if (opts.kid !== undefined) url.searchParams.set('kid', opts.kid);
  if (opts.agentId !== undefined) url.searchParams.set('agent_id', opts.agentId);
  for (const [k, v] of Object.entries(opts.query ?? {})) {
    url.searchParams.set(k, v);
  }
  // The SDK's canonicalMessage strips only `sig` (absent at this point), so it
  // yields exactly the `GET\n<url>` bytes the signer must sign — signer and
  // verifier share one source of the canonical form. It types its input as the
  // opaque URL string; url.toString() is the no-op String() coercion the SDK
  // applies internally, preserving byte-parity with the verifier.
  const sig = new Uint8Array(
    await subtle().sign('Ed25519', privateKey, canonicalMessage(url.toString())),
  );
  url.searchParams.set('sig', encodeBase64Url(sig));
  return url.toString();
}

// Build a FORA Web Bot Auth directory
// (/.well-known/http-message-signatures-directory) carrying the given keys — the
// shape the edge now fetches to resolve verify keys after the WBA split. Keys
// carry NO kid (named by thumbprint); each is fleshed out with the full JWK
// fields (use/alg and a wide not_before/not_after window) to match the
// production wire shape (protojson of fora.v1.WBAFile).
export function wbaDirectoryWithKeys(keys: JsonWebKey[]): {
  keys: Array<{
    kty: string;
    crv: string;
    x: string;
    alg: string;
    use: string;
    not_before: string;
    not_after: string;
  }>;
} {
  return {
    keys: keys.map((k) => ({
      kty: k.kty ?? 'OKP',
      crv: k.crv ?? 'Ed25519',
      x: k.x ?? '',
      alg: 'EdDSA',
      use: 'sig',
      not_before: '2000-01-01T00:00:00Z',
      not_after: '2100-01-01T00:00:00Z',
    })),
  };
}
