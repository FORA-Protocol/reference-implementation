// @vitest-environment happy-dom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  failed,
  mount,
  pending,
  saved,
  settle,
  unmount,
  verified,
} from './support/wizard-mount.js';

// The add-a-domain wizard's last step, Connect your site: the check it makes
// when it opens, again every 15 seconds while it is shown, and on Check now.
// Only the repeating timer is faked; the calls themselves still land on the
// event loop, which settle() waits for.

const RECHECK_MS = 15000;

let hidden = false;

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
  hidden = false;
  Object.defineProperty(document, 'hidden', { configurable: true, get: () => hidden });
});

afterEach(() => {
  unmount();
  vi.useRealTimers();
});

/** checks counts the calls the step made to its check. */
function checks(calls) {
  return calls.filter((c) => c.path === '/api/domain/check').length;
}

/** wait lets the step's timer fire for ms, and its calls land. */
async function wait(ms) {
  vi.advanceTimersByTime(ms);
  await settle();
}

/** file is one of the files the last step hands over. */
function file() {
  return {
    name: 'fora.json',
    description: '',
    body: '{}',
    url: '/domains/stoa.example/files/fora.json',
  };
}

/** setHidden tells the page the tab went to the background or came back. */
async function setHidden(value) {
  hidden = value;
  document.dispatchEvent(new Event('visibilitychange'));
  await settle();
}

describe('the last step', () => {
  it('draws both tasks from the check it makes when it opens', async () => {
    const { w, calls } = await mount({ resume: saved('connect') });

    expect(checks(calls)).toBe(1);
    expect(w.connection.file.status).toBe(pending().file.status);
    expect(w.connection.package.status).toBe(pending().package.status);
    expect(w.fileBadge).toBe('Not found yet');
    expect(w.packageBadge).toBe('Not installed yet');
    expect(w.checkedAt).not.toBe('');
  });

  it('checks again every 15 seconds while it is shown', async () => {
    const { calls } = await mount({ resume: saved('connect') });
    await wait(RECHECK_MS);
    expect(checks(calls)).toBe(2);
    await wait(RECHECK_MS);
    expect(checks(calls)).toBe(3);
  });

  it('stops checking once the domain is verified, and says so', async () => {
    let answered = 0;
    const { w, calls } = await mount({
      resume: saved('connect'),
      replies: { '/api/domain/check': () => (++answered < 2 ? pending() : verified()) },
    });
    await wait(RECHECK_MS);
    expect(w.verified).toBe(true);
    await wait(3 * RECHECK_MS);

    expect(checks(calls)).toBe(2);
    expect(w.stepClass(2)).toBe('wiz-step wiz-done');
    expect(w.fileBadge).toBe('Served');
    expect(w.packageBadge).toBe('Answering');
  });

  it('stops checking when the publisher goes back a step', async () => {
    const { w, calls } = await mount({ resume: saved('connect') });
    w.back();
    await wait(3 * RECHECK_MS);
    expect(checks(calls)).toBe(1);
  });

  it('stops checking on a refusal no later check would change', async () => {
    const { w, calls } = await mount({
      resume: saved('connect'),
      replies: { '/api/domain/check': { error: 'That domain is not one of yours.' } },
      statuses: { '/api/domain/check': 404 },
    });
    await wait(3 * RECHECK_MS);

    expect(w.pageError).toBe('That domain is not one of yours.');
    expect(checks(calls)).toBe(1);
  });

  it('keeps checking after a failure the next check may not meet', async () => {
    const { w, calls } = await mount({
      resume: saved('connect'),
      replies: { '/api/domain/check': { error: 'We could not reach the exchange.' } },
      statuses: { '/api/domain/check': 502 },
    });
    await wait(RECHECK_MS);

    expect(w.pageError).toBe('We could not reach the exchange.');
    expect(checks(calls)).toBe(2);
  });

  it('pauses while the tab is hidden, and checks at once when it comes back', async () => {
    const { calls } = await mount({ resume: saved('connect') });
    await setHidden(true);
    await wait(3 * RECHECK_MS);
    expect(checks(calls)).toBe(1);

    await setHidden(false);
    expect(checks(calls)).toBe(2);
    await wait(RECHECK_MS);
    expect(checks(calls)).toBe(3);
  });

  it('checks now on Check now, and never twice at the same time', async () => {
    const { w, calls } = await mount({ resume: saved('connect') });
    await Promise.all([w.checkNow(), w.checkNow()]);
    expect(checks(calls)).toBe(2);
  });

  it('keeps a failed files call on the page while the checks pass, and Check now asks again', async () => {
    const sorry = 'Something went wrong on our side. Your registration is unaffected.';
    let asked = 0;
    const { w, calls } = await mount({
      resume: saved('connect'),
      replies: {
        '/api/domain/files': () =>
          ++asked === 1 ? failed(500, sorry) : { files: [file()], edge_steps: [], deployed: '' },
      },
    });
    expect(w.pageError).toBe(sorry);
    await wait(RECHECK_MS);

    expect(checks(calls)).toBe(2);
    expect(w.pageError).toBe(sorry);
    expect(w.files).toHaveLength(0);

    await w.checkNow();
    expect(calls.filter((c) => c.path === '/api/domain/files')).toHaveLength(2);
    expect(w.files).toHaveLength(1);
    expect(w.pageError).toBe('');
  });

  it.each([
    ['verified', () => verified()],
    ['a failure', () => failed(502, 'We could not reach the exchange.')],
  ])('drops a check answered %s after the publisher went back', async (_, answer) => {
    let release;
    const held = new Promise((resolve) => {
      release = resolve;
    });
    const { w } = await mount({
      resume: saved('connect'),
      replies: {
        '/api/domain/check': async () => {
          await held;
          return answer();
        },
      },
    });
    w.back();
    release();
    await settle();

    expect(w.step).toBe('terms');
    expect(w.verified).toBe(false);
    expect(w.checkedAt).toBe('');
    expect(w.pageError).toBe('');
  });

  it('leaves a failure on the step it happened on', async () => {
    const { w } = await mount({
      resume: saved('terms'),
      replies: {
        '/api/domain/files': failed(500, 'Something went wrong on our side.'),
        '/api/domain/check': failed(502, 'We could not reach the exchange.'),
      },
    });
    await w.next();
    await settle();
    expect(w.step).toBe('connect');
    expect(w.error).toBe('Something went wrong on our side.');
    expect(w.checkError).toBe('We could not reach the exchange.');

    w.back();
    expect(w.pageError).toBe('');
  });

  it("asks for the saved domain's files once", async () => {
    const { w, calls } = await mount({
      resume: saved('connect'),
      replies: { '/api/domain/files': { files: [file()], edge_steps: [], deployed: '' } },
    });
    w.back();
    await w.next();
    await settle();
    await w.checkNow();

    expect(calls.filter((c) => c.path === '/api/domain/files')).toHaveLength(1);
    expect(calls.find((c) => c.path === '/api/domain/files').body).toEqual({
      domain: 'stoa.example',
    });
  });
});
