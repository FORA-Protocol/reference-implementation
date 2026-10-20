import { signDirectoryResponse } from '@fora-protocol/sdk/identity';

import { WBA_DIRECTORY_CONTENT_TYPE } from '../../src/well-known.mjs';
import { type TestKeypair, keyThumbprint, wbaDirectoryWithKeys } from './ed25519.js';

export interface SignedDirectory {
  // The exact body the response signatures cover, as served.
  body: string;
  // Content-Type, Content-Digest, Signature-Input and Signature.
  headers: Record<string, string>;
}

export interface SignedDirectoryOptions {
  // The keys that sign the response. Defaults to every listed key, which is what
  // the Web Bot Auth profile requires of a directory.
  signers?: TestKeypair[];
  // The authority the signatures cover ("@authority";req). Defaults to the
  // authority the caller says the directory is served under.
  signedFor?: string;
}

// signedDirectory builds a Web Bot Auth key directory response listing `listed`,
// signed with the SDK's signDirectoryResponse for `authority` — the host[:port]
// the edge fetches the directory from — and served under the profile's media
// type. A test that needs a directory the edge must refuse edits the result.
export async function signedDirectory(
  listed: TestKeypair[],
  authority: string,
  opts: SignedDirectoryOptions = {},
): Promise<SignedDirectory> {
  const body = JSON.stringify(wbaDirectoryWithKeys(listed.map((kp) => kp.publicJwk)));
  const signers = await Promise.all(
    (opts.signers ?? listed).map(async (kp) => ({
      privKey: kp.privateKey,
      keyid: await keyThumbprint(kp),
    })),
  );
  const now = Math.floor(Date.now() / 1000);
  const sig = await signDirectoryResponse(
    opts.signedFor ?? authority,
    new Uint8Array(new TextEncoder().encode(body)),
    signers,
    now - 60,
    now + 3600,
  );
  return {
    body,
    headers: {
      'content-type': WBA_DIRECTORY_CONTENT_TYPE,
      'content-digest': sig.contentDigest,
      'signature-input': sig.signatureInput,
      signature: sig.signature,
    },
  };
}
