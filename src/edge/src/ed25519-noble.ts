import * as ed from '@noble/ed25519';

import type { Ed25519Verify } from '@fora-protocol/sdk/pop';

import { refusingMalformedSignatures } from './ed25519.js';

// The Ed25519 primitive for Fastly Compute, whose SubtleCrypto has no Ed25519:
// @noble/ed25519, which runs on any modern JS runtime. Only the Fastly entries
// import this module, so no other bundle carries the library.
//
// verifyAsync hashes through WebCrypto subtle.digest, which Fastly supports for
// SHA-512, so the library needs no hash of its own here.

// nobleEd25519 takes the SDK's argument order, (publicKey, signature, message);
// @noble/ed25519 takes (signature, message, publicKey). Swapping them fails
// silently, as a refused key or a 403. @noble/ed25519 throws on a signature that
// is not 64 bytes, so it is wrapped like every edge primitive
// (refusingMalformedSignatures).
export const nobleEd25519: Ed25519Verify = refusingMalformedSignatures(
  (publicKey, signature, message) => ed.verifyAsync(signature, message, publicKey),
);
