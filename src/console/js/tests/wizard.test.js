// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest';

// The add-a-domain wizard as the page runs it: the three classic scripts the
// page loads, the boot attribute the server renders, and the browser's session
// storage. Alpine is not loaded. The component is the object wizard() returns,
// so a test builds it, stands in for Alpine's $watch and for the browser's
// navigation, and drives its methods the way the page's buttons do.

const STORE = 'fora.add-domain';

const terms = () => ({
  pricing_model: 'free',
  rate: '',
  permitted_functions: ['search'],
  prohibited_functions: ['ai-train'],
  attribution_required: false,
});

const nameStep = { key: 'name', title: 'Your business' };
const domainSteps = [
  { key: 'domain', title: 'Your domain' },
  { key: 'document', title: 'Publish a file' },
  { key: 'package', title: 'Install the package' },
  { key: 'terms', title: 'Set your terms' },
];

/** boot is what the server renders into the page's data-boot attribute. */
function boot(registered, resume) {
  return {
    resume,
    registered,
    has_domains: false,
    display_name: registered ? 'Stoa Press' : '',
    currency: 'EUR',
    default_rate: '0.01',
    unit: 'access',
    terms: terms(),
    functions: [],
    steps: registered ? domainSteps : [nameStep, ...domainSteps],
    paths: {
      publisher: '/api/publisher',
      lookup: '/api/domain/lookup',
      files: '/api/domain/files',
      check_manifest: '/api/domain/check-manifest',
      check_edge: '/api/domain/check-edge',
      page: '/api/domain/page',
      terms_preview: '/api/domain/terms-preview',
      register: '/api/domain',
      progress: '/api/domain/progress',
      home: '/domains',
      domain_prefix: '/domains/',
    },
  };
}

/** record is what an earlier visit left in session storage. */
function record(step, version = 6) {
  return {
    v: version,
    step,
    name: 'Stoa Press',
    domain: 'stoa.example',
    paths: '',
    network: 'cloudflare',
    terms: terms(),
    terms_settled: false,
    import_replaces_manual_terms: false,
  };
}

// What the console answers each route with, unless a test says otherwise.
const answers = {
  '/api/domain/files': { files: [], edge_steps: [] },
  '/api/domain/check-manifest': { passed: false, reasons: [] },
  '/api/domain/check-edge': { passed: false, state: '', message: '', checks: [] },
  '/api/domain/page': { page_title: 'A page', page_url: 'https://stoa.example/a', sitemap_url: '' },
  '/api/domain/terms-preview': { lines: [] },
  '/api/domain/progress': {
    verified: false,
    step: 'document',
    document: { passed: false, reasons: [] },
    edge: { passed: false, state: '', message: '', checks: [] },
  },
};

/**
 * mount renders the wizard's element with its boot attribute, leaves `stored`
 * in session storage when given, runs the page's three scripts, and starts the
 * component. Each call is answered with `replies[path]`, or the default answer,
 * under the status `statuses[path]` names and 200 otherwise. It returns the
 * component, every call it made, and the watchers it asked Alpine for.
 */
async function mount({ registered = true, stored, replies = {}, statuses = {}, resume } = {}) {
  sessionStorage.clear();
  if (stored) sessionStorage.setItem(STORE, JSON.stringify(stored));
  document.body.innerHTML = '<div id="wizard"></div>';
  document.getElementById('wizard').dataset.boot = JSON.stringify(boot(registered, resume));
  const calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path, init) => {
      calls.push({ path, body: init?.body ? JSON.parse(init.body) : undefined });
      const answer = replies[path] ?? answers[path] ?? {};
      return new Response(JSON.stringify(answer), { status: statuses[path] ?? 200 });
    }),
  );
  vi.resetModules();
  await import('../../internal/web/static/wizard-calls.js');
  await import('../../internal/web/static/wizard-flow.js');
  await import('../../internal/web/static/wizard.js');
  const w = globalThis.wizard();
  const watchers = {};
  w.$watch = (expression, fn) => {
    watchers[expression] = fn;
  };
  w.navigate = vi.fn();
  w.init();
  // Entering the opening step starts that step's checks without waiting on
  // them. Letting them land first means nothing they write can overwrite what
  // a test sets afterwards.
  await new Promise((resolve) => setTimeout(resolve, 0));
  return { w, calls, watchers };
}

afterEach(() => {
  vi.unstubAllGlobals();
  sessionStorage.clear();
  document.body.innerHTML = '';
});

