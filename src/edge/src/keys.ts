import { z } from 'zod';

// A single Ed25519 JWK as published in a FORA Web Bot Auth directory's keys[]
// (kty=OKP, crv=Ed25519), the shape a pre-provisioned verify key
// (FORA_VERIFY_KEYS) is validated against. After the WBA split there is NO
// kid — a key is named by its RFC 7638 thumbprint. use/alg/not_before/not_after
// may be present; the edge ignores the time bounds — the signed URL's own `exp`
// governs expiry. The fetched directory is decoded by the SDK's reader
// (keys-cache.ts), not by this schema.
export const JwkSchema = z.object({
  kty: z.literal('OKP'),
  crv: z.literal('Ed25519'),
  x: z.string().min(1),
  alg: z.string().optional(),
  use: z.string().optional(),
  not_before: z.string().optional(),
  not_after: z.string().optional(),
});

// Upper bound on a pre-provisioned key set, mirroring the Go schema's maxItems
// (internal/forawellknown/schema/fora-wba-directory.json). A directory holds
// only a handful of keys, so a larger array is malformed.
export const MAX_WBA_KEYS = 64;

export type Jwk = z.infer<typeof JwkSchema>;
