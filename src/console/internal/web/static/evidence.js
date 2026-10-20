// Checks the signed evidence of one transaction, in the browser, with WebCrypto.
//
// A transaction carries two Ed25519 signatures. The exchange signed the offer:
// offer_sig over offer_canonical_bytes, under exchange_signing_public_key. The
// agent signed its acceptance of that offer: agent_acceptance_signature over
// agent_acceptance_canonical_bytes, under agent_public_key. The bytes are the
// exact bytes that were signed, so nothing is canonicalised here; the check is
// a plain verify of each signature, plus one link between them: the acceptance
// names the offer signature it accepted, and that has to be this offer's. The
// link counts only when the acceptance verifies: an acceptance whose signature
// fails proves nothing about which offer it was for.
//
// Both keys are the ones recorded with the transaction. So a pass shows that
// the record is intact -- the signed bytes are the ones those keys signed --
// and not who holds the keys.
//
// Bytes and keys arrive as standard base64 and signatures as hex, the way the
// exchange serves them. Anything that does not decode fails its check rather
// than throwing, so a tampered field reads as "does not verify".
//
// The module has no DOM code, so the same file runs under Node's WebCrypto in
// its tests.

// A browser without Ed25519 in WebCrypto cannot check anything, and says so
// rather than reporting every signature as bad.
export const UNSUPPORTED = 'unsupported';

/**
 * Verifies both signatures and the link between them.
 *
 * @param {Record<string, string>} evidence the evidence fields, as served
 * @param {SubtleCrypto} [subtle] WebCrypto's subtle interface
 * @returns {Promise<{offer: boolean, acceptance: boolean, linked: boolean} | typeof UNSUPPORTED>}
 */
export async function verifyEvidence(evidence, subtle = globalThis.crypto?.subtle) {
  if (!(await supportsEd25519(subtle))) {
    return UNSUPPORTED;
  }
  const offer = await verifySignature(
    subtle,
    evidence.exchange_signing_public_key,
    evidence.offer_sig,
    evidence.offer_canonical_bytes,
  );
  const acceptance = await verifySignature(
    subtle,
    evidence.agent_public_key,
    evidence.agent_acceptance_signature,
    evidence.agent_acceptance_canonical_bytes,
  );
  return { offer, acceptance, linked: acceptance && acceptanceNamesOffer(evidence) };
}

/**
 * Whether subtle can verify Ed25519 at all, tried with a key of the right
 * size so a missing algorithm is not mistaken for a bad key.
 *
 * @param {SubtleCrypto | undefined} subtle
 * @returns {Promise<boolean>}
 */
async function supportsEd25519(subtle) {
  if (!subtle) {
    return false;
  }
  try {
    await subtle.importKey('raw', new Uint8Array(32), { name: 'Ed25519' }, false, ['verify']);
    return true;
  } catch {
    return false;
  }
}

/**
 * @param {SubtleCrypto} subtle
 * @param {string} keyB64 the raw 32-byte public key, base64
 * @param {string} sigHex the 64-byte signature, hex
 * @param {string} dataB64 the signed bytes, base64
 * @returns {Promise<boolean>}
 */
async function verifySignature(subtle, keyB64, sigHex, dataB64) {
  const key = fromBase64(keyB64);
  const signature = fromHex(sigHex);
  const data = fromBase64(dataB64);
  if (!key || key.length !== 32 || !signature || signature.length !== 64 || !data) {
    return false;
  }
  try {
    const imported = await subtle.importKey('raw', key, { name: 'Ed25519' }, false, ['verify']);
    return await subtle.verify({ name: 'Ed25519' }, imported, signature, data);
  } catch {
    return false;
  }
}

/**
 * Whether the acceptance names this offer's signature as the one it accepted.
 *
 * @param {Record<string, string>} evidence
 * @returns {boolean}
 */
function acceptanceNamesOffer(evidence) {
  const bytes = fromBase64(evidence.agent_acceptance_canonical_bytes);
  if (!bytes || !evidence.offer_sig) {
    return false;
  }
  try {
    const payload = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
    return payload?.offer_sig === evidence.offer_sig;
  } catch {
    return false;
  }
}

/**
 * @param {unknown} text
 * @returns {Uint8Array | null}
 */
function fromBase64(text) {
  if (typeof text !== 'string' || !/^[A-Za-z0-9+/]*={0,2}$/.test(text) || text.length % 4 !== 0) {
    return null;
  }
  const binary = atob(text);
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    out[i] = binary.charCodeAt(i);
  }
  return out;
}

/**
 * @param {unknown} text
 * @returns {Uint8Array | null}
 */
function fromHex(text) {
  if (typeof text !== 'string' || !/^(?:[0-9a-fA-F]{2})*$/.test(text)) {
    return null;
  }
  const out = new Uint8Array(text.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(text.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}
