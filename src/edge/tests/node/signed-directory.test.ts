// The pre-signed key directory (WBA_SIGNED_DIRECTORY_JSON): the production tool
// makes it offline, the edge serves it byte for byte, and a reader under the Web
// Bot Auth profile takes the listed key from it. The round trip runs through
// the configuration parser and the directory route; the reader is the SDK's
// verifyDirectoryResponse, the check every directory reader applies.
import { beforeAll, describe, expect, it } from 'vitest';

import { verifyDirectoryResponse } from '@fora-protocol/sdk/identity';
import { thumbprint } from '@fora-protocol/sdk/thumbprint';

import { createApp } from '../../src/app.js';
import { buildDeps, parseEnv } from '../../src/config.js';
import { WBA_PATH } from '../../src/types.js';
import {
  SIGNED_DIRECTORY_AUTHORITY,
  type SignedDirectoryConfig,
  signedDirectoryConfig,
} from '../helpers/signed-directory-config.js';
import { BASE_SCHEMA_ENV } from '../helpers/test-setup.js';

let config: SignedDirectoryConfig;

beforeAll(async () => {
  config = await signedDirectoryConfig();
});

// Both settings, as a deployment carrying a pre-signed directory sets them.
function configuredEnv(): Record<string, string> {
  return {
    WBA_KEYS_JSON: config.WBA_KEYS_JSON,
    WBA_SIGNED_DIRECTORY_JSON: config.WBA_SIGNED_DIRECTORY_JSON,
  };
}

function appWith(env: Record<string, string>) {
  return createApp(buildDeps(parseEnv({ ...BASE_SCHEMA_ENV, ...env })));
}

async function readerVerdict(res: Response, authority: string): Promise<Set<string>> {
  const body = new Uint8Array(await res.arrayBuffer());
  return verifyDirectoryResponse(
    authority,
    res.headers,
    body,
    [config.publicKey],
    Math.floor(Date.now() / 1000),
  );
}

describe('pre-signed key directory', () => {
  it('serves the signed body and its three signature headers verbatim', async () => {
    const res = await appWith(configuredEnv()).request(
      `https://${SIGNED_DIRECTORY_AUTHORITY}${WBA_PATH}`,
    );
    const signed = JSON.parse(config.WBA_SIGNED_DIRECTORY_JSON) as Record<string, string>;
    expect(res.status).toBe(200);
    expect(res.headers.get('content-type')).toBe(
      'application/http-message-signatures-directory+json',
    );
    expect(res.headers.get('content-digest')).toBe(signed.content_digest);
    expect(res.headers.get('signature-input')).toBe(signed.signature_input);
    expect(res.headers.get('signature')).toBe(signed.signature);
    expect(await res.text()).toBe(signed.body);
  });

  it('is accepted by a Web Bot Auth reader fetching it from the host it was signed for', async () => {
    const res = await appWith(configuredEnv()).request(
      `https://${SIGNED_DIRECTORY_AUTHORITY}${WBA_PATH}`,
    );
    const verified = await readerVerdict(res, SIGNED_DIRECTORY_AUTHORITY);
    expect([...verified]).toEqual([await thumbprint(config.publicKey)]);
  });

  it('carries no key for a reader fetching it from another host', async () => {
    const res = await appWith(configuredEnv()).request(`https://other.example${WBA_PATH}`);
    expect((await readerVerdict(res, 'other.example')).size).toBe(0);
  });

  it('is served without WBA_KEYS_JSON, the body being the directory', async () => {
    const res = await appWith({
      WBA_SIGNED_DIRECTORY_JSON: config.WBA_SIGNED_DIRECTORY_JSON,
    }).request(`https://${SIGNED_DIRECTORY_AUTHORITY}${WBA_PATH}`);
    expect(res.status).toBe(200);
    expect((await readerVerdict(res, SIGNED_DIRECTORY_AUTHORITY)).size).toBe(1);
  });

  it('serves the directory unsigned when only WBA_KEYS_JSON is set', async () => {
    const res = await appWith({ WBA_KEYS_JSON: config.WBA_KEYS_JSON }).request(
      `https://${SIGNED_DIRECTORY_AUTHORITY}${WBA_PATH}`,
    );
    expect(res.status).toBe(200);
    expect(res.headers.get('signature')).toBeNull();
    await expect(readerVerdict(res, SIGNED_DIRECTORY_AUTHORITY)).rejects.toThrow(/Content-Digest/);
  });
});

describe('pre-signed key directory configuration', () => {
  it('refuses a signed directory that lists other keys than WBA_KEYS_JSON', async () => {
    const other = await signedDirectoryConfig();
    expect(() =>
      buildDeps(
        parseEnv({
          ...BASE_SCHEMA_ENV,
          WBA_KEYS_JSON: config.WBA_KEYS_JSON,
          WBA_SIGNED_DIRECTORY_JSON: other.WBA_SIGNED_DIRECTORY_JSON,
        }),
      ),
    ).toThrow(/re-sign the directory/);
  });

  it('refuses a value missing one of the signature headers', () => {
    const signed = JSON.parse(config.WBA_SIGNED_DIRECTORY_JSON) as Record<string, string>;
    const { signature: _dropped, ...partial } = signed;
    expect(() =>
      buildDeps(
        parseEnv({ ...BASE_SCHEMA_ENV, WBA_SIGNED_DIRECTORY_JSON: JSON.stringify(partial) }),
      ),
    ).toThrow();
  });

  it('refuses a body that is not a key directory', () => {
    const signed = JSON.parse(config.WBA_SIGNED_DIRECTORY_JSON) as Record<string, string>;
    expect(() =>
      buildDeps(
        parseEnv({
          ...BASE_SCHEMA_ENV,
          WBA_SIGNED_DIRECTORY_JSON: JSON.stringify({ ...signed, body: '{"keys":[]}' }),
        }),
      ),
    ).toThrow();
  });
});
