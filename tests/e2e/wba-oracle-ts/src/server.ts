// Web Bot Auth conformance oracle: Cloudflare's web-bot-auth library behind HTTP.
//
// The e2e harness checks FORA's Web Bot Auth signatures against implementations
// FORA did not write. This sidecar exposes the `sign` and `verify` functions of
// the `web-bot-auth` npm package, version pinned in package.json, so the Python
// harness can call them over the compose network:
//
//   POST /sign    method, url, headers, Ed25519 private JWK, Signature-Agent origin
//                 -> the Signature-Agent, Signature-Input and Signature headers
//   POST /verify  method, url, headers, a JWKS ({ keys: [...] })
//                 -> { ok: true, ... } or { ok: false, code, error }
//   POST /import  a JWKS -> { imported: [{ keyid }], refused: [message] }: which
//                 keys the library's verifierFromJWK accepts, and why it refuses
//                 the others
//   GET  /healthz -> 200
//
// Every signing and verification decision is the library's. The server only
// builds the request descriptor the library takes, writes the Signature-Agent
// header the library expects to find already on the request, and hands the
// library its verification keys. It adds no check of its own: an oracle that
// second-guesses the library stops being an independent opinion.

import { type IncomingMessage, type ServerResponse, createServer } from 'node:http';
import { serializeDictionary, serializeItem } from 'structured-headers';
import {
  type RequestDescriptor,
  type WebBotVerifier,
  generateNonce,
  sign,
  verify,
} from 'web-bot-auth';
import { signerFromJWK, verifierFromJWK } from 'web-bot-auth/crypto';
import { z } from 'zod';

const PORT = 8090;
const MAX_BODY_BYTES = 1_000_000;

// The JWK members the library reads. Validating their types here is input
// hygiene; whether a key is acceptable is the library's call at import.
const optionalString = z.string().optional();
const jwkSchema = z
  .object({
    kty: z.string(),
    crv: optionalString,
    x: optionalString,
    y: optionalString,
    d: optionalString,
    n: optionalString,
    e: optionalString,
    alg: optionalString,
    kid: optionalString,
    use: optionalString,
  })
  .passthrough();
const componentSchema = z.union([
  z.string(),
  z.object({
    name: z.string(),
    parameters: z.record(z.string(), z.union([z.string(), z.boolean()])),
  }),
]);
const messageSchema = z.object({
  method: z.string().min(1),
  url: z.string().url(),
  headers: z.record(z.string(), z.string()).default({}),
});
const signSchema = messageSchema.extend({
  private_jwk: jwkSchema,
  signature_agent: z.string().min(1),
  label: z.string().min(1).default('sig1'),
  agent_key: z.string().min(1).optional(),
  extra_components: z.array(componentSchema).default([]),
  created: z.number().int().optional(),
  expires: z.number().int().optional(),
  // Absent: a fresh nonce from the library's generateNonce. null: no nonce.
  nonce: z.string().nullable().optional(),
  target: z.enum(['@authority', '@target-uri']).default('@authority'),
  legacy: z.boolean().default(false),
});
const verifySchema = messageSchema.extend({
  jwks: z.object({ keys: z.array(jwkSchema) }),
  label: z.string().min(1).optional(),
  now: z.number().int().optional(),
  max_age: z.number().int().optional(),
  clock_skew: z.number().int().optional(),
});

const importSchema = z.object({ jwks: z.object({ keys: z.array(jwkSchema) }) });

type SignInput = z.infer<typeof signSchema>;
type VerifyInput = z.infer<typeof verifySchema>;

class BadRequest extends Error {}

function descriptor(input: z.infer<typeof messageSchema>, extra: [string, string][] = []) {
  const fields = [...Object.entries(input.headers), ...extra].map(([name, value]) => ({
    name,
    value,
  }));
  const request: RequestDescriptor = {
    kind: 'request',
    method: input.method,
    targetUri: input.url,
    fields,
  };
  return request;
}

function signatureAgentHeader(input: SignInput): string {
  if (input.legacy) {
    return serializeItem(input.signature_agent);
  }
  return serializeDictionary({ [input.agent_key ?? input.label]: input.signature_agent });
}

async function handleSign(input: SignInput): Promise<Record<string, unknown>> {
  const agentHeader = signatureAgentHeader(input);
  const signer = await signerFromJWK(input.private_jwk);
  const created = input.created === undefined ? new Date() : new Date(input.created * 1000);
  const expires =
    input.expires === undefined
      ? new Date(created.getTime() + 300_000)
      : new Date(input.expires * 1000);
  const nonce = input.nonce === undefined ? generateNonce() : input.nonce;
  const fields = await sign(descriptor(input, [['signature-agent', agentHeader]]), {
    signer,
    created,
    expires,
    label: input.label,
    signatureAgentKey: input.agent_key ?? input.label,
    target: input.target,
    additionalComponents: input.extra_components,
    ...(nonce === null ? {} : { nonce }),
  });
  return {
    headers: {
      'Signature-Agent': agentHeader,
      'Signature-Input': fields.signatureInput,
      Signature: fields.signature,
    },
  };
}