describe('the wizard reopened in the same tab', () => {
  it('opens on the step the earlier visit left, found by its name', async () => {
    const withName = await mount({ registered: false, stored: record('package') });
    expect(withName.w.step).toBe('package');
    expect(withName.w.index).toBe(3);

    // The business step is gone once it is answered, so the same step sits
    // one position earlier. The record names the step, not the position.
    const without = await mount({ registered: true, stored: record('package') });
    expect(without.w.step).toBe('package');
    expect(without.w.index).toBe(2);
    expect(without.w.domain).toBe('stoa.example');
  });

  it('starts again when the record was written by another version', async () => {
    const { w } = await mount({ stored: record('package', 4) });
    expect(w.step).toBe('domain');
    expect(w.domain).toBe('');
    expect(w.network).toBe('');
  });
});

describe('the domain step', () => {
  it('stops at the limit without calling the domain a dead end', async () => {
    const message = 'You already have 10 domains that are not verified.';
    const { w } = await mount({
      replies: {
        '/api/domain/lookup': { domain: 'harmonia.example', limit_reached: true, message },
      },
    });
    w.domain = 'harmonia.example';
    await w.checkNetwork();

    expect(w.limitReached).toBe(true);
    expect(w.message).toBe(message);
    // Not the unsupported-network screen, which drops what the wizard holds.
    expect(w.unsupported).toBe(false);
    expect(w.canAdvance).toBe(false);
  });
});

describe('changing the domain', () => {
  it('drops what was found about the domain before it', async () => {
    const { w, watchers } = await mount();
    w.lookedUp = 'stoa.example';
    w.network = 'cloudflare';
    w.checks.document = { passed: true, reasons: [] };

    // The lookup writing back the spelling it normalized is not a change.
    watchers.domain('stoa.example');
    expect(w.network).toBe('cloudflare');

    watchers.domain('harmonia.example');
    expect(w.network).toBe('');
    expect(w.lookedUp).toBe('');
    expect(w.checks.document.passed).toBe(false);
  });
});

describe('Next', () => {
  it('registers the business name on the first step', async () => {
    const { w, calls } = await mount({ registered: false });
    w.name = 'Stoa Press';
    await w.next();
    expect(calls).toContainEqual({ path: '/api/publisher', body: { display_name: 'Stoa Press' } });
    expect(w.step).toBe('domain');
  });

  it('moves on from a step that has nothing to send', async () => {
    const { w, calls } = await mount();
    await w.next();
    expect(w.step).toBe('document');
    expect(calls.map((c) => c.path)).not.toContain('/api/domain');
  });

  it('sends the answers on the last step and opens the domain once it is verified', async () => {
    const { w, calls } = await mount({
      stored: record('terms'),
      replies: { '/api/domain': { domain: 'stoa.example', verified: true } },
    });
    w.paths_ = '/news/';
    w.page = {
      title: 'A page',
      url: 'https://stoa.example/a',
      sitemap: 'https://stoa.example/s.xml',
    };
    w.importReplacesManualTerms = true;
    await w.next();

    const registration = calls.find((c) => c.path === '/api/domain');
    expect(registration.body).toEqual({
      domain: 'stoa.example',
      cdn: 'cloudflare',
      content_paths: '/news/',
      sitemap_url: 'https://stoa.example/s.xml',
      terms: terms(),
      import_replaces_manual_terms: true,
    });
    expect(w.navigate).toHaveBeenCalledWith('/domains/stoa.example');
    expect(sessionStorage.getItem(STORE)).toBeNull();
  });

  it('takes the publisher to their domains when the saved domain is not verified', async () => {
    const { w } = await mount({
      stored: record('terms'),
      replies: { '/api/domain': { domain: 'stoa.example', verified: false } },
    });
    await w.next();

    // Saved either way: the wizard's record goes, and the list is where the
    // publisher sees the domain as not verified.
    expect(w.navigate).toHaveBeenCalledWith('/domains');
    expect(sessionStorage.getItem(STORE)).toBeNull();
  });
});

// A saved domain's answers, as the setup page renders them.
const saved = {
  domain: 'stoa.example',
  cdn: 'cloudflare',
  network_label: 'Cloudflare',
  content_paths: '/news/',
  terms_set: true,
  import_replaces_manual_terms: true,
};

