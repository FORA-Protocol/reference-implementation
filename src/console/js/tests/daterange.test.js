// @vitest-environment happy-dom
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// The overview's own period form over September 2026. A console test keeps
// this fixture equal to what the page renders, so these tests drive the
// picker the page actually draws.
// The form's own script tag is taken out: each test runs the script itself,
// after setting the day, and the DOM would otherwise try to fetch it.
const markup = readFileSync(
  join(process.cwd(), 'tests', 'testdata', 'period-picker.html'),
  'utf8',
).replace(/<script[^>]*><\/script>/, '');

const ms = (iso) => String(Date.parse(`${iso}T00:00:00Z`));

/**
 * mountPicker puts the period form on the page as though today were `today`,
 * with the fields holding `from` and `to` when given, runs the picker's script
 * on it, and returns what a test drives and reads.
 */
async function mountPicker(today, from, to) {
  vi.setSystemTime(new Date(`${today}T12:00:00Z`));
  document.body.innerHTML = markup;
  const form = document.querySelector('form');
  if (from) {
    form.elements.namedItem('from').value = from;
    form.elements.namedItem('to').value = to;
  }
  const submit = vi.fn();
  form.requestSubmit = submit;
  vi.resetModules();
  await import('../../internal/web/static/daterange.js');
  const q = (name) => document.querySelector(`[data-daterange-${name}]`);
  return {
    submit,
    q,
    field: (name) => form.elements.namedItem(name).value,
    open: () => q('trigger').click(),
    click: (selector) => document.querySelector(selector).click(),
    lit: () =>
      document.querySelector('[data-daterange-preset][aria-current="true"]')?.dataset
        .daterangePreset,
  };
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date'] });
});

afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

describe('the period picker on the page', () => {
  it('opens on the period its fields hold and lights the preset that period is', async () => {
    const p = await mountPicker('2026-10-22');
    p.open();
    expect(p.q('panel').hidden).toBe(false);
    expect(p.q('trigger').getAttribute('aria-expanded')).toBe('true');
    expect(p.lit()).toBe('lastMonth');
    expect(p.q('summary').textContent).toBe('1 Sep 2026 – 30 Sep 2026 (UTC)');
  });

  it('applies a preset by writing its days into the fields and submitting their form once', async () => {
    const p = await mountPicker('2026-10-22');
    p.open();
    p.click('[data-daterange-preset="last7"]');
    p.click('[data-daterange-apply]');
    expect([p.field('from'), p.field('to')]).toEqual(['2026-10-16', '2026-10-22']);
    expect(p.submit).toHaveBeenCalledTimes(1);
  });

  it('picks a range from two clicked days, and stays open while they are clicked', async () => {
    const p = await mountPicker('2026-10-22');
    p.open();
    p.click(`[data-daterange-day="${ms('2026-10-12')}"]`);
    p.click(`[data-daterange-day="${ms('2026-10-05')}"]`);
    expect(p.q('panel').hidden).toBe(false);
    p.click('[data-daterange-apply]');
    expect([p.field('from'), p.field('to')]).toEqual(['2026-10-05', '2026-10-12']);
  });

  it('will not apply a period longer than the server reads, and says why in its words', async () => {
    const p = await mountPicker('2026-10-22', '2024-01-01', '2026-01-01');
    p.open();
    expect(p.q('note').textContent).toBe('Choose a period of at most a year.');
    expect(p.q('apply').disabled).toBe(true);
    p.click('[data-daterange-apply]');
    expect(p.submit).not.toHaveBeenCalled();
  });

  it('lights the preset that was picked when two name the same days', async () => {
    // On 30 September, the last 30 days and this month are the same days.
    const p = await mountPicker('2026-09-30', '2026-09-10', '2026-09-12');
    p.open();
    p.click('[data-daterange-preset="last30"]');
    expect(p.lit()).toBe('last30');
    p.click('[data-daterange-preset="month"]');
    expect(p.lit()).toBe('month');
  });

  it('closes on Escape and on Cancel without writing anything', async () => {
    const p = await mountPicker('2026-10-22');
    p.open();
    p.click('[data-daterange-preset="last7"]');
    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(p.q('panel').hidden).toBe(true);
    p.open();
    p.click('[data-daterange-preset="last30"]');
    p.click('[data-daterange-cancel]');
    expect(p.q('panel').hidden).toBe(true);
    expect([p.field('from'), p.field('to')]).toEqual(['2026-09-01', '2026-09-30']);
    expect(p.submit).not.toHaveBeenCalled();
  });
});
