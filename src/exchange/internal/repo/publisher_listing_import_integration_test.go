//go:build integration

package repo_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// Persistence round-trips for the automatic import's half of the listing: the
// settings it reads -- the sitemap, the licensing and the answer to whether it
// may take over pages another source pushed, all recorded with the terms --
// and the outcome it writes back. Same tier as the listing file next to
// this one, and for the same reason: no RPC reads these columns yet, so every
// leg stops at the repository port.

// TestListingImportEligibility pins which listings the importer works on: an
// ACTIVE listing with a sitemap recorded, and nothing else. A listing still
// PENDING_MANIFEST is not proven and is not imported however complete its
// settings are; an ACTIVE listing with no sitemap recorded is skipped rather
// than imported from a guess.
func TestListingImportEligibility(t *testing.T) {
	ctx := context.Background()
	const (
		tenantID    = "t-elig"
		publisherID = "pub-elig"
	)
	pool := listingFixture(t, ctx, tenantID, publisherID, "ready.example")
	for _, site := range []string{"pending.example", "unset.example"} {
		if _, err := createDomain(t, ctx, pool, site, publisherID, "ED25519"); err != nil {
			t.Fatalf("seed site %s: %v", site, err)
		}
	}
	for site, sitemap := range map[string]string{
		"ready.example":   "https://ready.example/sitemap.xml",
		"pending.example": "https://pending.example/sitemap.xml",
		"unset.example":   "",
	} {
		if _, err := createListingFrom(t, ctx, pool, repo.NewListing{
			TenantID: tenantID, Domain: site, Terms: []byte(termsPerAccess), SitemapURL: sitemap,
		}); err != nil {
			t.Fatalf("Create listing %s: %v", site, err)
		}
	}
	activateAndAssertActive(t, ctx, pool, tenantID, "ready.example")
	activateAndAssertActive(t, ctx, pool, tenantID, "unset.example")

	eligible, err := repo.NewListingImportRepo(sqlc.New(pool)).Eligible(ctx, tenantID)
	if err != nil {
		t.Fatalf("Eligible: %v", err)
	}
	if len(eligible) != 1 || eligible[0].Domain != "ready.example" {
		t.Fatalf("Eligible = %+v; want only ready.example", eligible)
	}
	if eligible[0].ImportReplacesManualTerms {
		t.Error("a listing registered without the take-over answer lets the import take pages over")
	}

	// Another seat's listings are not this seat's work.
	other, err := repo.NewListingImportRepo(sqlc.New(pool)).Eligible(ctx, "t-nobody")
	if err != nil || len(other) != 0 {
		t.Errorf("Eligible for an unknown seat = %v, %v; want none", other, err)
	}
}

// TestEligibleListingCarriesItsRegistration pins what the importer is handed:
// the sitemap, the path scope, the payee, the licensing document and the
// take-over answer exactly as registration stored them. A listing reads back no
// import outcome before its first run.
func TestEligibleListingCarriesItsRegistration(t *testing.T) {
	ctx := context.Background()
	const (
		tenantID    = "t-carry"
		publisherID = "pub-carry"
		site        = "stoapress.example"
		sitemap     = "https://stoapress.example/sitemap.xml"
	)
	pool := listingFixture(t, ctx, tenantID, publisherID, site)
	if _, err := createListingFrom(t, ctx, pool, repo.NewListing{
		TenantID: tenantID, Domain: site, ContentPathScope: []string{"/news/"},
		Terms: []byte(termsPerAccess), SitemapURL: sitemap, ImportReplacesManualTerms: true,
	}); err != nil {
		t.Fatalf("Create listing: %v", err)
	}
	activateAndAssertActive(t, ctx, pool, tenantID, site)

	eligible, err := repo.NewListingImportRepo(sqlc.New(pool)).Eligible(ctx, tenantID)
	if err != nil || len(eligible) != 1 {
		t.Fatalf("Eligible = %+v, %v; want the one listing", eligible, err)
	}
	got := eligible[0]
	assertSameTerms(t, got.Terms, termsPerAccess)
	got.Terms = nil
	want := repo.ImportListing{
		TenantID: tenantID, Domain: site, PublisherID: publisherID,
		ContentPathScope: []string{"/news/"}, SitemapURL: sitemap, ImportReplacesManualTerms: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("eligible listing = %+v, want %+v", got, want)
	}

	fresh, err := repo.NewPublisherListingReadRepo(sqlc.New(pool)).ByDomain(ctx, tenantID, site)
	if err != nil {
		t.Fatalf("ByDomain: %v", err)
	}
	if fresh.SitemapURL != sitemap || fresh.LastImport != (repo.ListingImport{}) {
		t.Errorf("a never-imported listing reads sitemap %q and last import %+v", fresh.SitemapURL, fresh.LastImport)
	}
}

// The outcome tests list one site on two seats. The second seat's listing of
// the same site is the row a scoped write must never touch.
const (
	outcomeSeatA     = "t-seat-a"
	outcomeSeatB     = "t-seat-b"
	outcomePublisher = "pub-outcome"
	outcomeSite      = "harmonia.example"
)

func outcomeFixture(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	pool := listingFixture(t, ctx, outcomeSeatA, outcomePublisher, outcomeSite)
	seedTenantRow(t, ctx, sqlc.New(pool), outcomeSeatB, "seat-b.example")
	for _, seat := range []string{outcomeSeatA, outcomeSeatB} {
		if _, err := createListing(t, ctx, pool, seat, outcomeSite, nil, termsFree); err != nil {
			t.Fatalf("Create listing on %s: %v", seat, err)
		}
	}
	return pool
}

