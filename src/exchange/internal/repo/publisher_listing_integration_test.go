//go:build integration

package repo_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/db"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/repo"
)

// Persistence round-trips for the listing -- one site offered on one seat. Same
// tier and the same reason as the registration file next to this one: the RPC
// that will own these rows ships in a later slice, so every write and every read
// here stops at the repository port.

// createListing drives PublisherListingWriteRepo.Create through a real
// transaction; the port takes a pgx.Tx because in production the listing commits
// with the audit row that records it.
// The two licensing documents these tests store. They are written out rather
// than built, because this suite is about what the column carries between a
// write and a read, not about what a valid arrangement is -- the service's own
// suite owns that.
const (
	termsPerAccess = `{"pricing_model":"per_unit","rate":"0.02",` +
		`"permitted_functions":["search","ai-input"],"attribution_required":true}`
	termsFree = `{"pricing_model":"free",` +
		`"permitted_functions":["search"],"attribution_required":false}`
)

func createListing(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool,
	tenantID, domain string, scope []string, terms string,
) (repo.PublisherDomainListing, error) {
	t.Helper()
	return createListingFrom(t, ctx, pool, repo.NewListing{
		TenantID:         tenantID,
		Domain:           domain,
		ContentPathScope: scope,
		Terms:            []byte(terms),
	})
}

// createListingFrom is createListing for a suite that sets what its positional
// arguments do not, such as the sitemap the automatic import reads.
func createListingFrom(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, in repo.NewListing,
) (repo.PublisherDomainListing, error) {
	t.Helper()
	var l repo.PublisherDomainListing
	err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		l, terr = repo.NewPublisherListingWriteRepo(sqlc.New(pool)).Create(ctx, tx, in)
		return terr
	})
	return l, err
}

// listingFixture seeds a seat, a payee and a registered site, and returns the
// pool every listing assertion writes through.
func listingFixture(
	t *testing.T, ctx context.Context, tenantID, publisherID, site string,
) *pgxpool.Pool {
	t.Helper()
	pool := newTestPool(t, ctx)
	seedTenantRow(t, ctx, sqlc.New(pool), tenantID, "exchange.example")
	if _, err := createPublisher(t, ctx, pool, publisherID, "Stoa Press"); err != nil {
		t.Fatalf("seed publisher: %v", err)
	}
	if _, err := createDomain(t, ctx, pool, site, publisherID, "ED25519"); err != nil {
		t.Fatalf("seed site: %v", err)
	}
	return pool
}

// TestListingRoundTripAndActivation walks a listing from creation to proven. It
// starts PENDING_MANIFEST with no timestamps, keeps the path scope and the terms
// preset the registration agreed, and after Activate it is ACTIVE with both
// timestamps set.
func TestListingRoundTripAndActivation(t *testing.T) {
	ctx := context.Background()
	const (
		tenantID    = "t-listing"
		publisherID = "pub-listing"
		site        = "stoapress.example"
	)
	pool := listingFixture(t, ctx, tenantID, publisherID, site)
	scope := []string{"/articles/", "/archive/"}

	created, err := createListing(t, ctx, pool, tenantID, site, scope, termsPerAccess)
	if err != nil {
		t.Fatalf("Create listing: %v", err)
	}
	if created.State != repo.ListingStatePendingManifest {
		t.Errorf("a new listing is in state %q, want %q",
			created.State, repo.ListingStatePendingManifest)
	}
	if created.VerifiedAt != nil {
		t.Errorf("a new listing carries VerifiedAt = %v, want nil: nothing has proved it yet",
			created.VerifiedAt)
	}
	if created.LastCheckedAt != nil {
		t.Errorf("a new listing carries LastCheckedAt = %v, want nil", created.LastCheckedAt)
	}

	reads := repo.NewPublisherListingReadRepo(sqlc.New(pool))
	back, err := reads.ByDomain(ctx, tenantID, site)
	if err != nil {
		t.Fatalf("ByDomain after Create: %v", err)
	}
	assertSameTerms(t, back.Terms, termsPerAccess)
	if len(back.ContentPathScope) != 2 ||
		back.ContentPathScope[0] != "/articles/" || back.ContentPathScope[1] != "/archive/" {
		t.Errorf("ContentPathScope = %v, want %v", back.ContentPathScope, scope)
	}

	activateAndAssertActive(t, ctx, pool, tenantID, site)

	byPublisher, err := reads.ByPublisher(ctx, tenantID, publisherID)
	if err != nil {
		t.Fatalf("ByPublisher: %v", err)
	}
	if len(byPublisher) != 1 || byPublisher[0].Domain != site {
		t.Errorf("ByPublisher returned %d listings (%v), want the one for %q",
			len(byPublisher), byPublisher, site)
	}
}

