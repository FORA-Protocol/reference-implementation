// The Workers runtime settings the release is built and tested under, in the
// shape Miniflare and the Workers test pool take.
//
// They are read from edgerelease/workers-compat.json, which the console also
// embeds and writes into every publisher's wrangler.toml. So the runtime a
// publisher's Worker is deployed under is the runtime these tests ran. The
// checked-in wrangler.toml cannot read JSON; tests/node/workers-compat.test.ts
// holds it equal to this file.

import { readFileSync } from 'node:fs';

const raw = JSON.parse(
  readFileSync(new URL('../edgerelease/workers-compat.json', import.meta.url), 'utf8'),
);

export const WORKERS_COMPAT = {
  compatibilityDate: raw.compatibility_date,
  compatibilityFlags: raw.compatibility_flags,
};
