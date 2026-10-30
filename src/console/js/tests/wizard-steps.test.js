// @vitest-environment happy-dom
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  failed,
  found,
  mount,
  paths,
  saved,
  settle,
  terms,
  unmount,
  verified,
} from './support/wizard-mount.js';

// The add-a-domain wizard's steps before the last -- the business name, the
// domain and its terms -- each saved on its Next, and a saved domain reopened
// where it stands. The last step's checks are in wizard-connect.test.js.

afterEach(() => {
  vi.restoreAllMocks();
  unmount();
});

describe('the business step', () => {
  const newAccount = true;

  it('opens first, with the name empty', async () => {
    const { w, calls } = await mount({ newAccount });

    expect(w.steps.map((s) => s.key)).toEqual(['name', 'domain', 'terms', 'connect']);
    expect(w.step).toBe('name');
    expect(w.name).toBe('');
    expect(calls).toEqual([]);
  });

  it('offers no Finish later before the name is saved', async () => {
    const { w } = await mount({ newAccount });
    expect(w.canFinishLater).toBe(false);

    w.name = 'Stoa Press';
    await w.next();
    expect(w.step).toBe('domain');
    expect(w.canFinishLater).toBe(true);
  });

  it('is not shown again once its Next has saved the name', async () => {
    const { w, calls } = await mount({ newAccount });
    w.name = 'Stoa Press';
    await w.next();
    expect(w.step).toBe('domain');
    expect(w.stepDone(0)).toBe(true);
    expect(w.canReturnTo(0)).toBe(false);
    w.back();
    expect(w.step).toBe('domain');

    w.domain = 'stoa.example';
    await w.next();
    expect(w.step).toBe('terms');
    expect(w.canReturnTo(0)).toBe(false);
    expect(w.canReturnTo(1)).toBe(true);
    w.goBack(0);
    expect(w.step).toBe('terms');
    w.back();
    expect(w.step).toBe('domain');
    expect(paths(calls).filter((p) => p === '/api/publisher')).toHaveLength(1);
  });

  it("leaves the dead end only through the dead end's link to the domains", async () => {
    const { w } = await mount({
      newAccount,
      replies: {
        '/api/domain/lookup': {
          ...found,
          provider: 'akamai',
          provider_label: 'Akamai',
          supported: false,
        },
      },
    });
    w.name = 'Stoa Press';
    await w.next();
    w.domain = 'stoa.example';
    await w.next();
    expect(w.deadEnd).toBe(true);
    expect(w.canFinishLater).toBe(false);
    expect(w.canReturnTo(w.index - 1)).toBe(false);

    w.back();
    expect(w.step).toBe('domain');
    expect(w.deadEnd).toBe(true);
  });

  it('registers the name without the spaces around it, then opens the domain step', async () => {
    const { w, calls } = await mount({ newAccount });
    w.name = '  Stoa Press  ';
    await w.next();

    expect(paths(calls)).toEqual(['/api/publisher']);
    expect(calls[0].body).toEqual({ display_name: 'Stoa Press' });
    expect(w.step).toBe('domain');
  });

  it.each([[''], ['   ']])('keeps Next shut for the name %j', async (name) => {
    const { w, calls } = await mount({ newAccount });
    w.name = name;

    expect(w.canAdvance).toBe(false);
    await w.next();
    expect(calls).toEqual([]);
    expect(w.step).toBe('name');
  });

  it.each([
    [400, 'Please enter the name of your business.'],
    [502, 'Nothing was changed. Please try again in a moment.'],
  ])('stays on the step with the refusal the console answered %i with', async (status, refusal) => {
    const { w } = await mount({
      newAccount,
      replies: { '/api/publisher': failed(status, refusal) },
    });
    w.name = 'Stoa Press';
    await w.next();

    expect(w.error).toBe(refusal);
    expect(w.step).toBe('name');
  });

  it('counts itself in the step the package is installed at', async () => {
    const { w } = await mount({ newAccount });

    expect(w.connectStep).toBe(4);
    expect(w.networkNotice).toContain('You install that package at step 4.');
  });

  it('is not offered to an account that has named its business', async () => {
    const { w, calls } = await mount();

    expect(w.steps.map((s) => s.key)).toEqual(['domain', 'terms', 'connect']);
    expect(w.step).toBe('domain');
    expect(w.connectStep).toBe(3);
    w.domain = 'stoa.example';
    await w.next();
    expect(paths(calls)).not.toContain('/api/publisher');
  });
});