// recordOutcome writes one run's outcome to the site's listing on seat A and
// fails the test unless exactly that row was written.
func recordOutcome(t *testing.T, ctx context.Context, pool *pgxpool.Pool, out repo.ImportOutcome) {
	t.Helper()
	n, err := repo.NewListingImportRepo(sqlc.New(pool)).RecordOutcome(ctx, outcomeSeatA, outcomeSite, out)
	if err != nil || n != 1 {
		t.Fatalf("RecordOutcome %s = %d, %v; want 1 row", out.Outcome, n, err)
	}
}

// lastImportOn reads back what the site's listing on seat records.
func lastImportOn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, seat string) repo.ListingImport {
	t.Helper()
	l, err := repo.NewPublisherListingReadRepo(sqlc.New(pool)).ByDomain(ctx, seat, outcomeSite)
	if err != nil {
		t.Fatalf("ByDomain on %s: %v", seat, err)
	}
	return l.LastImport
}

// assertRecorded compares the outcome word, the reason and the six counts
// with the outcome that was written.
func assertRecorded(t *testing.T, got repo.ListingImport, want repo.ImportOutcome) {
	t.Helper()
	reason := ""
	if got.FailureReason != nil {
		reason = *got.FailureReason
	}
	read := repo.ImportOutcome{
		Outcome: got.Outcome, FailureReason: reason, Entries: got.Entries, Accepted: got.Accepted,
		Refused: got.Refused, Unconfirmed: got.Unconfirmed, Unsent: got.Unsent, Skipped: got.Skipped,
	}
	if read != want {
		t.Fatalf("LastImport = %+v; want %+v", got, want)
	}
}

// TestRecordListingImportOutcome_CompleteMovesSuccess: a complete run writes
// the outcome and every count -- here three pages left to another source --
// records no reason, and sets both timestamps.
func TestRecordListingImportOutcome_CompleteMovesSuccess(t *testing.T) {
	ctx := context.Background()
	pool := outcomeFixture(t, ctx)
	complete := repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 12, Accepted: 9, Skipped: 3}

	recordOutcome(t, ctx, pool, complete)
	li := lastImportOn(t, ctx, pool, outcomeSeatA)
	assertRecorded(t, li, complete)
	if li.AttemptAt == nil || li.SuccessAt == nil {
		t.Fatalf("after a complete run LastImport = %+v; want both timestamps", li)
	}
}

// TestRecordListingImportOutcome_PartialKeepsSuccess: a later partial run
// replaces the outcome and every count and advances the attempt timestamp, and
// the success timestamp stays where the complete run put it.
func TestRecordListingImportOutcome_PartialKeepsSuccess(t *testing.T) {
	ctx := context.Background()
	pool := outcomeFixture(t, ctx)
	recordOutcome(t, ctx, pool, repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 12, Accepted: 12})
	successAt := lastImportOn(t, ctx, pool, outcomeSeatA).SuccessAt
	if successAt == nil {
		t.Fatal("the complete run recorded no success timestamp")
	}
	partial := repo.ImportOutcome{
		Outcome: repo.ImportOutcomePartial, FailureReason: "submission 2 of 3 refused",
		Entries: 700, Accepted: 256, Refused: 256, Unsent: 188,
	}

	recordOutcome(t, ctx, pool, partial)
	li := lastImportOn(t, ctx, pool, outcomeSeatA)
	assertRecorded(t, li, partial)
	if li.SuccessAt == nil || !li.SuccessAt.Equal(*successAt) {
		t.Errorf("a partial run moved the success timestamp: %v -> %v", *successAt, li.SuccessAt)
	}
	if li.AttemptAt == nil || !li.AttemptAt.After(*successAt) {
		t.Errorf("a partial run did not advance the attempt timestamp: attempt %v, earlier success %v",
			li.AttemptAt, *successAt)
	}
}

// TestRecordListingImportOutcome_WritesOnlyTheNamedRow: the write is scoped to
// one seat's listing of one site, so the same site listed on another seat is
// never touched; an outcome outside the enum is refused; and a site the seat
// does not list is a detectable no-op.
func TestRecordListingImportOutcome_WritesOnlyTheNamedRow(t *testing.T) {
	ctx := context.Background()
	pool := outcomeFixture(t, ctx)
	recordOutcome(t, ctx, pool, repo.ImportOutcome{Outcome: repo.ImportOutcomeComplete, Entries: 12, Accepted: 12})

	if untouched := lastImportOn(t, ctx, pool, outcomeSeatB); untouched != (repo.ListingImport{}) {
		t.Errorf("the other seat's listing of the same site was written: %+v", untouched)
	}
	imports := repo.NewListingImportRepo(sqlc.New(pool))
	if _, err := imports.RecordOutcome(ctx, outcomeSeatA, outcomeSite, repo.ImportOutcome{Outcome: "skipped"}); err == nil {
		t.Error("an outcome outside the enum was accepted")
	}
	n, err := imports.RecordOutcome(ctx, outcomeSeatA, "nobody.example",
		repo.ImportOutcome{Outcome: repo.ImportOutcomeFailed})
	if err != nil || n != 0 {
		t.Errorf("RecordOutcome for an unlisted site = %d, %v; want 0 rows and no error", n, err)
	}
}
