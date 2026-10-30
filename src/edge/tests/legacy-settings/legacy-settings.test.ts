// A Worker deployed from a package an earlier release built. vitest.config.ts
// gives this project the settings that release made the discovery document and
// the RSL file from (tests/helpers/earlier-release-settings.ts). This release
// reads none of them: the Worker starts, ignores them, and answers both
// addresses from the origin, never with a document built from those settings.
// The gate it enforces is unchanged.
import { SELF } from 'cloudflare:test';
import { beforeAll, beforeEach, describe, expect, it } from 'vitest';

import { PUBLISHER_DOCUMENT_PATHS } from '../../src/types.js';
import { fetchMock } from '../helpers/fetch-mock.js';
import { expectOriginAnswer, mockOrigin, originDocument } from '../helpers/publisher-documents.js';
import { PUB_ORIGIN, setupE2ETest } from '../helpers/test-setup.js';

const AI_BOT = { 'user-agent': 'GPTBot/1.0' };
const BROWSER = { 'user-agent': 'Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) Safari/605.1.15' };

beforeAll(async () => {
  await setupE2ETest();
});

beforeEach(() => {
  fetchMock.reset();
});

describe("a Worker that still carries an earlier release's settings", () => {
  it.each(PUBLISHER_DOCUMENT_PATHS)('answers %s from the origin', async (path) => {
    const answer = originDocument(path);
    const hits = mockOrigin(path, answer);
    await expectOriginAnswer(await SELF.fetch(`${PUB_ORIGIN}${path}`, { headers: AI_BOT }), answer);
    expect(hits()).toBe(1);
  });

  it('still refuses an AI bot an article', async () => {
    const res = await SELF.fetch(`${PUB_ORIGIN}/article/42`, { headers: AI_BOT });
    expect(res.status).toBe(403);
    expect(((await res.json()) as { reason: string }).reason).toBe('ai_bot');
  });

  it('still serves a person the article from the origin', async () => {
    const article = { status: 200, headers: { 'content-type': 'text/html' }, body: 'the article' };
    mockOrigin('/article/42', article);
    await expectOriginAnswer(
      await SELF.fetch(`${PUB_ORIGIN}/article/42`, { headers: BROWSER }),
      article,
    );
  });
});