// activateAndAssertActive activates the listing for site and checks both the
// row Activate returns and the row a later read sees: ACTIVE, with VerifiedAt
// and LastCheckedAt set.
func activateAndAssertActive(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, site string) {
	t.Helper()
	reads := repo.NewPublisherListingReadRepo(sqlc.New(pool))
	listings := repo.NewPublisherListingWriteRepo(sqlc.New(pool))
	var activated repo.PublisherDomainListing
	if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		activated, terr = listings.Activate(ctx, tx, tenantID, site)
		return terr
	}); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	// Activate returns the row it wrote, so a caller inside the same transaction
	// can read what it activated without a second query.
	if activated.State != repo.ListingStateActive {
		t.Errorf("Activate returned state %q, want %q", activated.State, repo.ListingStateActive)
	}
	if activated.VerifiedAt == nil || activated.LastCheckedAt == nil {
		t.Errorf("Activate returned VerifiedAt = %v and LastCheckedAt = %v, want both set",
			activated.VerifiedAt, activated.LastCheckedAt)
	}
	proven, err := reads.ByDomain(ctx, tenantID, site)
	if err != nil {
		t.Fatalf("ByDomain after Activate: %v", err)
	}
	if proven.State != repo.ListingStateActive {
		t.Errorf("state after Activate = %q, want %q", proven.State, repo.ListingStateActive)
	}
	if proven.VerifiedAt == nil || proven.LastCheckedAt == nil {
		t.Fatalf("after Activate VerifiedAt = %v and LastCheckedAt = %v, want both set",
			proven.VerifiedAt, proven.LastCheckedAt)
	}
}

// TestRecordCheckKeepsStateAndVerifiedAt pins what a FAILED manifest check does
// and does not do. Only LastCheckedAt moves. The listing stays ACTIVE and keeps
// the VerifiedAt it earned, because the push path re-reads the manifest on every
// push and is what stops a site whose manifest has gone stale; revoking from
// here as well would give one property two owners that can disagree.
func TestRecordCheckKeepsStateAndVerifiedAt(t *testing.T) {
	ctx := context.Background()
	const (
		tenantID    = "t-recheck"
		publisherID = "pub-recheck"
		site        = "harmonia.example"
	)
	pool := listingFixture(t, ctx, tenantID, publisherID, site)
	if _, err := createListing(t, ctx, pool, tenantID, site, nil, termsFree); err != nil {
		t.Fatalf("Create listing: %v", err)
	}
	listings := repo.NewPublisherListingWriteRepo(sqlc.New(pool))
	reads := repo.NewPublisherListingReadRepo(sqlc.New(pool))
	if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, terr := listings.Activate(ctx, tx, tenantID, site)
		return terr
	}); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	proven, err := reads.ByDomain(ctx, tenantID, site)
	if err != nil {
		t.Fatalf("ByDomain after Activate: %v", err)
	}

	var checked int64
	if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var terr error
		checked, terr = listings.RecordCheck(ctx, tx, tenantID, site)
		return terr
	}); err != nil {
		t.Fatalf("RecordCheck: %v", err)
	}
	if checked != 1 {
		t.Errorf("RecordCheck affected %d rows, want 1", checked)
	}
	after, err := reads.ByDomain(ctx, tenantID, site)
	if err != nil {
		t.Fatalf("ByDomain after RecordCheck: %v", err)
	}
	if after.State != repo.ListingStateActive {
		t.Errorf("state after a failed check = %q, want it left at %q",
			after.State, repo.ListingStateActive)
	}
	if after.VerifiedAt == nil || !after.VerifiedAt.Equal(*proven.VerifiedAt) {
		t.Errorf("VerifiedAt moved on a failed check: %v, want it left at %v",
			after.VerifiedAt, proven.VerifiedAt)
	}
	if after.LastCheckedAt == nil || !after.LastCheckedAt.After(*proven.LastCheckedAt) {
		t.Errorf("LastCheckedAt = %v, want it moved past %v",
			after.LastCheckedAt, proven.LastCheckedAt)
	}

	// An empty content path scope offers the whole site, and it round-trips as
	// an empty slice rather than as a NULL the reader has to guess at.
	if after.ContentPathScope == nil {
		t.Error("ContentPathScope came back nil; an omitted scope stores as an empty array")
	}
}

// TestCreateListingRejectsDuplicateOnSameSeat is the negative path for the
// composite key. A second listing of the same site on the same seat carries its
// own path scope and terms preset, so absorbing it as an update would silently
// change what the publisher already agreed to offer.
func TestCreateListingRejectsDuplicateOnSameSeat(t *testing.T) {
	ctx := context.Background()
	const (
		tenantID    = "t-duplicate"
		publisherID = "pub-duplicate"
		site        = "stoapress.example"
	)
	pool := listingFixture(t, ctx, tenantID, publisherID, site)

	if _, err := createListing(t, ctx, pool, tenantID, site, []string{"/articles/"}, termsPerAccess); err != nil {
		t.Fatalf("first Create listing: %v", err)
	}
	_, err := createListing(t, ctx, pool, tenantID, site, []string{"/everything/"}, termsFree)
	if !errors.Is(err, repo.ErrListingExists) {
		t.Fatalf("second Create listing error = %v, want repo.ErrListingExists", err)
	}
	back, err := repo.NewPublisherListingReadRepo(sqlc.New(pool)).ByDomain(ctx, tenantID, site)
	if err != nil {
		t.Fatalf("ByDomain after the refused Create: %v", err)
	}
	if !sameTerms(t, back.Terms, termsPerAccess) {
		t.Errorf("Terms = %q after a refused Create, want the first one's %q",
			back.Terms, termsPerAccess)
	}
	if len(back.ContentPathScope) != 1 || back.ContentPathScope[0] != "/articles/" {
		t.Errorf("ContentPathScope = %v after a refused Create, want the first one's [/articles/]",
			back.ContentPathScope)
	}
}

