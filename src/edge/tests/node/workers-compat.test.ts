// The Workers runtime settings are defined once, in
// edgerelease/workers-compat.json. The console writes them into every
// publisher's wrangler.toml, and the test pools and the local Miniflare server
// read them through scripts/workers-compat.mjs. The checked-in wrangler.toml is
// the one configuration that cannot read JSON, so this suite reads it the way
// wrangler does and holds it to the same values.
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { unstable_readConfig } from 'wrangler';

import { WORKERS_COMPAT } from '../../scripts/workers-compat.mjs';
import { edgeDir } from '../helpers/release-build.js';

describe('the Workers runtime settings', () => {
  it('are the same in the checked-in wrangler.toml', () => {
    const config = unstable_readConfig({ config: join(edgeDir, 'wrangler.toml') });
    expect(config.compatibility_date).toBe(WORKERS_COMPAT.compatibilityDate);
    expect(config.compatibility_flags).toEqual(WORKERS_COMPAT.compatibilityFlags);
  });
});
