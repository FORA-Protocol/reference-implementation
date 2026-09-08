// Package foraauth holds transport-level helpers for FORA request
// authentication that are independent of httpsig signature parsing: the
// “/fora.“ procedure predicate that gates outbound RFC 9421 signing
// (IsFORAProcedure) and the Bearer-JWT claim helpers (jwtclaims.go).
package foraauth

import (
	"net/http"
	"strings"
)

// FORAProcedurePrefix is the URL-path prefix that marks a Connect RPC as a FORA
// procedure (“/fora.v1.<Service>/<Method>“). It is the single boundary both the
// server-side verify seam and the client-side sign predicate key off, so the two
// cannot drift.
const FORAProcedurePrefix = "/fora."

// IsFORAProcedure reports whether r targets a FORA procedure — the predicate that
// gates outbound RFC 9421 signing (core.WithSignPredicate) to the FORA namespace,
// mirroring the /fora. boundary the server-side verify seam enforces. A request
// with no URL is treated as out-of-namespace (not signed).
func IsFORAProcedure(r *http.Request) bool {
	return r.URL != nil && strings.HasPrefix(r.URL.Path, FORAProcedurePrefix)
}
