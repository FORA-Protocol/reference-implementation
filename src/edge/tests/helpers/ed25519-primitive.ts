import { describe, expect, it } from 'vitest';

import type { Ed25519Verify } from '@fora-protocol/sdk/pop';

import { generateKeypair, rawPublicKey } from './ed25519.js';

// Signature lengths a requester can send that no Ed25519 signature has. The
// signed URL's `sig` and the delivery proof's Signature are both the
// requester's to choose, so a primitive sees every one of these.
export const MALFORMED_SIGNATURE_LENGTHS = [0, 10, 63, 65] as const;

const MESSAGE = new Uint8Array(new TextEncoder().encode('the edge Ed25519 primitive contract'));

async function signedByFreshKey(): Promise<{
  publicKey: Uint8Array<ArrayBuffer>;
  signature: Uint8Array<ArrayBuffer>;
}> {
  const kp = await generateKeypair();
  const signature = new Uint8Array(await crypto.subtle.sign('Ed25519', kp.privateKey, MESSAGE));
  return { publicKey: rawPublicKey(kp), signature };
}

// describeEd25519Primitive pins the contract every Ed25519 primitive the edge
// verifies with must keep, on the runtime the calling suite runs in: a valid
// signature verifies, and any other signature resolves false. A signature that
// is not 64 bytes resolves false too, and never rejects: a rejection there
// would answer a requester's malformed signature with a 503 or a bare 500
// instead of the refusal it is.
export function describeEd25519Primitive(name: string, verify: Ed25519Verify): void {
  describe(`${name}: the Ed25519 primitive contract`, () => {
    it('verifies a valid signature', async () => {
      const { publicKey, signature } = await signedByFreshKey();
      await expect(verify(publicKey, signature, MESSAGE)).resolves.toBe(true);
    });

    it('answers false for 64 bytes that are not the signature', async () => {
      const { publicKey } = await signedByFreshKey();
      await expect(verify(publicKey, new Uint8Array(64), MESSAGE)).resolves.toBe(false);
    });

    for (const length of MALFORMED_SIGNATURE_LENGTHS) {
      it(`answers false, never a rejection, for a ${length}-byte signature`, async () => {
        const { publicKey } = await signedByFreshKey();
        await expect(verify(publicKey, new Uint8Array(length), MESSAGE)).resolves.toBe(false);
      });
    }
  });
}
