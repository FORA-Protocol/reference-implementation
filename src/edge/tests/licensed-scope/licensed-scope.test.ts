// A publisher that licenses part of its site: LICENSED_PATHS_JSON names
// /article/ (vitest.config.ts), and nothing else is for sale. This project runs
// the real Workers runtime through SELF, so the answers come from the route.
//
// Three properties, each a way the scope could go wrong. An AI bot reading
// outside the scope reaches the origin, because that content is not licensed
// and refusing it would block the publisher's public pages. The same bot inside
// the scope is sent to negotiate. And the discovery documents, which a denied
// bot is sent to, answer wherever the scope lies.
import { SELF } from 'cloudflare:test';
import { beforeAll, beforeEach, describe, expect, it } from 'vitest';

import { WELL_KNOWN_PATH } from '../../src/types.js';
import { fetchMock } from '../helpers/fetch-mock.js';
import { PUB_ORIGIN, setupE2ETest } from '../helpers/test-setup.js';

const ORIGIN_BODY = 'public page';
const BOT = { 'user-agent': 'GPTBot/1.0' };

beforeAll(async () => {
  await setupE2ETest();
});

beforeEach(() => {
  fetchMock.reset();
  fetchMock
    .get('https://origin.pub.test')
    .intercept({ path: /.*/, method: 'GET' })
    .reply(200, ORIGIN_BODY)
    .persist();
});

describe('an AI bot on a site that licenses /article/', () => {
  it('reaches the origin outside the licensed paths', async () => {
    const res = await SELF.fetch(`${PUB_ORIGIN}/about/team`, { headers: BOT });
    expect(res.status).toBe(200);
    expect(await res.text()).toBe(ORIGIN_BODY);
  });

  it('is sent to negotiate inside them', async () => {
    const res = await SELF.fetch(`${PUB_ORIGIN}/article/42`, { headers: BOT });
    expect(res.status).toBe(403);
    expect(((await res.json()) as { reason: string }).reason).toBe('ai_bot');
  });

  it('is sent to negotiate when the licensed path is spelled another way', async () => {
    const res = await SELF.fetch(`${PUB_ORIGIN}/%61rticle/42`, { headers: BOT });
    expect(res.status).toBe(403);
  });

  it('can still read the discovery document', async () => {
    const res = await SELF.fetch(`${PUB_ORIGIN}${WELL_KNOWN_PATH}`, { headers: BOT });
    expect(res.status).toBe(200);
    expect(((await res.json()) as { role: string }).role).toBe('ROLE_PUBLISHER');
  });
});
