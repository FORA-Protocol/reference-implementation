// Package sitemaptest holds the sitemap fixtures more than one suite
// arranges: the two document builders and an origin that serves documents by
// path and counts what was fetched. The walker's own tests, the importer's
// end-to-end scenarios and the import job's run tests all read sitemaps from
// it, so a change to what a sitemap looks like reaches every suite at once.
package sitemaptest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Urlset renders a sitemap URL set listing the pages.
func Urlset(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, l := range locs {
		fmt.Fprintf(&b, "<url><loc>%s</loc></url>", l)
	}
	b.WriteString("</urlset>")
	return b.String()
}

// Index renders a sitemap index naming the child sitemaps.
func Index(locs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, l := range locs {
		fmt.Fprintf(&b, "<sitemap><loc>%s</loc></sitemap>", l)
	}
	b.WriteString("</sitemapindex>")
	return b.String()
}

// Origin is a sitemap server: a handler per path, and a hit count per path so
// a test can prove what was and was not fetched. A path with no handler is a
// 404.
type Origin struct {
	*httptest.Server
	mu   sync.Mutex
	docs map[string]http.HandlerFunc
	hits map[string]int
}

// NewOrigin starts an origin, closed when the test ends.
func NewOrigin(tb testing.TB) *Origin {
	tb.Helper()
	o := &Origin{docs: map[string]http.HandlerFunc{}, hits: map[string]int{}}
	o.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o.mu.Lock()
		o.hits[r.URL.Path]++
		h, ok := o.docs[r.URL.Path]
		o.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	tb.Cleanup(o.Close)
	return o
}

// Handle serves path through h and returns the absolute URL. Handling a path
// again replaces the handler, so a scenario changes what an origin answers
// between two runs.
func (o *Origin) Handle(path string, h http.HandlerFunc) string {
	o.mu.Lock()
	o.docs[path] = h
	o.mu.Unlock()
	return o.URL + path
}

// Doc serves body at path as XML and returns the absolute URL.
func (o *Origin) Doc(path, body string) string {
	return o.Handle(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	})
}

// Fail answers path with status and an empty body.
func (o *Origin) Fail(path string, status int) string {
	return o.Handle(path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	})
}

// Hits is how many times path was requested.
func (o *Origin) Hits(path string) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.hits[path]
}
