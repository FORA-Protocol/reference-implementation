// The publisher's own documents as its origin serves them, for the suites that
// check the edge passes both addresses through to the origin and returns the
// origin's answer unchanged. Each status, header set and body is one the edge
// never makes itself, so an answer the edge built cannot pass for the origin's.
import { expect } from 'vitest';

import { RSL_PATH, WELL_KNOWN_PATH } from '../../src/types.js';
import { fetchMock } from './fetch-mock.js';

// The origin the Workers-runtime projects name as ORIGIN_URL (vitest.config.ts),
// and the backend the Fastly entry suite configures.
export const ORIGIN = 'https://origin.pub.test';

export interface OriginAnswer {
  status: number;
  headers: Record<string, string>;
  body: string;
}

const DOCUMENTS: ReadonlyMap<string, OriginAnswer> = new Map([
  [
    WELL_KNOWN_PATH,
    {
      status: 200,
      headers: {
        'content-type': 'application/json',
        'cache-control': 'max-age=60',
        etag: '"origin-fora-json"',
      },
      body: '{"ver":"1.0","role":"ROLE_PUBLISHER","domain":"the-origin-file.example"}',
    },
  ],
  [
    RSL_PATH,
    {
      status: 200,
      headers: {
        'content-type': 'text/plain',
        'cache-control': 'max-age=300',
        etag: '"origin-rsl"',
      },
      body: '<rsl xmlns="https://rslstandard.org/rsl"><content url="/*"/></rsl>',
    },
  ],
]);

// The addresses of the documents above. Suites loop over this list rather than
// the edge's own PUBLISHER_DOCUMENT_PATHS, so a suite still asks for /rsl.txt,
// and fails, if the edge's list ever drops it.
export const DOCUMENT_PATHS: readonly string[] = [...DOCUMENTS.keys()];

// originDocument is the answer the origin gives for one of the publisher's
// document addresses.
export function originDocument(path: string): OriginAnswer {
  const answer = DOCUMENTS.get(path);
  if (!answer) throw new Error(`no origin document for ${path}`);
  return answer;
}

// A client a suite asks as: who it is, and the headers it sends.
export type DocumentReader = [who: string, headers: Record<string, string>];

// documentReads is every document address, each asked by each of readers: the
// rows of a table that checks every address for every client.
export function documentReads(
  readers: readonly DocumentReader[],
): [path: string, who: string, headers: Record<string, string>][] {
  return DOCUMENT_PATHS.flatMap((path) =>
    readers.map(([who, headers]): [string, string, Record<string, string>] => [path, who, headers]),
  );
}

// The headers only an answer the edge made itself carries: the refusal's two
// discovery headers and the CDN hand-back marker.
const EDGE_HEADERS = ['x-content-rules', 'x-fora-exchange', 'x-edge-authorized'];

// mockOrigin answers `path` (path and query, matched exactly) on the origin
// with `answer`, and returns a reader of how many requests reached it.
export function mockOrigin(path: string, answer: OriginAnswer, method = 'GET'): () => number {
  let hits = 0;
  fetchMock
    .get(ORIGIN)
    .intercept({ path, method })
    .reply(() => {
      hits += 1;
      return {
        statusCode: answer.status,
        data: answer.body,
        responseOptions: { headers: answer.headers },
      };
    })
    .persist();
  return () => hits;
}

// What expectOriginAnswer reads of a response. It is written out rather than
// taken from Response because Miniflare's dispatchFetch answers with undici's
// Response, not the Workers type this project declares, and both must fit.
export interface ReplyLike {
  status: number;
  headers: { get(name: string): string | null; has(name: string): boolean };
  text(): Promise<string>;
}

// expectOriginAnswer asserts `res` is `answer` unchanged, and carries nothing
// the edge adds to an answer of its own.
export async function expectOriginAnswer(res: ReplyLike, answer: OriginAnswer): Promise<void> {
  expect(res.status).toBe(answer.status);
  for (const [name, value] of Object.entries(answer.headers)) {
    expect(res.headers.get(name), name).toBe(value);
  }
  for (const name of EDGE_HEADERS) {
    expect(res.headers.has(name), name).toBe(false);
  }
  expect(await res.text()).toBe(answer.body);
}
