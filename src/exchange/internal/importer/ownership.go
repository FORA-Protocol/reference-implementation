package importer

// Which of the pages a run built it pushes, and in what form.
//
// A page is the importer's to push when no other source pushed it: the catalog
// holds no row for it, or holds a row the importer wrote itself. A row any
// other source wrote -- a feed pushed by hand, the publisher's own tooling --
// belongs to that source and is left as it is, unless the publisher answered
// at registration that the import may take such pages over.
//
// Whenever the importer pushes a page that already has a row, it sends the
// row's content id, title and metadata back unchanged, so only the terms
// change. The content id is what keeps the entry on the row's resource id: an
// entry that mapped to another resource id would claim a URI the row already
// owns, and the Exchange would refuse the whole submission. A title or metadata
// left out would be cleared. That holds while the Exchange builds URIs with the
// scheme the row was stored under; the RUNBOOK says why that setting must not
// change once pages are stored.

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/catalogid"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/metadatacodec"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// pushPlan is what one run sends, in sitemap order, and how many of the
// entries it built it leaves out.
type pushPlan struct {
	push    []*forav1.ResourceEntry
	skipped int
}

// planEntries decides, for each built entry, whether the run pushes it as
// built, pushes it again carrying its stored page's content id, title and
// metadata, or leaves it out:
//
//   - no stored row: pushed as built;
//   - a row the import wrote: pushed again, carrying the row over;
//   - a row another source wrote, with the listing's take-over answer on:
//     pushed again, carrying the row over, and the import's from then on;
//   - a row another source wrote, with the answer off: left out;
//   - two rows on one page, or a row whose resource id this Exchange does not
//     build: left out, because the page could not be pushed under the row's
//     own identity.
//
// A page it would push again whose stored metadata cannot be read, or no longer
// passes the checks every entry gets, stops the listing before anything is
// signed, and the error names the page.
func planEntries(
	listing repo.ImportListing, built []*forav1.ResourceEntry, stored []repo.StoredPage,
) (pushPlan, error) {
	rows := rowsByKey(stored)
	out := pushPlan{push: make([]*forav1.ResourceEntry, 0, len(built))}
	for _, e := range built {
		pages := rows[catalogid.Page{Domain: e.GetDomain(), Path: e.GetPath()}.Key()]
		if len(pages) == 0 {
			out.push = append(out.push, e)
			continue
		}
		if len(pages) > 1 || (!pages[0].AutoImported && !listing.ImportReplacesManualTerms) {
			out.skipped++
			continue
		}
		entry, ok, err := carryOver(listing.TenantID, e, pages[0])
		if err != nil {
			return pushPlan{}, err
		}
		if !ok {
			out.skipped++
			continue
		}
		out.push = append(out.push, entry)
	}
	return out, nil
}

// rowsByKey indexes the stored rows by their key: the URI without its scheme,
// compared byte for byte with the key of a built entry. More than one row on a
// key happens only when the Exchange's URI scheme changed after the site's
// pages were stored.
func rowsByKey(stored []repo.StoredPage) map[string][]repo.StoredPage {
	out := make(map[string][]repo.StoredPage, len(stored))
	for _, page := range stored {
		if key, ok := (catalogid.Row{URI: page.URI}).Key(); ok {
			out[key] = append(out[key], page)
		}
	}
	return out
}

// carryOver builds the entry that pushes a stored page again: the built entry's
// domain, path and terms, with the row's content id, title and metadata. It
// starts from the stored metadata document, so every field that document holds
// travels without being named here. The content id comes from the resource
// id -- the value the Exchange keys the row on -- never from the metadata's
// copy.
//
// It reports false for a row whose resource id is in neither form the Exchange
// builds today: pushed again, it would land on another resource id, so it is
// left alone like any row whose owner cannot be told.
func carryOver(
	tenantID string, built *forav1.ResourceEntry, page repo.StoredPage,
) (*forav1.ResourceEntry, bool, error) {
	contentID, ok := storedContentID(tenantID, page)
	if !ok {
		return nil, false, nil
	}
	entry, err := metadatacodec.Unmarshal(page.Metadata)
	if err != nil {
		return nil, false, fmt.Errorf("stored page %s cannot be pushed again: %w", page.URI, err)
	}
	if entry == nil {
		entry = &forav1.ResourceEntry{}
	}
	entry.Domain = built.GetDomain()
	entry.Path = built.GetPath()
	entry.Terms = built.GetTerms()
	entry.Title = page.Title
	entry.ContentId = nil
	if contentID != "" {
		entry.ContentId = &contentID
	}
	if verdict := helpers.ValidateResourceEntry(entry); !verdict.OK() {
		v := verdict.Violations[0]
		return nil, false, fmt.Errorf(
			"stored page %s cannot be pushed again, its stored data is invalid: rule=%s path=%s: %s",
			page.URI, v.Rule, v.Path, v.Message)
	}
	return entry, true, nil
}

// storedContentID is the content id a stored page was pushed with, and false
// when the row's resource id is in neither form the Exchange builds.
func storedContentID(tenantID string, page repo.StoredPage) (string, bool) {
	contentID, err := catalogid.Row{TenantID: tenantID, URI: page.URI, ResourceID: page.ResourceID}.ContentID()
	return contentID, err == nil
}
