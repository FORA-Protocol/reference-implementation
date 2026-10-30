import { vi } from 'vitest';

// The add-a-domain wizard as the page runs it: the four classic scripts the
// page loads, the boot attribute the server renders, and a stand-in for the
// console's routes. Alpine is not loaded. The component is the object wizard()
// returns, so a test builds it, stands in for Alpine's $watch, and drives its
// methods the way the page's buttons do.

export const steps = [
  { key: 'domain', title: 'Your domain' },
  { key: 'terms', title: 'Your terms' },
  { key: 'connect', title: 'Connect your site' },
];

/** businessStep comes first for an account that has not named its business. */
export const businessStep = { key: 'name', title: 'Your business' };

export const terms = () => ({
  pricing_model: 'free',
  rate: '',
  permitted_functions: ['search'],
  prohibited_functions: ['ai-train'],
  attribution_required: false,
});

/**
 * boot is what the server renders into the page's data-boot attribute. An
 * account that has not named its business (newAccount) gets the business step
 * first.
 */
export function boot(resume, newAccount) {
  return {
    resume,
    registered: !newAccount,
    currency: 'EUR',
    default_rate: '0.01',
    unit: 'fetch',
    terms: terms(),
    functions: [],
    steps: newAccount ? [businessStep, ...steps] : steps,
    paths: {
      publisher: '/api/publisher',
      lookup: '/api/domain/lookup',
      register: '/api/domain',
      terms: '/api/domain/terms',
      files: '/api/domain/files',
      check: '/api/domain/check',
      page: '/api/domain/page',
      terms_preview: '/api/domain/terms-preview',
      home: '/domains',
      domain_prefix: '/domains/',
    },
  };
}

/** saved is a saved domain's answers, as the setup page renders them. */
export function saved(step, extra = {}) {
  return {
    domain: 'stoa.example',
    cdn: 'cloudflare',
    network_label: 'Cloudflare',
    content_paths: '/news/',
    terms_set: step === 'connect',
    import_replaces_manual_terms: false,
    step,
    ...extra,
  };
}

/** found is what the lookup answers for a domain the publisher can save. */
export const found = {
  domain: 'stoa.example',
  provider: 'cloudflare',
  provider_label: 'Cloudflare',
  supported: true,
  taken: false,
  yours: false,
  verified: false,
  limit_reached: false,
};

/** pending is the last step's check before either task is done. */
export function pending() {
  return {
    domain: 'stoa.example',
    verified: false,
    file: {
      passed: false,
      state: 'missing',
      status: 'We found no file at /.well-known/fora.json on your site yet.',
      reasons: [],
    },
    package: {
      passed: false,
      state: 'absent',
      status: 'We asked your site as an AI agent would and it served the page.',
      checks: [],
    },
  };
}

/** verified is the last step's check once the domain is verified. */
export function verified() {
  const out = pending();
  out.verified = true;
  out.file = { passed: true, state: 'served', status: 'Your file is in place.', reasons: [] };
  out.package = { passed: true, state: 'deployed', status: 'Your package answered.', checks: [] };
  return out;
}

// What the console answers each route with, unless a test says otherwise.
const answers = {
  '/api/publisher': { publisher_id: 'pub_1', display_name: 'Stoa Press' },
  '/api/domain/lookup': found,
  '/api/domain': {
    domain: 'stoa.example',
    cdn: 'cloudflare',
    content_paths: '',
    setup: '/domains/stoa.example/setup',
  },
  '/api/domain/terms': { domain: 'stoa.example', verified: false },
  '/api/domain/files': { files: [], edge_steps: [], deployed: '' },
  '/api/domain/check': pending(),
  '/api/domain/page': { page_title: 'A page', page_url: 'https://stoa.example/a', sitemap_url: '' },
  '/api/domain/terms-preview': { lines: [] },
};

const mounted = [];

/**
 * failed is an answer the console refused or failed with: the status, and the
 * sentence its body carries.
 */
export function failed(status, error) {
  return new Response(JSON.stringify({ error }), { status });
}

/**
 * mount renders the wizard's element with its boot attribute, runs the page's
 * four scripts and starts the component. Each call is answered with
 * `replies[path]` -- a value, or a function called for each answer, which may
 * return a promise to hold the answer -- or the default answer, under the
 * status `statuses[path]` names and 200 otherwise. A reply that is a Response,
 * such as one failed() builds, is the answer as it stands. It returns the
 * component, every call it made, and the watchers it asked Alpine for. A
 * newAccount (true) is an account that has not named its business.
 */
export async function mount({ replies = {}, statuses = {}, resume, newAccount } = {}) {
  document.body.innerHTML = '<div id="wizard"></div>';
  document.getElementById('wizard').dataset.boot = JSON.stringify(boot(resume, newAccount));
  const calls = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (path, init) => {
      calls.push({ path, body: init?.body ? JSON.parse(init.body) : undefined });
      const reply = replies[path] ?? answers[path] ?? {};
      const answer = typeof reply === 'function' ? await reply() : reply;
      if (answer instanceof Response) return answer.clone();
      return new Response(JSON.stringify(answer), { status: statuses[path] ?? 200 });
    }),
  );
  vi.resetModules();
  await import('../../../internal/web/static/wizard-calls.js');
  await import('../../../internal/web/static/wizard-flow.js');
  await import('../../../internal/web/static/wizard-connect.js');
  await import('../../../internal/web/static/wizard.js');
  const w = globalThis.wizard();
  const watchers = {};
  w.$watch = (expression, fn) => {
    watchers[expression] = fn;
  };
  w.init();
  mounted.push(w);
  await settle();
  return { w, calls, watchers };
}

/**
 * settle lets the calls a step started without waiting on them land, so
 * nothing they write can overwrite what a test sets afterwards.
 */
export function settle() {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

/** paths is the routes the calls went to, in order. */
export function paths(calls) {
  return calls.map((c) => c.path);
}

/** unmount takes every wizard a test mounted down, as Alpine does with a page's. */
export function unmount() {
  for (const w of mounted.splice(0)) w.destroy();
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
}
