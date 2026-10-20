// Package robotstxt reads the two site-wide pointers a robots.txt can carry:
// where the sitemaps are, and where the licensing document is.
//
// It is not a crawl-permission parser. Nothing here reads user-agent groups,
// allow or disallow rules, or crawl delay, because nothing in this repository
// crawls a site — one page is fetched, and only when a visitor asked for it.
package robotstxt

import (
	"bufio"
	"io"
	"strings"
)

// maxPerField bounds how many values one field contributes. A robots.txt is
// fetched under a size cap already; this second bound keeps a file that is all
// sitemap lines from turning into a long slice a caller then walks.
const maxPerField = 64

// Directives are the pointers found in a robots.txt, in the order they appeared.
type Directives struct {
	// Sitemaps are the values of Sitemap: lines.
	Sitemaps []string
	// Licenses are the values of License: lines, the discovery form RSL 1.0
	// defines for pointing at a licensing document that is not at /rsl.txt.
	Licenses []string
}

// Parse reads r and returns the pointers it carries. A robots.txt is a
// best-effort document: a line that does not parse is skipped rather than
// failing the file, so the only error that can come back is a read failure.
//
// Field names are matched without regard to case, as the format requires.
// Everything from a "#" onward is a comment and is dropped — that is the rule
// robots.txt parsers follow, so an address whose fragment starts with "#"
// cannot be written here at all.
func Parse(r io.Reader) (Directives, error) {
	var d Directives
	sc := bufio.NewScanner(r)
	// Long lines are truncated by the default 64 KiB limit and would parse as
	// two; a larger buffer keeps a long sitemap address in one piece.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		field, value, ok := splitLine(sc.Text())
		if !ok {
			continue
		}
		switch field {
		case "sitemap":
			if len(d.Sitemaps) < maxPerField {
				d.Sitemaps = append(d.Sitemaps, value)
			}
		case "license":
			if len(d.Licenses) < maxPerField {
				d.Licenses = append(d.Licenses, value)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return Directives{}, err
	}
	return d, nil
}

// splitLine returns the lower-cased field name and its value, or ok=false for a
// comment, a blank line, or a line with no value.
func splitLine(line string) (field, value string, ok bool) {
	if idx := strings.IndexByte(line, '#'); idx >= 0 {
		line = line[:idx]
	}
	name, rest, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}
	value = strings.TrimSpace(rest)
	if value == "" {
		return "", "", false
	}
	return strings.ToLower(strings.TrimSpace(name)), value, true
}
