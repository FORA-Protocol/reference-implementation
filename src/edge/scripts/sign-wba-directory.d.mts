import type { WbaKey } from '../src/types.js';

// Type declaration for the plain-JS sign-wba-directory.mjs, which runs under
// plain Node and so carries no types.
export interface WbaDirectorySigner {
  // The listed key's public `x`.
  x: string;
  privKey: CryptoKey;
}

// The WBA_SIGNED_DIRECTORY_JSON value, before JSON.stringify.
export interface SignedWbaDirectoryValue {
  body: string;
  content_digest: string;
  signature_input: string;
  signature: string;
}

export declare function signWbaDirectory(opts: {
  authority: string;
  keys: WbaKey[];
  revocationUrl?: string;
  signers: WbaDirectorySigner[];
  validDays: number;
  now: number;
}): Promise<SignedWbaDirectoryValue>;