describe('the domain step', () => {
  it('looks the domain up on Next and saves it at once, then opens the licensing step', async () => {
    const remember = vi.spyOn(history, 'replaceState');
    const { w, calls } = await mount();
    w.domain = 'stoa.example';
    w.paths_ = '/news/';
    await w.next();

    expect(paths(calls).slice(0, 2)).toEqual(['/api/domain/lookup', '/api/domain']);
    expect(calls[1].body).toEqual({
      domain: 'stoa.example',
      cdn: 'cloudflare',
      content_paths: '/news/',
    });
    expect(w.step).toBe('terms');
    expect(w.saved).toBe(true);
    // The address becomes the page that finishes the domain's setup, so a
    // reload reopens it rather than an empty domain step.
    expect(remember).toHaveBeenCalledWith(null, '', '/domains/stoa.example/setup');
  });

  it('saves without looking up again when Enter already did', async () => {
    const { w, calls } = await mount();
    w.domain = 'stoa.example';
    await w.checkNetwork();
    await w.next();

    expect(paths(calls).slice(0, 2)).toEqual(['/api/domain/lookup', '/api/domain']);
  });

  it.each([
    [
      'taken',
      { ...found, taken: true, message: 'This domain is already registered to another publisher.' },
    ],
    ['unsupported', { ...found, provider: 'akamai', provider_label: 'Akamai', supported: false }],
    [
      'limitReached',
      { domain: 'stoa.example', limit_reached: true, message: 'You already have 10.' },
    ],
    [
      'yours',
      { domain: 'stoa.example', yours: true, verified: false, link: '/domains/stoa.example/setup' },
    ],
  ])('saves nothing when the lookup answers %s', async (flag, answer) => {
    const { w, calls } = await mount({ replies: { '/api/domain/lookup': answer } });
    w.domain = 'stoa.example';
    await w.next();

    expect(w[flag]).toBe(true);
    expect(paths(calls)).not.toContain('/api/domain');
    expect(w.step).toBe('domain');
    expect(w.saved).toBe(false);
    expect(w.canAdvance).toBe(false);
    // Only the dead end takes Next and Finish later away: its own link back
    // to the domains is the way out there.
    expect(w.deadEnd).toBe(flag === 'unsupported');
    expect(w.canFinishLater).toBe(flag !== 'unsupported');
  });

  it('stays on the step with the refusal when the save is refused', async () => {
    const refusal = 'This domain is already registered to another publisher.';
    const remember = vi.spyOn(history, 'replaceState');
    const { w } = await mount({
      replies: { '/api/domain': { error: refusal } },
      statuses: { '/api/domain': 400 },
    });
    w.domain = 'stoa.example';
    await w.next();

    expect(w.error).toBe(refusal);
    expect(w.step).toBe('domain');
    expect(w.saved).toBe(false);
    expect(remember).not.toHaveBeenCalled();
  });

  it('only moves on once the domain is saved', async () => {
    const { w, calls } = await mount();
    w.domain = 'stoa.example';
    await w.next();
    w.back();
    const before = calls.length;
    await w.next();

    expect(w.step).toBe('terms');
    expect(paths(calls.slice(before))).not.toContain('/api/domain');
    expect(paths(calls.slice(before))).not.toContain('/api/domain/lookup');
  });

  it('drops what was found about the domain before a new one is typed', async () => {
    const { w, watchers } = await mount();
    w.domain = 'stoa.example';
    await w.checkNetwork();

    // The lookup writing back the spelling it normalized is not a change.
    watchers.domain('stoa.example');
    expect(w.network).toBe('cloudflare');

    watchers.domain('harmonia.example');
    expect(w.network).toBe('');
    expect(w.lookedUp).toBe('');
  });
});