describe('a saved domain reopened to finish its setup', () => {
  it('opens on the step the server names, without asking the site again', async () => {
    const { w, calls } = await mount({
      resume: saved,
      replies: {
        '/api/domain/progress': {
          verified: false,
          step: 'package',
          document: { passed: true, reasons: [] },
          edge: { passed: false, state: 'absent', message: 'Nothing answers yet.', checks: [] },
        },
      },
    });
    expect(w.resuming).toBe(true);
    expect(w.step).toBe('package');
    expect(w.domain).toBe('stoa.example');
    expect(w.paths_).toBe('/news/');
    expect(w.edge.state).toBe('absent');
    expect(calls.filter((c) => c.path === '/api/domain/progress')).toHaveLength(1);
    expect(calls.map((c) => c.path)).not.toContain('/api/domain/check-edge');
    // The saved answers are the Exchange's; this tab keeps no copy.
    expect(sessionStorage.getItem(STORE)).toBeNull();
  });

  it('goes to the domain when it was verified meanwhile', async () => {
    const { w } = await mount({
      resume: saved,
      replies: { '/api/domain/progress': { verified: true, document: {}, edge: {} } },
    });
    expect(w.navigate).toHaveBeenCalledWith('/domains/stoa.example');
  });

  it('fills the terms of a domain saved without them from the site, as a first visit does', async () => {
    const fromSite = { ...terms(), pricing_model: 'per_unit', rate: '0.05' };
    const { w } = await mount({
      resume: { ...saved, terms_set: false },
      replies: { '/api/domain/progress': atTerms, '/api/domain/page': pageWith(fromSite) },
    });
    await vi.waitFor(() => expect(w.terms).toEqual(fromSite));
  });

  it('keeps the terms a domain was saved with', async () => {
    const fromSite = { ...terms(), pricing_model: 'per_unit', rate: '0.05' };
    const { w } = await mount({
      resume: saved,
      replies: { '/api/domain/progress': atTerms, '/api/domain/page': pageWith(fromSite) },
    });
    await vi.waitFor(() => expect(w.page).not.toBeNull());
    expect(w.terms).toEqual(terms());
  });

  it("leaves this tab's record of another domain being added", async () => {
    const adding = { ...record('document'), domain: 'harmonia.example' };
    const { w } = await mount({
      resume: saved,
      stored: adding,
      replies: { '/api/domain': { domain: 'stoa.example', verified: false } },
    });
    expect(w.domain).toBe('stoa.example');
    await w.finish();

    expect(w.navigate).toHaveBeenCalledWith('/domains');
    expect(JSON.parse(sessionStorage.getItem(STORE))).toEqual(adding);
  });

  it('does not look the saved domain up again', async () => {
    const { w, calls } = await mount({ resume: saved });
    await w.checkNetwork();

    expect(calls.map((c) => c.path)).not.toContain('/api/domain/lookup');
    expect(w.yours).toBe(false);
  });
});

// atTerms is the progress of a domain whose file and package both answer.
const atTerms = {
  verified: false,
  step: 'terms',
  document: { passed: true, reasons: [] },
  edge: { passed: true, state: 'deployed', message: '', checks: [] },
};

/** pageWith is the page answer when the site's own licensing says `found`. */
function pageWith(found) {
  return {
    page_title: 'A page',
    page_url: 'https://stoa.example/a',
    sitemap_url: '',
    terms: found,
  };
}

describe('a domain the publisher already holds', () => {
  it('links to it instead of adding it again', async () => {
    const { w } = await mount({
      replies: {
        '/api/domain/lookup': {
          domain: 'stoa.example',
          yours: true,
          verified: false,
          link: '/domains/stoa.example/setup',
        },
      },
    });
    w.domain = 'stoa.example';
    await w.checkNetwork();

    expect(w.yours).toBe(true);
    expect(w.yoursLink).toBe('/domains/stoa.example/setup');
    expect(w.network).toBe('');
    expect(w.canAdvance).toBe(false);
  });
});

