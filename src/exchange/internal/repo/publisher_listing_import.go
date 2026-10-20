package repo

// The automatic catalog import's view of a listing: the settings it reads
// (the sitemap, the licensing, and whether it may take over pages another
// source pushed -- all recorded with the terms, when the site is registered or
// while it is not verified) and the outcome it writes back. Split from
// publisher_listing.go because registration never touches the outcome columns
// and the importer never writes a listing's settings.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
)

// Import outcomes, mirroring the fora.import_outcome enum. See the migration
// that adds the column for what each one means.
const (
	ImportOutcomeComplete    = "complete"
	ImportOutcomeFailed      = "failed"
	ImportOutcomePartial     = "partial"
	ImportOutcomeUnconfirmed = "unconfirmed"
)

// ListingImport is what the last import run recorded on a listing. Outcome is
// empty and the timestamps nil until the first run.
type ListingImport struct {
	AttemptAt     *time.Time
	SuccessAt     *time.Time
	Outcome       string
	FailureReason *string
	Entries       int
	Accepted      int
	Refused       int
	Unconfirmed   int
	Unsent        int
	Skipped       int
}

// ImportListing is one listing the importer works on: where its pages are
// listed, which of them are offered, the licensing the publisher chose, and
// whether the import may take over pages another source pushed.
//
// Terms is the stored document as registration wrote it, not license terms:
// the importer builds those on every run, inside the run of the one listing
// the record belongs to, so a record that cannot be read fails that listing
// and leaves every other listing's run untouched. It is raw JSON for the
// reason PublisherDomainListing.Terms is: the service owns the document's
// shape, and this package only moves it.
type ImportListing struct {
	TenantID         string
	Domain           string
	PublisherID      string
	ContentPathScope []string
	SitemapURL       string
	Terms            []byte
	// ImportReplacesManualTerms lets the import take over the site's pages
	// that another source pushed: it replaces their terms and keeps their
	// content id, title and metadata. Off, it leaves them alone.
	ImportReplacesManualTerms bool
}

// ImportOutcome is what one run writes back for one listing. FailureReason is
// stored NULL when empty. The six counts partition Entries: Skipped counts the
// entries built but not sent, because another source pushed the page or
// because it cannot be pushed again under its row's identity.
type ImportOutcome struct {
	Outcome       string
	FailureReason string
	Entries       int
	Accepted      int
	Refused       int
	Unconfirmed   int
	Unsent        int
	Skipped       int
}

// ListingImportRepo is the importer's contract: the listings to work on and
// the outcome to record. Both ports are tenant-scoped, because the importer
// runs for one seat and a listing is the seat's arrangement with a site.
type ListingImportRepo interface {
	// Eligible lists the seat's ACTIVE listings that have a sitemap recorded,
	// ordered by domain.
	Eligible(ctx context.Context, tenantID string) ([]ImportListing, error)
	// RecordOutcome writes one run's result to one listing. The success
	// timestamp moves only for a complete outcome. Returns rows-affected so a
	// call for a listing that is not there is a detectable no-op.
	RecordOutcome(ctx context.Context, tenantID, domain string, out ImportOutcome) (int64, error)
}

// NewListingImportRepo composes the importer's ports over the fat sqlc.Querier.
func NewListingImportRepo(q sqlc.Querier) ListingImportRepo {
	return &publisherListingRepo{q: q}
}

func (r *publisherListingRepo) Eligible(ctx context.Context, tenantID string) ([]ImportListing, error) {
	rows, err := r.q.ListImportEligibleListings(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list import-eligible listings: %w", err)
	}
	out := make([]ImportListing, 0, len(rows))
	for _, row := range rows {
		out = append(out, ImportListing{
			TenantID:                  row.TenantID,
			Domain:                    row.Domain,
			PublisherID:               row.PublisherID,
			ContentPathScope:          row.ContentPathScope,
			SitemapURL:                row.SitemapUrl,
			Terms:                     row.Terms,
			ImportReplacesManualTerms: row.ImportReplacesManualTerms,
		})
	}
	return out, nil
}

func (r *publisherListingRepo) RecordOutcome(
	ctx context.Context, tenantID, domain string, out ImportOutcome,
) (int64, error) {
	outcome := sqlc.ForaImportOutcome(out.Outcome)
	if !outcome.Valid() {
		return 0, fmt.Errorf("record listing import outcome: %q is not an import outcome", out.Outcome)
	}
	var reason pgtype.Text
	if out.FailureReason != "" {
		reason = pgText(out.FailureReason)
	}
	n, err := r.q.RecordListingImportOutcome(ctx, sqlc.RecordListingImportOutcomeParams{
		TenantID:      tenantID,
		Domain:        domain,
		Outcome:       outcome,
		FailureReason: reason,
		Entries:       int32(out.Entries),     //nolint:gosec // bounded by the resource limit, far below int32
		Accepted:      int32(out.Accepted),    //nolint:gosec // same bound
		Refused:       int32(out.Refused),     //nolint:gosec // same bound
		Unconfirmed:   int32(out.Unconfirmed), //nolint:gosec // same bound
		Unsent:        int32(out.Unsent),      //nolint:gosec // same bound
		Skipped:       int32(out.Skipped),     //nolint:gosec // same bound
	})
	if err != nil {
		return 0, fmt.Errorf("record listing import outcome: %w", err)
	}
	return n, nil
}

// lastImportFromRow reads the last recorded outcome off a listing row. Every
// field is its zero value until the first run.
func lastImportFromRow(row sqlc.ForaPublisherDomainListing) ListingImport {
	last := ListingImport{
		AttemptAt:     timeFromPG(row.LastImportAttemptAt),
		SuccessAt:     timeFromPG(row.LastImportSuccessAt),
		FailureReason: textFromPG(row.LastImportFailureReason),
		Entries:       int(row.LastImportEntries),
		Accepted:      int(row.LastImportAccepted),
		Refused:       int(row.LastImportRefused),
		Unconfirmed:   int(row.LastImportUnconfirmed),
		Unsent:        int(row.LastImportUnsent),
		Skipped:       int(row.LastImportSkipped),
	}
	if row.LastImportOutcome.Valid {
		last.Outcome = string(row.LastImportOutcome.ForaImportOutcome)
	}
	return last
}
