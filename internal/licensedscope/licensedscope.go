// Package licensedscope decides whether a page's path lies inside a listing's
// licensed path scope, by the rule the edge worker applies to every request it
// gates (src/edge/src/licensed-scope.ts). The edge sends an AI bot to negotiate
// for a path inside the scope and the catalog importer offers the pages inside
// it, so the two must agree on every path: a page the edge gates but the
// importer leaves out has no offer to buy, and a page the importer offers but
// the edge lets through is sold for what any bot reads free. Both
// implementations are pinned to the same cases,
// testdata/licensed-scope-vectors.json.
//
// A scope entry is a path prefix. The path is percent-decoded, repeated slashes
// are merged, and both sides are compared in lower case; the entry itself is
// lower-cased but never decoded. A path that does not decode to UTF-8, or that
// holds a backslash or a dot segment once decoded, cannot be placed. The edge
// counts such a path as inside, because it fails closed for gating. A caller
// that sells pages must count it as outside, because it fails closed for
// selling, and because the edge's URL parser resolves "." and ".." segments
// before it matches a request, so the path the edge would judge is not the
// path that would be sold.
//
// The one difference the shared cases leave out is Unicode case mapping:
// JavaScript lower-cases with the full mapping and Go with the simple one, so a
// few letters, such as U+0130 and a final capital sigma, fold differently.
package licensedscope

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Verdict is what the rule says about one path. Its values are the tokens the
// shared cases use.
type Verdict string

const (
	// Inside is a path under one of the scope's entries, and every path when
	// the scope is empty.
	Inside Verdict = "inside"
	// Outside is a path under none of the scope's entries.
	Outside Verdict = "outside"
	// Undecidable is a path the rule cannot place: it does not decode to
	// UTF-8, or it holds a backslash or a dot segment once decoded.
	Undecidable Verdict = "undecidable"
)

var (
	// dotSegment matches a "." or ".." segment of a decoded path, the pattern
	// the edge uses.
	dotSegment = regexp.MustCompile(`(^|/)\.\.?(/|$)`)
	// repeatedSlashes matches a run of two or more slashes.
	repeatedSlashes = regexp.MustCompile(`/{2,}`)
)

// Classify places pathname, a path as a URL carries it (percent-encoded, such
// as "/caf%C3%A9/x"), against scope. An empty scope is the whole site, so every
// path is Inside, as the edge gates every path of a site with no scope.
func Classify(pathname string, scope []string) Verdict {
	if len(scope) == 0 {
		return Inside
	}
	path, ok := canonical(pathname)
	if !ok {
		return Undecidable
	}
	for _, prefix := range scope {
		if strings.HasPrefix(path, strings.ToLower(prefix)) {
			return Inside
		}
	}
	return Outside
}

// canonical is the form a path is compared in: decoded, repeated slashes
// merged, lower-cased. It reports false for a path the rule cannot place.
// JavaScript's decodeURIComponent refuses a malformed escape and an escape
// that is not UTF-8; url.PathUnescape refuses only the first, so the second is
// checked here.
func canonical(pathname string) (string, bool) {
	decoded, err := url.PathUnescape(pathname)
	if err != nil || !utf8.ValidString(decoded) {
		return "", false
	}
	if strings.Contains(decoded, `\`) || dotSegment.MatchString(decoded) {
		return "", false
	}
	return strings.ToLower(repeatedSlashes.ReplaceAllString(decoded, "/")), true
}