describe('Skip for now', () => {
  it('is not offered on the business name, which every publisher needs', async () => {
    const { w } = await mount({ registered: false });
    expect(w.skippable).toBe(false);
    w.skip();

    expect(w.step).toBe('name');
    expect(w.navigate).not.toHaveBeenCalled();
  });

  it('leaves the domain step with nothing saved, ending registration with no domain', async () => {
    const { w, calls } = await mount({ registered: false });
    w.name = 'Stoa Press';
    await w.next();
    expect(w.step).toBe('domain');
    expect(w.skippable).toBe(true);
    w.skip();

    expect(w.navigate).toHaveBeenCalledWith('/domains');
    expect(calls.map((c) => c.path)).toContain('/api/publisher');
    expect(calls.map((c) => c.path)).not.toContain('/api/domain');
    expect(sessionStorage.getItem(STORE)).toBeNull();
  });

  it('is not offered on a domain no package exists for, which has its own way back', async () => {
    const { w } = await mount({
      replies: {
        '/api/domain/lookup': {
          domain: 'stoa.example',
          provider: 'akamai',
          provider_label: 'Akamai',
          supported: false,
          taken: false,
        },
      },
    });
    w.domain = 'stoa.example';
    await w.checkNetwork();

    expect(w.unsupported).toBe(true);
    expect(w.skippable).toBe(false);
  });

  it('moves on from the file step and marks it skipped', async () => {
    const { w } = await mount({ stored: record('document') });
    expect(w.skippable).toBe(true);
    w.skip();

    expect(w.step).toBe('package');
    expect(w.stepClass(0)).toBe('wiz-step wiz-done');
    expect(w.stepClass(1)).toBe('wiz-step wiz-skipped');
    expect(JSON.parse(sessionStorage.getItem(STORE)).skipped).toEqual(['document']);
  });

  it('is not offered on a step whose check passed, which Next leaves', async () => {
    const { w } = await mount({
      stored: record('document'),
      replies: { '/api/domain/check-manifest': { passed: true, reasons: [] } },
    });
    await vi.waitFor(() => expect(w.checks.document.passed).toBe(true));
    expect(w.skippable).toBe(false);
  });

  it('counts a skipped step as done once Next leaves it', async () => {
    const { w } = await mount({
      stored: { ...record('package'), skipped: ['document'] },
      replies: { '/api/domain/check-manifest': { passed: true, reasons: [] } },
    });
    expect(w.stepClass(1)).toBe('wiz-step wiz-skipped');
    w.goBack(1);
    await vi.waitFor(() => expect(w.checks.document.passed).toBe(true));
    await w.next();

    expect(w.step).toBe('package');
    expect(w.stepClass(1)).toBe('wiz-step wiz-done');
  });

  it('saves the domain with no terms on the last step', async () => {
    const { w, calls } = await mount({
      stored: record('terms'),
      replies: { '/api/domain': { domain: 'stoa.example', verified: false } },
    });
    await w.skip();

    const registration = calls.find((c) => c.path === '/api/domain');
    expect(registration.body.terms).toBeNull();
    expect(w.navigate).toHaveBeenCalledWith('/domains');
    expect(sessionStorage.getItem(STORE)).toBeNull();
  });

  it('only leaves the licensing step of a reopened domain, which is saved already', async () => {
    const { w, calls } = await mount({
      resume: saved,
      replies: { '/api/domain/progress': atTerms },
    });
    expect(w.step).toBe('terms');
    await w.skip();

    expect(w.navigate).toHaveBeenCalledWith('/domains');
    expect(calls.map((c) => c.path)).not.toContain('/api/domain');
  });
});

// What a lookup answers for a domain behind a network the console equips.
const found = {
  domain: 'stoa.example',
  provider: 'cloudflare',
  provider_label: 'Cloudflare',
  supported: true,
  taken: false,
  manifest: { passed: false, reasons: [] },
};

describe('a second Check on the same domain', () => {
  it.each([
    [
      'limitReached',
      {
        domain: 'stoa.example',
        limit_reached: true,
        message: 'You already have 10 domains that are not verified.',
      },
    ],
    [
      'yours',
      { domain: 'stoa.example', yours: true, verified: false, link: '/domains/stoa.example/setup' },
    ],
  ])('drops the network found before when it answers %s', async (flag, second) => {
    const replies = { '/api/domain/lookup': found };
    const { w, watchers } = await mount({ replies });
    w.domain = 'stoa.example';
    await w.checkNetwork();
    expect(w.canAdvance).toBe(true);

    replies['/api/domain/lookup'] = second;
    await w.checkNetwork();
    // The lookup writes back the name it answered for, which is not a change.
    watchers.domain(w.domain);

    expect(w[flag]).toBe(true);
    expect(w.network).toBe('');
    expect(w.canAdvance).toBe(false);
  });
});

describe('a refused save', () => {
  it.each([
    ['Finish setup', (w) => w.next()],
    ['Skip for now', (w) => w.skip()],
  ])(
    'keeps the publisher on the licensing step with their answers when %s is refused',
    async (_, press) => {
      const refusal = 'You already have 10 domains that are not verified.';
      const { w } = await mount({
        stored: record('terms'),
        replies: { '/api/domain': { error: refusal } },
        statuses: { '/api/domain': 400 },
      });
      await press(w);

      expect(w.error).toBe(refusal);
      expect(w.step).toBe('terms');
      expect(w.navigate).not.toHaveBeenCalled();
      expect(JSON.parse(sessionStorage.getItem(STORE))?.domain).toBe('stoa.example');
    },
  );
});

describe('a saved domain whose setup cannot be read', () => {
  it('stays on the domain step with the refusal', async () => {
    const refusal = 'That domain is not one of yours.';
    const { w } = await mount({
      resume: saved,
      replies: { '/api/domain/progress': { error: refusal } },
      statuses: { '/api/domain/progress': 404 },
    });
    await expect(w.resumeAt()).resolves.toBeUndefined();

    expect(w.error).toBe(refusal);
    expect(w.step).toBe('domain');
    expect(w.navigate).not.toHaveBeenCalled();
  });
});
