import { encodeBase64Url } from '@fora-protocol/sdk/base64url';

import { signWbaDirectory } from '../../scripts/sign-wba-directory.mjs';
import type { WbaKey } from '../../src/types.js';

// The host every pre-signed directory in the suites is made for.
export const SIGNED_DIRECTORY_AUTHORITY = 'pub.test';

export interface SignedDirectoryConfig {
  // WBA_KEYS_JSON and WBA_SIGNED_DIRECTORY_JSON, as the edge is configured.
  WBA_KEYS_JSON: string;
  WBA_SIGNED_DIRECTORY_JSON: string;
  // The listed key's raw public key, for the reader-side check.
  publicKey: Uint8Array<ArrayBuffer>;
}

// signedDirectoryConfig mints a publisher key and produces the configuration a
// deployment would carry for it: WBA_KEYS_JSON listing the key, and the
// pre-signed directory the production tool (scripts/sign-wba-directory.mjs)
// makes for `authority` with the key's private half.
export async function signedDirectoryConfig(
  authority = SIGNED_DIRECTORY_AUTHORITY,
): Promise<SignedDirectoryConfig> {
  const pair = (await crypto.subtle.generateKey({ name: 'Ed25519' }, false, [
    'sign',
    'verify',
  ])) as CryptoKeyPair;
  const publicKey = new Uint8Array(
    (await crypto.subtle.exportKey('raw', pair.publicKey)) as ArrayBuffer,
  );
  const x = encodeBase64Url(publicKey);
  const keys: WbaKey[] = [
    {
      kty: 'OKP',
      crv: 'Ed25519',
      use: 'sig',
      alg: 'EdDSA',
      x,
      not_before: '2020-01-01T00:00:00Z',
      not_after: '2100-01-01T00:00:00Z',
    },
  ];
  const signed = await signWbaDirectory({
    authority,
    keys,
    signers: [{ x, privKey: pair.privateKey }],
    validDays: 1,
    now: Math.floor(Date.now() / 1000),
  });
  return {
    WBA_KEYS_JSON: JSON.stringify(keys),
    WBA_SIGNED_DIRECTORY_JSON: JSON.stringify(signed),
    publicKey,
  };
}
