// NOTE: filename intentionally avoids the substring "binding" — the
// @cloudflare/vitest-pool-workers / workerd module loader fails to collect a
// test file named *binding* ("No test suite found"). Do not rename to *binding*.
//
// App-level coverage of the proof-of-possession enforcement path, exercised
// against every failure reason in the vocabulary. Enforcement is the production
// default (ADR-013 D6.1); this suite pins it explicitly through deps so the
// matrix stays independent of whatever the config default happens to be, and so
// a deployment that opts down to bearer security cannot take these cases with
// it.
import { beforeAll, describe, expect, it } from 'vitest';

import { POP_ACCEPT_SIGNATURE } from '@fora-protocol/sdk/pop';

import { createApp } from '../../src/app.js';
import { type AppDeps, buildPublisherManifest } from '../../src/types.js';
import {
  type TestKeypair,
  generateKeypair,
  keyThumbprint,
  rawPublicKey,
  signGetHeaders,
  signUrl,
} from '../helpers/ed25519.js';

const PUB_ORIGIN = 'https://pub.example.com';

let exchangeKeypair: TestKeypair;
let exchangeKid: string;
let app: ReturnType<typeof createApp>;

beforeAll(async () => {
  exchangeKeypair = await generateKeypair('exchange');
  exchangeKid = await keyThumbprint(exchangeKeypair);
  const deps: AppDeps = {
    manifest: buildPublisherManifest('pub.example.com', [
      { domain: 'exchange.test', endpoint: 'https://exchange.test' },
    ]),
    exchangeUrl: 'https://exchange.test',
    // Hand-wired verify key — no WBA directory fetch, so the app runs fully
    // in-memory with no worker env involved.
    resolveKey: async (kid) => (kid === exchangeKid ? rawPublicKey(exchangeKeypair) : undefined),
    enforceBinding: true,
  };
  app = createApp(deps);
});

async function boundUrl(agentKp: TestKeypair): Promise<{ url: string; agentId: string }> {
  const agentId = await keyThumbprint(agentKp);
  const url = await signUrl(PUB_ORIGIN, exchangeKeypair.privateKey, {
    exp: Math.floor(Date.now() / 1000) + 300,
    kid: exchangeKid,
    agentId,
  });
  return { url, agentId };
}

function popHeaders(
  url: string,
  agentKp: TestKeypair,
  keyid: string,
): Promise<Record<string, string>> {
  return signGetHeaders(url, agentKp, {
    keyid,
    created: Math.floor(Date.now() / 1000) - 5,
    expires: Math.floor(Date.now() / 1000) + 300,
  });
}

describe('proof-of-possession enforcement (deps-injected ON)', () => {
  it('200 for a bound URL with a valid proof', async () => {
    const agentKp = await generateKeypair('agent');
    const { url, agentId } = await boundUrl(agentKp);
    const res = await app.request(url, { headers: await popHeaders(url, agentKp, agentId) });
    expect(res.status).toBe(200);
  });

  it('403 missing_agent_key when no proof is presented', async () => {
    const agentKp = await generateKeypair('agent');
    const { url } = await boundUrl(agentKp);
    const res = await app.request(url);
    expect(res.status).toBe(403);
    // No presented key is not a form the agent fixes by signing differently, so
    // the refusal names no Accept-Signature.
    expect(res.headers.get('accept-signature')).toBeNull();
    expect(((await res.json()) as { reason: string }).reason).toBe('missing_agent_key');
  });

  // A refusal the agent can fix by signing again is answered 401 with the
  // Accept-Signature value the SDK's verdict carries (RFC 9421 §5.1, WG-00 §5.3):
  // the components and parameters a delivery proof must carry.
  it('401 with Accept-Signature when the key is presented with no signature', async () => {
    const agentKp = await generateKeypair('agent');
    const { url, agentId } = await boundUrl(agentKp);
    const presented = (await popHeaders(url, agentKp, agentId))['X-FORA-Agent-Key'] as string;
    const res = await app.request(url, { headers: { 'X-FORA-Agent-Key': presented } });
    expect(res.status).toBe(401);
    expect(res.headers.get('accept-signature')).toBe(POP_ACCEPT_SIGNATURE);
    expect(((await res.json()) as { reason: string }).reason).toBe('missing_sig');
  });

  it('401 with Accept-Signature for a proof that carries no Web Bot Auth tag', async () => {
    const agentKp = await generateKeypair('agent');
    const { url, agentId } = await boundUrl(agentKp);
    const headers = await popHeaders(url, agentKp, agentId);
    headers['Signature-Input'] = (headers['Signature-Input'] as string).replace(/;tag="[^"]*"/, '');
    const res = await app.request(url, { headers });
    expect(res.status).toBe(401);
    expect(res.headers.get('accept-signature')).toBe(POP_ACCEPT_SIGNATURE);
    expect(((await res.json()) as { reason: string }).reason).toBe('bad_tag');
  });

  it('401 with Accept-Signature for the v1.0.8 proof form, which names no directory', async () => {
    const agentKp = await generateKeypair('agent');
    const { url, agentId } = await boundUrl(agentKp);
    const headers = await popHeaders(url, agentKp, agentId);
    headers['Signature-Input'] = (headers['Signature-Input'] as string).replace(
      '("@method" "@target-uri" "signature-agent";key="sig1")',
      '("@method" "@target-uri")',
    );
    const res = await app.request(url, { headers });
    expect(res.status).toBe(401);
    expect(res.headers.get('accept-signature')).toBe(POP_ACCEPT_SIGNATURE);
    expect(((await res.json()) as { reason: string }).reason).toBe('bad_covered_components');
  });

  it('403 thumbprint_mismatch when a different key is presented', async () => {
    const agentKp = await generateKeypair('agent');
    const wrongKp = await generateKeypair('wrong');
    const { url, agentId } = await boundUrl(agentKp);
    // Sign with the wrong key but claim the bound keyid: the presented key's
    // thumbprint no longer matches agent_id.
    const res = await app.request(url, { headers: await popHeaders(url, wrongKp, agentId) });
    expect(res.status).toBe(403);
    expect(((await res.json()) as { reason: string }).reason).toBe('thumbprint_mismatch');
  });

  it('403 keyid_mismatch when the proof keyid is not the bound agent_id', async () => {
    const agentKp = await generateKeypair('agent');
    const { url } = await boundUrl(agentKp);
    const res = await app.request(url, {
      headers: await popHeaders(url, agentKp, 'not-the-thumbprint'),
    });
    expect(res.status).toBe(403);
    expect(((await res.json()) as { reason: string }).reason).toBe('keyid_mismatch');
  });
});
