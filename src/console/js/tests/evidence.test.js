import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { UNSUPPORTED, verifyEvidence } from '../../internal/web/static/evidence.js';

// The evidence of a real transaction: a sale made through the Exchange's
// trading surface in the console's integration suite, read off its detail
// page. That suite writes this file and checks it still has the fields the
// page hands over.
const real = JSON.parse(
  readFileSync(new URL('./testdata/transaction-evidence.json', import.meta.url), 'utf8'),
);

// flipByte returns base64 whose decoded bytes differ from text's in one bit.
function flipByte(text, index = 0) {
  const bytes = Buffer.from(text, 'base64');
  bytes[index] ^= 0x01;
  return bytes.toString('base64');
}

// flipHex returns hex whose decoded bytes differ from text's in one bit.
function flipHex(text) {
  const first = Number.parseInt(text.slice(0, 2), 16) ^ 0x01;
  return first.toString(16).padStart(2, '0') + text.slice(2);
}

describe('verifyEvidence', () => {
  it('verifies a real transaction', async () => {
    expect(await verifyEvidence(real)).toEqual({ offer: true, acceptance: true, linked: true });
  });

  it('fails the offer when one byte of the signed offer changes', async () => {
    const tampered = { ...real, offer_canonical_bytes: flipByte(real.offer_canonical_bytes, 40) };
    expect(await verifyEvidence(tampered)).toEqual({
      offer: false,
      acceptance: true,
      linked: true,
    });
  });

  it("fails the offer when the exchange's signature changes", async () => {
    const tampered = { ...real, offer_sig: flipHex(real.offer_sig) };
    const outcome = await verifyEvidence(tampered);
    expect(outcome.offer).toBe(false);
    // The acceptance still verifies, but it names the original signature,
    // so it no longer belongs to this offer.
    expect(outcome.acceptance).toBe(true);
    expect(outcome.linked).toBe(false);
  });

  it('fails the acceptance when one byte of it changes', async () => {
    const tampered = {
      ...real,
      agent_acceptance_canonical_bytes: flipByte(real.agent_acceptance_canonical_bytes, 5),
    };
    expect(await verifyEvidence(tampered)).toEqual({
      offer: true,
      acceptance: false,
      linked: false,
    });
  });

  it('does not link an acceptance whose bytes changed without being signed again', async () => {
    // The acceptance still names this offer's signature, but its own
    // signature no longer covers what it says, so what it names counts for
    // nothing.
    const payload = JSON.parse(
      Buffer.from(real.agent_acceptance_canonical_bytes, 'base64').toString('utf8'),
    );
    payload.idempotency_key = `${payload.idempotency_key ?? ''}-changed`;
    const tampered = {
      ...real,
      agent_acceptance_canonical_bytes: Buffer.from(JSON.stringify(payload)).toString('base64'),
    };
    expect(await verifyEvidence(tampered)).toEqual({
      offer: true,
      acceptance: false,
      linked: false,
    });
  });

  it("fails the acceptance when the agent's signature changes", async () => {
    const tampered = {
      ...real,
      agent_acceptance_signature: flipHex(real.agent_acceptance_signature),
    };
    expect(await verifyEvidence(tampered)).toEqual({
      offer: true,
      acceptance: false,
      linked: false,
    });
  });

  it('fails each signature under the other party’s key', async () => {
    const swapped = {
      ...real,
      exchange_signing_public_key: real.agent_public_key,
      agent_public_key: real.exchange_signing_public_key,
    };
    expect(await verifyEvidence(swapped)).toEqual({
      offer: false,
      acceptance: false,
      linked: false,
    });
  });

  it('does not link an acceptance of another offer', async () => {
    const otherOffer = JSON.parse(
      Buffer.from(real.agent_acceptance_canonical_bytes, 'base64').toString('utf8'),
    );
    otherOffer.offer_sig = flipHex(otherOffer.offer_sig);
    const tampered = {
      ...real,
      agent_acceptance_canonical_bytes: Buffer.from(JSON.stringify(otherOffer)).toString('base64'),
    };
    const outcome = await verifyEvidence(tampered);
    expect(outcome.linked).toBe(false);
    expect(outcome.acceptance).toBe(false);
  });

  it('fails a field that does not decode instead of throwing', async () => {
    const broken = { ...real, offer_sig: 'not hex', agent_public_key: 'not base64!' };
    // The acceptance names the original offer signature, which the broken
    // field no longer is, so the link fails too.
    expect(await verifyEvidence(broken)).toEqual({
      offer: false,
      acceptance: false,
      linked: false,
    });
  });

  it('says so when the browser cannot check Ed25519', async () => {
    const noEd25519 = {
      importKey: async () => {
        throw new DOMException('Unrecognized algorithm name', 'NotSupportedError');
      },
    };
    expect(await verifyEvidence(real, noEd25519)).toBe(UNSUPPORTED);
    expect(await verifyEvidence(real, undefined)).not.toBe(UNSUPPORTED);
  });
});
