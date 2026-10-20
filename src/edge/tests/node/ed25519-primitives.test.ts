import { describe, expect, it, vi } from 'vitest';

import { nobleEd25519 } from '../../src/ed25519-noble.js';
import { refusingMalformedSignatures, webCryptoEd25519 } from '../../src/ed25519.js';
import {
  MALFORMED_SIGNATURE_LENGTHS,
  describeEd25519Primitive,
} from '../helpers/ed25519-primitive.js';

// On Node: WebCrypto Ed25519 is the primitive the Lambda@Edge bundle verifies
// with, and @noble/ed25519 is the one the Fastly entry injects.
describeEd25519Primitive('webCryptoEd25519 on Node', webCryptoEd25519);
describeEd25519Primitive('nobleEd25519 on Node', nobleEd25519);

// The wrapper both primitives are built with decides one thing only: a
// signature that is not 64 bytes is refused before the primitive sees it. It
// must not turn anything else into a refusal.
describe('refusingMalformedSignatures', () => {
  const publicKey = new Uint8Array(32);
  const message = new Uint8Array([1, 2, 3]);

  it('answers false for a signature that is not 64 bytes, without calling the primitive', async () => {
    const primitive = vi.fn(async () => true);
    const guarded = refusingMalformedSignatures(primitive);
    for (const length of MALFORMED_SIGNATURE_LENGTHS) {
      await expect(guarded(publicKey, new Uint8Array(length), message)).resolves.toBe(false);
    }
    expect(primitive).not.toHaveBeenCalled();
  });

  it("hands a 64-byte signature to the primitive and returns the primitive's answer", async () => {
    const primitive = vi.fn(async () => true);
    const guarded = refusingMalformedSignatures(primitive);
    await expect(guarded(publicKey, new Uint8Array(64), message)).resolves.toBe(true);
    expect(primitive).toHaveBeenCalledTimes(1);
  });

  it('lets any other failure of the primitive through, so a runtime fault is never a refusal', async () => {
    const fault = new DOMException('Ed25519 is not supported', 'NotSupportedError');
    const guarded = refusingMalformedSignatures(async () => {
      throw fault;
    });
    await expect(guarded(publicKey, new Uint8Array(64), message)).rejects.toBe(fault);
  });
});
