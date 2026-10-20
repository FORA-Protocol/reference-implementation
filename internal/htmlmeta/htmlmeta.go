// Package htmlmeta reads the few facts about a web page that a catalog entry
// needs: what the page is called, and its canonical address.
//
// It is not a content extractor. Nothing here reads body text, counts words or
// estimates tokens — the preview shows a publisher what one page looks like as
// an offer, and an offer carries a title and an address.
package htmlmeta

import (
	"io"
	"strings"

	"golang.org/x/net/html"
)

// Meta is what one page said about itself.
type Meta struct {
	// Title is the contents of the <title> element.
	Title string
	// OGTitle is the Open Graph title, which a site sets when the headline it
	// wants shown elsewhere differs from the browser tab's.
	OGTitle string
	// Canonical is the address the page names as its own, when it names one.
	Canonical string
}

// BestTitle returns the title to show, preferring what the page asks other
// sites to display over what it puts in a browser tab. A tab title often
// carries the site name as a suffix; the Open Graph title usually does not.
func (m Meta) BestTitle() string {
	if m.OGTitle != "" {
		return m.OGTitle
	}
	return m.Title
}

// Extract reads r as HTML and returns what the head declared.
//
// Reading stops at the end of the head, or at the first element that can only
// appear in the body. The three facts this package wants are head-only, so
// parsing a long article body would be work with no answer in it.
//
// A malformed page is not an error: HTML parsing recovers by design, and a page
// with no title is a page with no title rather than a failed fetch.
func Extract(r io.Reader) Meta {
	var m Meta
	z := html.NewTokenizer(r)
	for {
		switch z.Next() {
		case html.ErrorToken:
			return m
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			switch string(name) {
			case "title":
				m.Title = collapse(readText(z))
			case "meta":
				readMeta(z, hasAttr, &m)
			case "link":
				readLink(z, hasAttr, &m)
			case "body":
				return m
			}
		case html.EndTagToken:
			if name, _ := z.TagName(); string(name) == "head" {
				return m
			}
		}
	}
}

// readText returns the text of the element the tokenizer is positioned on.
func readText(z *html.Tokenizer) string {
	if z.Next() != html.TextToken {
		return ""
	}
	return string(z.Text())
}

// readMeta records an Open Graph title, whichever of the two attribute
// spellings the page used. Open Graph specifies "property"; many pages write
// "name" instead, and both are the same declaration.
func readMeta(z *html.Tokenizer, hasAttr bool, m *Meta) {
	if !hasAttr || m.OGTitle != "" {
		return
	}
	attrs := readAttrs(z)
	if attrs["property"] != "og:title" && attrs["name"] != "og:title" {
		return
	}
	m.OGTitle = collapse(attrs["content"])
}

// readLink records the page's canonical address.
func readLink(z *html.Tokenizer, hasAttr bool, m *Meta) {
	if !hasAttr || m.Canonical != "" {
		return
	}
	attrs := readAttrs(z)
	// The rel attribute is a space-separated list, and a page may write
	// "canonical shortlink" in one attribute.
	for _, rel := range strings.Fields(attrs["rel"]) {
		if rel == "canonical" {
			m.Canonical = strings.TrimSpace(attrs["href"])
			return
		}
	}
}

// readAttrs collects the attributes of the current tag, lower-casing the names
// as HTML requires and keeping the first spelling of a repeated one, which is
// the value a browser would use.
func readAttrs(z *html.Tokenizer) map[string]string {
	attrs := map[string]string{}
	for {
		key, val, more := z.TagAttr()
		name := strings.ToLower(string(key))
		if _, seen := attrs[name]; !seen {
			attrs[name] = string(val)
		}
		if !more {
			return attrs
		}
	}
}

// collapse trims a value and folds runs of whitespace into single spaces, which
// is how a browser renders a title written across several lines.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
