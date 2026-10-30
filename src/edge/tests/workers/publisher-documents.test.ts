// The publisher's own documents, its discovery document and its RSL file, are
// files on its origin. This project runs the real Workers runtime through SELF
// on a site whose whole hostname is licensed, so both addresses sit inside the
// licensed paths: the edge forwards each to the origin, for every client, and
// returns the origin's answer unchanged. The licensed-scope project covers the
// same addresses outside the licensed paths.
import { SELF } from 'cloudflare:test';
import { beforeAll, beforeEach, describe, expect, it } from 'vitest';

import { PUBLISHER_DOCUMENT_PATHS } from '../../src/types.js';
import { fetchMock } from '../helpers/fetch-mock.js';
import {
  type OriginAnswer,
  expectOriginAnswer,
  mockOrigin,
  originDocument,
} from '../helpers/publisher-documents.js';
import { PUB_ORIGIN, setupE2ETest } from '../helpers/test-setup.js';

const BROWSER = { 'user-agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605.1.15' };
const AI_BOT = { 'user-agent': 'GPTBot/1.0' };
const CLIENTS: [string, Record<string, string>][] = [
  ['a browser', BROWSER],
  ['an AI bot', AI_BOT],
  ['a client with no User-Agent', {}],
];

beforeAll(async () => {
  await setupE2ETest();
});

beforeEach(() => {
  fetchMock.reset();
});

describe.each(PUBLISHER_DOCUMENT_PATHS)('%s', (path) => {
  const answer = originDocument(path);

  it.each(CLIENTS)("is the origin's answer for %s", async (_who, headers) => {
    const hits = mockOrigin(path, answer);
    const res = await SELF.fetch(`${PUB_ORIGIN}${path}`, { headers });
    await expectOriginAnswer(res, answer);
    expect(hits()).toBe(1);
  });

  it('reaches the origin without the reserved signature params, the rest of the query kept', async () => {
    // A signed-looking query is not checked here: the document is not content,
    // so nothing about it is for sale. The params are stripped like on every
    // forward, so the origin never sees attribution data it could take as
    // verified.
    const hits = mockOrigin(`${path}?lang=en`, answer);
    const res = await SELF.fetch(
      `${PUB_ORIGIN}${path}?sig=forged&kid=k1&exp=1&agent_id=a1&lang=en`,
      {
        headers: AI_BOT,
      },
    );
    await expectOriginAnswer(res, answer);
    expect(hits()).toBe(1);
  });

  it("answers a HEAD with the origin's status and headers", async () => {
    const hits = mockOrigin(path, answer, 'HEAD');
    const res = await SELF.fetch(`${PUB_ORIGIN}${path}`, { method: 'HEAD', headers: AI_BOT });
    expect(res.status).toBe(answer.status);
    expect(res.headers.get('etag')).toBe(answer.headers.etag);
    expect(hits()).toBe(1);
  });

  it("passes the origin's 404 through when the site has no such file", async () => {
    const missing: OriginAnswer = {
      status: 404,
      headers: { 'content-type': 'text/html' },
      body: '<h1>no such file</h1>',
    };
    mockOrigin(path, missing);
    await expectOriginAnswer(
      await SELF.fetch(`${PUB_ORIGIN}${path}`, { headers: AI_BOT }),
      missing,
    );
  });

  it('answers 502 when the origin cannot be reached, and never makes the document itself', async () => {
    // No origin answer is registered, and the fetch mock refuses every other
    // fetch, so the forward fails the way an unreachable origin does.
    const res = await SELF.fetch(`${PUB_ORIGIN}${path}`, { headers: AI_BOT });
    expect(res.status).toBe(502);
    expect(((await res.json()) as { reason: string }).reason).toBe('origin_fetch_failed');
  });

  it('stays gated under any other spelling of the address', async () => {
    // The exemption is the exact address. A trailing slash is another path,
    // inside the licensed paths like the rest of the site.
    const hits = mockOrigin(`${path}/`, answer);
    const res = await SELF.fetch(`${PUB_ORIGIN}${path}/`, { headers: AI_BOT });
    expect(res.status).toBe(403);
    expect(hits()).toBe(0);
  });
});

describe('the gate the documents bypass', () => {
  it('still refuses an AI bot an article', async () => {
    const hits = mockOrigin('/article/42', { status: 200, headers: {}, body: 'the article' });
    const res = await SELF.fetch(`${PUB_ORIGIN}/article/42`, { headers: AI_BOT });
    expect(res.status).toBe(403);
    expect(hits()).toBe(0);
  });
});
