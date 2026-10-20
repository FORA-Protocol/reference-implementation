import type { CatalogContributor, Manifest, SignedWbaDirectory, WbaFile, WbaKey } from './types.js';

// Type declaration for the plain-JS well-known.mjs (which carries no
// types so the build-step-less aws-edge shim can import it). types.ts re-exports
// the functions, so production callers get these typed signatures.
export function buildPublisherManifest(
  provider: string,
  exchanges: { domain: string; endpoint: string; supported_profiles?: string[] }[],
  contributors?: CatalogContributor[],
): Manifest;

// buildPublisherWba assembles the publisher's Web Bot Auth directory (the
// publisher's Ed25519 signing keys, no kid, plus an optional directory-level
// revocation_url).
export function buildPublisherWba(keys: WbaKey[], revocationUrl?: string): WbaFile;

// signedDirectoryFrom reads a WBA_SIGNED_DIRECTORY_JSON value, checking its
// envelope only, and throws on a malformed one.
export function signedDirectoryFrom(raw: string): SignedWbaDirectory;

// signedDirectoryHeaders is the header set a pre-signed directory is served with.
export function signedDirectoryHeaders(signed: SignedWbaDirectory): Record<string, string>;

// Fixed discovery paths + WBA content type, single-sourced in
// well-known.mjs.
export const WELL_KNOWN_PATH: string;
export const WBA_PATH: string;
export const WBA_DIRECTORY_CONTENT_TYPE: string;
