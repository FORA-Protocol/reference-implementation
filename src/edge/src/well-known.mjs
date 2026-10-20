// Canonical ROLE_PUBLISHER discovery builders. Plain ESM JS — no types, no
// dependencies — so they have exactly one home shared by two consumers that
// cannot share a TypeScript module:
//
//   * the Hono edge worker (src/edge), which re-exports through types.ts; and
//   * the raw-Node AWS CloudFront E2E shim (tests/e2e/aws-edge/server.mjs),
//     which has no build step and imports this file directly.
//
// One source means a protocol bump is edited once;
// tests/e2e/harness/test_manifest_parity.py guards the two emitters against
// drift. Per-entry supported_profiles are hoisted into the top-level
// supported_profiles (the unified AuthorizedExchange has no per-entry slot);
// relationship defaults to PROVIDER_RELATIONSHIP_DIRECT.
//
// After the WBA split identity keys are NOT part of the overlay
// manifest: a publisher's signing key(s) are published in a separate Web Bot
// Auth directory (buildPublisherWba below), and referenced elsewhere only by
// RFC 7638 thumbprint.

// Fixed path every FORA participant serves its commercial overlay manifest at.
// Single-sourced here (plain ESM) so the Hono worker (via types.ts) and the
// build-step-less AWS shim share one literal — no drift between the route mount
// and the X-Content-Rules header URL.
export const WELL_KNOWN_PATH = '/.well-known/fora.json';

// Fixed path every FORA participant serves its pure Web Bot Auth directory at.
// The directory is a `keys` array of JWKs plus an optional directory-level
// revocation_url, served with Content-Type
// application/http-message-signatures-directory+json. Mirrors the Go
// forawellknown.WBAPath.
export const WBA_PATH = '/.well-known/http-message-signatures-directory';

// Content-Type of a Web Bot Auth key directory (draft-ietf-webbotauth-httpsig-
// protocol-00 §5.5). Every reader under the profile refuses any other label,
// application/jwk-set+json included. The edge serves its own directory under it
// and requires it of the Exchange directory it fetches. Written as a literal
// because this file is plain ESM shared with the raw-Node shim, which cannot
// import the SDK's TypeScript source; a Node test asserts the literal equals the
// SDK's WBA_DIRECTORY_MEDIA_TYPE.
export const WBA_DIRECTORY_CONTENT_TYPE = 'application/http-message-signatures-directory+json';

export function buildPublisherManifest(provider, exchanges, contributors) {
  const profiles = [...new Set(exchanges.flatMap((e) => e.supported_profiles ?? []))];
  return {
    // The manifest DOCUMENT version — the SDK's WellKnownManifestVersion, not
    // ProtocolVersion. Written as a literal because this file is plain ESM shared
    // with the raw-Node shim, which cannot import the SDK's TypeScript source; a
    // Node test asserts the literal equals the SDK export.
    ver: '1.0',
    role: 'ROLE_PUBLISHER',
    domain: provider,
    exchanges: exchanges.map((e) => ({
      domain: e.domain,
      endpoint: e.endpoint,
      relationship: 'PROVIDER_RELATIONSHIP_DIRECT',
      // The publisher attests its resource_owner_id payee in ext for the Exchange's
      // settlement gate. Conditional so an entry without ext stays byte-identical
      // (the manifest-parity emitters and ext-less configs are unaffected).
      ...(e.ext !== undefined ? { ext: e.ext } : {}),
    })),
    ...(profiles.length > 0 ? { supported_profiles: profiles } : {}),
    ...(contributors !== undefined ? { catalog_contributors: contributors } : {}),
  };
}

// buildPublisherWba assembles the publisher's Web Bot Auth directory: a JWK Set
// carrying the publisher's Ed25519 signing key(s) with NO kid (keys are named
// by their RFC 7638 thumbprint), plus an optional directory-level revocation_url
// pointing at the thumbprint-keyed revocation channel. Wire shape mirrors
// protojson of fora.v1.WBAFile (UseProtoNames=true). A directory always
// publishes at least one key.
export function buildPublisherWba(keys, revocationUrl) {
  return {
    keys,
    ...(revocationUrl !== undefined ? { revocation_url: revocationUrl } : {}),
  };
}

// The four members of a WBA_SIGNED_DIRECTORY_JSON value, in the order the
// signing tool (scripts/sign-wba-directory.mjs) writes them.
const SIGNED_DIRECTORY_FIELDS = ['body', 'content_digest', 'signature_input', 'signature'];

// signedDirectoryFrom reads a WBA_SIGNED_DIRECTORY_JSON value: a pre-signed Web
// Bot Auth directory response, made offline by whoever holds the publisher's
// key. It checks the envelope only — four non-empty strings and nothing else —
// and throws otherwise. Whether the body is a directory is the caller's check:
// the Hono worker validates it with its key schema, while the raw-Node shim has
// no schema library and serves what the tool produced.
export function signedDirectoryFrom(raw) {
  const value = JSON.parse(raw);
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new Error('WBA_SIGNED_DIRECTORY_JSON must be a JSON object');
  }
  const extra = Object.keys(value).filter((k) => !SIGNED_DIRECTORY_FIELDS.includes(k));
  if (extra.length > 0) {
    throw new Error(`WBA_SIGNED_DIRECTORY_JSON carries unknown member(s): ${extra.join(', ')}`);
  }
  for (const field of SIGNED_DIRECTORY_FIELDS) {
    if (typeof value[field] !== 'string' || value[field] === '') {
      throw new Error(`WBA_SIGNED_DIRECTORY_JSON: ${field} must be a non-empty string`);
    }
  }
  return {
    body: value.body,
    contentDigest: value.content_digest,
    signatureInput: value.signature_input,
    signature: value.signature,
  };
}

// signedDirectoryHeaders is the header set a pre-signed directory is served
// with: the profile's media type and the three values the signatures need. The
// body goes out byte for byte beside them; a changed byte breaks the digest.
export function signedDirectoryHeaders(signed) {
  return {
    'content-type': WBA_DIRECTORY_CONTENT_TYPE,
    'content-digest': signed.contentDigest,
    'signature-input': signed.signatureInput,
    signature: signed.signature,
  };
}
