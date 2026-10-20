// The demo-on-AWS edge with a licensed scope: the real Lambda@Edge entry, no
// origin mode (CloudFront fetches the origin), and LICENSED_PATHS_JSON naming
// the four prefixes the demo publishers sell. The demo stack renders the same
// list into the bundle (licensed_paths in deploy/terraform/stacks/demo-aws).
//
// Two properties. An AI bot reading a license document, which sits outside
// the scope, reaches the origin, so an agent can read the terms before it
// buys. The same bot under each prefix that is for sale is still sent to
// negotiate. Both are checked for a declared AI User-Agent and for a request
// with no User-Agent, which the edge also treats as a bot.
import type { CloudFrontRequest } from 'hono/lambda-edge';
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest';

import type { handler as awsHandler } from '../../src/entries/aws-lambda.js';
import { makeViewerRequestEvent as makeEvent } from '../helpers/cloudfront.js';
import { PUB_ORIGIN } from '../helpers/test-setup.js';

const SCOPE = ['/articles/', '/lyrics/', '/tracks/', '/sfx/'];
const LICENSE_PATH = '/licensing/harmonia/music-v0.1.txt';
const BOTS: Record<string, Record<string, string>> = {
  'Claude-User': { 'user-agent': 'Mozilla/5.0 (compatible; Claude-User/1.0)' },
  'no User-Agent': {},
};

let handler: typeof awsHandler;

function isRequest(result: Awaited<ReturnType<typeof awsHandler>>): result is CloudFrontRequest {
  return 'uri' in result;
}

beforeAll(async () => {
  vi.stubEnv('EXCHANGE_URL', 'https://exchange.test');
  vi.stubEnv(
    'EXCHANGE_WBA_URL',
    'https://exchange.test/.well-known/http-message-signatures-directory',
  );
  vi.stubEnv('PROVIDER', 'pub.example.com');
  vi.stubEnv('EXCHANGES_JSON', '[{"domain":"exchange.test","endpoint":"https://exchange.test"}]');
  vi.stubEnv('LICENSED_PATHS_JSON', JSON.stringify(SCOPE));
  ({ handler } = await import('../../src/entries/aws-lambda.js'));
});

afterAll(() => {
  vi.unstubAllEnvs();
});

describe.each(Object.entries(BOTS))('AWS entry with the demo scope, bot with %s', (_, headers) => {
  it('passes a license document read to the origin', async () => {
    const result = await handler(makeEvent(`${PUB_ORIGIN}${LICENSE_PATH}`, headers));
    expect(isRequest(result)).toBe(true);
    if (!isRequest(result)) return;
    expect(result.uri).toBe(LICENSE_PATH);
  });

  it.each(SCOPE)('sends a read under %s to negotiate', async (prefix) => {
    const result = await handler(makeEvent(`${PUB_ORIGIN}${prefix}item`, headers));
    expect(isRequest(result)).toBe(false);
    if (isRequest(result)) return;
    expect(result.status).toBe('403');
    expect((JSON.parse(result.body ?? '{}') as { reason?: string }).reason).toBe('ai_bot');
  });
});