describe('the licensing step', () => {
  it('saves the terms with the sitemap and the catalog answer, then opens the last step', async () => {
    const { w, calls } = await mount({
      resume: saved('terms'),
      replies: {
        '/api/domain/page': {
          page_title: 'A',
          page_url: 'https://stoa.example/a',
          sitemap_url: 'https://stoa.example/s.xml',
        },
      },
    });
    w.importReplacesManualTerms = true;
    await w.next();

    const save = calls.find((c) => c.path === '/api/domain/terms');
    expect(save.body).toEqual({
      domain: 'stoa.example',
      terms: terms(),
      sitemap_url: 'https://stoa.example/s.xml',
      import_replaces_manual_terms: true,
    });
    expect(w.step).toBe('connect');
  });

  it('stays on the step with the refusal when the terms are refused', async () => {
    const refusal = 'These terms cannot be offered as they stand.';
    const { w } = await mount({
      resume: saved('terms'),
      replies: { '/api/domain/terms': { error: refusal } },
      statuses: { '/api/domain/terms': 400 },
    });
    await w.next();

    expect(w.error).toBe(refusal);
    expect(w.step).toBe('terms');
  });

  it('fills the terms of a domain saved without them from the site', async () => {
    const fromSite = { ...terms(), pricing_model: 'per_unit', rate: '0.05' };
    const { w } = await mount({
      resume: saved('terms'),
      replies: {
        '/api/domain/page': {
          page_title: 'A',
          page_url: 'https://stoa.example/a',
          terms: fromSite,
        },
      },
    });
    await vi.waitFor(() => expect(w.terms).toEqual(fromSite));
  });

  it('keeps the terms a domain was saved with', async () => {
    const fromSite = { ...terms(), pricing_model: 'per_unit', rate: '0.05' };
    const { w } = await mount({
      resume: saved('connect'),
      replies: {
        '/api/domain/page': {
          page_title: 'A',
          page_url: 'https://stoa.example/a',
          terms: fromSite,
        },
      },
    });
    w.back();
    await vi.waitFor(() => expect(w.page).not.toBeNull());
    expect(w.terms).toEqual(terms());
  });

  it("shows a verified domain's terms and saves nothing over them", async () => {
    const { w, calls } = await mount({
      resume: saved('connect'),
      replies: { '/api/domain/check': verified() },
    });
    expect(w.verified).toBe(true);
    w.back();
    await w.next();

    expect(paths(calls)).not.toContain('/api/domain/terms');
    expect(w.step).toBe('connect');
  });
});

describe('Finish later', () => {
  it.each([
    ['the domain step', {}, true],
    ['the licensing step', { resume: saved('terms') }, true],
    ['the last step before the domain is verified', { resume: saved('connect') }, true],
    [
      'the last step once the domain is verified',
      { resume: saved('connect'), replies: { '/api/domain/check': verified() } },
      false,
    ],
  ])('on %s is offered: %s', async (_, options, offered) => {
    const { w } = await mount(options);

    expect(w.canFinishLater).toBe(offered);
  });
});

describe('a saved domain reopened to finish its setup', () => {
  it('opens on the step the server names, with its saved answers, without looking it up', async () => {
    const { w, calls } = await mount({ resume: saved('connect') });

    expect(w.step).toBe('connect');
    expect(w.saved).toBe(true);
    expect(w.domain).toBe('stoa.example');
    expect(w.paths_).toBe('/news/');
    expect(paths(calls)).toEqual(
      expect.arrayContaining(['/api/domain/files', '/api/domain/check']),
    );
    expect(paths(calls)).not.toContain('/api/domain/lookup');
    await w.checkNetwork();
    expect(paths(calls)).not.toContain('/api/domain/lookup');
  });
});

describe('the browser', () => {
  // What is stored is read back rather than spied on: happy-dom binds each
  // storage method onto the storage object itself, so a spy on
  // Storage.prototype never sees a call.
  it('keeps nothing about the domain in its storage', async () => {
    sessionStorage.clear();
    localStorage.clear();
    const { w } = await mount();
    w.domain = 'stoa.example';
    await w.next();
    await w.next();
    await settle();

    expect(w.step).toBe('connect');
    expect(sessionStorage.length).toBe(0);
    expect(localStorage.length).toBe(0);
  });
});
