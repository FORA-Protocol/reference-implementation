// Package sitemap reads the two sitemap documents a site publishes: the URL set
// listing its pages, and the index listing its other sitemaps.
//
// It exists to answer one question — which page should we look at first — so it
// reads locations and nothing else. Change frequency, priority and the image,
// video and news extensions are all skipped.
package sitemap

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotASitemap is returned when the document's root element is neither a URL
// set nor an index. An origin that answers a sitemap request with its home page
// lands here, which is the common case and not an error worth alarming about.
var ErrNotASitemap = errors.New("sitemap: the document is neither a urlset nor a sitemapindex")

// Kind says which of the two documents was read.
type Kind int

const (
	// KindUnknown is the zero value, carried by the empty Document a failed
	// parse returns.
	KindUnknown Kind = iota
	// KindURLSet is a list of the site's pages.
	KindURLSet
	// KindIndex is a list of the site's other sitemaps.
	KindIndex
)

// String names the kind for a log line.
func (k Kind) String() string {
	switch k {
	case KindURLSet:
		return "urlset"
	case KindIndex:
		return "sitemapindex"
	case KindUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

// Document is what one sitemap yielded.
type Document struct {
	Kind Kind
	// Locs are the locations in document order, at most the maxLocs the caller
	// asked for. For a URL set these are pages; for an index they are further
	// sitemaps.
	Locs []string
}

// First returns the first location, if the document had any.
func (d Document) First() (string, bool) {
	if len(d.Locs) == 0 {
		return "", false
	}
	return d.Locs[0], true
}

// Parse reads at most maxLocs locations from r.
//
// Element names are matched without their namespace, because sitemaps in the
// wild declare the sitemap namespace as the default, as a prefix, or not at
// all, and all three are the same document. A location is only taken from a
// <loc> directly inside a <url> or a <sitemap>: the image and video extensions
// spell their own media addresses <loc> too, and those are not pages.
//
// Parsing stops as soon as maxLocs locations are in hand, so a caller that
// wants one page does not read a sitemap with fifty thousand of them.
func Parse(r io.Reader, maxLocs int) (Document, error) {
	if maxLocs <= 0 {
		return Document{}, fmt.Errorf("sitemap: maxLocs must be positive, got %d", maxLocs)
	}
	dec := xml.NewDecoder(r)
	// Named HTML entities appear in hand-written sitemaps. Recognising them
	// keeps a document readable rather than failing it on &nbsp;; nothing else
	// about strict parsing changes, and Go never expands entities declared by
	// the document itself.
	dec.Entity = xml.HTMLEntity
	p := &parser{dec: dec, maxLocs: maxLocs}
	return p.run()
}

// parser carries the state one pass over the token stream needs: where we are
// in the element tree, and what has been collected so far.
type parser struct {
	dec     *xml.Decoder
	maxLocs int
	doc     Document
	stack   []string
}

// run walks the token stream until the document ends or the cap is reached.
func (p *parser) run() (Document, error) {
	for {
		tok, err := p.dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Document{}, fmt.Errorf("sitemap: read: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			full, err := p.start(t)
			if err != nil {
				return Document{}, err
			}
			if full {
				return p.doc, nil
			}
		case xml.EndElement:
			if len(p.stack) > 0 {
				p.stack = p.stack[:len(p.stack)-1]
			}
		}
	}
	if p.doc.Kind == KindUnknown {
		return Document{}, fmt.Errorf("%w: the document has no root element", ErrNotASitemap)
	}
	return p.doc, nil
}

// start handles one opening tag and reports whether the cap has been reached.
func (p *parser) start(t xml.StartElement) (full bool, err error) {
	name := strings.ToLower(t.Name.Local)
	if len(p.stack) == 0 {
		if err := p.setKind(name, t.Name.Local); err != nil {
			return false, err
		}
	}
	if name == "loc" && locBelongsToAPage(p.stack) {
		// DecodeElement consumes the element's end tag as well, so this one
		// never reaches the stack and must not be pushed onto it.
		loc, err := readLoc(p.dec, t)
		if err != nil {
			return false, err
		}
		if loc == "" {
			return false, nil
		}
		p.doc.Locs = append(p.doc.Locs, loc)
		return len(p.doc.Locs) >= p.maxLocs, nil
	}
	p.stack = append(p.stack, name)
	return false, nil
}

// setKind reads the document kind off the root element, and refuses anything
// that is not one of the two sitemap documents.
func (p *parser) setKind(lowered, asWritten string) error {
	switch lowered {
	case "urlset":
		p.doc.Kind = KindURLSet
	case "sitemapindex":
		p.doc.Kind = KindIndex
	default:
		return fmt.Errorf("%w: root element is <%s>", ErrNotASitemap, asWritten)
	}
	return nil
}

// locBelongsToAPage reports whether a <loc> at this position addresses a page
// or a sitemap, rather than a media file an extension attached to one.
func locBelongsToAPage(stack []string) bool {
	if len(stack) == 0 {
		return false
	}
	switch stack[len(stack)-1] {
	case "url", "sitemap":
		return true
	default:
		return false
	}
}

// readLoc consumes the element's text up to its end tag and returns it trimmed.
func readLoc(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var text string
	if err := dec.DecodeElement(&text, &start); err != nil {
		return "", fmt.Errorf("sitemap: read <loc>: %w", err)
	}
	return strings.TrimSpace(text), nil
}