async function verifiers(jwks: VerifyInput['jwks']['keys']) {
  const imported: WebBotVerifier[] = [];
  const refused: string[] = [];
  for (const jwk of jwks) {
    try {
      imported.push(await verifierFromJWK(jwk));
    } catch (error) {
      refused.push(error instanceof Error ? error.message : String(error));
    }
  }
  return { imported, refused };
}

async function handleImport(input: z.infer<typeof importSchema>): Promise<Record<string, unknown>> {
  const { imported, refused } = await verifiers(input.jwks.keys);
  return { imported: imported.map((verifier) => ({ keyid: verifier.keyid })), refused };
}

async function handleVerify(input: VerifyInput): Promise<Record<string, unknown>> {
  const { imported, refused } = await verifiers(input.jwks.keys);
  try {
    const verified = await verify(descriptor(input), {
      resolver: (candidate) => {
        const match = imported.find((verifier) => verifier.keyid === candidate.keyid);
        if (match === undefined) {
          const why = refused.length === 0 ? '' : `; keys the library refused: ${refused}`;
          throw new Error(`no key with keyid ${candidate.keyid}${why}`);
        }
        return match;
      },
      ...(input.label === undefined ? {} : { label: input.label }),
      ...(input.now === undefined ? {} : { now: new Date(input.now * 1000) }),
      ...(input.max_age === undefined ? {} : { maxAge: input.max_age }),
      ...(input.clock_skew === undefined ? {} : { clockSkew: input.clock_skew }),
    });
    return {
      ok: true,
      label: verified.label,
      keyid: verified.keyid,
      components: verified.components,
      parameters: verified.parameters,
      signature_agent: verified.signatureAgent ?? null,
    };
  } catch (error) {
    return failure(error);
  }
}

function failure(error: unknown): Record<string, unknown> {
  const code =
    typeof error === 'object' && error !== null && 'code' in error ? String(error.code) : null;
  const message = error instanceof Error ? error.message : String(error);
  const cause = error instanceof Error && error.cause instanceof Error ? error.cause.message : null;
  return { ok: false, code, error: message, cause };
}

async function readJson(request: IncomingMessage): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of request) {
    const buffer = chunk as Buffer;
    size += buffer.length;
    if (size > MAX_BODY_BYTES) {
      throw new BadRequest('request body too large');
    }
    chunks.push(buffer);
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString('utf8'));
  } catch {
    throw new BadRequest('request body is not JSON');
  }
}

function send(response: ServerResponse, status: number, body: unknown): void {
  response.writeHead(status, { 'content-type': 'application/json' });
  response.end(JSON.stringify(body));
}

async function route(request: IncomingMessage): Promise<[number, unknown]> {
  const path = new URL(request.url ?? '/', 'http://oracle').pathname;
  if (request.method === 'GET' && path === '/healthz') {
    return [200, { ok: true }];
  }
  if (request.method !== 'POST' || !['/sign', '/verify', '/import'].includes(path)) {
    return [404, { error: `no route ${request.method} ${path}` }];
  }
  const body = await readJson(request);
  if (path === '/import') {
    const input = importSchema.safeParse(body);
    if (!input.success) {
      throw new BadRequest(input.error.message);
    }
    return [200, await handleImport(input.data)];
  }
  if (path === '/sign') {
    const input = signSchema.safeParse(body);
    if (!input.success) {
      throw new BadRequest(input.error.message);
    }
    // A refusal to sign is the library's answer, not a server fault: 422 with
    // the library's error code, the same shape a failed verification returns.
    return handleSign(input.data).then(
      (signed): [number, unknown] => [200, signed],
      (error: unknown): [number, unknown] => [422, failure(error)],
    );
  }
  const input = verifySchema.safeParse(body);
  if (!input.success) {
    throw new BadRequest(input.error.message);
  }
  return [200, await handleVerify(input.data)];
}

const server = createServer((request, response) => {
  route(request).then(
    ([status, body]) => send(response, status, body),
    (error: unknown) => {
      const status = error instanceof BadRequest ? 400 : 500;
      send(response, status, failure(error));
    },
  );
});

server.listen(PORT);

// Exit promptly on `docker compose down`: as PID 1, node ignores SIGTERM unless
// a handler is installed, and compose would wait out its stop timeout.
process.on('SIGTERM', () => {
  server.close(() => process.exit(0));
});
