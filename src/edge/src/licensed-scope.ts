// Which paths of the site the publisher licenses. Only a read inside them is
// sent to negotiate when it comes from an AI bot; everything else on the site
// passes as it would with no Worker in front of it. No scope is the whole site.
//
// The route stays over the whole hostname either way, because the discovery
// documents (/.well-known/fora.json and the rest) are served by the Worker and
// have their own routes, ahead of the gate. So a scope can never make them
// unreachable.
//
// A scope entry is a path prefix, compared against the request path. The
// comparison is made to fail closed: a request is treated as OUTSIDE the scope
// only when that is certain. Otherwise a bot could reach licensed content
// through a spelling the origin reads as the same path and this check does not
// -- /%70aid/ for /paid/, //paid/, /PAID/ on an origin that ignores case. So the
// path is percent-decoded, repeated slashes are merged, both sides are
// compared in lower case, and a path that cannot be decoded, or that still
// holds a dot segment or a backslash after decoding, counts as inside.
//
// The catalog importer decides which sitemap pages it offers by the same rule
// (internal/licensedscope), except that it leaves out a path this check counts
// as inside only because it cannot place it. Both are pinned to the cases in
// testdata/licensed-scope-vectors.json.

export function inLicensedScope(pathname: string, scope: readonly string[] | undefined): boolean {
  if (!scope || scope.length === 0) return true;
  const path = canonicalPath(pathname);
  if (path === undefined) return true;
  return scope.some((prefix) => path.startsWith(prefix.toLowerCase()));
}

function canonicalPath(pathname: string): string | undefined {
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return undefined;
  }
  if (decoded.includes('\\') || /(^|\/)\.\.?(\/|$)/.test(decoded)) return undefined;
  return decoded.replace(/\/{2,}/g, '/').toLowerCase();
}