// TestOneSiteListsOnTwoSeatsWithDifferentScopes is the property the amendment
// moved the columns for. One site, two seats, two listings -- and the path scope
// and terms preset differ per seat, which they could not if they still sat on the
// site row.
func TestOneSiteListsOnTwoSeatsWithDifferentScopes(t *testing.T) {
	ctx := context.Background()
	const (
		seatA       = "t-seat-a"
		seatB       = "t-seat-b"
		publisherID = "pub-two-seats"
		site        = "stoapress.example"
	)
	pool := listingFixture(t, ctx, seatA, publisherID, site)
	seedTenantRow(t, ctx, sqlc.New(pool), seatB, "other-exchange.example")

	if _, err := createListing(t, ctx, pool, seatA, site, []string{"/articles/"}, termsPerAccess); err != nil {
		t.Fatalf("Create listing on seat A: %v", err)
	}
	if _, err := createListing(t, ctx, pool, seatB, site, []string{"/archive/"}, termsFree); err != nil {
		t.Fatalf("Create listing on seat B: %v", err)
	}

	reads := repo.NewPublisherListingReadRepo(sqlc.New(pool))
	a, err := reads.ByDomain(ctx, seatA, site)
	if err != nil {
		t.Fatalf("ByDomain(seat A): %v", err)
	}
	b, err := reads.ByDomain(ctx, seatB, site)
	if err != nil {
		t.Fatalf("ByDomain(seat B): %v", err)
	}
	if a.ContentPathScope[0] != "/articles/" || b.ContentPathScope[0] != "/archive/" {
		t.Errorf("scopes are %v and %v; each seat must keep its own", a.ContentPathScope, b.ContentPathScope)
	}
	if sameTerms(t, a.Terms, string(b.Terms)) {
		t.Errorf("both seats read Terms = %q; the licensing is per relationship", a.Terms)
	}
}

// TestListingReadIsSeatScoped is the negative path for the tenant predicate. A
// site listed on one seat is invisible to another, which is what keeps one
// publisher's two relationships from leaking into each other.
func TestListingReadIsSeatScoped(t *testing.T) {
	ctx := context.Background()
	const (
		seatA       = "t-scope-a"
		seatB       = "t-scope-b"
		publisherID = "pub-scoped"
		site        = "harmonia.example"
	)
	pool := listingFixture(t, ctx, seatA, publisherID, site)
	seedTenantRow(t, ctx, sqlc.New(pool), seatB, "other-exchange.example")
	if _, err := createListing(t, ctx, pool, seatA, site, nil, termsFree); err != nil {
		t.Fatalf("Create listing on seat A: %v", err)
	}

	reads := repo.NewPublisherListingReadRepo(sqlc.New(pool))
	if _, err := reads.ByDomain(ctx, seatB, site); !errors.Is(err, repo.ErrListingNotFound) {
		t.Fatalf("ByDomain(seat B) = %v, want repo.ErrListingNotFound", err)
	}
	got, err := reads.ByPublisher(ctx, seatB, publisherID)
	if err != nil {
		t.Fatalf("ByPublisher(seat B): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ByPublisher(seat B) returned %v, want none", got)
	}
}

// TestCreateListingRejectsUnregisteredSite is the negative path for the
// listing's reference to the site: a seat cannot list a site nobody registered.
func TestCreateListingRejectsUnregisteredSite(t *testing.T) {
	ctx := context.Background()
	const tenantID = "t-ghost-site"
	pool := newTestPool(t, ctx)
	seedTenantRow(t, ctx, sqlc.New(pool), tenantID, "exchange.example")

	_, err := createListing(t, ctx, pool, tenantID, "unregistered.example", nil, termsPerAccess)
	if !errors.Is(err, repo.ErrPublisherDomainNotFound) {
		t.Fatalf("Create listing error = %v, want repo.ErrPublisherDomainNotFound", err)
	}
}

// sameTerms compares two terms documents by what they say rather than by how
// they are written. The column is JSONB, so Postgres returns the members in its
// own order and with its own spacing; a byte comparison would be asserting the
// storage format, not the licensing.
func sameTerms(tb testing.TB, got []byte, want string) bool {
	tb.Helper()
	var a, b map[string]any
	if err := json.Unmarshal(got, &a); err != nil {
		tb.Fatalf("stored terms %q do not parse: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		tb.Fatalf("expected terms %q do not parse: %v", want, err)
	}
	return reflect.DeepEqual(a, b)
}

func assertSameTerms(tb testing.TB, got []byte, want string) {
	tb.Helper()
	if !sameTerms(tb, got, want) {
		tb.Errorf("Terms = %q, want the same document as %q", got, want)
	}
}
