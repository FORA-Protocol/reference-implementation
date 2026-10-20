package repo

// The automatic import's read of the catalog: the rows already stored for the
// one site it is importing. Split from catalog.go because the push path never
// reads the catalog this way, and the importer never writes it: every change it
// makes goes through the signed catalog push.

import (
	"context"
	"fmt"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// StoredPage is one catalog row of a site, as the automatic import compares
// the pages it builds with it: the identity the page must be pushed under
// again, the title and metadata to send back with it, and whether the import
// wrote the row itself.
type StoredPage struct {
	ResourceID string
	URI        string
	// Title is nil when the row has none.
	Title *string
	// Metadata is the stored metadata document, nil when the row has none.
	Metadata []byte
	// AutoImported is CatalogEntry.AutoImported: true only when the last push
	// that wrote the row carried the automatic import's label.
	AutoImported bool
}

// SitePagesRepo is the automatic import's read of the catalog. It is the whole
// of the importer's access to catalog rows: it never receives the Querier and
// has no write port.
type SitePagesRepo interface {
	// PagesOfSite lists the seat's rows stored under domain, whatever scheme
	// their URI was built with, ordered by URI.
	PagesOfSite(ctx context.Context, tenantID, domain string) ([]StoredPage, error)
}

// NewSitePagesRepo composes the importer's catalog read over the fat
// sqlc.Querier.
func NewSitePagesRepo(q sqlc.Querier) SitePagesRepo { return &catalogRepo{q: q} }

func (r *catalogRepo) PagesOfSite(ctx context.Context, tenantID, domain string) ([]StoredPage, error) {
	rows, err := r.q.ListCatalogPagesOfSite(ctx, sqlc.ListCatalogPagesOfSiteParams{
		TenantID: tenantID,
		Domain:   domain,
	})
	if err != nil {
		return nil, fmt.Errorf("list catalog pages of site: %w", err)
	}
	out := make([]StoredPage, 0, len(rows))
	for _, row := range rows {
		out = append(out, StoredPage{
			ResourceID:   row.ResourceID,
			URI:          row.Uri,
			Title:        textFromPG(row.Title),
			Metadata:     row.Metadata,
			AutoImported: row.AutoImported,
		})
	}
	return out, nil
}
