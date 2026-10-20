// Package catalogid is how the Exchange names a catalog page: the key a page
// is matched on, the URI it is stored and sold under, and the resource id its
// row is keyed by -- and, read the other way, which content id a stored row
// was pushed with.
//
// The catalog push builds every stored row through it, and the automatic
// import matches the pages it builds against the rows already stored through
// it. The two therefore cannot disagree about which page is which. That
// matters: a stored page the import wrongly judged new would be overwritten,
// or would get the import's whole submission refused because its URI is
// already owned by another resource id.
package catalogid

import (
	"errors"
	"fmt"
	"strings"
)

// schemeSeparator ends the scheme of every catalog URI.
const schemeSeparator = "://"

// Page is a catalog page as a push names it: the seat its row is stored
// under, the site and path it lives at, and the publisher's own id for it. It
// is a struct rather than positional arguments because every member is a
// string, and a transposed pair would compile.
type Page struct {
	// TenantID is the seat the row is stored under.
	TenantID string
	// Domain is the site the page lives on, as the push carried it.
	Domain string
	// Path is the page's path on that site, as the push carried it.
	Path string
	// ContentID is the publisher's own identifier for the page; empty when
	// the push carried none.
	ContentID string
}

// Key is the page's address without a scheme: the domain, then the path,
// exactly as pushed. Nothing is parsed, decoded or case-folded. The Exchange
// stores a page's URI by concatenating these same bytes, so two spellings of
// one path are two pages, and a comparison on anything but the bytes would
// call a stored page new.
func (p Page) Key() string {
	return p.Domain + p.Path
}

// URI is the address the page is stored and sold under: the scheme, "://",
// then the key.
func (p Page) URI(scheme string) string {
	return scheme + schemeSeparator + p.Key()
}

// ResourceID is the key of the page's catalog row. It is scoped to the seat
// AND to the site the page belongs to, so a resource id can never collide
// across either: a contributor for one site cannot address -- and so cannot
// overwrite -- another site's row, even when both sites sell through the same
// seat. The seat comes first. Row.ContentID strips it as an exact prefix
// rather than splitting on ':', so it reads back correctly even though nothing
// forbids a ':' in the id an operator gives a seat.
//
// The site is part of the key because the content id is the PUBLISHER's own
// identifier and is only unique inside their own site. Under the earlier data
// model a tenant was a publisher domain, so scoping by tenant alone scoped by
// site as a side effect. A tenant is now a seat that sells many sites, and two
// publishers that both call an article "story-1" would otherwise land on one
// resource id.
//
// The scoping key is the content id when the push carried one, prefixed by
// the site. When it did not, the key falls back to the page's URI rather than
// a fresh server UUID: the URI is the stable identity of the page, so a re-push
// of the same (domain, path) without a content id maps to the SAME resource id
// and upserts in place, instead of minting a new resource id that would falsely
// collide with the incumbent under the URI-ownership gate. A URI-keyed id needs
// no site prefix because the URI already contains the domain.
func (p Page) ResourceID(scheme string) string {
	if p.ContentID == "" {
		return p.TenantID + ":" + p.URI(scheme)
	}
	return p.TenantID + ":" + p.Domain + ":" + p.ContentID
}

// Row is the identity of a stored catalog row, as the catalog table holds it.
type Row struct {
	TenantID   string
	URI        string
	ResourceID string
}

// ErrUnrecognizedResourceID is ContentID's answer for a row whose resource id
// has neither form Page.ResourceID builds for the row's URI: a row written
// under an earlier identity scheme.
var ErrUnrecognizedResourceID = errors.New("catalogid: resource id is not one this Exchange builds for the row's URI")

// Key is the row's address without a scheme -- its URI with the leading
// "<scheme>://" removed -- to compare with Page.Key. Whatever the scheme was,
// the rest of the URI is the key the push concatenated; it is never parsed or
// decoded. It reports false for a URI that carries no scheme.
func (r Row) Key() (string, bool) {
	_, key, ok := strings.Cut(r.URI, schemeSeparator)
	return key, ok
}

// ContentID recovers the content id the row was pushed with, the inverse of
// Page.ResourceID: empty when the row is keyed by its URI, the publisher's id
// otherwise. A caller that pushes the page again must send exactly this value,
// or its entry maps to another resource id and its submission is refused for
// a URI the row already owns.
//
// It strips prefixes it knows exactly -- the seat, and the site, which is the
// host part of the row's own key -- and never splits on ':', so a content id
// that itself contains ':' or "://" comes back whole. The URI form is checked
// first. A resource id in neither form is ErrUnrecognizedResourceID.
func (r Row) ContentID() (string, error) {
	if r.ResourceID == r.TenantID+":"+r.URI {
		return "", nil
	}
	key, ok := r.Key()
	if !ok {
		return "", fmt.Errorf("%w: uri %q carries no scheme", ErrUnrecognizedResourceID, r.URI)
	}
	domain, _, _ := strings.Cut(key, "/")
	contentID, ok := strings.CutPrefix(r.ResourceID, r.TenantID+":"+domain+":")
	if !ok || contentID == "" {
		return "", fmt.Errorf("%w: resource id %q, uri %q", ErrUnrecognizedResourceID, r.ResourceID, r.URI)
	}
	return contentID, nil
}
