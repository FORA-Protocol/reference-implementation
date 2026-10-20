import { describe, expect, it, vi } from 'vitest';

import { CDN_AUTHORIZED_HEADER, createApp } from '../../src/app.js';
import { buildDeps, parseEnv } from '../../src/config.js';

describe('publisher RSL document (FORA-318)', () => {
  it.each([undefined, '', ' \t\r\n', ' \n<rsl xmlns="https://rslstandard.org/rsl"/>\n'])(
    'serves only configured content: %j',
    async (body) => {
      const fetcher = vi.fn();
      const app = createApp({
        ...buildDeps(
          parseEnv({
            EXCHANGE_URL: 'https://exchange.test',
            EXCHANGE_WBA_URL: 'https://exchange.test/.well-known/http-message-signatures-directory',
            PROVIDER: 'publisher.test',
            EXCHANGES_JSON: '[{"domain":"exchange.test","endpoint":"https://exchange.test"}]',
            RSL_BODY: body,
            ORIGIN_URL: 'https://origin.test',
          }),
        ),
        fetcher,
      });
      const res = await app.request('/rsl.txt');
      expect(res.status).toBe(body?.trim() ? 200 : 404);
      if (body?.trim()) {
        expect(await res.text()).toBe(body);
        expect(res.headers.get('content-type')).toBe('text/plain');
      }
      expect(res.headers.has(CDN_AUTHORIZED_HEADER)).toBe(false);
      expect(fetcher).not.toHaveBeenCalled();
    },
  );
});
