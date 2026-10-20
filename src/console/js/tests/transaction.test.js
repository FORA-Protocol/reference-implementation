// @vitest-environment happy-dom
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';

// The evidence of a real transaction, as a console test writes it from a
// detail page.
const real = JSON.parse(
  readFileSync(join(process.cwd(), 'tests', 'testdata', 'transaction-evidence.json'), 'utf8'),
);

/**
 * verify puts the transaction page's evidence section, Verify button and
 * results list on the page, carrying evidence as the page's data-evidence
 * attribute does, runs the page's script, presses Verify, and returns each
 * result line with whether it passed.
 */
async function verify(evidence) {
  document.body.innerHTML = `
    <section id="evidence"></section>
    <button id="verify" type="button" disabled>Verify</button>
    <ul id="verify-results"></ul>`;
  document.getElementById('evidence').dataset.evidence =
    typeof evidence === 'string' ? evidence : JSON.stringify(evidence);
  vi.resetModules();
  await import('../../internal/web/static/transaction.js');
  const button = document.getElementById('verify');
  expect(button.disabled).toBe(false);
  button.click();
  const results = () => [...document.querySelectorAll('#verify-results li')];
  await vi.waitFor(() => {
    if (button.disabled || results().length === 0) {
      throw new Error('still verifying');
    }
  });
  return results().map((li) => ({ ok: li.className === 'ok', text: li.textContent }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.body.innerHTML = '';
});

describe('the Verify button', () => {
  it('says each signature matches the key recorded with the transaction', async () => {
    expect(await verify(real)).toEqual([
      {
        ok: true,
        text: "✓ The offer's signature matches the exchange key recorded with this transaction.",
      },
      {
        ok: true,
        text: "✓ The acceptance's signature matches the agent key recorded with this transaction.",
      },
      { ok: true, text: '✓ The acceptance is for this offer.' },
    ]);
  });

  it('does not tie an acceptance that does not verify to the offer', async () => {
    const bytes = Buffer.from(real.agent_acceptance_canonical_bytes, 'base64');
    bytes[5] ^= 0x01;
    const lines = await verify({
      ...real,
      agent_acceptance_canonical_bytes: bytes.toString('base64'),
    });
    expect(lines[0].ok).toBe(true);
    expect(lines[1]).toEqual({
      ok: false,
      text: "✗ The acceptance's signature does not match the agent key recorded with this transaction.",
    });
    expect(lines[2]).toEqual({
      ok: false,
      text: '✗ Which offer the acceptance is for cannot be told, because its signature does not match.',
    });
  });

  it('says so when the browser cannot check Ed25519', async () => {
    vi.stubGlobal('crypto', {});
    expect(await verify(real)).toEqual([
      {
        ok: false,
        text: '✗ This browser cannot check Ed25519 signatures. Try a current version of it.',
      },
    ]);
  });

  it('says so when the evidence on the page cannot be read', async () => {
    expect(await verify('not json')).toEqual([
      { ok: false, text: '✗ The evidence on this page could not be read.' },
    ]);
  });
});
