// The transaction page's Verify button. The checking is evidence.js; this file
// only reads the evidence the page carries and writes the outcome under the
// button.

import { UNSUPPORTED, verifyEvidence } from './evidence.js';

const section = document.getElementById('evidence');
const button = document.getElementById('verify');
const results = document.getElementById('verify-results');

/**
 * @param {string} text
 * @param {boolean} ok
 */
function line(text, ok) {
  const item = document.createElement('li');
  item.className = ok ? 'ok' : 'bad';
  item.textContent = `${ok ? '✓' : '✗'} ${text}`;
  results.append(item);
}

/**
 * linkText says whether the acceptance is for this offer. An acceptance whose
 * signature does not match proves nothing about which offer it names.
 * @param {{acceptance: boolean, linked: boolean}} outcome
 * @returns {string}
 */
function linkText(outcome) {
  if (!outcome.acceptance) {
    return 'Which offer the acceptance is for cannot be told, because its signature does not match.';
  }
  return outcome.linked
    ? 'The acceptance is for this offer.'
    : 'The acceptance names a different offer.';
}

async function verify() {
  results.replaceChildren();
  button.disabled = true;
  try {
    const outcome = await verifyEvidence(JSON.parse(section.dataset.evidence));
    if (outcome === UNSUPPORTED) {
      line('This browser cannot check Ed25519 signatures. Try a current version of it.', false);
      return;
    }
    line(
      outcome.offer
        ? "The offer's signature matches the exchange key recorded with this transaction."
        : "The offer's signature does not match the exchange key recorded with this transaction.",
      outcome.offer,
    );
    line(
      outcome.acceptance
        ? "The acceptance's signature matches the agent key recorded with this transaction."
        : "The acceptance's signature does not match the agent key recorded with this transaction.",
      outcome.acceptance,
    );
    line(linkText(outcome), outcome.linked);
  } catch {
    line('The evidence on this page could not be read.', false);
  } finally {
    button.disabled = false;
  }
}

if (section && button && results) {
  button.addEventListener('click', verify);
  button.disabled = false;
}
