// A publisher whose key directory is served pre-signed (WBA_SIGNED_DIRECTORY_JSON).
//
// The Web Bot Auth profile hands out a listed key only when the directory
// response carries a valid signature by it, and the edge holds no private key.
// So the signatures are made offline with the production tool and configured;
// vitest.config.ts makes them for this project's host. The case runs on the real
// Workers runtime through SELF, and the reader is the SDK's
// verifyDirectoryResponse, the check every directory reader applies, here
// running in workerd.
import { SELF } from 'cloudflare:test';

import { describe, expect, it } from 'vitest';

import { decodeBase64Url } from '@fora-protocol/sdk/base64url';
import { verifyDirectoryResponse } from '@fora-protocol/sdk/identity';
import { thumbprint } from '@fora-protocol/sdk/thumbprint';

import { WBA_PATH } from '../../src/types.js';
import { PUB_ORIGIN } from '../helpers/test-setup.js';

async function fetchDirectory(): Promise<{ res: Response; body: Uint8Array<ArrayBuffer> }> {
  const res = await SELF.fetch(`${PUB_ORIGIN}${WBA_PATH}`);
  return { res, body: new Uint8Array(await res.arrayBuffer()) };
}

function listedKey(body: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
  const doc = JSON.parse(new TextDecoder().decode(body)) as { keys: Array<{ x: string }> };
  const raw = decodeBase64Url(doc.keys[0]?.x ?? '');
  if (!raw) throw new Error('the served directory lists no decodable key');
  return new Uint8Array(raw);
}

describe('pre-signed key directory on the Workers runtime', () => {
  it('serves the directory under the profile media type with its response signatures', async () => {
    const { res } = await fetchDirectory();
    expect(res.status).toBe(200);
    expect(res.headers.get('content-type')).toBe(
      'application/http-message-signatures-directory+json',
    );
    expect(res.headers.get('content-digest')).toMatch(/^sha-256=:/);
    expect(res.headers.get('signature-input')).toContain('tag="http-message-signatures-directory"');
    expect(res.headers.get('signature')).toMatch(/^sig1=:/);
  });

  it('hands its key to a reader fetching it from the host it was signed for', async () => {
    const { res, body } = await fetchDirectory();
    const key = listedKey(body);
    const verified = await verifyDirectoryResponse(
      new URL(PUB_ORIGIN).host,
      res.headers,
      body,
      [key],
      Math.floor(Date.now() / 1000),
    );
    expect([...verified]).toEqual([await thumbprint(key)]);
  });
});
