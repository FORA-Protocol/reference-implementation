import type { Ed25519Verify } from '@fora-protocol/sdk/pop';

// ED25519_SIGNATURE_BYTES is the only length an Ed25519 signature has (RFC 8032
// section 5.1.6).
export const ED25519_SIGNATURE_BYTES = 64;

// refusingMalformedSignatures wraps an Ed25519 primitive so a signature that is
// not 64 bytes resolves false without reaching it. The signed URL's `sig` and the
// delivery proof's Signature are the requester's to choose, and the runtimes'
// primitives throw on such a signature (@noble/ed25519, and workerd's WebCrypto)
// instead of answering false, which turned a requester's malformed signature into
// a 503 or a bare 500. The Go verifier the SDK's vectors come from answers false
// for it, so the edge now refuses it the way it refuses any signature that does
// not verify. Every other failure of the primitive still propagates: a runtime
// fault stays a 503 or a 500 and is never reported as a refusal.
export function refusingMalformedSignatures(verify: Ed25519Verify): Ed25519Verify {
  return async (publicKey, signature, message) =>
    signature.length === ED25519_SIGNATURE_BYTES ? verify(publicKey, signature, message) : false;
}

// webCryptoEd25519 is the Ed25519 primitive on a runtime whose WebCrypto has
// Ed25519 (Cloudflare Workers, Lambda@Edge): it imports the raw public key and
// verifies through crypto.subtle. A runtime without it (Fastly Compute) injects
// its own primitive through AppDeps.verifyEd25519 instead (ed25519-noble.ts).
// The argument order is the SDK's: (publicKey, signature, message).
export const webCryptoEd25519: Ed25519Verify = refusingMalformedSignatures(
  async (publicKey, signature, message) => {
    const key = await crypto.subtle.importKey('raw', publicKey, { name: 'Ed25519' }, false, [
      'verify',
    ]);
    return crypto.subtle.verify({ name: 'Ed25519' }, key, signature, message);
  },
);
