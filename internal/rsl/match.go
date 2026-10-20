package rsl

import (
	"net/url"
	"strings"
)

// Match returns the content rule covering pageURL, if the document has one.
//
// Address patterns follow the robots.txt convention RSL inherits: "*" stands
// for any run of characters, a trailing "$" anchors the pattern to the end of
// the path, and a pattern without either is a prefix. A pattern written as an
// absolute address must name the same host as the page, so one document cannot
// license another site's content; its path is what is then compared.
//
// When several rules match, the longest pattern wins, because that is the most
// specific statement the publisher made. Equal-length patterns are resolved in
// document order, so the publisher's own ordering decides and the answer does
// not depend on map iteration.
func (d *Document) Match(pageURL string) (*Content, bool) {
	if d == nil {
		return nil, false
	}
	page, err := url.Parse(pageURL)
	if err != nil {
		return nil, false
	}
	path := page.EscapedPath()
	if path == "" {
		path = "/"
	}
	best := -1
	bestLen := -1
	for i := range d.Contents {
		pattern, ok := patternFor(d.Contents[i].URL, page.Host)
		if !ok || !patternMatches(pattern, path) {
			continue
		}
		if n := len(strings.TrimSuffix(pattern, "$")); n > bestLen {
			best, bestLen = i, n
		}
	}
	if best < 0 {
		return nil, false
	}
	return &d.Contents[best], true
}

// patternFor reduces a content address to the path pattern to compare against,
// and reports whether it applies to this host at all.
func patternFor(raw, host string) (string, bool) {
	if raw == "" {
		// RSL treats an empty address the same as "/": the whole site.
		return "/", true
	}
	if !strings.Contains(raw, "://") {
		if !strings.HasPrefix(raw, "/") {
			// A bare relative pattern is anchored at the site root, which is
			// how the examples in the specification are written.
			return "/" + raw, true
		}
		return raw, true
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(ref.Host, host) {
		return "", false
	}
	if ref.Path == "" {
		return "/", true
	}
	return ref.EscapedPath(), true
}

// patternMatches applies the robots.txt matching rules to one pattern.
//
// The run after the final "*" is matched against the END of the path rather
// than at its first occurrence. Searching forwards for it finds the earliest
// place it fits, which is the wrong one whenever the text appears more than
// once: "/news/*.html$" against "/news/a.html/b.html" stops at the first
// ".html", is left mid-path, and reports no match although the "*" covers
// "a.html/b" perfectly well. A rule that fails to match here does not merely
// go unreported — the caller then settles for a broader rule, so a page the
// publisher priced can end up carrying the terms of a rule meant for the whole
// site.
//
// Every earlier run is still taken at its first occurrence, which is correct:
// the earliest match leaves the most room for the runs that follow, so no later
// choice is ruled out by it.
func patternMatches(pattern, path string) bool {
	pattern, anchored := strings.CutSuffix(pattern, "$")
	parts := strings.Split(pattern, "*")
	last := len(parts) - 1
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		switch {
		case i == 0:
			// The run before the first "*" has to sit at the start: a pattern
			// without any "*" is therefore a plain prefix.
			if !strings.HasPrefix(path, part) {
				return false
			}
			pos = len(part)
		case anchored && i == last:
			// The run after the final "*", with the pattern anchored: it has to
			// be the end of the path, and it has to start at or after whatever
			// the earlier runs already consumed.
			if len(path)-len(part) < pos || !strings.HasSuffix(path, part) {
				return false
			}
			pos = len(path)
		default:
			idx := strings.Index(path[pos:], part)
			if idx < 0 {
				return false
			}
			pos += idx + len(part)
		}
	}
	if anchored && parts[last] != "" {
		// Anchored, and the pattern ends in literal text: that text has to be
		// the end of the path. A pattern ending "*$" places no such demand.
		return pos == len(path)
	}
	return true
}
