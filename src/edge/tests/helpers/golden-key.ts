// The key behind the goldens in edgerelease/testdata. Their FORA_VERIFY_KEYS
// holds its public half, so a URL signed with it verifies against the golden's
// settings without a directory fetch. It is derived from a fixed phrase rather
// than stored, so the repository holds no private key material; it signs
// nothing outside the suites that run the goldens.
import { createHash, createPrivateKey } from 'node:crypto';
import { thumbprint } from '@fora-protocol/sdk/thumbprint';

const SIGNING_SEED = createHash('sha256').update('fora release-package test key').digest();
const PKCS8_ED25519_PREFIX = Buffer.from('302e020100300506032b657004220420', 'hex');

export async function goldenSigningKey(): Promise<{ privateKey: CryptoKey; kid: string }> {
  const der = Buffer.concat([PKCS8_ED25519_PREFIX, SIGNING_SEED]);
  const jwk = createPrivateKey({ key: der, format: 'der', type: 'pkcs8' }).export({
    format: 'jwk',
  });
  const privateKey = await crypto.subtle.importKey('pkcs8', der, { name: 'Ed25519' }, false, [
    'sign',
  ]);
  const kid = await thumbprint(Buffer.from(jwk.x ?? '', 'base64url'));
  return { privateKey, kid };
}
